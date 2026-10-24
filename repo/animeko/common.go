// Package animeko builds AUR packages for the Animeko desktop AppImage.
//
// All three channels (stable, beta, alpha) share the same generator; the only
// differences are the release search term and a handful of PKGBUILD fields.
// Adding a channel means adding one small file, not copying the template.
package animeko

import (
	"fmt"
	"strings"

	"github.com/rinnein/auto-aur/internal/conf"
	"github.com/rinnein/auto-aur/internal/pkgbuild"
	"github.com/rinnein/auto-aur/repo"
)

// Channel describes one release channel of the app.
type Channel struct {
	// Task is the registry name, also the key used in conf.json.
	Task string
	// Pkgname is the default AUR package base.
	Pkgname string
	// Search is the substring matched against release names/tags.
	Search string
	// Tag pins a specific release; mutually exclusive with Search.
	Tag string
	// Disname is the desktop Name= value. Empty means "leave upstream alone".
	Disname string
	// Depends overrides the dependency list for this channel.
	Depends []string
	// IconDir is the hicolor size directory, e.g. "512x512".
	IconDir string
}

const (
	upstreamRepo = "open-ani/animeko"
	appPkgname   = "animeko"
	appPkgdesc   = "集找番、追番、看番的一站式弹幕追番平台，云收藏同步 (Bangumi)，离线缓存，BitTorrent，弹幕云过滤。100% Kotlin/Compose Multiplatform"
	appURL       = "https://myani.org"
)

var (
	// stableDeps is what the stable channel has always used.
	stableDeps = []string{"vlc", "gvfs"}
	// previewDeps splits VLC into plugins, which the beta/alpha builds need.
	previewDeps = []string{"vlc-plugin-ffmpeg", "vlc-plugin-dvb", "vlc-plugin-pulse", "gvfs"}
)

// Generator renders one channel's PKGBUILD.
type Generator struct {
	Channel Channel
	// Maintainer comes from conf.json and is injected by the app layer.
	Maintainer string

	// Overridable PKGBUILD fields; zero values fall back to the defaults in
	// data(), except Disname which lives on Channel because it is optional.
	PkgnameIn string
	Pkgdesc   string
	URL       string
	License   []string
	Arch      []string
	Conflicts []string
	Provides  []string
	Options   []string
	Pkgrel    int
}

// New builds a generator for a channel.
func New(c Channel) *Generator { return &Generator{Channel: c} }

// WithMaintainer returns a copy carrying the configured maintainer line and
// any per-package overrides from conf.json. The copy is returned as a pointer:
// returning the struct by value would silently drop the configured fields.
func (g *Generator) WithMaintainer(m string, o conf.Overrides) repo.Generator {
	cp := *g
	cp.Maintainer = m
	applyOverrides(&cp, o)
	return &cp
}

// applyOverrides folds conf.json overrides into the generator so a channel can
// be retargeted (rename, extra deps) without touching Go code.
func applyOverrides(g *Generator, o conf.Overrides) {
	if o.Pkgname != nil {
		g.Channel.Pkgname = *o.Pkgname
	}
	if o.Disname != nil {
		g.Channel.Disname = *o.Disname
	}
	if o.Depends != nil {
		g.Channel.Depends = *o.Depends
	}
	if o.PkgnameIn != nil {
		g.PkgnameIn = *o.PkgnameIn
	}
	if o.Pkgdesc != nil {
		g.Pkgdesc = *o.Pkgdesc
	}
	if o.URL != nil {
		g.URL = *o.URL
	}
	if o.License != nil {
		g.License = *o.License
	}
	if o.Arch != nil {
		g.Arch = *o.Arch
	}
	if o.Conflicts != nil {
		g.Conflicts = *o.Conflicts
	}
	if o.Provides != nil {
		g.Provides = *o.Provides
	}
	if o.Options != nil {
		g.Options = *o.Options
	}
	if o.Pkgrel != nil {
		g.Pkgrel = *o.Pkgrel
	}
}

// Pkgname implements repo.Generator.
func (g *Generator) Pkgname() string { return g.Channel.Pkgname }

// Wanted implements repo.Generator.
func (g *Generator) Wanted() repo.Wanted {
	return repo.Wanted{Repo: upstreamRepo, Search: g.Channel.Search, Tag: g.Channel.Tag}
}

