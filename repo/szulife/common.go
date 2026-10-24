// Package szulife packages Your SZU Life (https://github.com/rinnein/your-szu-life),
// a Tauri desktop app, for the AUR.
//
// Two packages are published from the same upstream:
//
//   - your-szu-life-git builds from the main branch
//   - your-szu-life-bin repackages the prebuilt .deb attached to the rolling
//     "canary" release
//
// Both declare provides=('your-szu-life'), which makes
//
//	paru -S your-szu-life
//
// list them as providers so the user picks one. No real package named
// your-szu-life is published: such a package would shadow the providers and
// remove the choice.
package szulife

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/rinnein/auto-aur/repo"
)

const (
	upstreamRepo = "rinnein/your-szu-life"
	releaseTag   = "canary"

	pkgGit = "your-szu-life-git"
	pkgBin = "your-szu-life-bin"

	// virtualName is the name both packages provide.
	virtualName = "your-szu-life"

	// fallbackPkgdesc is used only when the repository's About description is
	// empty; normally pkgdesc is the upstream "About" text, fetched each run.
	fallbackPkgdesc = "Shenzhen University campus life application"
	pkgURL          = "https://github.com/rinnein/your-szu-life"

	// MIT per the repository LICENSE file. Upstream's own PKGBUILD says
	// "unknown", which would fail AUR review.
	licenseName = "MIT"

	// Arch names for the libraries the Tauri webview needs. They mirror the
	// Debian Depends of the prebuilt package.
	// aria2 is a hard dependency: the app shells out to aria2c (found via PATH
	// or ARIA2C_PATH) for downloads. The .deb also ships its own aria2c, but
	// that copy is Debian's dynamically linked frontend and clashes with the
	// real /usr/bin/aria2c, so it is dropped in package().
	depends = "libayatana-appindicator webkit2gtk-4.1 gtk3 aria2"
)

// binOptions apply to the prebuilt package. !strip keeps the shipped binary
// untouched; !debug avoids an empty usr/src/debug tree.
var binOptions = []string{"!strip", "!debug"}

// gitMakedepends are the build-time requirements for the from-source package.
// libarchive provides bsdtar, which unpacks the .deb the Tauri build produces.
var gitMakedepends = []string{"base-devel", "git", "pnpm", "rust", "libarchive"}

// conflictList returns the mutual-conflict names. Each package names the other
// and the virtual name, so installing one removes the other and any third-party
// package that provides the same virtual name.
func conflictList(self string) []string {
	other := pkgBin
	if self == pkgBin {
		other = pkgGit
	}
	return []string{other, virtualName}
}

// conflicts renders the PKGBUILD array form.
func conflicts(self string) string {
	parts := conflictList(self)
	for i, p := range parts {
		parts[i] = "'" + p + "'"
	}
	return strings.Join(parts, " ")
}

// AppimageVersion matches the version embedded in release asset filenames, e.g.
// your-szu-life_linux_x64_0.10.0-release.deb.
// dropBundledAria2 is shell shared by both package() functions.
//
// The Tauri build copies Debian's aria2c frontend into the .deb. That binary
// links libaria2.so.0, which the .deb itself does not ship, and installing it
// at /usr/bin/aria2c collides with the aria2 package:
//
//	your-szu-life-bin: filesystem conflict on /usr/bin/aria2c (owned by aria2)
//
// The app resolves aria2c through PATH (or ARIA2C_PATH) and the declared
// dependency already puts the real binary there, so the bundled copy is
// removed rather than installed.
const dropBundledAria2 = `  # Drop the bundled aria2c: it collides with the aria2 package and needs a
  # libaria2.so.0 that this package does not ship.
  rm -f "${pkgdir}/usr/bin/aria2c"
`

// sanitizeDesc makes an arbitrary About description safe for pkgdesc.
//
// pkgdesc is a single line in both PKGBUILD and .SRCINFO, so whitespace runs
// (including newlines) are collapsed.
func sanitizeDesc(desc string) string {
	return strings.Join(strings.Fields(desc), " ")
}

// chooseDesc returns the description to publish, falling back when upstream has
// none set. The second return value explains the fallback for the run summary.
func chooseDesc(up repo.Upstream) (string, string) {
	if d := sanitizeDesc(up.Description); d != "" {
		return d, ""
	}
	return fallbackPkgdesc, "repository has no About description; using the built-in default"
}

