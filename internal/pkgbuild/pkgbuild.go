// Package pkgbuild renders PKGBUILD files and extracts the fields auto-aur
// needs to reason about them.
package pkgbuild

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"text/template"
)

// Data is everything a task supplies. Field names mirror PKGBUILD variables so
// the template reads like the file it produces.
type Data struct {
	Maintainer string
	BasedOn    string

	Pkgname   string // AUR package base
	PkgnameIn string // _pkgname: binary / desktop / icon basename
	Appimage  string // _appimage: upstream asset filename
	Pkgver    string
	Pkgrel    int
	Disname   string
	Pkgdesc   string
	URL       string
	License   []string
	Arch      []string
	Depends   []string
	Conflicts []string
	Provides  []string
	Options   []string
	SourceURL string
	SHA1      string

	// IconDir is the hicolor subdirectory the icon is installed into, e.g.
	// "512x512". Tasks pick it per channel.
	IconDir string
	// SetDesktopName controls whether build() rewrites Name= in the .desktop
	// file; the stable channel historically left it alone.
	SetDesktopName bool
}

const defaultTemplate = `# Based off of: https://daveparrish.net/posts/2019-11-16-Better-AppImage-PKGBUILD-template.html
# Maintainer: {{.Maintainer}}

_pkgname={{.PkgnameIn}}
_appimage={{.Appimage}}

pkgname={{.Pkgname}}
pkgver={{.Pkgver}}
pkgrel={{.Pkgrel}}
pkgdesc="{{.Pkgdesc}}"
arch=({{quoteList .Arch}})
url="{{.URL}}"
license=({{quoteList .License}})
depends=({{quoteList .Depends}})
{{- if .Conflicts}}
conflicts=({{quoteList .Conflicts}})
{{- end}}
provides=({{quoteList .Provides}})
options=({{noQuoteList .Options}})
source_{{.Arch0}}=("${_appimage}::{{.SourceURL}}")
noextract=("${_appimage}")
sha1sums_{{.Arch0}}=('{{.SHA1}}')

prepare() {
    chmod +x "${_appimage}"
    rm -rf ./squashfs-root
    # ./"${_appimage}" --appimage-extract
    ./"${_appimage}" --appimage-extract ${_pkgname}.desktop
    ./"${_appimage}" --appimage-extract icon.png
}

build() {
    # Adjust .desktop so it will work outside of AppImage container
    sed -i -e "s|Exec=.*|Exec=env DESKTOPINTEGRATION=false /usr/bin/${_pkgname}|" -e "s|Icon=.*|Icon=/usr/share/icons/${_pkgname}.png|"{{if .SetDesktopName}} -e "s|Name=.*|Name={{.Disname}}|"{{end}} "squashfs-root/${_pkgname}.desktop"
    # Fix permissions; .AppImage permissions are 700 for all directories
    # chmod -R a-x+rX squashfs-root/usr
}

package() {
    # AppImage
    install -Dm755 "${srcdir}/${_appimage}" "${pkgdir}/opt/${pkgname}/${pkgname}.AppImage"
    # install -Dm644 "${srcdir}/LICENSE" "${pkgdir}/opt/${pkgname}/LICENSE"

    # Desktop file
    install -Dm644 "${srcdir}/squashfs-root/${_pkgname}.desktop" "${pkgdir}/usr/share/applications/${_pkgname}.desktop"

    # Icon images
    install -dm755 "${pkgdir}/usr/share/icons/hicolor/{{.IconDir}}/apps"
    cp -a "${srcdir}/squashfs-root/icon.png" "${pkgdir}/usr/share/icons/hicolor/{{.IconDir}}/apps/${_pkgname}.png"

    # Symlink executable
    install -dm755 "${pkgdir}/usr/bin"
    ln -s "/opt/${pkgname}/${pkgname}.AppImage" "${pkgdir}/usr/bin/${_pkgname}"

    # Symlink license
    # install -dm755 "${pkgdir}/usr/share/licenses/${pkgname}/"
    # ln -s "/opt/$pkgname/LICENSE" "$pkgdir/usr/share/licenses/$pkgname"
}
`

