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
)

const (
	upstreamRepo = "rinnein/your-szu-life"
	releaseTag   = "canary"

	pkgGit = "your-szu-life-git"
	pkgBin = "your-szu-life-bin"

	// virtualName is the name both packages provide.
	virtualName = "your-szu-life"

	pkgdesc = "Shenzhen University campus life application"
	pkgURL  = "https://github.com/rinnein/your-szu-life"

	// MIT per the repository LICENSE file. Upstream's own PKGBUILD says
	// "unknown", which would fail AUR review.
	licenseName = "MIT"

	// Arch names for the libraries the Tauri webview needs. They mirror the
	// Debian Depends of the prebuilt package.
	depends = "libayatana-appindicator webkit2gtk-4.1 gtk3"
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
	fmt.Fprintf(&b, "pkgdesc='%s'\n", desc)
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
