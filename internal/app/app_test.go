package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rinnein/auto-aur/internal/aur"
	"github.com/rinnein/auto-aur/internal/conf"
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
func (f *fakeGen) Render(rel repo.Release, _ repo.Checksum) (repo.Generated, error) {
	f.renders++
	if f.err != nil {
		return repo.Generated{}, f.err
	}
	// pkgver comes from upstream, exactly like a real task.
	src := "pkgname=" + f.pkgname + "\npkgver=" + strings.ReplaceAll(rel.TagName, "-", "_") + "\ndepends=("
	for i, d := range f.depends {
		if i > 0 {
			src += " "
		}
		src += "'" + d + "'"
	}
	src += ")\n"
	return repo.Generated{
		Pkgname:  f.pkgname,
		Pkgver:   strings.ReplaceAll(rel.TagName, "-", "_"),
		PKGBUILD: src,
		// Fields must mirror PKGBUILD; the app compares them to detect edits
		// that do not change the version.
		Fields: pkgbuild.Fields{
			Pkgname: f.pkgname,
			Pkgver:  strings.ReplaceAll(rel.TagName, "-", "_"),
			Depends: append([]string{}, f.depends...),
		},
		Source: "demo::https://example.invalid/demo.appimage",
		SHA1:   "abc123",
	}, nil
}
func (f *fakeGen) Srcinfo(g repo.Generated) string {
	return "pkgbase = " + g.Pkgname + "\n\tpkgver = " + g.Pkgver + "\n"
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

type fakePublisher struct{ got []published }

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

func (g *overridableGen) Render(rel repo.Release, sum repo.Checksum) (repo.Generated, error) {
	digest, err := sum("https://example.invalid/demo.sha1")
	if err != nil {
		return repo.Generated{}, err
	}
	src, err := pkgbuild.Render(pkgbuild.Data{
		Maintainer: g.maintainer,
		Pkgname:    g.pkgname,
		PkgnameIn:  "demo",
		Appimage:   "demo-" + rel.TagName + "-linux-x86_64.appimage",
		Pkgver:     strings.ReplaceAll(rel.TagName, "-", "_"),
		Pkgrel:     1,
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
		Pkgname:  g.pkgname,
		Pkgver:   strings.ReplaceAll(rel.TagName, "-", "_"),
		Pkgrel:   1,
		PKGBUILD: src,
		Fields:   pkgbuild.Parse(src),
		Source:   "demo-" + rel.TagName + "-linux-x86_64.appimage::https://example.invalid/demo.appimage",
		SHA1:     digest,
	}, nil
}

func (g *overridableGen) Srcinfo(gen repo.Generated) string {
	return "pkgbase = " + gen.Pkgname + "\n\tpkgver = " + gen.Pkgver + "\n\npkgname = " + gen.Pkgname + "\n"
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
