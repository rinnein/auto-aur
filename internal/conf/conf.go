// Package conf loads and validates conf.json, and decides which tasks are
// enabled. It is the single entry point for user configuration.
package conf

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// DefaultAURHost / DefaultAURUser mirror the SSH configuration that the CI
// workflow already writes to ~/.ssh/config. Changing these here is not needed
// for the workflow to work; they exist so local runs can point elsewhere.
const (
	DefaultAURHost   = "aur.archlinux.org"
	DefaultAURUser   = "aur"
	DefaultAURBranch = "master"
)

// AUR describes how package repositories are reached.
type AUR struct {
	Host   string `json:"host,omitempty"`
	User   string `json:"user,omitempty"`
	Branch string `json:"branch,omitempty"`
}

// Overrides lets a package entry customise the PKGBUILD fields a task would
// otherwise emit. Every field is optional; nil means "keep the task default".
type Overrides struct {
	Pkgname   *string   `json:"pkgname,omitempty"`   // AUR package base
	PkgnameIn *string   `json:"_pkgname,omitempty"`  // binary/desktop/icon basename
	Disname   *string   `json:"disname,omitempty"`   // desktop Name=
	Pkgdesc   *string   `json:"pkgdesc,omitempty"`   //
	URL       *string   `json:"url,omitempty"`       //
	License   *[]string `json:"license,omitempty"`   //
	Arch      *[]string `json:"arch,omitempty"`      //
	Depends   *[]string `json:"depends,omitempty"`   //
	Conflicts *[]string `json:"conflicts,omitempty"` //
	Provides  *[]string `json:"provides,omitempty"`  //
	Options   *[]string `json:"options,omitempty"`   //
	Pkgrel    *int      `json:"pkgrel,omitempty"`    //
}

// Package is one entry in packages[].
type Package struct {
	Task      string    `json:"task"`
	Disabled  bool      `json:"disabled,omitempty"`
	Overrides Overrides `json:"overrides,omitempty"`
}

// Config is the on-disk conf.json.
type Config struct {
	Maintainer      string    `json:"maintainer"`
	AUR             AUR       `json:"aur,omitempty"`
	CreateOnMissing *bool     `json:"createOnMissing,omitempty"`
	Packages        []Package `json:"packages"`
	Disabled        []string  `json:"disabled,omitempty"`
}

// Load reads path and normalises defaults. It does not validate entries against
// the task registry; call Validate for that once the registry is known.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.check(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.AUR.Host == "" {
		c.AUR.Host = DefaultAURHost
	}
	if c.AUR.User == "" {
		c.AUR.User = DefaultAURUser
	}
	if c.AUR.Branch == "" {
		c.AUR.Branch = DefaultAURBranch
	}
	if c.Packages == nil {
		c.Packages = []Package{}
	}
}

func (c *Config) check() error {
	if c.Maintainer == "" {
		return fmt.Errorf("config: maintainer is required")
	}
	seen := map[string]bool{}
	for i, p := range c.Packages {
		if p.Task == "" {
			return fmt.Errorf("config: packages[%d] is missing task", i)
		}
		if seen[p.Task] {
			return fmt.Errorf("config: duplicate task %q", p.Task)
		}
		seen[p.Task] = true
	}
	return nil
}

// MaintainerName returns the display name from the "Name <email>" maintainer
// line, falling back to the whole string when it is not in that form.
func (c *Config) MaintainerName() string {
	if i := strings.LastIndex(c.Maintainer, "<"); i > 0 {
		if n := strings.TrimSpace(c.Maintainer[:i]); n != "" {
			return n
		}
	}
	return strings.TrimSpace(c.Maintainer)
}

// MaintainerEmail extracts the address from "Name <email>", or "" when absent.
func (c *Config) MaintainerEmail() string {
	open := strings.LastIndex(c.Maintainer, "<")
	closeIdx := strings.LastIndex(c.Maintainer, ">")
	if open >= 0 && closeIdx > open {
		return strings.TrimSpace(c.Maintainer[open+1 : closeIdx])
	}
	return ""
}

// CreateMissing reports whether missing AUR packages should be created.
func (c *Config) CreateMissing() bool {
	if c.CreateOnMissing == nil {
		return true
	}
	return *c.CreateOnMissing
}

// KnownTask validates every packages[].task against the registry and rejects
// duplicate package bases after overrides are applied.
func (c *Config) KnownTask(has func(string) bool, names func() []string, pkgname func(string) (string, bool)) error {
	seenPkg := map[string]string{}
	for _, p := range c.Packages {
		if !has(p.Task) {
			return fmt.Errorf("config: unknown task %q (available: %v)", p.Task, names())
		}
		base, ok := pkgname(p.Task)
		if !ok {
			return fmt.Errorf("config: task %q has no package name", p.Task)
		}
		if p.Overrides.Pkgname != nil {
			base = *p.Overrides.Pkgname
		}
		if prev, dup := seenPkg[base]; dup {
			return fmt.Errorf("config: package base %q used by both %q and %q", base, prev, p.Task)
		}
		seenPkg[base] = p.Task
	}
	return nil
}

// DisabledReason explains why a task is skipped, or returns "" when it runs.
// A task is disabled when it is marked disabled on its own entry or when its
// name matches any pattern in the top-level disabled list.
func (c *Config) DisabledReason(p Package) string {
	if p.Disabled {
		return "disabled in packages[]"
	}
	for _, pattern := range c.Disabled {
		if pattern == p.Task {
			return fmt.Sprintf("matched disabled pattern %q", pattern)
		}
		if ok, err := doublestar.Match(pattern, p.Task); err == nil && ok {
			return fmt.Sprintf("matched disabled pattern %q", pattern)
		}
	}
	return ""
}
