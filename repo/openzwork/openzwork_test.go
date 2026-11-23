package openzwork

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rinnein/auto-aur/repo"
)

const (
	assetSum  = "292eb3393184957ad036aaada4159e558b00db4c1bf067dc02e6d7c725ff8860"
	sumsSum   = "1111111111111111111111111111111111111111111111111111111111111111"
	digestSum = "2222222222222222222222222222222222222222222222222222222222222222"
)

// releaseAssets mirrors the real release of BlueSkyXN/OpenZWork.
func releaseAssets() []repo.Asset {
	return []repo.Asset{
		{Name: "OpenZWork-3.18.2+openzwork.4-linux-amd64.deb", URL: "https://e/deb", Size: 1},
		{Name: "OpenZWork-3.18.2+openzwork.4-linux-x64.pkg.tar.zst", URL: "https://e/pkg", Size: 2, SHA256: assetSum},
		{Name: "OpenZWork-3.18.2+openzwork.4-linux-x86_64.AppImage", URL: "https://e/appimage", Size: 3},
		{Name: "SHA256SUMS", URL: "https://e/SHA256SUMS", Size: 4},
	}
}

func sumsBody() string {
	return sumsSum + "  OpenZWork-3.18.2+openzwork.4-linux-x64.pkg.tar.zst\n" +
		sumsSum + "  OpenZWork-3.18.2+openzwork.4-linux-amd64.deb\n"
}

func testUpstream() repo.Upstream {
	return repo.Upstream{
		Tag:     "v3.18.2+openzwork.4",
		Version: "3.18.2+openzwork.4",
		Assets:  releaseAssets(),
	}
}

func testNet() repo.Net {
	return repo.Net{
		Checksum: func(string) (string, error) { return sumsBody(), nil },
		Digest:   func(string, string) (string, error) { return digestSum, nil },
	}
}