var tmpl = template.Must(template.New("pkgbuild").
	Funcs(template.FuncMap{
		"quoteList": func(v []string) string {
			parts := make([]string, len(v))
			for i, s := range v {
				parts[i] = "'" + s + "'"
			}
			return strings.Join(parts, " ")
		},
		"noQuoteList": func(v []string) string {
			return strings.Join(v, " ")
		},
	}).
	Parse(defaultTemplate))

// Render turns data into a PKGBUILD. The function is deterministic: the same
// input always yields byte-identical output, which is what lets the caller
// detect "metadata changed but version did not".
func Render(d Data) (string, error) {
	if d.Pkgrel == 0 {
		d.Pkgrel = 1
	}
	if d.IconDir == "" {
		d.IconDir = "512x512"
	}
	if len(d.Arch) == 0 {
		d.Arch = []string{"x86_64"}
	}
	if len(d.Provides) == 0 {
		d.Provides = []string{d.PkgnameIn}
	}
	if len(d.Options) == 0 {
		d.Options = []string{"!strip"}
	}
	if strings.Contains(d.Pkgdesc, `"`) {
		return "", fmt.Errorf("pkgbuild: pkgdesc must not contain a double quote")
	}
	if strings.ContainsAny(d.Disname, `|"`) {
		return "", fmt.Errorf("pkgbuild: disname must not contain quotes or pipes")
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, struct {
		Data
		Arch0 string
	}{d, d.Arch[0]}); err != nil {
		return "", fmt.Errorf("pkgbuild: render: %w", err)
	}
	return buf.String(), nil
}

// Fields is the subset of a PKGBUILD that auto-aur compares between runs.
type Fields struct {
	Pkgname   string
	Pkgver    string
	Pkgdesc   string
	URL       string
	License   []string
	Depends   []string
	Conflicts []string
	Provides  []string
	Appimage  string
}

var (
	reAssign = regexp.MustCompile(`(?m)^\s*([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)
	reQuote  = regexp.MustCompile(`'([^']*)'`)
)

// Parse extracts Fields from an existing PKGBUILD. Unknown lines are ignored so
// hand-edited files do not break comparisons.
func Parse(src string) Fields {
	var f Fields
	arrays := map[string]*[]string{}
	for _, m := range reAssign.FindAllStringSubmatch(src, -1) {
		key, raw := m[1], strings.TrimSpace(m[2])
		list := func(dst *[]string) {
			arrays[key] = dst
			*dst = extractQuoted(raw)
		}
		switch key {
		case "pkgname":
			f.Pkgname = unquote(raw)
		case "pkgver":
			f.Pkgver = unquote(raw)
		case "pkgdesc":
			f.Pkgdesc = unquote(raw)
		case "url":
			f.URL = unquote(raw)
		case "_appimage":
			f.Appimage = unquote(raw)
		case "license":
			list(&f.License)
		case "depends":
			list(&f.Depends)
		case "conflicts":
			list(&f.Conflicts)
		case "provides":
			list(&f.Provides)
		}
	}
	return f
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1]
		}
		if s[0] == '(' {
			if inner := extractQuoted(s); len(inner) > 0 {
				return inner[0]
			}
		}
	}
	return s
}

func extractQuoted(s string) []string {
	var out []string
	for _, m := range reQuote.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	if out == nil {
		out = []string{}
	}
	return out
}

// Diff returns a human readable list of fields that differ between two sets.
func Diff(oldF, newF Fields) []string {
	var diffs []string
	add := func(name, a, b string) {
		if a != b {
			diffs = append(diffs, fmt.Sprintf("%s: %q -> %q", name, a, b))
		}
	}
	addList := func(name string, a, b []string) {
		if !sameList(a, b) {
			diffs = append(diffs, fmt.Sprintf("%s: %v -> %v", name, a, b))
		}
	}
	add("pkgdesc", oldF.Pkgdesc, newF.Pkgdesc)
	add("url", oldF.URL, newF.URL)
	add("_appimage", oldF.Appimage, newF.Appimage)
	addList("license", oldF.License, newF.License)
	addList("depends", oldF.Depends, newF.Depends)
	addList("conflicts", oldF.Conflicts, newF.Conflicts)
	addList("provides", oldF.Provides, newF.Provides)
	return diffs
}

func sameList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
