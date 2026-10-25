package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rinnein/auto-aur/internal/aur"
	"github.com/rinnein/auto-aur/internal/conf"
	"github.com/rinnein/auto-aur/internal/github"
	"github.com/rinnein/auto-aur/repo"
)

// srcGen is a task whose upstream is fixed, so trigger selection can be tested
// without the real package definitions.
type srcGen struct {
	pkgname string
	w       repo.Wanted
	rel     github.Release
}

func (g *srcGen) Pkgname() string     { return g.pkgname }
func (g *srcGen) Wanted() repo.Wanted { return g.w }
func (g *srcGen) Pkgver(repo.Upstream) (string, error) {
	return strings.ReplaceAll(g.rel.TagName, "-", "_"), nil
}
func (g *srcGen) Render(ctx repo.Context) (repo.Generated, error) {
	src := "pkgname=" + g.pkgname + "\npkgver=" + ctx.Upstream.Version + "\npkgrel=" + itoa(ctx.Pkgrel) + "\n"
	return repo.Generated{
		Pkgname:     g.pkgname,
		Pkgver:      ctx.Upstream.Version,
		Pkgrel:      ctx.Pkgrel,
		PKGBUILD:    src,
		Srcinfo:     "pkgbase = " + g.pkgname + "\n\tpkgver = " + ctx.Upstream.Version + "\n",
		Fingerprint: ctx.Upstream.Fingerprint,
	}, nil
}
func (g *srcGen) WithMaintainer(string, conf.Overrides) repo.Generator { return g }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// triggerSetup registers two packages for one upstream and one for another, all
// enabled in conf.json, and returns options for a run without a trigger.
func triggerSetup(t *testing.T) (Options, Deps, *fakePublisher) {
	t.Helper()
	dir := t.TempDir()
	for _, g := range []*srcGen{
		{pkgname: "src-bin", w: repo.Wanted{Repo: "owner/app", Tag: "canary"},
			rel: github.Release{TagName: "canary", Name: "canary v2.0.0", PublishedAt: "2024-01-01"}},
		{pkgname: "src-git", w: repo.Wanted{Repo: "owner/app", Branch: "main"}},
		{pkgname: "src-other", w: repo.Wanted{Repo: "owner/other", Tag: "canary"},
			rel: github.Release{TagName: "canary", Name: "canary v3.0.0", PublishedAt: "2024-01-01"}},
	} {
		task := "trigger-" + g.pkgname
		if !repo.Has(task) {
			repo.Register(task, g)
		}
	}
	confPath := filepath.Join(dir, "conf.json")
	cfg := `{"maintainer":"T <t@e.i>","packages":[
	  {"task":"trigger-src-bin"},
	  {"task":"trigger-src-git"},
	  {"task":"trigger-src-other"}]}`
	if err := os.WriteFile(confPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	pub := &fakePublisher{}
	deps := Deps{
		Fetch:    &fakeFetcher{rel: github.Release{TagName: "canary", Name: "canary v2.0.0", PublishedAt: "2024-01-01"}},
		Branches: &fakeBranches{sha: "abc1234def", date: "2024-01-01T00:00:00Z"},
		Files:    &triggerFiles{version: "2.0.0"},
		Publish:  pub,
	}
	return Options{ConfPath: confPath, OutDir: filepath.Join(dir, "out")}, deps, pub
}

// triggerFiles stands in for the contents endpoint, which a -git package uses
// to read package.json from the tracked branch.
type triggerFiles struct{ version string }

func (f *triggerFiles) FileVersion(string, string, string) (string, error) { return f.version, nil }

func TestTriggerRunsOnlyNamedTasks(t *testing.T) {
	opts, deps, pub := triggerSetup(t)
	opts.Trigger = repo.Trigger{Source: "owner/app", Tasks: []string{"trigger-src-bin"}}
	opts.Only = "" // triggers and -only are independent; exercise the trigger alone

	results, err := Run(opts, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Task != "trigger-src-bin" {
		t.Fatalf("expected only src-bin, got %+v", results)
	}
	if len(pub.got) != 1 || pub.got[0].pkgbase != "src-bin" {
		t.Fatalf("expected one publish for src-bin, got %+v", pub.got)
	}
}

func TestTriggerSelectsWholeUpstream(t *testing.T) {
	opts, deps, pub := triggerSetup(t)
	// Naming the repository without tasks selects every package it feeds, and
	// nothing from another upstream.
	opts.Trigger = repo.Trigger{Source: "owner/app"}

	results, err := Run(opts, deps)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range results {
		got[r.Task] = true
	}
	if !got["trigger-src-bin"] || !got["trigger-src-git"] || got["trigger-src-other"] {
		t.Fatalf("unexpected selection: %+v", results)
	}
	for _, p := range pub.got {
		if p.pkgbase == "src-other" {
			t.Fatal("another upstream's package must never be published")
		}
	}
}

func TestTriggerWithNoMatchPublishesNothing(t *testing.T) {
	opts, deps, pub := triggerSetup(t)
	opts.Trigger = repo.Trigger{Source: "attacker/fork", Tasks: []string{"trigger-src-bin"}}

	results, err := Run(opts, deps)
	if err != nil {
		t.Fatalf("an unmatched trigger must be a no-op, not a failure: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results, got %+v", results)
	}
	if len(pub.got) != 0 {
		t.Fatalf("nothing may be published, got %+v", pub.got)
	}
	// The workflow runs update.sh unconditionally, so it must exist and be idle.
	script, err := os.ReadFile(filepath.Join(opts.OutDir, "update.sh"))
	if err != nil {
		t.Fatalf("update.sh must still be written: %v", err)
	}
	if !strings.Contains(string(script), "nothing to publish") {
		t.Fatalf("idle update.sh expected:\n%s", script)
	}
}

func TestTriggerRespectsDisabledTasks(t *testing.T) {
	dir := t.TempDir()
	if !repo.Has("trigger-src-bin") {
		triggerSetup(t)
	}
	confPath := filepath.Join(dir, "conf.json")
	cfg := `{"maintainer":"T <t@e.i>","packages":[{"task":"trigger-src-bin","disabled":true}]}`
	if err := os.WriteFile(confPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	pub := &fakePublisher{}
	deps := Deps{
		Fetch:   &fakeFetcher{rel: github.Release{TagName: "canary", Name: "canary v2.0.0", PublishedAt: "2024-01-01"}},
		Publish: pub,
	}
	opts := Options{ConfPath: confPath, OutDir: filepath.Join(dir, "out")}
	opts.Trigger = repo.Trigger{Source: "owner/app"}

	results, err := Run(opts, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Action != "disabled" {
		t.Fatalf("a trigger must not wake a disabled task: %+v", results)
	}
	if len(pub.got) != 0 {
		t.Fatal("disabled task must not publish")
	}
}

func TestTriggerAndOnlyCombine(t *testing.T) {
	opts, deps, _ := triggerSetup(t)
	opts.Trigger = repo.Trigger{Source: "owner/app"}
	opts.Only = "trigger-src-git"

	results, err := Run(opts, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Task != "trigger-src-git" {
		t.Fatalf("-only must still win: %+v", results)
	}
}

func TestZeroTriggerRunsEverything(t *testing.T) {
	opts, deps, _ := triggerSetup(t)
	results, err := Run(opts, deps)
	if err != nil {
		t.Fatal(err)
	}
	// The scheduled run is unchanged: every enabled task is considered.
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d: %+v", len(results), results)
	}
}

var _ = aur.ActionUpdated
