package yourszulife

import (
	"os"
	"os/exec"
	"path/filepath"
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
	up := repo.Upstream{Tag: "canary", Version: "0.10.0", Assets: canaryAssets()}

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
	// The version comes from the release, so Pkgver succeeds; Render is what
	// must fail when the release carries no .deb.
	up := repo.Upstream{Tag: "canary", Version: "0.10.0", Assets: []repo.Asset{{Name: "x.apk", URL: "u"}}}
	if _, err := g.Pkgver(up); err != nil {
		t.Fatalf("version should come from the release, got %v", err)
	}
	if _, err := g.Render(repo.Context{Upstream: up, Net: testIO()}); err == nil {
		t.Fatal("expected an error when the release has no .deb")
	}
}

// TestBinVersionFallsBackToAssetName: the version must still come from the
// release when its name/body do not carry one, because the asset filename is
// generated from the same package.json.
func TestBinVersionFallsBackToAssetName(t *testing.T) {
	g := &binGen{}
	ver, err := g.Pkgver(repo.Upstream{Tag: "canary", Assets: canaryAssets()})
	if err != nil {
		t.Fatal(err)
	}
	if ver != "0.10.0" {
		t.Fatalf("pkgver=%q want 0.10.0 from the asset name", ver)
	}
}

// TestBinVersionPrefersReleaseOverAssetName: the release metadata wins, so a
// stale filename cannot freeze the package.
func TestBinVersionPrefersReleaseOverAssetName(t *testing.T) {
	g := &binGen{}
	assets := []repo.Asset{{Name: "your-szu-life_linux_x64_0.9.9-release.deb", URL: "u", SHA256: testSum}}
	ver, err := g.Pkgver(repo.Upstream{Tag: "canary", Version: "0.10.0", Assets: assets})
	if err != nil {
		t.Fatal(err)
	}
	if ver != "0.10.0" {
		t.Fatalf("pkgver=%q want the release version 0.10.0", ver)
	}
}

