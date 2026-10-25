// Package repo holds the task registry: every package auto-aur can publish is
// described by a Generator and registered under a task name.
package repo

import (
	"fmt"
	"sort"
	"sync"

	"github.com/rinnein/auto-aur/internal/conf"
	"github.com/rinnein/auto-aur/internal/pkgbuild"
)

// Wanted describes the upstream a task tracks.
//
// Exactly one mode applies, in this order: Tag (a specific release), Search (the
// newest release whose name or tag matches), Branch (the head of a git branch),
// otherwise the repository's latest release.
type Wanted struct {
	Repo   string
	Tag    string
	Search string
	// Branch tracks the head commit of a branch instead of a release. VCS
	// packages (-git) use this: they package a moving branch, not a tarball.
	Branch string
	// RepoDescription asks the app to also resolve the repository's About
	// description into Upstream.Description, for packages that use it as
	// pkgdesc.
	RepoDescription bool
	// VersionFile names a JSON file (e.g. "package.json") whose "version" field
	// is the version of a branch-tracked package. Empty means the version comes
	// from the release as usual.
	VersionFile string
}

// Asset is a release artefact.
type Asset struct {
	Name string
	URL  string
	Size int64
	// SHA256 is the checksum GitHub recorded for the asset at upload time.
	// Empty when the API did not expose one.
	SHA256 string
}

// Upstream is the resolved source a task renders from.
type Upstream struct {
	// Tag is the release tag, empty in branch mode.
	Tag string
	// Commit is the commit the package resolves to, when known.
	Commit string
	// Date is the commit (or release) date as YYYYMMDD, in UTC. VCS packages
	// need it to build a monotonic pkgver without cloning.
	Date string
	// Assets are the release artefacts, empty in branch mode.
	Assets []Asset
	// Version is the upstream version the release declares (its name/body, or
	// the tag when tags are per-version). Empty in branch mode.
	Version string
	// Description is the repository's About text, when requested via
	// Wanted.RepoDescription.
	Description string
	// Fingerprint identifies this exact upstream state. It changes when the
	// underlying content changes even though the version string may not (a
	// republished release, a rebuilt asset).
	Fingerprint string
}

// Generated is a rendered package definition.
type Generated struct {
	Pkgname string
	Pkgver  string
	Pkgrel  int
	// PKGBUILD and Srcinfo are the file contents to publish.
	PKGBUILD string
	Srcinfo  string
	// Fields is the parsed view of PKGBUILD, used to detect edits that do not
	// change the version.
	Fields pkgbuild.Fields
	// Fingerprint is carried into the lock file so the next run can tell
	// "same version, different content" from "nothing changed".
	Fingerprint string
	// Note is an optional one-line remark for the run summary.
	Note string
}

// Checksum fetches a small text asset, typically a .sha1/.sha256 file.
type Checksum func(url string) (string, error)

// Digest streams a (potentially large) asset and returns its hex digest.
// algo is "sha256" or "sha1". Generators use it for binary assets that ship no
// checksum file of their own.
type Digest func(url, algo string) (string, error)

// Net is the set of network helpers available to a generator. They are
// injected so package definitions stay free of HTTP details.
type Net struct {
	Checksum Checksum
	Digest   Digest
}

// Context is everything a generator needs for one render.
type Context struct {
	Upstream Upstream
	// Pkgrel, when non-zero, overrides the generator's default. The app layer
	// sets it when it must bump pkgrel for a same-version content change.
	Pkgrel int
	Net    Net
}

// Generator is implemented by each package family.
type Generator interface {
	// Pkgname is the default AUR package base.
	Pkgname() string
	// Wanted describes the upstream to resolve.
	Wanted() Wanted
	// Pkgver derives the version from upstream metadata alone, without
	// downloading assets. It lets the app skip unchanged packages cheaply.
	Pkgver(up Upstream) (string, error)
	// Render produces the files to publish. It may download assets.
	Render(ctx Context) (Generated, error)
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
