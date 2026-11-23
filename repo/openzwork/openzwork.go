// Package openzwork packages OpenZWork (https://github.com/BlueSkyXN/OpenZWork),
// an Electron desktop app, for the AUR.
//
// Only openzwork-electron-bin is published. Upstream's Linux build bundles
// Electron and an app.asar of roughly 400 MB; this package follows the
// zcode-bin layout instead: the app runs on the repository's electron44 and the
// release is only used for its resources. That drops the duplicated runtime (a
// few hundred megabytes) and lets pacman update Electron for every Electron app
// at once.
package openzwork

import (
	"fmt"
	"strings"

	"github.com/rinnein/auto-aur/internal/conf"
	"github.com/rinnein/auto-aur/internal/pkgbuild"
	"github.com/rinnein/auto-aur/repo"
)

const (
	upstreamRepo = "BlueSkyXN/OpenZWork"

	// pkgName states both halves of the AUR convention: -electron means the app
	// runs on the distribution's Electron instead of a bundled copy, -bin means
	// it repackages upstream's prebuilt build rather than compiling the source.
	pkgName = "openzwork-electron-bin"
	// virtualName is what the package provides. No real package is published
	// under it, and conflicting with it keeps a future source-built package
	// from being installed alongside this one.
	virtualName = "openzwork"

	// electronMajor is the Electron series the app is built against (read from
	// the bundled binary as 44.x). The package depends on electron44 and runs
	// app.asar with it; prepare() warns when an upstream release moves on.
	electronMajor = "44"

	// installDir is where resources (app.asar, config/, glm/, tools/, icons)
	// live. prepare() rewrites process.resourcesPath to this path.
	installDir = "/usr/lib/openzwork"

	pkgURL = "https://github.com/BlueSkyXN/OpenZWork"

	// fallbackPkgdesc is used when the repository has no About description;
	// normally pkgdesc is that description, fetched each run.
	fallbackPkgdesc = "AI coding workbench with a desktop app, browser UI and terminal agent"

	// GPL-3.0 per the repository LICENSE. Upstream's own package metadata says
	// "unknown", which would fail AUR review.
	licenseName = "GPL-3.0-only"

	// Runtime requirements. electron44 replaces the bundled runtime; the Python
	// and Node packages serve the bundled document skills (docx/pdf/xlsx) and
	// npm-based tooling; ripgrep replaces the bundled rg; the native modules in
	// app.asar.unpacked need libstdc++/libgcc.
	depends = "electron44 python python-reportlab python-lxml python-pillow python-defusedxml libstdc++ libgcc nodejs ripgrep xdg-utils"

	// makedepends repacks app.asar after rewriting process.resourcesPath.
	makedepends = "asar"
)

// binOptions keep makepkg from stripping the shipped native modules.
var binOptions = []string{"!strip"}

// maintainer is set per run from conf.json.
var maintainer string

func init() { repo.Register(pkgName, &Generator{}) }

// Generator renders openzwork-electron-bin.
type Generator struct{}

func (g *Generator) Pkgname() string { return pkgName }

func (g *Generator) Wanted() repo.Wanted {
	return repo.Wanted{Repo: upstreamRepo, RepoDescription: true}
}

func (g *Generator) WithMaintainer(m string, _ conf.Overrides) repo.Generator {
	maintainer = m
	return g
}

// releaseAsset picks the native Arch package from the release. It is smaller
// than the .deb and already laid out for Arch, so its payload is what gets
// repacked.
func releaseAsset(up repo.Upstream) (repo.Asset, error) {
	for _, a := range up.Assets {
		if strings.Contains(a.Name, "linux-x64") && strings.HasSuffix(a.Name, ".pkg.tar.zst") {
			return a, nil
		}
	}
	return repo.Asset{}, fmt.Errorf("openzwork: release %s has no linux-x64 .pkg.tar.zst asset", up.Tag)
}

// sumsAsset returns the release's SHA256SUMS file, when it ships one.
func sumsAsset(up repo.Upstream) (repo.Asset, bool) {
	for _, a := range up.Assets {
		if a.Name == "SHA256SUMS" {
			return a, true
		}
	}
	return repo.Asset{}, false
}

