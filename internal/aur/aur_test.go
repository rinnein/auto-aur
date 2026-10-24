package aur

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rinnein/auto-aur/internal/gitx"
)

const rpcEmpty = `{"resultcount":0,"results":[],"type":"multiinfo","version":5}`

const rpcOne = `{"resultcount":1,"results":[{` +
	`"Name":"demo-bin","Version":"1.0.0-1","Maintainer":null,` +
	`"Depends":["glibc"],"Provides":["demo"],"OutOfDate":1787817902,` +
	`"Conflicts":null,"Description":"x"}],"type":"multiinfo","version":5}`

func TestLookupExistence(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		exists bool
	}{
		{"missing", rpcEmpty, false},
		{"present", rpcOne, true},
		// The RPC returns every hit of a batch query; only the requested name
		// counts, so a stray result must not be read as "it exists".
		{"other-name", strings.Replace(rpcOne, `"demo-bin"`, `"other"`, 1), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/rpc/v5/info") {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
			info, exists, err := c.Lookup("demo-bin")
			if err != nil {
				t.Fatal(err)
			}
			if exists != tc.exists {
				t.Fatalf("exists=%v want %v", exists, tc.exists)
			}
			if exists && info.Maintainer != nil {
				t.Fatalf("null maintainer should decode to nil, got %v", *info.Maintainer)
			}
		})
	}
}

// fakeAUR stands in for the AUR: a bare repository plus an RPC server that
// reports whether it has the package.
type fakeAUR struct {
	root string
	host string
	srv  *httptest.Server
}

func newFakeAUR(t *testing.T, pkgbase string, seed bool) *fakeAUR {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, pkgbase+".git")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q", "--bare")

	f := &fakeAUR{root: root, host: dir}
	if seed {
		// Publish an initial commit so the repository is not empty and a second
		// update must be a fast-forward.
		work := filepath.Join(t.TempDir(), "seed")
		git(t, root, "clone", "-q", dir, work)
		if err := os.WriteFile(filepath.Join(work, "PKGBUILD"), []byte("pkgname=demo-bin\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, work, "add", "-f", "PKGBUILD")
		git(t, work, "-c", "user.name=x", "-c", "user.email=x@y", "commit", "-q", "-m", "seed")
		git(t, work, "push", "-q", "origin", "HEAD:master")
	}

	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := rpcEmpty
		if hasRefs(t, dir) {
			body = strings.Replace(rpcOne, `"demo-bin"`, `"`+pkgbase+`"`, 1)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func hasRefs(t *testing.T, dir string) bool {
	cmd := exec.Command("git", "--git-dir", dir, "rev-parse", "--all")
	out, err := cmd.CombinedOutput()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

func workdirFor(t *testing.T, f *fakeAUR, pkgbase string) *Workdir {
	t.Helper()
	out := t.TempDir()
	return &Workdir{
		Dir:     filepath.Join(out, pkgbase),
		Pkgbase: pkgbase,
		Remote:  Remote{Host: "localhost", User: "nobody", Branch: "master", BaseURL: f.root},
		Git:     &gitx.Runner{Dir: filepath.Join(out, pkgbase), Name: "test", Email: "test@example.invalid"},
	}
}

func files(pkgver string) Files {
	return Files{
		Pkgbuild: "pkgname=demo-bin\npkgver=" + pkgver + "\n",
		Srcinfo:  "pkgbase = demo-bin\n\tpkgver = " + pkgver + "\n\npkgname = demo-bin\n",
		Lock:     `{"pkgver":"` + pkgver + `"}` + "\n",
	}
}

// TestCreateNewPackage covers requirement: publish to a package base the AUR
// does not have yet.
func TestCreateNewPackage(t *testing.T) {
	f := newFakeAUR(t, "demo-bin", false)
	wd := workdirFor(t, f, "demo-bin")

	empty, err := wd.Clone()
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if !empty {
		t.Fatal("expected empty clone for a brand new package")
	}

	action, err := wd.Push(files("1.0.0"))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if action != ActionCreated {
		t.Fatalf("action=%v want created", action)
	}
	if !hasRefs(t, f.host) {
		t.Fatal("remote still empty after create")
	}

	// The repository must contain exactly the files the AUR expects.
	tree := git(t, f.host, "--git-dir", f.host, "ls-tree", "--name-only", "master")
	for _, want := range []string{"PKGBUILD", ".SRCINFO", "auto-aur.lock.json", ".gitignore"} {
		if !strings.Contains(tree, want) {
			t.Errorf("missing %s in committed tree:\n%s", want, tree)
		}
	}
}

func TestUpdateExistingPackage(t *testing.T) {
	f := newFakeAUR(t, "demo-bin", true)
	wd := workdirFor(t, f, "demo-bin")

	empty, err := wd.Clone()
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if empty {
		t.Fatal("seeded repository should not look empty")
	}

	action, err := wd.Push(files("2.0.0"))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if action != ActionUpdated {
		t.Fatalf("action=%v want updated", action)
	}
}

func TestPushIsIdempotent(t *testing.T) {
	f := newFakeAUR(t, "demo-bin", false)
	wd := workdirFor(t, f, "demo-bin")
	if _, err := wd.Clone(); err != nil {
		t.Fatal(err)
	}
	if _, err := wd.Push(files("1.0.0")); err != nil {
		t.Fatal(err)
	}
	action, err := wd.Push(files("1.0.0"))
	if err != nil {
		t.Fatalf("second push: %v", err)
	}
	if action != ActionUnchanged {
		t.Fatalf("action=%v want unchanged (no diff to commit)", action)
	}
}

func TestEmptyRepoIsDetectedAsNew(t *testing.T) {
	f := newFakeAUR(t, "demo-bin", false)
	wd := workdirFor(t, f, "demo-bin")
	empty, err := wd.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if !empty {
		t.Fatal("empty remote should be reported as empty")
	}
	// A missing repository must not be confused with a failed clone.
	if wd.RemoteExists() {
		t.Fatal("RemoteExists should be false for an empty repository")
	}
}
