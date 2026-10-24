// Package repo holds the task registry: every package auto-aur can publish is
// described by one Entry and built by a shared generator.
package repo

import (
	"fmt"
	"sort"
	"sync"

	"github.com/rinnein/auto-aur/internal/conf"
	"github.com/rinnein/auto-aur/internal/pkgbuild"
)

// Wanted is the upstream release a task tracks.
type Wanted struct {
	Repo   string
	Tag    string
	Search string
}

// Generated is what a task produces once its release is known.
type Generated struct {
	Pkgname string
	Pkgver  string
	// Pkgrel defaults to 1 and only changes when a task says so.
	Pkgrel int
	// PKGBUILD is the rendered file text.
	PKGBUILD string
	// Fields is the parsed view of PKGBUILD, used to compare against the
	// previously published file.
	Fields pkgbuild.Fields
	// Source is the resolved download URL handed to PKGBUILD.
	Source string
	// SHA1 is the checksum sum for Source.
	SHA1 string
}

// Overrider is implemented by generators that accept conf.json overrides.
type Overrider interface {
	WithMaintainer(maintainer string, o conf.Overrides) Generator
}

// WithOverrides applies conf.json customisations when supported; otherwise the
// generator is returned unchanged.
func WithOverrides(g Generator, maintainer string, o conf.Overrides) Generator {
	if ov, ok := g.(Overrider); ok {
		return ov.WithMaintainer(maintainer, o)
	}
	return g
}

// Checksum fetches the content of a small text asset (typically a .sha1 file).
// It is injected so generators stay free of network code.
type Checksum func(url string) (string, error)

// Generator is implemented by each package family.
type Generator interface {
	// Pkgname is the default AUR package base.
	Pkgname() string
	// Wanted describes the upstream release to resolve.
	Wanted() Wanted
	// Render builds the PKGBUILD for a resolved release, fetching whatever
	// small auxiliary files (checksums) the package definition needs.
	Render(rel Release, sum Checksum) (Generated, error)
	// Srcinfo renders .SRCINFO without makepkg.
	Srcinfo(g Generated) string
}

// Release is the minimal view of a GitHub release a generator needs.
type Release struct {
	TagName string
	Assets  []Asset
}

// Asset is a release artefact.
type Asset struct {
	Name string
	URL  string
}

// Entry pairs a generator with the metadata the app layer needs.
type Entry struct {
	Name string
	Gen  Generator
	// Overrides from conf.json, applied on top of generator defaults.
	Overrides conf.Overrides
}

var (
	mu      sync.RWMutex
	entries = map[string]Generator{}
	order   []string
)

// Register adds a task. Called from package init() functions so tasks stay
// declarative and adding one never touches the app layer.
func Register(name string, g Generator) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := entries[name]; dup {
		panic("repo: duplicate task " + name)
	}
	entries[name] = g
	order = append(order, name)
}

// Get resolves a task by name.
func Get(name string) (Generator, bool) {
	mu.RLock()
	defer mu.RUnlock()
	g, ok := entries[name]
	return g, ok
}

// Has reports whether name is registered.
func Has(name string) bool {
	_, ok := Get(name)
	return ok
}

// Names lists registered tasks in registration order.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, len(order))
	copy(out, order)
	return out
}

// SortedNames lists registered tasks alphabetically, for error messages.
func SortedNames() []string {
	out := Names()
	sort.Strings(out)
	return out
}

// Pkgname returns the default package base for a task.
func Pkgname(name string) (string, bool) {
	g, ok := Get(name)
	if !ok {
		return "", false
	}
	return g.Pkgname(), true
}

// MustGet is Get for code that has already validated the name.
func MustGet(name string) Generator {
	g, ok := Get(name)
	if !ok {
		panic(fmt.Sprintf("repo: unknown task %q", name))
	}
	return g
}