func TestVersionFromAsset(t *testing.T) {
	cases := []struct {
		name string
		want string
		ok   bool
	}{
		{"OpenZWork-3.18.2+openzwork.4-linux-x64.pkg.tar.zst", "3.18.2+openzwork.4", true},
		{"OpenZWork-3.17.1+openzwork.2-linux-x64.pkg.tar.zst", "3.17.1+openzwork.2", true},
		{"OpenZWork-3.18.2+openzwork.4-linux-amd64.deb", "", false},
		{"whatever.pkg.tar.zst", "", false},
	}
	for _, tc := range cases {
		got, ok := versionFromAsset(tc.name)
		if ok != tc.ok || got != tc.want {
			t.Errorf("versionFromAsset(%q) = (%q,%v) want (%q,%v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestPkgverPrefersReleaseVersion(t *testing.T) {
	ver, err := (&Generator{}).Pkgver(testUpstream())
	if err != nil {
		t.Fatal(err)
	}
	if ver != "3.18.2+openzwork.4" {
		t.Fatalf("pkgver=%q want 3.18.2+openzwork.4", ver)
	}
}

func TestPkgverFallsBackToAssetName(t *testing.T) {
	up := testUpstream()
	up.Version = ""
	ver, err := (&Generator{}).Pkgver(up)
	if err != nil {
		t.Fatal(err)
	}
	if ver != "3.18.2+openzwork.4" {
		t.Fatalf("pkgver=%q want the version from the asset name", ver)
	}
}

func TestPkgverRejectsHyphen(t *testing.T) {
	up := testUpstream()
	up.Version = "3.18.2-openzwork.4"
	if _, err := (&Generator{}).Pkgver(up); err == nil {
		t.Fatal("a hyphen cannot appear in pkgver, expected an error")
	}
}

func TestRenderRequiresArchAsset(t *testing.T) {
	up := repo.Upstream{Tag: "v3.18.2+openzwork.4", Version: "3.18.2+openzwork.4",
		Assets: []repo.Asset{{Name: "OpenZWork-3.18.2+openzwork.4-linux-amd64.deb", URL: "u"}}}
	if _, err := (&Generator{}).Render(repo.Context{Upstream: up, Net: testNet()}); err == nil {
		t.Fatal("expected an error when the release has no .pkg.tar.zst")
	}
}

func TestRenderUsesSumsFile(t *testing.T) {
	gen, err := (&Generator{}).Render(repo.Context{Upstream: testUpstream(), Net: testNet()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gen.PKGBUILD, "sha256sums_x86_64=('"+sumsSum+"')") {
		t.Fatalf("checksum must come from SHA256SUMS, got:\n%s", gen.PKGBUILD)
	}
	for _, want := range []string{
		"pkgname=openzwork-electron-bin",
		"pkgver=3.18.2+openzwork.4",
		"pkgrel=1",
		"license=('GPL-3.0-only')",
		"provides=('openzwork')",
		"conflicts=('openzwork')",
		"depends=('electron44' 'python' 'python-reportlab' 'python-lxml' 'python-pillow' 'python-defusedxml' 'libstdc++' 'libgcc' 'nodejs' 'ripgrep' 'xdg-utils')",
		"makedepends=('asar')",
		"_archive=\"OpenZWork-3.18.2+openzwork.4-linux-x64.pkg.tar.zst\"",
		"source_x86_64=(\"${_archive}::https://e/pkg\")",
		"noextract=(\"${_archive}\")",
		// The app runs on the system Electron, the bundled one is discarded.
		"exec electron44 \"${_RUNNAME}\"",
		// app.asar is repacked and native modules stay unpacked.
		"asar e \"${_appdir}/resources/app.asar\"",
		"--unpack='*.node'",
		"process.resourcesPath|'/usr/lib/openzwork'",
		// The desktop entry points at the launcher.
		"sed -i \"s|/opt/OpenZWork/||g\"",
	} {
		if !strings.Contains(gen.PKGBUILD, want) {
			t.Errorf("missing %q in PKGBUILD:\n%s", want, gen.PKGBUILD)
		}
	}
	// The bundled Electron runtime must not be installed.
	if strings.Contains(gen.PKGBUILD, "install -Dm755 \"${_appdir}") {
		t.Errorf("the bundled Electron runtime must not be installed:\n%s", gen.PKGBUILD)
	}
	// No template placeholder may survive into the published file.
	for _, leftover := range []string{"@install_dir@", "@launcher@", "@electronversion@"} {
		if strings.Contains(gen.PKGBUILD, leftover) {
			t.Errorf("placeholder %q left in PKGBUILD:\n%s", leftover, gen.PKGBUILD)
		}
	}
}

func TestChecksumFallsBackToRecordedDigest(t *testing.T) {
	net := testNet()
	net.Checksum = func(string) (string, error) { return "", os.ErrDeadlineExceeded }
	gen, err := (&Generator{}).Render(repo.Context{Upstream: testUpstream(), Net: net})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gen.PKGBUILD, assetSum) {
		t.Fatalf("expected the release digest as fallback:\n%s", gen.PKGBUILD)
	}
}

func TestChecksumFallsBackToDownload(t *testing.T) {
	up := testUpstream()
	kept := up.Assets[:0]
	for _, a := range up.Assets {
		if a.Name == "SHA256SUMS" {
			continue // only the download can provide the checksum
		}
		a.SHA256 = ""
		kept = append(kept, a)
	}
	up.Assets = kept

	gen, err := (&Generator{}).Render(repo.Context{Upstream: up, Net: testNet()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gen.PKGBUILD, digestSum) {
		t.Fatalf("expected the streamed digest as fallback:\n%s", gen.PKGBUILD)
	}
}

func TestChecksumFromSumsIgnoresUnrelatedLines(t *testing.T) {
	if got := sumFromSums("not a line\n"+sumsSum+" *OpenZWork-3.18.2+openzwork.4-linux-x64.pkg.tar.zst\n", "OpenZWork-3.18.2+openzwork.4-linux-x64.pkg.tar.zst"); got != sumsSum {
		t.Fatalf("sumFromSums=%q", got)
	}
	if got := sumFromSums("", "x"); got != "" {
		t.Fatalf("sumFromSums on empty input = %q", got)
	}
}

func TestDescriptionComesFromUpstream(t *testing.T) {
	const about = "openzwork's about $text"
	up := testUpstream()
	up.Description = about
	gen, err := (&Generator{}).Render(repo.Context{Upstream: up, Net: testNet()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gen.PKGBUILD, `pkgdesc="openzwork's about \$text"`) {
		t.Errorf("PKGBUILD does not carry the About text:\n%s", gen.PKGBUILD)
	}
	if !strings.Contains(gen.Srcinfo, "\tpkgdesc = "+about+"\n") {
		t.Errorf("SRCINFO does not carry the About text:\n%s", gen.Srcinfo)
	}
}

func TestEmptyDescriptionFallsBack(t *testing.T) {
	gen, err := (&Generator{}).Render(repo.Context{Upstream: testUpstream(), Net: testNet()})
	if err != nil {
		t.Fatal(err)
	}
	if gen.Fields.Pkgdesc != fallbackPkgdesc {
		t.Fatalf("pkgdesc=%q want %q", gen.Fields.Pkgdesc, fallbackPkgdesc)
	}
	if gen.Note == "" {
		t.Fatal("the fallback description should be reported in the summary")
	}
}

func TestPkgrelPassedThrough(t *testing.T) {
	gen, err := (&Generator{}).Render(repo.Context{Upstream: testUpstream(), Pkgrel: 3, Net: testNet()})
	if err != nil {
		t.Fatal(err)
	}
	if gen.Pkgrel != 3 || !strings.Contains(gen.PKGBUILD, "pkgrel=3") {
		t.Fatalf("pkgrel=%d:\n%s", gen.Pkgrel, gen.PKGBUILD)
	}
	if !strings.Contains(gen.Srcinfo, "\tpkgrel = 3\n") {
		t.Fatalf("SRCINFO lacks pkgrel 3:\n%s", gen.Srcinfo)
	}
}

func TestSrcinfoUsesArchSpecificSource(t *testing.T) {
	gen, err := (&Generator{}).Render(repo.Context{Upstream: testUpstream(), Net: testNet()})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"pkgbase = openzwork-electron-bin\n",
		"\tarch = x86_64\n",
		"\tlicense = GPL-3.0-only\n",
		"\tmakedepends = asar\n",
		"\tdepends = electron44\n",
		"\tprovides = openzwork\n",
		"\tconflicts = openzwork\n",
		"\tnoextract = OpenZWork-3.18.2+openzwork.4-linux-x64.pkg.tar.zst\n",
		"\toptions = !strip\n",
		"\tsource_x86_64 = OpenZWork-3.18.2+openzwork.4-linux-x64.pkg.tar.zst::https://e/pkg\n",
		"\tsha256sums_x86_64 = " + sumsSum + "\n",
		"\npkgname = openzwork-electron-bin\n",
	} {
		if !strings.Contains(gen.Srcinfo, want) {
			t.Errorf("missing %q in SRCINFO:\n%s", want, gen.Srcinfo)
		}
	}
	// The PKGBUILD uses the arch-specific source arrays, so .SRCINFO must not
	// declare the arch-independent ones.
	for _, unwanted := range []string{"\tsource = ", "\tsha256sums = ", "' '"} {
		if strings.Contains(gen.Srcinfo, unwanted) {
			t.Errorf("unexpected %q in SRCINFO:\n%s", unwanted, gen.Srcinfo)
		}
	}
}

func TestLauncherParsesAsBash(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "launcher")
	if err := os.WriteFile(p, []byte(launcher()), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("bash", "-n", p).CombinedOutput(); err != nil {
		t.Fatalf("launcher does not parse: %v\n%s", err, out)
	}
}

func TestGeneratedPkgbuildParsesAsBash(t *testing.T) {
	gen, err := (&Generator{}).Render(repo.Context{Upstream: testUpstream(), Net: testNet()})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "PKGBUILD")
	if err := os.WriteFile(p, []byte(gen.PKGBUILD), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("bash", "-n", p).CombinedOutput(); err != nil {
		t.Fatalf("PKGBUILD does not parse: %v\n%s\n---\n%s", err, out, gen.PKGBUILD)
	}
}

func TestWantedTracksUpstreamRepo(t *testing.T) {
	w := (&Generator{}).Wanted()
	if w.Repo != "BlueSkyXN/OpenZWork" {
		t.Fatalf("repo=%q", w.Repo)
	}
	if w.Tag != "" || w.Branch != "" || w.Search != "" {
		t.Fatalf("openzwork-electron-bin must track the latest release, got %+v", w)
	}
	if !w.RepoDescription {
		t.Fatal("pkgdesc should come from the repository About text")
	}
}