// Pkgver takes the version the release declares, falling back to the Arch asset
// filename. It never downloads anything, so unchanged packages are cheap to skip.
func (g *Generator) Pkgver(up repo.Upstream) (string, error) {
	ver := up.Version
	if ver == "" {
		asset, err := releaseAsset(up)
		if err != nil {
			return "", err
		}
		v, ok := versionFromAsset(asset.Name)
		if !ok {
			return "", fmt.Errorf("openzwork: cannot determine a version for release %s", up.Tag)
		}
		ver = v
	}
	if err := checkPkgver(ver); err != nil {
		return "", err
	}
	return ver, nil
}

// checkPkgver rejects what makepkg would refuse: whitespace, colons, slashes,
// hyphens or non-ASCII characters. Versions such as 3.18.2+openzwork.4 are fine.
func checkPkgver(ver string) error {
	if ver == "" {
		return fmt.Errorf("openzwork: empty pkgver")
	}
	if strings.ContainsAny(ver, " \t\n/:-") {
		return fmt.Errorf("openzwork: upstream version %q cannot be a pkgver (contains a forbidden character)", ver)
	}
	for _, r := range ver {
		if r > 127 {
			return fmt.Errorf("openzwork: upstream version %q is not ASCII", ver)
		}
	}
	return nil
}

// versionFromAsset extracts the version from
// OpenZWork-3.18.2+openzwork.4-linux-x64.pkg.tar.zst.
func versionFromAsset(name string) (string, bool) {
	const prefix, suffix = "OpenZWork-", "-linux-x64.pkg.tar.zst"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return "", false
	}
	ver := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	return ver, ver != ""
}

// sumFromSums finds the sha256 for name in a SHA256SUMS file.
func sumFromSums(sums, name string) string {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		file := strings.TrimPrefix(fields[1], "*")
		if file == name {
			return fields[0]
		}
	}
	return ""
}

// checksum resolves the sha256 of the release asset at run time.
//
// The release publishes SHA256SUMS, which is preferred: users can verify the
// same file with sha256sum -c. GitHub's recorded digest is the next choice, and
// only when both are missing is the asset streamed to hash it.
func checksum(ctx repo.Context, asset repo.Asset) (string, error) {
	if sums, ok := sumsAsset(ctx.Upstream); ok && ctx.Net.Checksum != nil {
		if body, err := ctx.Net.Checksum(sums.URL); err == nil {
			if sum := sumFromSums(body, asset.Name); sum != "" {
				return sum, nil
			}
		}
	}
	if asset.SHA256 != "" {
		return asset.SHA256, nil
	}
	if ctx.Net.Digest != nil {
		sum, err := ctx.Net.Digest(asset.URL, "sha256")
		if err != nil {
			return "", fmt.Errorf("openzwork: checksum %s: %w", asset.Name, err)
		}
		return sum, nil
	}
	return "", fmt.Errorf("openzwork: no checksum available for %s", asset.Name)
}

// sanitizeDesc makes an arbitrary About description safe for pkgdesc, which is
// a single line in both PKGBUILD and .SRCINFO.
func sanitizeDesc(desc string) string {
	return strings.Join(strings.Fields(desc), " ")
}

// chooseDesc returns the description to publish and, when it had to fall back,
// the reason for the run summary.
func chooseDesc(up repo.Upstream) (string, string) {
	if d := sanitizeDesc(up.Description); d != "" {
		return d, ""
	}
	return fallbackPkgdesc, "repository has no About description; using the built-in default"
}

// bashDoubleQuote renders s as a double-quoted bash word. Double quotes are
// used because the About text may contain an apostrophe, which would end a
// single-quoted string.
func bashDoubleQuote(s string) string {
	escaped := strings.NewReplacer(
		"\\", "\\\\",
		"\"", "\\\"",
		"$", "\\$",
		"`", "\\`",
	).Replace(s)
	return `"` + escaped + `"`
}

// quoteAll renders values as a PKGBUILD array body.
func quoteAll(in []string) string {
	parts := make([]string, len(in))
	for i, p := range in {
		parts[i] = "'" + p + "'"
	}
	return strings.Join(parts, " ")
}