// bashDoubleQuote renders s as a double-quoted bash word.
//
// Double quotes are used rather than single quotes because the About text may
// legitimately contain an apostrophe, which would terminate a single-quoted
// string and break the PKGBUILD. Everything bash would otherwise expand inside
// double quotes is escaped.
func bashDoubleQuote(s string) string {
	escaped := strings.NewReplacer(
		"\\", "\\\\",
		"\"", "\\\"",
		"$", "\\$",
		"`", "\\`",
	).Replace(s)
	return `"` + escaped + `"`
}

var assetVersion = regexp.MustCompile(`_([0-9]+(?:\.[0-9]+)*)-release\.`)

// versionFromAsset extracts the version from a release asset filename.
func versionFromAsset(name string) (string, bool) {
	m := assetVersion.FindStringSubmatch(name)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// commonHeader renders the fields both packages share.
func commonHeader(name, ver string, rel int, desc string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Maintainer: %s\n", maintainer)
	fmt.Fprintf(&b, "pkgname=%s\n", name)
	fmt.Fprintf(&b, "pkgver=%s\n", ver)
	fmt.Fprintf(&b, "pkgrel=%d\n", rel)
	fmt.Fprintf(&b, "pkgdesc=%s\n", bashDoubleQuote(desc))
	fmt.Fprintf(&b, "arch=('x86_64')\n")
	fmt.Fprintf(&b, "url='%s'\n", pkgURL)
	fmt.Fprintf(&b, "license=('%s')\n", licenseName)
	fmt.Fprintf(&b, "depends=(%s)\n", dependsQuoted())
	fmt.Fprintf(&b, "provides=('%s')\n", virtualName)
	fmt.Fprintf(&b, "conflicts=(%s)\n", conflicts(name))
	return b.String()
}

func dependsQuoted() string { return quoteAll(strings.Fields(depends)) }

// quoteAll renders values as a PKGBUILD array body.
func quoteAll(in []string) string {
	parts := make([]string, len(in))
	for i, p := range in {
		parts[i] = "'" + p + "'"
	}
	return strings.Join(parts, " ")
}

// extraFields is a .SRCINFO key with one or more values, emitted one per line.
type extraFields struct {
	Key    string
	Values []string
}

// srcinfo builds a .SRCINFO without makepkg.
//
// archSuffix follows how the PKGBUILD declares its source: "x86_64" for
// arch-specific arrays (source_x86_64), "" for the arch-independent source.
// Field order mirrors makepkg --printsrcinfo so the file this tool writes is
// byte-identical to the one the CI build step regenerates.
func srcinfo(base, desc, ver string, rel int, extras []extraFields, sources, sums []string, archSuffix string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "pkgbase = %s\n", base)
	fmt.Fprintf(&b, "\tpkgdesc = %s\n", desc)
	fmt.Fprintf(&b, "\tpkgver = %s\n", ver)
	fmt.Fprintf(&b, "\tpkgrel = %d\n", rel)
	fmt.Fprintf(&b, "\turl = %s\n", pkgURL)
	fmt.Fprintf(&b, "\tarch = x86_64\n")
	fmt.Fprintf(&b, "\tlicense = %s\n", licenseName)
	for _, e := range extras {
		if e.Key == "makedepends" {
			for _, v := range e.Values {
				fmt.Fprintf(&b, "\tmakedepends = %s\n", v)
			}
		}
	}
	for _, d := range strings.Fields(depends) {
		fmt.Fprintf(&b, "\tdepends = %s\n", d)
	}
	fmt.Fprintf(&b, "\tprovides = %s\n", virtualName)
	// .SRCINFO lists one value per line; the PKGBUILD array form is not valid
	// here and would be read as a single malformed value.
	for _, c := range conflictList(base) {
		fmt.Fprintf(&b, "\tconflicts = %s\n", c)
	}
	// makepkg prints options after conflicts, and drops values that match its
	// defaults, so only explicitly non-default options are listed.
	for _, e := range extras {
		if e.Key == "options" {
			for _, v := range e.Values {
				fmt.Fprintf(&b, "\toptions = %s\n", v)
			}
		}
	}
	srcKey := "source"
	sumKey := "sha256sums"
	if archSuffix != "" {
		srcKey += "_" + archSuffix
		sumKey += "_" + archSuffix
	}
	for _, s := range sources {
		fmt.Fprintf(&b, "\t%s = %s\n", srcKey, s)
	}
	for _, s := range sums {
		fmt.Fprintf(&b, "\t%s = %s\n", sumKey, s)
	}
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "pkgname = %s\n", base)
	return b.String()
}
