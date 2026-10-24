package pkgbuild

import (
	"strings"
	"testing"
)

func base() Data {
	return Data{
		Maintainer: "Rinne <r@example.invalid>",
		Pkgname:    "demo-bin",
		PkgnameIn:  "demo",
		Appimage:   "demo-1.2.3-linux-x86_64.appimage",
		Pkgver:     "v1.2.3",
		Pkgrel:     1,
		Disname:    "Demo",
		Pkgdesc:    "a demo app",
		URL:        "https://example.invalid",
		License:    []string{"MIT"},
		Depends:    []string{"glibc", "fuse2"},
		Provides:   []string{"demo"},
		Options:    []string{"!strip"},
		SourceURL:  "https://example.invalid/demo.appimage",
		SHA1:       "abcdef0123456789",
		IconDir:    "512x512",

		SetDesktopName: true,
	}
}

func TestRenderGolden(t *testing.T) {
	got, err := Render(base())
	if err != nil {
		t.Fatal(err)
	}
	// Shape assertions rather than a full golden file: the template is the
	// contract, and every one of these lines has bitten this repo before.
	want := []string{
		"# Maintainer: Rinne <r@example.invalid>",
		"_pkgname=demo",
		"_appimage=demo-1.2.3-linux-x86_64.appimage",
		"pkgname=demo-bin",
		"pkgver=v1.2.3",
		"pkgrel=1",
		`pkgdesc="a demo app"`,
		"arch=('x86_64')",
		"license=('MIT')",
		"depends=('glibc' 'fuse2')",
		"provides=('demo')",
		"options=(!strip)",
		"source_x86_64=(\"${_appimage}::https://example.invalid/demo.appimage\")",
		`noextract=("${_appimage}")`,
		"sha1sums_x86_64=('abcdef0123456789')",
		"prepare() {",
		"build() {",
		"package() {",
		"usr/share/icons/hicolor/512x512/apps/${_pkgname}.png",
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in rendered PKGBUILD:\n%s", w, got)
		}
	}
}

func TestRenderDeterministic(t *testing.T) {
	a, err := Render(base())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Render(base())
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("Render is not deterministic; metadata diffing would flap")
	}
}

func TestRenderDesktopNameOptional(t *testing.T) {
	d := base()
	d.SetDesktopName = false
	got, err := Render(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "Name=Demo") {
		t.Fatalf("Name= rewrite should be omitted when SetDesktopName is false:\n%s", got)
	}
}

func TestRenderRejectsQuotes(t *testing.T) {
	d := base()
	d.Pkgdesc = `bad "quote`
	if _, err := Render(d); err == nil {
		t.Fatal("expected error for quote in pkgdesc")
	}
}

func TestParseRoundTrip(t *testing.T) {
	d := base()
	src, err := Render(d)
	if err != nil {
		t.Fatal(err)
	}
	f := Parse(src)
	if f.Pkgname != "demo-bin" || f.Pkgver != "v1.2.3" || f.Appimage != d.Appimage {
		t.Fatalf("parse mismatch: %+v", f)
	}
	t.Logf("parsed: pkgname=%q pkgver=%q appimage=%q depends=%v provides=%v", f.Pkgname, f.Pkgver, f.Appimage, f.Depends, f.Provides)
}

func TestDiff(t *testing.T) {
	a := Fields{Depends: []string{"glibc"}}
	b := Fields{Depends: []string{"glibc", "fuse2"}}
	if len(Diff(a, b)) == 0 {
		t.Fatal("expected a diff for changed depends")
	}
	if len(Diff(a, a)) != 0 {
		t.Fatal("identical fields must not diff")
	}
}
