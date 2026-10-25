package repo

import (
	"strings"
	"testing"

	"github.com/rinnein/auto-aur/internal/conf"
)

// trigGen is a task whose only interesting property is what it tracks.
type trigGen struct {
	pkg  string
	w    Wanted
	repy string // repository to claim, defaulting to w.Repo
}

func (g *trigGen) Pkgname() string { return g.pkg }
func (g *trigGen) Wanted() Wanted  { return g.w }
func (g *trigGen) Pkgver(Upstream) (string, error) {
	return "1.0.0", nil
}
func (g *trigGen) Render(Context) (Generated, error) { return Generated{}, nil }

func (g *trigGen) WithMaintainer(string, conf.Overrides) Generator { return g }

// registerFixture mirrors the real package family: one release package and one
// VCS package built from the same upstream.
func registerFixture(t *testing.T) {
	t.Helper()
	if !Has("trig-bin") {
		Register("trig-bin", &trigGen{pkg: "trig-bin", w: Wanted{Repo: "owner/app", Tag: "canary"}})
		Register("trig-git", &trigGen{pkg: "trig-git", w: Wanted{Repo: "owner/app", Branch: "main"}})
		Register("trig-other", &trigGen{pkg: "trig-other", w: Wanted{Repo: "owner/other", Tag: "canary"}})
	}
}

func TestNormalizeRepo(t *testing.T) {
	for in, want := range map[string]string{
		"owner/app":                        "owner/app",
		"owner/app.git":                    "owner/app",
		"https://github.com/owner/app":     "owner/app",
		"https://github.com/owner/app.git": "owner/app",
		"  owner/app/  ":                   "owner/app",
		"http://github.com/owner/app.git/": "owner/app",
	} {
		if got := NormalizeRepo(in); got != want {
			t.Errorf("NormalizeRepo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEmptyTriggerSelectsEverything(t *testing.T) {
	registerFixture(t)
	if !(Trigger{}).IsZero() {
		t.Fatal("a zero trigger must be zero")
	}
	got, unknown := (Trigger{}).Select()
	for _, want := range []string{"trig-bin", "trig-git", "trig-other"} {
		if !contains(got, want) {
			t.Errorf("empty trigger should select %q, got %v", want, got)
		}
	}
	if len(unknown) != 0 {
		t.Errorf("empty trigger reported %v", unknown)
	}
}

func TestTriggerSelectsByRepoAndName(t *testing.T) {
	registerFixture(t)
	got, unknown := Trigger{Source: "owner/app", Tasks: []string{"trig-bin", "trig-git"}}.Select()
	if !contains(got, "trig-bin") || !contains(got, "trig-git") {
		t.Fatalf("expected both app packages, got %v", got)
	}
	if contains(got, "trig-other") {
		t.Fatalf("another repository's package must not be selected: %v", got)
	}
	if len(unknown) != 0 {
		t.Fatalf("unexpected unknown tasks: %v", unknown)
	}
}

func TestTriggerMatchesUpstreamURL(t *testing.T) {
	registerFixture(t)
	got, _ := Trigger{Source: "https://github.com/owner/app.git"}.Select()
	if !contains(got, "trig-bin") {
		t.Fatalf("URL form of the repo must match: %v", got)
	}
}

func TestTriggerPrefixPattern(t *testing.T) {
	registerFixture(t)
	got, _ := Trigger{Source: "owner/app", Tasks: []string{"trig-*"}}.Select()
	// "trig-*" selects the two app packages but never the other repository's.
	if !contains(got, "trig-bin") || !contains(got, "trig-git") {
		t.Fatalf("prefix should select both app tasks: %v", got)
	}
	if contains(got, "trig-other") {
		t.Fatalf("prefix must still respect the upstream filter: %v", got)
	}
}

func TestTriggerReportsUnknownTask(t *testing.T) {
	registerFixture(t)
	got, unknown := Trigger{Source: "owner/app", Tasks: []string{"trig-bin", "does-not-exist"}}.Select()
	if !contains(got, "trig-bin") {
		t.Fatalf("known task must still be selected: %v", got)
	}
	if !contains(unknown, "does-not-exist") {
		t.Fatalf("unknown task must be reported: %v", unknown)
	}
}

func TestTriggerForeignRepoSelectsNothing(t *testing.T) {
	registerFixture(t)
	// A dispatch naming a repository this project does not package must not
	// publish anything, even for a task name that normally exists.
	got, _ := Trigger{Source: "attacker/fork", Tasks: []string{"trig-bin"}}.Select()
	if len(got) != 0 {
		t.Fatalf("foreign repository must select nothing, got %v", got)
	}
}

func TestTriggerTagMustMatchTaskTag(t *testing.T) {
	if !Has("trig-bin") {
		registerFixture(t)
	}
	// The task packages canary only; a dispatch for a different tag is a no-op.
	got, _ := Trigger{Source: "owner/app", Tag: "v2.0.0"}.Select()
	if contains(got, "trig-bin") {
		t.Fatalf("release package must not fire for an unrelated tag: %v", got)
	}
	got, _ = Trigger{Source: "owner/app", Tag: "canary"}.Select()
	if !contains(got, "trig-bin") {
		t.Fatalf("release package should fire for its own tag: %v", got)
	}
	// A VCS package has no release to compare against, so it stays eligible;
	// its lock file decides whether anything is published.
	if !contains(got, "trig-git") {
		t.Fatalf("vcs package should stay eligible: %v", got)
	}
}

func TestRequireUpstream(t *testing.T) {
	registerFixture(t)
	if _, err := RequireUpstream("trig-bin", "owner/app").Pkgver(Upstream{Tag: "canary"}); err != nil {
		t.Fatalf("matching upstream must pass: %v", err)
	}
	_, err := RequireUpstream("trig-bin", "attacker/fork").Pkgver(Upstream{Tag: "canary"})
	if err == nil {
		t.Fatal("mismatched upstream must fail")
	}
	if !strings.Contains(err.Error(), "packages owner/app") {
		t.Fatalf("error should name the real upstream, got %v", err)
	}
	if _, err := RequireUpstream("trig-bin", "attacker/fork").Render(Context{}); err == nil {
		t.Fatal("Render must fail too, so no file is written")
	}
}

func contains(in []string, want string) bool {
	for _, v := range in {
		if v == want {
			return true
		}
	}
	return false
}
