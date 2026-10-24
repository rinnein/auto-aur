package szulife

import (
	"strings"
	"testing"

	"github.com/rinnein/auto-aur/repo"
)

// canaryAssets mirrors the real canary release of rinnein/your-szu-life.
func canaryAssets() []repo.Asset {
	return []repo.Asset{
		{Name: "your-szu-life_android_arm64_0.10.0-release.apk", URL: "https://e/apk", Size: 1},
		{Name: "your-szu-life_linux_x64_0.10.0-release.AppImage", URL: "https://e/appimage", Size: 2},
		{Name: "your-szu-life_linux_x64_0.10.0-release.deb", URL: "https://e/your-szu-life_linux_x64_0.10.0-release.deb", Size: 3},
		{Name: "your-szu-life_windows_x64_0.10.0-release.msi", URL: "https://e/msi", Size: 4},
	}
}

const testSum = "20fe986930b25fa290fa2e59cec678be170d1eb0a4829567967648fea884d1d1"

func testIO() repo.Net {
	return repo.Net{
		Checksum: func(string) (string, error) { return testSum, nil },
		Digest:   func(string, string) (string, error) { return testSum, nil },
	}
}

func TestVersionFromAsset(t *testing.T) {
	cases := []struct {
		name string
		want string
		ok   bool
	}{
		{"your-szu-life_linux_x64_0.10.0-release.deb", "0.10.0", true},
		{"app_linux_x64_1.2.3-release.deb", "1.2.3", true},
		{"app_linux_x64-release.deb", "", false},
		{"unrelated.tar.gz", "", false},
	}
	for _, tc := range cases {
		got, ok := versionFromAsset(tc.name)
		if ok != tc.ok || got != tc.want {
			t.Errorf("versionFromAsset(%q) = (%q,%v) want (%q,%v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestBinPicksDebAndVersion(t *testing.T) {
	g := &binGen{}
	up := repo.Upstream{Tag: "canary", Assets: canaryAssets()}

	ver, err := g.Pkgver(up)
	if err != nil {
		t.Fatal(err)
	}
	if ver != "0.10.0" {
		t.Fatalf("pkgver=%q want 0.10.0 (from the deb filename)", ver)
	}

	gen, err := g.Render(repo.Context{Upstream: up, Net: testIO()})
	if err != nil {
		t.Fatal(err)
	}
	if gen.Pkgrel != 1 {
		t.Fatalf("pkgrel=%d", gen.Pkgrel)
	}
	for _, want := range []string{
		"pkgname=your-szu-life-bin",
		"pkgver=0.10.0",
		"provides=('your-szu-life')",
		"conflicts=('your-szu-life-git' 'your-szu-life')",
		"sha256sums_x86_64=('" + testSum + "')",
		"_debname=\"your-szu-life_linux_x64_0.10.0-release.deb\"",
	} {
		if !strings.Contains(gen.PKGBUILD, want) {
			t.Errorf("missing %q in PKGBUILD:\n%s", want, gen.PKGBUILD)
		}
	}
}

func TestBinRejectsMissingDeb(t *testing.T) {
	g := &binGen{}
	up := repo.Upstream{Tag: "canary", Assets: []repo.Asset{{Name: "x.apk", URL: "u"}}}
	if _, err := g.Pkgver(up); err == nil {
		t.Fatal("expected an error when the release has no .deb")
	}
}

func TestBinRejectsBadDigest(t *testing.T) {
	g := &binGen{}
	up := repo.Upstream{Tag: "canary", Assets: canaryAssets()}
	bad := repo.Net{Digest: func(string, string) (string, error) { return "short", nil }}
	if _, err := g.Render(repo.Context{Upstream: up, Net: bad}); err == nil {
		t.Fatal("expected an error for a malformed sha256")
	}
}

func TestBinPkgrelPassedThrough(t *testing.T) {
	g := &binGen{}
	gen, err := g.Render(repo.Context{Upstream: repo.Upstream{Tag: "canary", Assets: canaryAssets()}, Pkgrel: 3, Net: testIO()})
	if err != nil {
		t.Fatal(err)
	}
	if gen.Pkgrel != 3 {
		t.Fatalf("pkgrel=%d want 3", gen.Pkgrel)
	}
	if !strings.Contains(gen.PKGBUILD, "pkgrel=3") {
		t.Fatalf("PKGBUILD missing bumped pkgrel:\n%s", gen.PKGBUILD)
	}
}

func TestGitPkgverMatchesPkgverFunction(t *testing.T) {
	g := &gitGen{}
	up := repo.Upstream{Commit: "572cce5eb41fbdfce9e27017e6183deed4cd624b", Date: "20261024"}
	ver, err := g.Pkgver(up)
	if err != nil {
		t.Fatal(err)
	}
	if ver != "20261024.r572cce5" {
		t.Fatalf("pkgver=%q", ver)
	}
	gen, err := g.Render(repo.Context{Upstream: up, Net: testIO()})
	if err != nil {
		t.Fatal(err)
	}
	// The published pkgver and the pkgver() function must agree, otherwise
	// makepkg rewrites the version to something the lock file never saw.
	if !strings.Contains(gen.PKGBUILD, "pkgver="+ver) {
		t.Errorf("PKGBUILD does not carry %q:\n%s", ver, gen.PKGBUILD)
	}
	for _, frag := range []string{"TZ=UTC", "format-local:%Y%m%d", "rev-parse --short=7"} {
		if !strings.Contains(gen.PKGBUILD, frag) {
			t.Errorf("pkgver() missing %q; it must reproduce %q", frag, ver)
		}
	}
}

func TestGitTracksMainBranch(t *testing.T) {
	g := &gitGen{}
	w := g.Wanted()
	if w.Branch != "main" || w.Tag != "" || w.Search != "" {
		t.Fatalf("git task must track the main branch, got %+v", w)
	}
	if w.Repo != "rinnein/your-szu-life" {
		t.Fatalf("repo=%q", w.Repo)
	}
}

func TestBinTracksCanaryTag(t *testing.T) {
	g := &binGen{}
	w := g.Wanted()
	if w.Tag != "canary" {
		t.Fatalf("bin task must pin the canary tag, got %+v", w)
	}
}

func TestBothPackagesConflictWithEachOther(t *testing.T) {
	up := repo.Upstream{Tag: "canary", Commit: "572cce5eb41fbdfce9e27017e6183deed4cd624b", Date: "20261024", Assets: canaryAssets()}

	bin, err := (&binGen{}).Render(repo.Context{Upstream: up, Net: testIO()})
	if err != nil {
		t.Fatal(err)
	}
	git, err := (&gitGen{}).Render(repo.Context{Upstream: up, Net: testIO()})
	if err != nil {
		t.Fatal(err)
	}

	// Mutual conflicts: each names the other.
	if !strings.Contains(bin.PKGBUILD, "'your-szu-life-git'") {
		t.Error("bin does not conflict with git")
	}
	if !strings.Contains(git.PKGBUILD, "'your-szu-life-bin'") {
		t.Error("git does not conflict with bin")
	}
	// Both provide the virtual name so paru -S your-szu-life offers a choice.
	for name, gen := range map[string]repo.Generated{"bin": bin, "git": git} {
		if !strings.Contains(gen.PKGBUILD, "provides=('your-szu-life')") {
			t.Errorf("%s does not provide the virtual name", name)
		}
		if !strings.Contains(gen.Srcinfo, "\tprovides = your-szu-life\n") {
			t.Errorf("%s SRCINFO lacks provides", name)
		}
	}
}

func TestNoRealAliasPackageIsPublished(t *testing.T) {
	// A real package named your-szu-life would shadow the providers and remove
	// the install-time choice, so the task set must not contain one.
	for _, name := range repo.Names() {
		g, ok := repo.Get(name)
		if !ok {
			continue
		}
		if g.Pkgname() == "your-szu-life" {
			t.Fatalf("task %q publishes a real your-szu-life package; it would shadow the providers", name)
		}
	}
}

func TestSrcinfoListsOneValuePerLine(t *testing.T) {
	gen, err := (&binGen{}).Render(repo.Context{Upstream: repo.Upstream{Tag: "canary", Assets: canaryAssets()}, Net: testIO()})
	if err != nil {
		t.Fatal(err)
	}
	// A malformed array form here would be read as one broken value.
	if strings.Contains(gen.Srcinfo, "' '") {
		t.Fatalf("SRCINFO contains a PKGBUILD array:\n%s", gen.Srcinfo)
	}
	for _, want := range []string{
		"\tconflicts = your-szu-life-git\n",
		"\tconflicts = your-szu-life\n",
		"\tsource_x86_64 = ",
		"\tsha256sums_x86_64 = " + testSum,
	} {
		if !strings.Contains(gen.Srcinfo, want) {
			t.Errorf("missing %q in SRCINFO:\n%s", want, gen.Srcinfo)
		}
	}
}

func TestGitSrcinfoUsesArchIndependentSource(t *testing.T) {
	gen, err := (&gitGen{}).Render(repo.Context{Upstream: repo.Upstream{Commit: "572cce5eb41fbdfce9e27017e6183deed4cd624b", Date: "20261024"}, Net: testIO()})
	if err != nil {
		t.Fatal(err)
	}
	// The PKGBUILD declares plain source=, so SRCINFO must too.
	if !strings.Contains(gen.Srcinfo, "\tsource = ") {
		t.Errorf("missing plain source field:\n%s", gen.Srcinfo)
	}
	if strings.Contains(gen.Srcinfo, "source_x86_64") {
		t.Errorf("git package must not use an arch-specific source:\n%s", gen.Srcinfo)
	}
	if !strings.Contains(gen.Srcinfo, "\tmakedepends = pnpm\n") {
		t.Errorf("makedepends missing from SRCINFO:\n%s", gen.Srcinfo)
	}
}
