package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rinnein/auto-aur/internal/aur"
	"github.com/rinnein/auto-aur/internal/conf"
	"github.com/rinnein/auto-aur/internal/fsx"
	"github.com/rinnein/auto-aur/internal/github"
	"github.com/rinnein/auto-aur/internal/pkgbuild"
	"github.com/rinnein/auto-aur/repo"
)

// fakeGen is a minimal task, so the app tests do not depend on a real upstream.
type fakeGen struct {
	pkgname string
	pkgver  string
	depends []string
	rel     github.Release
	err     error
	// calls counts Render invocations, to prove disabled tasks never render.
	renders int
}

func (f *fakeGen) Pkgname() string { return f.pkgname }
func (f *fakeGen) Wanted() repo.Wanted {
	return repo.Wanted{Repo: "example/demo"}
}
func (f *fakeGen) Pkgver(up repo.Upstream) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return strings.ReplaceAll(up.Tag, "-", "_"), nil
}

func (f *fakeGen) Render(ctx repo.Context) (repo.Generated, error) {
	f.renders++
	if f.err != nil {
		return repo.Generated{}, f.err
	}
	ver := strings.ReplaceAll(ctx.Upstream.Tag, "-", "_")
	pkgrel := ctx.Pkgrel
	if pkgrel == 0 {
		pkgrel = 1
	}
	// pkgver comes from upstream, exactly like a real task.
	src := "pkgname=" + f.pkgname + "\npkgver=" + ver + "\ndepends=("
	for i, d := range f.depends {
		if i > 0 {
			src += " "
		}
		src += "'" + d + "'"
	}
	src += ")\n"
	return repo.Generated{
		Pkgname:  f.pkgname,
		Pkgver:   ver,
		Pkgrel:   pkgrel,
		PKGBUILD: src,
		// Fields must mirror PKGBUILD; the app compares them to detect edits
		// that do not change the version.
		Fields: pkgbuild.Fields{
			Pkgname: f.pkgname,
			Pkgver:  ver,
			Depends: append([]string{}, f.depends...),
		},
		Srcinfo:     "pkgbase = " + f.pkgname + "\n\tpkgver = " + ver + "\n",
		Fingerprint: ctx.Upstream.Fingerprint,
	}, nil
}

type fakeFetcher struct {
	rel github.Release
	err error
}

func (f *fakeFetcher) Fetch(github.Wanted) (*github.Release, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &f.rel, nil
}

type published struct {
	pkgbase string
	files   aur.Files
}

type fakePublisher struct {
	got      []published
	prepared []string
	prepErr  error
}

func (p *fakePublisher) Prepare(pkgbase string) error {
	if p.prepErr != nil {
		return p.prepErr
	}
	p.prepared = append(p.prepared, pkgbase)
	return nil
}

func (p *fakePublisher) Publish(req PublishRequest) (aur.Action, error) {
	p.got = append(p.got, published{req.Pkgbase, req.Files})
	return aur.ActionUpdated, nil
}