func (g *Generator) Render(ctx repo.Context) (repo.Generated, error) {
	asset, err := releaseAsset(ctx.Upstream)
	if err != nil {
		return repo.Generated{}, err
	}
	ver, err := g.Pkgver(ctx.Upstream)
	if err != nil {
		return repo.Generated{}, err
	}
	sum, err := checksum(ctx, asset)
	if err != nil {
		return repo.Generated{}, err
	}
	if len(sum) != 64 || strings.Trim(sum, "0123456789abcdef") != "" {
		return repo.Generated{}, fmt.Errorf("openzwork: unexpected sha256 %q for %s", sum, asset.Name)
	}

	pkgrel := ctx.Pkgrel
	if pkgrel == 0 {
		pkgrel = 1
	}
	desc, note := chooseDesc(ctx.Upstream)

	var b strings.Builder
	fmt.Fprintf(&b, "# Maintainer: %s\n\n", maintainer)
	fmt.Fprintf(&b, "pkgname=%s\n", pkgName)
	fmt.Fprintf(&b, "pkgver=%s\n", ver)
	fmt.Fprintf(&b, "pkgrel=%d\n", pkgrel)
	fmt.Fprintf(&b, "pkgdesc=%s\n", bashDoubleQuote(desc))
	b.WriteString("arch=('x86_64')\n")
	fmt.Fprintf(&b, "url=\"%s\"\n", pkgURL)
	fmt.Fprintf(&b, "license=('%s')\n", licenseName)
	fmt.Fprintf(&b, "depends=(%s)\n", quoteAll(strings.Fields(depends)))
	fmt.Fprintf(&b, "makedepends=('%s')\n", makedepends)
	fmt.Fprintf(&b, "provides=('%s')\n", virtualName)
	fmt.Fprintf(&b, "conflicts=('%s')\n", virtualName)
	fmt.Fprintf(&b, "options=(%s)\n\n", quoteAll(binOptions))
	fmt.Fprintf(&b, "_electronversion=%s\n", electronMajor)
	fmt.Fprintf(&b, "_archive=\"%s\"\n", asset.Name)
	fmt.Fprintf(&b, "source_x86_64=(\"${_archive}::%s\")\n", asset.URL)
	b.WriteString("noextract=(\"${_archive}\")\n")
	fmt.Fprintf(&b, "sha256sums_x86_64=('%s')\n", sum)
	b.WriteString(pkgbuildScript())

	src := b.String()
	return repo.Generated{
		Pkgname:  pkgName,
		Pkgver:   ver,
		Pkgrel:   pkgrel,
		PKGBUILD: src,
		Srcinfo:  srcinfo(desc, ver, pkgrel, asset.Name, asset.URL, sum),
		Fields:   pkgbuild.Parse(src),
		// The fingerprint tracks the release itself, so a rebuilt asset for the
		// same version still bumps pkgrel.
		Fingerprint: ctx.Upstream.Fingerprint,
		Note:        note,
	}, nil
}

// srcinfo builds a .SRCINFO without makepkg, byte-identical to what
// makepkg --printsrcinfo writes (checked by TestSrcinfoMatchesMakepkg).
func srcinfo(desc, ver string, rel int, archive, url, sum string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "pkgbase = %s\n", pkgName)
	fmt.Fprintf(&b, "\tpkgdesc = %s\n", desc)
	fmt.Fprintf(&b, "\tpkgver = %s\n", ver)
	fmt.Fprintf(&b, "\tpkgrel = %d\n", rel)
	fmt.Fprintf(&b, "\turl = %s\n", pkgURL)
	b.WriteString("\tarch = x86_64\n")
	fmt.Fprintf(&b, "\tlicense = %s\n", licenseName)
	fmt.Fprintf(&b, "\tmakedepends = %s\n", makedepends)
	for _, d := range strings.Fields(depends) {
		fmt.Fprintf(&b, "\tdepends = %s\n", d)
	}
	fmt.Fprintf(&b, "\tprovides = %s\n", virtualName)
	fmt.Fprintf(&b, "\tconflicts = %s\n", virtualName)
	fmt.Fprintf(&b, "\tnoextract = %s\n", archive)
	for _, o := range binOptions {
		fmt.Fprintf(&b, "\toptions = %s\n", o)
	}
	fmt.Fprintf(&b, "\tsource_x86_64 = %s::%s\n", archive, url)
	fmt.Fprintf(&b, "\tsha256sums_x86_64 = %s\n", sum)
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "pkgname = %s\n", pkgName)
	return b.String()
}
