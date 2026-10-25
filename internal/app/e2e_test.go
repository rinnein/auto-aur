package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rinnein/auto-aur/internal/aur"
	"github.com/rinnein/auto-aur/internal/github"
	"github.com/rinnein/auto-aur/repo"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// rpcHit is the AUR RPC answer for a package that exists; PKGBASE is
// substituted so one fixture serves any package name.
const rpcHit = `{"resultcount":1,"results":[{"Name":"PKGBASE","Version":"1-1"}],"type":"multiinfo","version":5}`

// TestE2ECreateThenUpdate drives the real publisher against a local bare
// repository standing in for the AUR, with an RPC server that flips to
// "exists" once the repository has refs. It covers the requirement "create the
// package when the AUR does not have it yet", end to end.
func TestE2ECreateThenUpdate(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git missing")
	}
	const task, pkgbase = "e2e-demo", "e2e-demo-bin"
	repo.Register(task, &overridableGen{pkgname: pkgbase})

	root := t.TempDir()
	remoteDir := filepath.Join(root, pkgbase+".git")
	if err := os.MkdirAll(remoteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "-q", "--bare", remoteDir)

	refs := func() bool {
		cmd := exec.Command("git", "--git-dir", remoteDir, "rev-parse", "--all")
		out, err := cmd.CombinedOutput()
		return err == nil && strings.TrimSpace(string(out)) != ""
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refs() {
			_, _ = w.Write([]byte(strings.Replace(rpcHit, "PKGBASE", pkgbase, 1)))
			return
		}
		_, _ = w.Write([]byte(`{"resultcount":0,"results":[],"type":"multiinfo","version":5}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	confPath := filepath.Join(dir, "conf.json")
	cfgBody := `{"maintainer":"Alice <alice@example.invalid>","packages":[{"task":"` + task + `"}]}`
	if err := os.WriteFile(confPath, []byte(cfgBody), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")

	pub := &AURPublisher{
		OutDir: outDir,
		Remote: aur.Remote{Host: "localhost", User: "aur", Branch: "master", BaseURL: root},
		Name:   "Alice",
		Email:  "alice@example.invalid",
	}
	pub.RPCBaseURL = srv.URL

	deps := Deps{
		Fetch:    &fakeFetcher{rel: github.Release{TagName: "v1.0.0"}},
		Publish:  pub,
		Checksum: func(string) (string, error) { return "0123456789abcdef0123456789abcdef01234567", nil },
	}

	res, err := Run(Options{ConfPath: confPath, OutDir: outDir}, deps)
	if err != nil {
		t.Fatalf("first run: %v (%+v)", err, res)
	}
	if res[0].Action != "created" {
		t.Fatalf("first run action=%q want created", res[0].Action)
	}
	if !refs() {
		t.Fatal("remote still empty after create")
	}

	// A second run with a newer upstream release must update in place.
	deps.Fetch = &fakeFetcher{rel: github.Release{TagName: "v2.0.0"}}
	res, err = Run(Options{ConfPath: confPath, OutDir: outDir}, deps)
	if err != nil {
		t.Fatalf("second run: %v (%+v)", err, res)
	}
	if res[0].Action != "updated" {
		t.Fatalf("second run action=%q want updated", res[0].Action)
	}

	remotePkgbuild := runGit(t, root, "--git-dir", remoteDir, "show", "master:PKGBUILD")
	// The version is the release version ("v2.0.0" tag -> "2.0.0" pkgver).
	if !strings.Contains(remotePkgbuild, "pkgver=2.0.0") {
		t.Fatalf("remote PKGBUILD not updated:\n%s", remotePkgbuild)
	}
	if !strings.Contains(remotePkgbuild, "# Maintainer: Alice <alice@example.invalid>") {
		t.Fatalf("maintainer missing on remote:\n%s", remotePkgbuild)
	}
	script, err := os.ReadFile(filepath.Join(outDir, "update.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "git push origin master") {
		t.Fatalf("update.sh missing push:\n%s", script)
	}
}
