package openzwork

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rinnein/auto-aur/repo"
)

// TestSrcinfoMatchesMakepkg compares the generated .SRCINFO with makepkg's own
// output byte for byte. auto-aur writes .SRCINFO itself, so nothing in CI would
// notice a drift otherwise; makepkg is the reference implementation.
//
// The workflow runs this in an archlinux:base-devel container with
// AUTO_AUR_REQUIRE_MAKEPKG=1, which turns a missing makepkg into a failure
// instead of a skip.
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

	up := testUpstream()
	up.Description = "a description with an apostrophe's quote and $dollar"

	gen, err := (&Generator{}).Render(repo.Context{Upstream: up, Net: testNet(), Pkgrel: 2})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "PKGBUILD"), []byte(gen.PKGBUILD), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("makepkg", "--printsrcinfo")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("makepkg --printsrcinfo: %v\nPKGBUILD:\n%s", err, gen.PKGBUILD)
	}
	want := strings.TrimRight(string(out), "\n")
	have := strings.TrimRight(gen.Srcinfo, "\n")
	if have != want {
		t.Fatalf(".SRCINFO differs from makepkg --printsrcinfo\n--- tool ---\n%s\n--- makepkg ---\n%s", have, want)
	}
}
