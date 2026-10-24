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

func releaseFor(tag string) github.Release {
	base := "ani-" + tag
	return github.Release{
		TagName: tag,
		Assets: []github.Asset{
			{Name: base + "-linux-x86_64.appimage", BrowserDownloadURL: "https://e/" + base + ".appimage"},
			{Name: base + "-linux-x86_64.appimage.sha1", BrowserDownloadURL: "https://e/" + base + ".sha1"},
		},
	}
}

// TestE2ECreateThenUpdate drives the real publisher against a local bare
// repository standing in for the AUR, with an RPC server that tracks whether
// the package exists. It covers the requirement "create the package when the
// AUR does not have it yet" end to end.
func TestE2ECreateThenUpdate(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git missing")
	}
	root := t.TempDir()
	remoteDir := filepath.Join(root, "animeko-appimage-beta.git")
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
			_, _ = w.Write([]byte(`{"resultcount":1,"results":[{"Name":"animeko-appimage-beta","Version":"1-1"}],"type":"multiinfo","version":5}`))
			return
		}
		_, _ = w.Write([]byte(`{"resultcount":0,"results":[],"type":"multiinfo","version":5}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	confPath := filepath.Join(dir, "conf.json")
	if err := os.WriteFile(confPath, []byte(`{"maintainer":"Alice <alice@example.invalid>","packages":[{"task":"animeko-beta"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	pub := &AURPublisher{
		OutDir: filepath.Join(dir, "out"),
		Remote: aur.Remote{Host: "localhost", User: "aur", Branch: "master", BaseURL: root},
		Name:   "Alice",
		Email:  "redacted-af28a3c2c6ea@private.invalid",
	}
	pub.RPCBaseURL = srv.URL

	deps := Deps{
		Fetch:    &fakeFetcher{rel: releaseFor("v1.0.0")},
		Publish:  pub,
		Checksum: func(string) (string, error) { return "0123456789abcdef0123456789abcdef01234567", nil },
	}

	res, err := Run(Options{ConfPath: confPath, OutDir: filepath.Join(dir, "out")}, deps)
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
	deps.Fetch = &fakeFetcher{rel: releaseFor("v2.0.0")}
	res, err = Run(Options{ConfPath: confPath, OutDir: filepath.Join(dir, "out")}, deps)
	if err != nil {
		t.Fatalf("second run: %v (%+v)", err, res)
	}
	if res[0].Action != "updated" {
		t.Fatalf("second run action=%q want updated", res[0].Action)
	}

	remotePkgbuild := runGit(t, root, "--git-dir", remoteDir, "show", "master:PKGBUILD")
	if !strings.Contains(remotePkgbuild, "pkgver=v2.0.0") {
		t.Fatalf("remote PKGBUILD not updated:\n%s", remotePkgbuild)
	}
	if !strings.Contains(remotePkgbuild, "# Maintainer: Alice <alice@example.invalid>") {
		t.Fatalf("maintainer missing on remote:\n%s", strings.SplitN(remotePkgbuild, "\n", 4)[0])
	}
	script, err := os.ReadFile(filepath.Join(dir, "out", "update.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "git push origin master") {
		t.Fatalf("update.sh missing push:\n%s", script)
	}
}
