package yourszulife

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rinnein/auto-aur/repo"
)

// TestSrcinfoMatchesMakepkg is the invariant that replaced the workflow's
// makepkg step. The tool writes .SRCINFO itself, so nothing in CI would notice
// if that output drifted from what a packager (and the AUR) expects. makepkg is
// the reference implementation, so compare against it byte for byte.
//
// makepkg only exists on Arch, which ubuntu-latest is not, so the workflow runs
// this test in an archlinux:base-devel job with AUTO_AUR_REQUIRE_MAKEPKG=1. That
// variable turns a missing tool into a failure: a silently skipped check would
// defeat the point of having it.
func TestSrcinfoMatchesMakepkg(t *testing.T) {
	required := os.Getenv("AUTO_AUR_REQUIRE_MAKEPKG") != ""
	missing := func(why string) {
		if required {
			t.Fatalf("AUTO_AUR_REQUIRE_MAKEPKG is set but %s", why)
		}
		t.Skip(why)
	}
	if _, err := exec.LookPath("makepkg"); err != nil {
		missing("makepkg is not installed")
	}
	if os.Geteuid() == 0 {
		// makepkg refuses to run as root by design.
		missing("makepkg refuses to run as root")
	}

	up := repo.Upstream{
		Tag:         "canary",
		Commit:      "572cce5eb41fbdfce9e27017e6183deed4cd624b",
		Date:        "20261024",
		Version:     "0.10.0",
		Assets:      canaryAssets(),
		Description: "a description with an apostrophe's quote and $dollar",
	}
	for name, gen := range map[string]repo.Generator{"bin": &binGen{}, "git": &gitGen{}} {
		gen := gen
		t.Run(name, func(t *testing.T) {
			got, err := gen.Render(repo.Context{Upstream: up, Net: testIO()})
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "PKGBUILD"), []byte(got.PKGBUILD), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("makepkg", "--printsrcinfo")
			cmd.Dir = dir
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("makepkg --printsrcinfo: %v\nPKGBUILD:\n%s", err, got.PKGBUILD)
			}
			// .SRCINFO ends with the trailing pkgname block and no blank line, so
			// compare with trailing whitespace trimmed on both sides.
			want := strings.TrimRight(string(out), "\n")
			have := strings.TrimRight(got.Srcinfo, "\n")
			if have != want {
				t.Fatalf(".SRCINFO differs from makepkg --printsrcinfo\n--- tool ---\n%s\n--- makepkg ---\n%s", have, want)
			}
		})
	}
}