// TestGitDoesNotDependOnRelease: a VCS package follows its branch head, so it
// must render correctly with no release information at all.
func TestGitDoesNotDependOnRelease(t *testing.T) {
	g := &gitGen{}
	up := repo.Upstream{Commit: "572cce5eb41fbdfce9e27017e6183deed4cd624b", Version: "0.10.0"}
	gen, err := g.Render(repo.Context{Upstream: up, Net: testIO()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gen.PKGBUILD, "#branch=main") {
		t.Errorf("git package must build from the main branch:\n%s", gen.PKGBUILD)
	}
	if !strings.Contains(gen.PKGBUILD, "pkgver=0.10.0.r572cce5") {
		t.Errorf("unexpected pkgver:\n%s", gen.PKGBUILD)
	}
	// No release-only fields may leak in.
	if strings.Contains(gen.PKGBUILD, "releases/download") {
		t.Error("git package must not reference release assets")
	}
}

func TestBinRejectsBadDigest(t *testing.T) {
	g := &binGen{}
	up := repo.Upstream{Tag: "canary", Version: "0.10.0", Assets: canaryAssets()}
	bad := repo.Net{Digest: func(string, string) (string, error) { return "short", nil }}
	if _, err := g.Render(repo.Context{Upstream: up, Net: bad}); err == nil {
		t.Fatal("expected an error for a malformed sha256")
	}
}

func TestBinPkgrelPassedThrough(t *testing.T) {
	g := &binGen{}
	gen, err := g.Render(repo.Context{Upstream: repo.Upstream{Tag: "canary", Version: "0.10.0", Assets: canaryAssets()}, Pkgrel: 3, Net: testIO()})
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
	up := repo.Upstream{Commit: "572cce5eb41fbdfce9e27017e6183deed4cd624b", Date: "20261024", Version: "0.10.0"}
	ver, err := g.Pkgver(up)
	if err != nil {
		t.Fatal(err)
	}
	if ver != "0.10.0.r572cce5" {
		t.Fatalf("pkgver=%q want 0.10.0.r572cce5 (upstream version + commit)", ver)
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
	// pkgver() must rebuild the same string from the checked-out source: the
	// version from package.json plus the same short commit.
	for _, frag := range []string{"package.json", "rev-parse --short=7", `printf '%s.r%s`} {
		if !strings.Contains(gen.PKGBUILD, frag) {
			t.Errorf("pkgver() missing %q; it must reproduce %q", frag, ver)
		}
	}
	if strings.Contains(gen.PKGBUILD, "TZ=UTC") {
		t.Error("pkgver() should no longer derive the version from the commit date")
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
	up := repo.Upstream{Tag: "canary", Commit: "572cce5eb41fbdfce9e27017e6183deed4cd624b", Date: "20261024", Version: "0.10.0", Assets: canaryAssets()}

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
	gen, err := (&binGen{}).Render(repo.Context{Upstream: repo.Upstream{Tag: "canary", Version: "0.10.0", Assets: canaryAssets()}, Net: testIO()})
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
	gen, err := (&gitGen{}).Render(repo.Context{Upstream: repo.Upstream{Commit: "572cce5eb41fbdfce9e27017e6183deed4cd624b", Date: "20261024", Version: "0.10.0"}, Net: testIO()})
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

// TestBundledAria2cIsNotInstalled guards a real-world install failure:
//
//	your-szu-life-bin: filesystem conflict on /usr/bin/aria2c (owned by aria2)
//
// The .deb ships Debian's aria2c frontend, which links libaria2.so.0 that the
// .deb does not include. Installing it both collides with the aria2 package and
// cannot work on its own, so package() must delete it.
func TestBundledAria2cIsNotInstalled(t *testing.T) {
	up := repo.Upstream{
		Tag:     "canary",
		Commit:  "572cce5eb41fbdfce9e27017e6183deed4cd624b",
		Date:    "20261024",
		Version: "0.10.0",
		Assets:  canaryAssets(),
	}
	for name, g := range map[string]repo.Generator{
		"bin": &binGen{},
		"git": &gitGen{},
	} {
		gen, err := g.Render(repo.Context{Upstream: up, Net: testIO()})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(gen.PKGBUILD, `rm -f "${pkgdir}/usr/bin/aria2c"`) {
			t.Errorf("%s package() does not remove the bundled aria2c:\n%s", name, gen.PKGBUILD)
		}
		// The real aria2c must come from the aria2 package instead.
		if !strings.Contains(gen.PKGBUILD, "'aria2'") {
			t.Errorf("%s does not depend on aria2, so aria2c would be missing", name)
		}
		if !strings.Contains(gen.Srcinfo, "\tdepends = aria2\n") {
			t.Errorf("%s SRCINFO lacks the aria2 dependency", name)
		}
	}
}

func TestQuotingSurvivesShell(t *testing.T) {
	nasty := []string{
		`泥深 - 你的SZU助手`,
		`it's a "quoted" app`,
		`costs $5 and ` + "`backtick`",
		`back\slash and $HOME`,
		`a&b | c; d`,
	}
	for _, d := range nasty {
		gen, err := (&binGen{}).Render(repo.Context{
			Upstream: repo.Upstream{Tag: "canary", Version: "0.10.0", Assets: canaryAssets(), Description: d},
			Net:      testIO(),
		})
		if err != nil {
			t.Fatalf("%q: %v", d, err)
		}
		dir := t.TempDir()
		p := filepath.Join(dir, "PKGBUILD")
		if err := os.WriteFile(p, []byte(gen.PKGBUILD), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("bash", "-n", p).CombinedOutput()
		if err != nil {
			t.Errorf("PKGBUILD does not parse for %q: %v\n%s\n---\n%s", d, err, out, gen.PKGBUILD)
			continue
		}
		// Syntax alone is not enough: the value must survive shell expansion
		// byte for byte, otherwise $ and backticks would be substituted away.
		got, err := exec.Command("bash", "-c",
			`set -e; source "$1" >/dev/null 2>&1; printf '%s' "$pkgdesc"`, "bash", p).Output()
		if err != nil {
			t.Errorf("cannot source PKGBUILD for %q: %v", d, err)
			continue
		}
		if want := sanitizeDesc(d); string(got) != want {
			t.Errorf("pkgdesc round-trip mismatch\n got: %q\nwant: %q", string(got), want)
		}
	}
}

// TestPkgdescComesFromUpstream covers the requirement that the AUR description
// is the repository's About text rather than a hardcoded string.
func TestPkgdescComesFromUpstream(t *testing.T) {
	const about = "泥深 - 你的SZU助手"
	up := repo.Upstream{
		Tag:         "canary",
		Commit:      "572cce5eb41fbdfce9e27017e6183deed4cd624b",
		Date:        "20261024",
		Version:     "0.10.0",
		Assets:      canaryAssets(),
		Description: about,
	}
	for name, g := range map[string]repo.Generator{"bin": &binGen{}, "git": &gitGen{}} {
		gen, err := g.Render(repo.Context{Upstream: up, Net: testIO()})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(gen.PKGBUILD, `pkgdesc="`+about+`"`) {
			t.Errorf("%s PKGBUILD does not carry the About text:\n%s", name, gen.PKGBUILD)
		}
		if !strings.Contains(gen.Srcinfo, "\tpkgdesc = "+about+"\n") {
			t.Errorf("%s SRCINFO does not carry the About text:\n%s", name, gen.Srcinfo)
		}
	}
}

func TestPkgdescSanitisedToSingleLine(t *testing.T) {
	gen, err := (&binGen{}).Render(repo.Context{
		Upstream: repo.Upstream{Tag: "canary", Version: "0.10.0", Assets: canaryAssets(), Description: "line one\n  line two\ttabbed  "},
		Net:      testIO(),
	})
	if err != nil {
		t.Fatal(err)
	}
	// A newline in pkgdesc would break both files.
	if strings.Contains(gen.Fields.Pkgdesc, "\n") {
		t.Fatalf("pkgdesc kept a newline: %q", gen.Fields.Pkgdesc)
	}
	if gen.Fields.Pkgdesc != "line one line two tabbed" {
		t.Fatalf("pkgdesc=%q", gen.Fields.Pkgdesc)
	}
}

func TestEmptyDescriptionFallsBack(t *testing.T) {
	gen, err := (&binGen{}).Render(repo.Context{
		Upstream: repo.Upstream{Tag: "canary", Version: "0.10.0", Assets: canaryAssets()},
		Net:      testIO(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if gen.Fields.Pkgdesc != fallbackPkgdesc {
		t.Fatalf("pkgdesc=%q want %q", gen.Fields.Pkgdesc, fallbackPkgdesc)
	}
	// The fallback must be reported, not silent.
	if gen.Note == "" {
		t.Fatal("fallback description should be noted in the summary")
	}
}

func TestWantedRequestsRepoDescription(t *testing.T) {
	for name, g := range map[string]repo.Generator{"bin": &binGen{}, "git": &gitGen{}} {
		if !g.Wanted().RepoDescription {
			t.Errorf("%s does not request the repository description", name)
		}
	}
}