func setup(t *testing.T, gen *fakeGen, cfgJSON string) (Options, Deps, *fakePublisher) {
	t.Helper()
	dir := t.TempDir()
	confPath := filepath.Join(dir, "conf.json")
	if err := os.WriteFile(confPath, []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	task := "fake-" + gen.pkgname
	repo.Register(task, gen) // panics on re-register; each test uses a unique name
	pub := &fakePublisher{}
	deps := Deps{
		Fetch:   &fakeFetcher{rel: github.Release{TagName: gen.pkgver}},
		Lookup:  nil,
		Publish: pub,
	}
	return Options{ConfPath: confPath, OutDir: filepath.Join(dir, "out")}, deps, pub
}

const cfgTemplate = `{
  "maintainer": "Test <test@example.invalid>",
  "packages": [{"task": "%s"}],
  "disabled": []
}`

func TestNothingToDoWithEmptyPackages(t *testing.T) {
	dir := t.TempDir()
	confPath := filepath.Join(dir, "conf.json")
	if err := os.WriteFile(confPath, []byte(`{"maintainer":"T <t@e.i>","packages":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	results, err := Run(Options{ConfPath: confPath, OutDir: filepath.Join(dir, "out")}, Deps{})
	if err != nil {
		t.Fatalf("empty config should succeed: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results, got %d", len(results))
	}
	script, err := os.ReadFile(filepath.Join(dir, "out", "update.sh"))
	if err != nil {
		t.Fatalf("update.sh must exist even when nothing ran: %v", err)
	}
	if !strings.Contains(string(script), "exit 0") {
		t.Fatalf("update.sh must exit 0 when idle:\n%s", script)
	}
}

func TestFirstRunPublishes(t *testing.T) {
	g := &fakeGen{pkgname: "demo-first", pkgver: "1.0.0"}
	task := "fake-" + g.pkgname
	opts, deps, pub := setup(t, g, strings.Replace(cfgTemplate, "%s", task, 1))

	results, err := Run(opts, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Action != "updated" {
		t.Fatalf("unexpected results: %+v", results)
	}
	if len(pub.got) != 1 {
		t.Fatalf("expected one publish, got %d", len(pub.got))
	}
	// Files on disk must match what was published.
	for _, name := range []string{"PKGBUILD", ".SRCINFO", "auto-aur.lock.json"} {
		if _, err := os.Stat(filepath.Join(opts.OutDir, g.pkgname, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	script, err := os.ReadFile(filepath.Join(opts.OutDir, "update.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "git push origin master") {
		t.Fatalf("update.sh should push:\n%s", script)
	}
}

func TestSecondRunSkips(t *testing.T) {
	g := &fakeGen{pkgname: "demo-skip", pkgver: "1.0.0", depends: []string{"glibc"}}
	task := "fake-" + g.pkgname
	opts, deps, pub := setup(t, g, strings.Replace(cfgTemplate, "%s", task, 1))

	if _, err := Run(opts, deps); err != nil {
		t.Fatal(err)
	}
	results, err := Run(opts, deps)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Action != "skip" {
		t.Fatalf("expected skip, got %q", results[0].Action)
	}
	if len(pub.got) != 1 {
		t.Fatalf("skip run must not publish again, published %d times", len(pub.got))
	}
}

func TestMetadataChangeForcesRepublish(t *testing.T) {
	g := &fakeGen{pkgname: "demo-meta", pkgver: "1.0.0", depends: []string{"glibc"}}
	task := "fake-" + g.pkgname
	opts, deps, pub := setup(t, g, strings.Replace(cfgTemplate, "%s", task, 1))
	if _, err := Run(opts, deps); err != nil {
		t.Fatal(err)
	}

	// Same version, different dependencies: must not be skipped, otherwise a
	// dependency fix would never reach users.
	g.depends = []string{"glibc", "fuse2"}
	results, err := Run(opts, deps)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Action == "skip" {
		t.Fatal("metadata change must republish")
	}
	if len(pub.got) != 2 {
		t.Fatalf("expected 2 publishes, got %d", len(pub.got))
	}
}

func TestForceBypassesSkip(t *testing.T) {
	g := &fakeGen{pkgname: "demo-force", pkgver: "1.0.0"}
	task := "fake-" + g.pkgname
	opts, deps, _ := setup(t, g, strings.Replace(cfgTemplate, "%s", task, 1))
	if _, err := Run(opts, deps); err != nil {
		t.Fatal(err)
	}
	opts.Force = true
	results, err := Run(opts, deps)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Action == "skip" {
		t.Fatal("force must bypass the lock file")
	}
}

func TestDisabledTaskIsInert(t *testing.T) {
	g := &fakeGen{pkgname: "demo-disabled", pkgver: "1.0.0"}
	task := "fake-" + g.pkgname
	cfg := `{"maintainer":"T <t@e.i>","packages":[{"task":"` + task + `","disabled":true}]}`
	opts, deps, pub := setup(t, g, cfg)

	results, err := Run(opts, deps)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Action != "disabled" {
		t.Fatalf("action=%q want disabled", results[0].Action)
	}
	if g.renders != 0 {
		t.Fatal("disabled task must not render")
	}
	if len(pub.got) != 0 {
		t.Fatal("disabled task must not publish")
	}
	if _, err := os.Stat(filepath.Join(opts.OutDir, g.pkgname)); err == nil {
		t.Fatal("disabled task must not create a work directory")
	}
}

func TestDisabledPatternIsInert(t *testing.T) {
	g := &fakeGen{pkgname: "demo-pattern", pkgver: "1.0.0"}
	task := "fake-" + g.pkgname
	cfg := `{"maintainer":"T <t@e.i>","packages":[{"task":"` + task + `"}],"disabled":["fake-*"]}`
	opts, deps, _ := setup(t, g, cfg)
	results, err := Run(opts, deps)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Action != "disabled" {
		t.Fatalf("glob disable ignored, action=%q", results[0].Action)
	}
}

func TestFailureDoesNotStopOtherTasks(t *testing.T) {
	bad := &fakeGen{pkgname: "demo-bad", pkgver: "1.0.0", err: os.ErrInvalid}
	good := &fakeGen{pkgname: "demo-good", pkgver: "2.0.0"}
	dir := t.TempDir()
	confPath := filepath.Join(dir, "conf.json")
	cfg := `{"maintainer":"T <t@e.i>","packages":[{"task":"fake-demo-bad"},{"task":"fake-demo-good"}]}`
	if err := os.WriteFile(confPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	repo.Register("fake-demo-bad", bad)
	repo.Register("fake-demo-good", good)

	// One fetcher per task: route by repo is unnecessary because each fake
	// renders deterministically, so a single fetcher is enough.
	deps := Deps{Fetch: &fakeFetcher{rel: github.Release{TagName: "v1"}}, Publish: &fakePublisher{}}
	results, err := Run(Options{ConfPath: confPath, OutDir: filepath.Join(dir, "out")}, deps)
	if err == nil {
		t.Fatal("expected an aggregated error")
	}
	report, _ := Summary(results)
	if !strings.Contains(report, "failed") {
		t.Fatalf("summary should mention the failure:\n%s", report)
	}
	// The healthy task must still have been published.
	if results[1].Err != nil {
		t.Fatalf("second task failed: %v", results[1].Err)
	}
}

func TestUnknownTaskFailsFast(t *testing.T) {
	dir := t.TempDir()
	confPath := filepath.Join(dir, "conf.json")
	cfg := `{"maintainer":"T <t@e.i>","packages":[{"task":"does-not-exist"}]}`
	if err := os.WriteFile(confPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(Options{ConfPath: confPath, OutDir: filepath.Join(dir, "out")}, Deps{}); err == nil {
		t.Fatal("unknown task must be rejected before any work happens")
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	g := &fakeGen{pkgname: "demo-dry", pkgver: "1.0.0"}
	task := "fake-" + g.pkgname
	opts, deps, pub := setup(t, g, strings.Replace(cfgTemplate, "%s", task, 1))
	opts.DryRun = true

	if _, err := Run(opts, deps); err != nil {
		t.Fatal(err)
	}
	if len(pub.got) != 0 {
		t.Fatal("dry-run must not publish")
	}
	if _, err := os.Stat(filepath.Join(opts.OutDir, g.pkgname, "PKGBUILD")); err == nil {
		t.Fatal("dry-run must not write files")
	}
}

func TestOverridesApply(t *testing.T) {
	g := &fakeGen{pkgname: "demo-override", pkgver: "1.0.0"}
	cfg := conf.Overrides{Pkgname: strptr("renamed-demo")}
	got := repo.WithOverrides(g, "T <t@e.i>", cfg)
	if _, ok := got.(repo.Overrider); ok {
		t.Log("generator implements Overrider")
	}
	// fakeGen does not implement Overrider, so it must be returned unchanged
	// rather than silently mutated.
	if got.Pkgname() != "demo-override" {
		t.Fatalf("non-overridable generator must be unchanged, got %q", got.Pkgname())
	}
}

func strptr(s string) *string { return &s }

// overridableGen renders through the real PKGBUILD template and accepts
// conf.json overrides, so tests can exercise the full path without shipping a
// concrete task in the repository.
type overridableGen struct {
	pkgname    string
	maintainer string
}

func (g *overridableGen) Pkgname() string { return g.pkgname }

func (g *overridableGen) Wanted() repo.Wanted { return repo.Wanted{Repo: "example/demo"} }

func (g *overridableGen) WithMaintainer(m string, o conf.Overrides) repo.Generator {
	cp := *g
	cp.maintainer = m
	if o.Pkgname != nil {
		cp.pkgname = *o.Pkgname
	}
	return &cp
}

func (g *overridableGen) Pkgver(up repo.Upstream) (string, error) {
	return strings.ReplaceAll(up.Tag, "-", "_"), nil
}

func (g *overridableGen) Render(ctx repo.Context) (repo.Generated, error) {
	ver := strings.ReplaceAll(ctx.Upstream.Tag, "-", "_")
	pkgrel := ctx.Pkgrel
	if pkgrel == 0 {
		pkgrel = 1
	}
	digest, err := ctx.Net.Checksum("https://example.invalid/demo.sha1")
	if err != nil {
		return repo.Generated{}, err
	}
	src, err := pkgbuild.Render(pkgbuild.Data{
		Maintainer: g.maintainer,
		Pkgname:    g.pkgname,
		PkgnameIn:  "demo",
		Appimage:   "demo-" + ctx.Upstream.Tag + "-linux-x86_64.appimage",
		Pkgver:     ver,
		Pkgrel:     pkgrel,
		Pkgdesc:    "demo package",
		URL:        "https://example.invalid",
		License:    []string{"MIT"},
		Depends:    []string{"glibc"},
		Provides:   []string{"demo"},
		Options:    []string{"!strip"},
		SourceURL:  "https://example.invalid/demo.appimage",
		SHA1:       digest,
		IconDir:    "512x512",
	})
	if err != nil {
		return repo.Generated{}, err
	}
	return repo.Generated{
		Pkgname:     g.pkgname,
		Pkgver:      ver,
		Pkgrel:      pkgrel,
		PKGBUILD:    src,
		Srcinfo:     "pkgbase = " + g.pkgname + "\n\tpkgver = " + ver + "\n",
		Fields:      pkgbuild.Parse(src),
		Fingerprint: ctx.Upstream.Fingerprint,
	}, nil
}

// TestMaintainerReachesPKGBUILD guards a refactor hazard: WithOverrides once
// returned the generator struct by value, so the configured maintainer was
// silently dropped from every rendered file.
func TestMaintainerReachesPKGBUILD(t *testing.T) {
	const task, pkgbase = "maint-demo", "maint-demo-pkg"
	repo.Register(task, &overridableGen{pkgname: pkgbase})

	dir := t.TempDir()
	confPath := filepath.Join(dir, "conf.json")
	cfg := `{"maintainer":"Alice <alice@example.invalid>","packages":[{"task":"` + task + `"}]}`
	if err := os.WriteFile(confPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	deps := Deps{
		Fetch:    &fakeFetcher{rel: github.Release{TagName: "v1.0.0"}},
		Publish:  &fakePublisher{},
		Checksum: func(string) (string, error) { return "0123456789abcdef0123456789abcdef01234567", nil },
	}
	outDir := filepath.Join(dir, "out")
	if _, err := Run(Options{ConfPath: confPath, OutDir: outDir}, deps); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(outDir, pkgbase, "PKGBUILD"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "# Maintainer: Alice <alice@example.invalid>") {
		t.Fatalf("maintainer missing from PKGBUILD:\n%s", raw)
	}
}

// fakeBranches stands in for the GitHub branch endpoint.
type fakeBranches struct {
	sha  string
	date string
	err  error
}

func (f *fakeBranches) BranchHead(repo, branch string) (*github.Commit, error) {
	if f.err != nil {
		return nil, f.err
	}
	c := &github.Commit{SHA: f.sha}
	c.Info.Committer.Date = f.date
	return c, nil
}

// branchGen renders a version from a tracked branch, like a -git package.
type branchGen struct{ pkgname string }

func (g *branchGen) Pkgname() string { return g.pkgname }
func (g *branchGen) Wanted() repo.Wanted {
	return repo.Wanted{Repo: "example/demo", Branch: "main"}
}
func (g *branchGen) Pkgver(up repo.Upstream) (string, error) {
	if up.Commit == "" {
		return "", fmt.Errorf("no commit")
	}
	return up.Date + ".r" + up.Commit[:7], nil
}
func (g *branchGen) Render(ctx repo.Context) (repo.Generated, error) {
	ver, err := g.Pkgver(ctx.Upstream)
	if err != nil {
		return repo.Generated{}, err
	}
	rel := ctx.Pkgrel
	if rel == 0 {
		rel = 1
	}
	src := "pkgname=" + g.pkgname + "\npkgver=" + ver + "\ndepends=('glibc')\n"
	return repo.Generated{
		Pkgname: g.pkgname, Pkgver: ver, Pkgrel: rel,
		PKGBUILD:    src,
		Srcinfo:     "pkgbase = " + g.pkgname + "\n",
		Fields:      pkgbuild.Parse(src),
		Fingerprint: ctx.Upstream.Fingerprint,
	}, nil
}

func TestBranchModeTracksCommits(t *testing.T) {
	const task, pkgbase = "branch-demo", "branch-demo-pkg"
	repo.Register(task, &branchGen{pkgname: pkgbase})

	dir := t.TempDir()
	confPath := filepath.Join(dir, "conf.json")
	cfg := `{"maintainer":"T <t@e.i>","packages":[{"task":"` + task + `"}]}`
	if err := os.WriteFile(confPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	branches := &fakeBranches{sha: "572cce5eb41fbdfce9e27017e6183deed4cd624b", date: "2026-10-24T07:05:27Z"}
	pub := &fakePublisher{}
	deps := Deps{Branches: branches, Publish: pub}

	results, err := Run(Options{ConfPath: confPath, OutDir: outDir}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if got := results[0].Pkgver; got != "20261024.r572cce5" {
		t.Fatalf("pkgver=%q", got)
	}

	// A new commit must republish with a new version.
	branches.sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"
	results, err = Run(Options{ConfPath: confPath, OutDir: outDir}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Action == "skip" {
		t.Fatal("a new upstream commit must not be skipped")
	}
	if results[0].Pkgver == "20261024.r572cce5" {
		t.Fatalf("version did not move: %q", results[0].Pkgver)
	}

	// Unchanged commit must skip.
	branches.sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"
	results, err = Run(Options{ConfPath: confPath, OutDir: outDir}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Action != "skip" {
		t.Fatalf("unchanged commit should skip, got %q (%s)", results[0].Action, results[0].Reason)
	}
}

// TestRebuiltAssetBumpsPkgrel covers a rolling release whose version string is
// stable but whose payload changes: without a pkgrel bump pacman would consider
// the package up to date.
func TestRebuiltAssetBumpsPkgrel(t *testing.T) {
	const task, pkgbase = "rebuild-demo", "rebuild-demo-pkg"
	repo.Register(task, &fakeGen{pkgname: pkgbase})

	dir := t.TempDir()
	confPath := filepath.Join(dir, "conf.json")
	cfg := `{"maintainer":"T <t@e.i>","packages":[{"task":"` + task + `"}]}`
	if err := os.WriteFile(confPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	fetch := &fakeFetcher{rel: github.Release{TagName: "v1.0.0", PublishedAt: "2026-10-10T17:18:03Z"}}
	deps := Deps{Fetch: fetch, Publish: &fakePublisher{}}

	if _, err := Run(Options{ConfPath: confPath, OutDir: outDir}, deps); err != nil {
		t.Fatal(err)
	}
	lock, err := fsxReadLock(filepath.Join(outDir, pkgbase, "auto-aur.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	if lock.Pkgrel != 1 {
		t.Fatalf("initial pkgrel=%d want 1", lock.Pkgrel)
	}

	// Same version, republished release: pkgrel must move to 2.
	fetch.rel.PublishedAt = "2026-10-24T07:05:34Z"
	if _, err := Run(Options{ConfPath: confPath, OutDir: outDir}, deps); err != nil {
		t.Fatal(err)
	}
	lock, err = fsxReadLock(filepath.Join(outDir, pkgbase, "auto-aur.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	if lock.Pkgrel != 2 {
		t.Fatalf("pkgrel=%d want 2 after a republished release", lock.Pkgrel)
	}
	if lock.Pkgver != "v1.0.0" {
		t.Fatalf("pkgver changed unexpectedly: %q", lock.Pkgver)
	}
}

// fsxReadLock keeps the lock assertions readable.
func fsxReadLock(path string) (fsx.LockFile, error) { return fsx.ReadLock(path) }

// recordingPublisher mimics the real publisher: Prepare materialises a checkout
// containing a previously published lock file, which is what makes skip
// detection and pkgrel bumping possible on a fresh CI run.
type recordingPublisher struct {
	outDir  string
	task    string
	pkgbase string
	lock    string // lock content written by Prepare
	done    []string
}

func (p *recordingPublisher) Prepare(pkgbase string) error {
	dir := filepath.Join(p.outDir, pkgbase)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if p.lock != "" {
		return os.WriteFile(filepath.Join(dir, "auto-aur.lock.json"), []byte(p.lock), 0o644)
	}
	return nil
}

func (p *recordingPublisher) Publish(req PublishRequest) (aur.Action, error) {
	p.done = append(p.done, req.Pkgver)
	return aur.ActionUnchanged, nil
}

// TestLockIsReadFromPreparedCheckout guards the bug CI exposed: the lock was
// read before the AUR checkout existed, so pkgver/pkgrel from a previous run
// were invisible and a rebuilt asset could never bump pkgrel.
func TestLockIsReadFromPreparedCheckout(t *testing.T) {
	const task, pkgbase = "prep-demo", "prep-demo-pkg"
	repo.Register(task, &fakeGen{pkgname: pkgbase})

	dir := t.TempDir()
	confPath := filepath.Join(dir, "conf.json")
	cfg := `{"maintainer":"T <t@e.i>","packages":[{"task":"` + task + `"}]}`
	if err := os.WriteFile(confPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")

	pub := &recordingPublisher{outDir: outDir, pkgbase: pkgbase}
	// Pretend a previous run published v1.0.0 with the same fingerprint.
	pub.lock = `{"pkgver":"v1.0.0","pkgrel":1,"fingerprint":"v1.0.0|same"}`
	deps := Deps{
		Fetch:   &fakeFetcher{rel: github.Release{TagName: "v1.0.0", PublishedAt: "same"}},
		Publish: pub,
	}

	results, err := Run(Options{ConfPath: confPath, OutDir: outDir}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Action != "skip" {
		t.Fatalf("action=%q want skip; the lock from the checkout was not consulted", results[0].Action)
	}
	if len(pub.done) != 0 {
		t.Fatalf("skip must not publish, got %v", pub.done)
	}
}

// TestPackageDefinitionChangeBumpsPkgrel covers the case that shipped a broken
// package: the recipe gained a dependency while the version stayed the same, so
// pkgrel had to move or already-installed users would never upgrade.
func TestPackageDefinitionChangeBumpsPkgrel(t *testing.T) {
	const task, pkgbase = "defchange-demo", "defchange-demo-pkg"
	g := &fakeGen{pkgname: pkgbase, depends: []string{"glibc"}}
	repo.Register(task, g)

	dir := t.TempDir()
	confPath := filepath.Join(dir, "conf.json")
	cfg := `{"maintainer":"T <t@e.i>","packages":[{"task":"` + task + `"}]}`
	if err := os.WriteFile(confPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	deps := Deps{
		Fetch:   &fakeFetcher{rel: github.Release{TagName: "v1.0.0", PublishedAt: "same"}},
		Publish: &fakePublisher{},
	}

	if _, err := Run(Options{ConfPath: confPath, OutDir: outDir}, deps); err != nil {
		t.Fatal(err)
	}

	// Same upstream, different recipe.
	g.depends = []string{"glibc", "aria2"}
	results, err := Run(Options{ConfPath: confPath, OutDir: outDir}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Action == "skip" {
		t.Fatal("a recipe change must not be skipped")
	}
	lock, err := fsx.ReadLock(filepath.Join(outDir, pkgbase, "auto-aur.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	if lock.Pkgrel != 2 {
		t.Fatalf("pkgrel=%d want 2; installed users would never receive the fix", lock.Pkgrel)
	}

	// A third run with nothing changed must not keep bumping.
	results, err = Run(Options{ConfPath: confPath, OutDir: outDir}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Action != "skip" {
		t.Fatalf("unchanged rerun should skip, got %q (%s)", results[0].Action, results[0].Reason)
	}
	lock, err = fsx.ReadLock(filepath.Join(outDir, pkgbase, "auto-aur.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	if lock.Pkgrel != 2 {
		t.Fatalf("pkgrel drifted to %d on an unchanged rerun", lock.Pkgrel)
	}
}

// fakeRepos serves repository metadata.
type fakeRepos struct {
	description string
	err         error
}

func (f *fakeRepos) RepoInfo(repo string) (*github.Repository, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &github.Repository{FullName: repo, Description: f.description}, nil
}

// descGen uses the upstream description as its pkgdesc.
type descGen struct{ pkgname string }

func (g *descGen) Pkgname() string { return g.pkgname }
func (g *descGen) Wanted() repo.Wanted {
	return repo.Wanted{Repo: "example/demo", RepoDescription: true}
}
func (g *descGen) Pkgver(up repo.Upstream) (string, error) { return up.Tag, nil }
func (g *descGen) Render(ctx repo.Context) (repo.Generated, error) {
	rel := ctx.Pkgrel
	if rel == 0 {
		rel = 1
	}
	desc := strings.Join(strings.Fields(ctx.Upstream.Description), " ")
	if desc == "" {
		desc = "fallback"
	}
	src := "pkgname=" + g.pkgname + "\npkgver=" + ctx.Upstream.Tag + "\npkgdesc=\"" + desc + "\"\n"
	return repo.Generated{
		Pkgname: g.pkgname, Pkgver: ctx.Upstream.Tag, Pkgrel: rel,
		PKGBUILD:    src,
		Srcinfo:     "pkgbase = " + g.pkgname + "\n\tpkgdesc = " + desc + "\n",
		Fields:      pkgbuild.Parse(src),
		Fingerprint: ctx.Upstream.Fingerprint,
	}, nil
}

// TestDescriptionChangeBumpsPkgrel: the About text is a published field, so
// changing it must reach existing installs.
func TestDescriptionChangeBumpsPkgrel(t *testing.T) {
	const task, pkgbase = "desc-demo", "desc-demo-pkg"
	repo.Register(task, &descGen{pkgname: pkgbase})

	dir := t.TempDir()
	confPath := filepath.Join(dir, "conf.json")
	cfg := `{"maintainer":"T <t@e.i>","packages":[{"task":"` + task + `"}]}`
	if err := os.WriteFile(confPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	repos := &fakeRepos{description: "first description"}
	deps := Deps{
		Fetch:   &fakeFetcher{rel: github.Release{TagName: "v1", PublishedAt: "same"}},
		Repos:   repos,
		Publish: &fakePublisher{},
	}

	if _, err := Run(Options{ConfPath: confPath, OutDir: outDir}, deps); err != nil {
		t.Fatal(err)
	}
	lock, err := fsx.ReadLock(filepath.Join(outDir, pkgbase, "auto-aur.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	if lock.Pkgrel != 1 {
		t.Fatalf("pkgrel=%d", lock.Pkgrel)
	}

	repos.description = "second description"
	results, err := Run(Options{ConfPath: confPath, OutDir: outDir}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Action == "skip" {
		t.Fatal("a description change must not be skipped")
	}
	lock, err = fsx.ReadLock(filepath.Join(outDir, pkgbase, "auto-aur.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	if lock.Pkgrel != 2 {
		t.Fatalf("pkgrel=%d want 2 after an About change", lock.Pkgrel)
	}
	raw, err := os.ReadFile(filepath.Join(outDir, pkgbase, "PKGBUILD"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "second description") {
		t.Fatalf("PKGBUILD still has the old description:\n%s", raw)
	}
}

// TestDescriptionFetchFailureFailsTask: a silent fallback would look like a
// recipe change and bump pkgrel for no reason.
func TestDescriptionFetchFailureFailsTask(t *testing.T) {
	const task, pkgbase = "desc-fail", "desc-fail-pkg"
	repo.Register(task, &descGen{pkgname: pkgbase})

	dir := t.TempDir()
	confPath := filepath.Join(dir, "conf.json")
	cfg := `{"maintainer":"T <t@e.i>","packages":[{"task":"` + task + `"}]}`
	if err := os.WriteFile(confPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	deps := Deps{
		Fetch:   &fakeFetcher{rel: github.Release{TagName: "v1"}},
		Repos:   &fakeRepos{err: os.ErrPermission},
		Publish: &fakePublisher{},
	}
	results, err := Run(Options{ConfPath: confPath, OutDir: filepath.Join(dir, "out")}, deps)
	if err == nil {
		t.Fatal("expected the task to fail when the description cannot be read")
	}
	if results[0].Err == nil {
		t.Fatal("result should carry the error")
	}
}