// data assembles template input for a resolved release.
func (g *Generator) data(rel repo.Release, sum repo.Checksum) (pkgbuild.Data, error) {
	appimage, ok := pick(rel.Assets, "linux-x86_64.appimage", "")
	if !ok {
		return pkgbuild.Data{}, fmt.Errorf("animeko: release %s has no linux-x86_64 appimage asset", rel.TagName)
	}
	sumAsset, ok := pick(rel.Assets, "linux-x86_64.appimage.sha1", "")
	if !ok {
		return pkgbuild.Data{}, fmt.Errorf("animeko: release %s has no sha1 asset for %s", rel.TagName, appimage.Name)
	}
	// The .sha1 asset holds the bare digest, not its own name; the checksum has
	// to be downloaded or pkgsrc will reject the build.
	if sum == nil {
		return pkgbuild.Data{}, fmt.Errorf("animeko: no checksum fetcher configured")
	}
	digest, err := sum(sumAsset.URL)
	if err != nil {
		return pkgbuild.Data{}, fmt.Errorf("animeko: fetch checksum for %s: %w", appimage.Name, err)
	}
	digest = normalizeDigest(digest)
	if len(digest) != 40 {
		return pkgbuild.Data{}, fmt.Errorf("animeko: checksum for %s is not a sha1 digest: %q", appimage.Name, digest)
	}
	if strings.ContainsAny(appimage.URL, `["$`) {
		return pkgbuild.Data{}, fmt.Errorf("animeko: asset url %q is not safe to embed", appimage.URL)
	}

	ch := g.Channel
	depends := ch.Depends
	if len(depends) == 0 {
		depends = stableDeps
	}
	d := pkgbuild.Data{
		Maintainer:     g.Maintainer,
		Pkgname:        ch.Pkgname,
		PkgnameIn:      orDefault(g.PkgnameIn, appPkgname),
		Appimage:       appimage.Name,
		Pkgver:         Pkgver(rel.TagName),
		Pkgrel:         orInt(g.Pkgrel, 1),
		Disname:        ch.Disname,
		Pkgdesc:        orDefault(g.Pkgdesc, appPkgdesc),
		URL:            orDefault(g.URL, appURL),
		License:        orList(g.License, []string{"AGPL-3.0-or-later"}),
		Arch:           orList(g.Arch, []string{"x86_64"}),
		Depends:        depends,
		Conflicts:      orList(g.Conflicts, nil),
		Provides:       orList(g.Provides, []string{appPkgname}),
		Options:        orList(g.Options, []string{"!strip"}),
		SourceURL:      appimage.URL,
		SHA1:           digest,
		IconDir:        ch.IconDir,
		SetDesktopName: ch.Disname != "",
	}
	return d, nil
}

// normalizeDigest trims whitespace and takes the first field, tolerating files
// that carry "digest  filename" instead of just the digest.
func normalizeDigest(raw string) string {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return ""
	}
	return strings.ToLower(fields[0])
}

func orDefault(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

func orInt(v, fallback int) int {
	if v != 0 {
		return v
	}
	return fallback
}

func orList(v, fallback []string) []string {
	if len(v) > 0 {
		return v
	}
	return fallback
}

// Pkgver converts an upstream tag to a valid pkgver: hyphens are not allowed.
func Pkgver(tag string) string {
	return strings.ReplaceAll(tag, "-", "_")
}

// pick finds an asset whose name ends with suffix (case preserved, matching the
// previous implementation's behaviour). When skip is non-empty, assets
// containing it are ignored.
func pick(assets []repo.Asset, suffix, skip string) (repo.Asset, bool) {
	for _, a := range assets {
		if skip != "" && strings.Contains(a.Name, skip) {
			continue
		}
		if strings.HasSuffix(a.Name, suffix) {
			return a, true
		}
	}
	return repo.Asset{}, false
}

// Render implements repo.Generator.
func (g *Generator) Render(rel repo.Release, sum repo.Checksum) (repo.Generated, error) {
	d, err := g.data(rel, sum)
	if err != nil {
		return repo.Generated{}, err
	}
	src, err := pkgbuild.Render(d)
	if err != nil {
		return repo.Generated{}, err
	}
	return repo.Generated{
		Pkgname:  d.Pkgname,
		Pkgver:   d.Pkgver,
		Pkgrel:   d.Pkgrel,
		PKGBUILD: src,
		Fields:   pkgbuild.Parse(src),
		Source:   d.Appimage + "::" + d.SourceURL,
		SHA1:     d.SHA1,
	}, nil
}

// Srcinfo renders a .SRCINFO without invoking makepkg, so the tool works on
// machines that have no Arch packaging tools. The field order matches what
// makepkg --printsrcinfo emits, so the two are interchangeable in review.
func (g *Generator) Srcinfo(gen repo.Generated) string {
	f := gen.Fields
	var b strings.Builder
	fmt.Fprintf(&b, "pkgbase = %s\n", f.Pkgname)
	fmt.Fprintf(&b, "\tpkgdesc = %s\n", f.Pkgdesc)
	fmt.Fprintf(&b, "\tpkgver = %s\n", f.Pkgver)
	fmt.Fprintf(&b, "\tpkgrel = %d\n", gen.Pkgrel)
	fmt.Fprintf(&b, "\turl = %s\n", f.URL)
	fmt.Fprintf(&b, "\tarch = x86_64\n")
	for _, l := range f.License {
		fmt.Fprintf(&b, "\tlicense = %s\n", l)
	}
	fmt.Fprintf(&b, "\toptions = !strip\n")
	for _, d := range f.Depends {
		fmt.Fprintf(&b, "\tdepends = %s\n", d)
	}
	for _, c := range f.Conflicts {
		fmt.Fprintf(&b, "\tconflicts = %s\n", c)
	}
	for _, p := range f.Provides {
		fmt.Fprintf(&b, "\tprovides = %s\n", p)
	}
	fmt.Fprintf(&b, "\tsource_x86_64 = %s\n", gen.Source)
	fmt.Fprintf(&b, "\tsha1sums_x86_64 = %s\n", gen.SHA1)
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "pkgname = %s\n", f.Pkgname)
	return b.String()
}
