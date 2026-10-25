package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rinnein/auto-aur/internal/aur"
	"github.com/rinnein/auto-aur/internal/github"
	"github.com/rinnein/auto-aur/repo"
)

// scriptPublisher prepares a real checkout under out/ so the generated
// update.sh can be executed exactly the way the CI workflow does.
type scriptPublisher struct {
	t      *testing.T
	outDir string
	remote string
}

func (p *scriptPublisher) Prepare(pkgbase string) error {
	dir := filepath.Join(p.outDir, pkgbase)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	runGit(p.t, dir, "init", "-q")
	runGit(p.t, dir, "symbolic-ref", "HEAD", "refs/heads/master")
	runGit(p.t, dir, "remote", "add", "origin", p.remote)
	runGit(p.t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid",
		"commit", "-q", "--allow-empty", "-m", "seed")
	return nil
}

// Publish reports a push so update.sh is generated with the publish lines,
// which is the only case that can trip over the working directory.
func (p *scriptPublisher) Publish(PublishRequest) (aur.Action, error) {
	return aur.ActionUpdated, nil
}

// TestUpdateScriptRunsFromOutDir reproduces the CI failure: the workflow runs
// the script from inside out/ ("cd ./out; bash ./update.sh"), so a cd built
// from a relative OutDir was applied twice and pointed at out/out/<pkgbase>.
func TestUpdateScriptRunsFromOutDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git missing")
	}
	const task, pkgbase = "script-demo", "script-demo-pkg"
	repo.Register(task, &fakeGen{pkgname: pkgbase})

	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	runGit(t, root, "init", "-q", "--bare", remote)
	runGit(t, root, "--git-dir", remote, "symbolic-ref", "HEAD", "refs/heads/master")

	// A relative OutDir is what CI uses (the -out default), and it is the value
	// that made the generated path relative to the wrong directory.
	t.Chdir(t.TempDir())
	outDir := "out"

	if err := os.WriteFile("conf.json",
		[]byte(`{"maintainer":"T <t@e.i>","packages":[{"task":"`+task+`"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	deps := Deps{
		Fetch:   &fakeFetcher{rel: github.Release{TagName: "v1.0.0"}},
		Publish: &scriptPublisher{t: t, outDir: outDir, remote: remote},
	}
	if _, err := Run(Options{ConfPath: "conf.json", OutDir: outDir}, deps); err != nil {
		t.Fatal(err)
	}

	// Both invocations the workflow and a human might use must work: CI runs the
	// script from inside out/, while running it from the repository root as
	// out/update.sh must resolve the same checkouts.
	for _, run := range []struct {
		name   string
		dir    string
		script string
	}{
		{"from out/", outDir, "./update.sh"},
		{"from repo root", ".", filepath.Join(outDir, "update.sh")},
	} {
		t.Run(run.name, func(t *testing.T) {
			cmd := exec.Command("bash", run.script)
			cmd.Dir = run.dir
			cmd.Env = append(os.Environ(),
				"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid",
				"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid",
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("update.sh failed when run %s: %v\n%s", run.name, err, out)
			}

			got := runGit(t, root, "--git-dir", remote, "show", "master:PKGBUILD")
			if !strings.Contains(got, "pkgname="+pkgbase) {
				t.Fatalf("update.sh did not push the rendered PKGBUILD:\n%s", got)
			}
		})
	}
}
