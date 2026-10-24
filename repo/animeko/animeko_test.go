package animeko

import (
	"strings"
	"testing"

	"github.com/rinnein/auto-aur/repo"
)

// realAssets mirrors the asset names of an actual open-ani/animeko release.
func realAssets(tag string) []repo.Asset {
	base := "ani-" + tag
	return []repo.Asset{
		{Name: base + "-x86_64.apk", URL: "https://x/apk"},
		{Name: base + "-linux-x86_64.appimage", URL: "https://x/" + base + "-linux-x86_64.appimage"},
		{Name: base + "-linux-x86_64.appimage.sha1", URL: "https://x/appimage.sha1"},
		{Name: base + "-linux-x86_64.appimage.zsync", URL: "https://x/zsync"},
		{Name: base + "-macos-aarch64.dmg", URL: "https://x/dmg"},
	}
}

// fakeSum stands in for downloading the upstream .sha1 asset.
func fakeSum(string) (string, error) { return "0f995f61696ecb8f791e04833c03ee684f6f6a18\n", nil }

func TestPkgverNormalisation(t *testing.T) {
	if got := Pkgver("v6.4.0-beta02"); got != "v6.4.0_beta02" {
		t.Fatalf("got %q", got)
	}
	if got := Pkgver("v6.4.0"); got != "v6.4.0" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderSelectsAppimageAndSum(t *testing.T) {
	g := New(Channel{Task: "t", Pkgname: "animeko-appimage", Disname: "Animeko", Depends: previewDeps, IconDir: "512x512"})
	gen, err := g.Render(repo.Release{TagName: "v6.4.0-beta02", Assets: realAssets("6.4.0-beta02")}, fakeSum)
	if err != nil {
		t.Fatal(err)
	}
	if gen.Source != "ani-6.4.0-beta02-linux-x86_64.appimage::https://x/ani-6.4.0-beta02-linux-x86_64.appimage" {
		t.Fatalf("source = %q", gen.Source)
	}
	// The digest comes from the .sha1 asset's content, not its filename.
	if !strings.Contains(gen.PKGBUILD, "sha1sums_x86_64=('0f995f61696ecb8f791e04833c03ee684f6f6a18')") {
		t.Fatalf("checksum missing:\n%s", gen.PKGBUILD)
	}
	if !strings.Contains(gen.PKGBUILD, "pkgver=v6.4.0_beta02") {
		t.Fatalf("pkgver not normalised:\n%s", gen.PKGBUILD)
	}
	if !strings.Contains(gen.PKGBUILD, "usr/share/icons/hicolor/512x512/apps") {
		t.Fatalf("icon dir wrong:\n%s", gen.PKGBUILD)
	}
}

func TestRenderRejectsReleaseWithoutAppimage(t *testing.T) {
	g := New(Channel{Task: "t", Pkgname: "x"})
	_, err := g.Render(repo.Release{TagName: "v1", Assets: []repo.Asset{{Name: "a.apk", URL: "u"}}}, fakeSum)
	if err == nil || !strings.Contains(err.Error(), "appimage") {
		t.Fatalf("want appimage error, got %v", err)
	}
}

func TestChecksumMustBeAValidDigest(t *testing.T) {
	g := New(Channel{Task: "t", Pkgname: "x"})
	bad := func(string) (string, error) { return "not-a-digest", nil }
	if _, err := g.Render(repo.Release{TagName: "v1", Assets: realAssets("1")}, bad); err == nil {
		t.Fatal("expected an error for a malformed checksum")
	}
	empty := func(string) (string, error) { return "", nil }
	if _, err := g.Render(repo.Release{TagName: "v1", Assets: realAssets("1")}, empty); err == nil {
		t.Fatal("expected an error for an empty checksum")
	}
}

func TestChecksumFetchedFromSumAsset(t *testing.T) {
	g := New(Channel{Task: "t", Pkgname: "x"})
	var asked string
	spy := func(u string) (string, error) {
		asked = u
		return "0F995F61696ECB8F791E04833C03EE684F6F6A18  some-file\n", nil
	}
	gen, err := g.Render(repo.Release{TagName: "v1", Assets: realAssets("1")}, spy)
	if err != nil {
		t.Fatal(err)
	}
	// The .sha1 asset must be fetched, not the .appimage itself.
	if !strings.HasSuffix(asked, "appimage.sha1") {
		t.Fatalf("fetched wrong asset: %q", asked)
	}
	// Digest normalisation keeps only the hash and lowercases it.
	if !strings.Contains(gen.PKGBUILD, "'0f995f61696ecb8f791e04833c03ee684f6f6a18'") {
		t.Fatalf("digest not normalised:\n%s", gen.PKGBUILD)
	}
}

func TestStableHasNoChannelSearch(t *testing.T) {
	g, ok := repo.Get("animeko")
	if !ok {
		t.Fatal("stable task not registered")
	}
	if w := g.Wanted(); w.Search != "" || w.Tag != "" || w.Repo != "open-ani/animeko" {
		t.Fatalf("stable should track latest, got %+v", w)
	}
}

func TestChannelsRegistered(t *testing.T) {
	for _, name := range []string{"animeko", "animeko-beta", "animeko-alpha"} {
		if !repo.Has(name) {
			t.Errorf("task %q not registered", name)
		}
	}
}

func TestSrcinfoShape(t *testing.T) {
	g := New(Channel{Task: "t", Pkgname: "animeko-appimage-alpha", Disname: "A", Depends: previewDeps, IconDir: "512x512"})
	gen, err := g.Render(repo.Release{TagName: "v6.4.0-alpha01", Assets: realAssets("6.4.0-alpha01")}, fakeSum)
	if err != nil {
		t.Fatal(err)
	}
	out := g.Srcinfo(gen)
	for _, want := range []string{
		"pkgbase = animeko-appimage-alpha",
		"\tpkgver = v6.4.0_alpha01",
		"\tdepends = vlc-plugin-ffmpeg",
		"\tsource_x86_64 = ani-6.4.0-alpha01-linux-x86_64.appimage::",
		"\npkgname = animeko-appimage-alpha\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
