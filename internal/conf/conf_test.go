package conf

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "conf.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDefaults(t *testing.T) {
	p := write(t, `{"maintainer":"A <a@b.c>","packages":[]}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.AUR.Host != DefaultAURHost || c.AUR.User != DefaultAURUser || c.AUR.Branch != DefaultAURBranch {
		t.Fatalf("unexpected aur defaults: %+v", c.AUR)
	}
	if !c.CreateMissing() {
		t.Fatal("createOnMissing should default to true")
	}
	if c.MaintainerName() != "A" || c.MaintainerEmail() != "a@b.c" {
		t.Fatalf("maintainer split failed: %q / %q", c.MaintainerName(), c.MaintainerEmail())
	}
}

func TestLoadRequiresMaintainer(t *testing.T) {
	if _, err := Load(write(t, `{"packages":[]}`)); err == nil {
		t.Fatal("expected error for missing maintainer")
	}
}

func TestDuplicateTaskRejected(t *testing.T) {
	_, err := Load(write(t, `{"maintainer":"A <a@b.c>","packages":[{"task":"x"},{"task":"x"}]}`))
	if err == nil {
		t.Fatal("expected duplicate task error")
	}
}

func TestDisabledReason(t *testing.T) {
	c := &Config{Disabled: []string{"animeko-*", "exact"}}
	cases := []struct {
		task string
		want bool
	}{
		{"animeko-beta", true},
		{"animeko-alpha", true},
		{"exact", true},
		{"other", false},
	}
	for _, tc := range cases {
		got := c.DisabledReason(Package{Task: tc.task}) != ""
		if got != tc.want {
			t.Errorf("%s: disabled=%v want %v", tc.task, got, tc.want)
		}
	}
	if c.DisabledReason(Package{Task: "animeko-beta", Disabled: true}) == "" {
		t.Error("per-package disabled flag ignored")
	}
}

func TestEmptyPackagesIsValid(t *testing.T) {
	c, err := Load(write(t, `{"maintainer":"A <a@b.c>","packages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Packages) != 0 {
		t.Fatalf("expected no packages, got %d", len(c.Packages))
	}
}

func TestKnownTaskValidation(t *testing.T) {
	c, err := Load(write(t, `{"maintainer":"A <a@b.c>","packages":[{"task":"nope"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	has := func(n string) bool { return n == "ok" }
	names := func() []string { return []string{"ok"} }
	pkgname := func(n string) (string, bool) { return "pkg-" + n, true }
	if err := c.KnownTask(has, names, pkgname); err == nil {
		t.Fatal("expected unknown task error")
	}
}
