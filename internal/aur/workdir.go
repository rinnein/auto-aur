package aur

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rinnein/auto-aur/internal/fsx"
	"github.com/rinnein/auto-aur/internal/gitx"
)

// Remote describes how package repositories are addressed.
type Remote struct {
	Host   string
	User   string
	Branch string
	// BaseURL overrides the derived endpoint, e.g. a local path in tests.
	// Empty means ssh://<user>@<host>/<pkgbase>.git.
	BaseURL string
}

// URL is the endpoint for one package base. The AUR accepts any SSH username
// and authenticates by key, so the configured user is used verbatim.
func (r Remote) URL(pkgbase string) string {
	if r.BaseURL != "" {
		return strings.TrimSuffix(r.BaseURL, "/") + "/" + pkgbase + ".git"
	}
	return fmt.Sprintf("ssh://%s@%s/%s.git", r.User, r.Host, pkgbase)
}

// Action describes what happened to one package repository.
type Action int

const (
	ActionUpdated   Action = iota // existing repository, new commit pushed
	ActionCreated                 // repository did not exist and was created
	ActionRecreated               // deleted package left a repo behind; forced
	ActionUnchanged               // nothing to commit
)

func (a Action) String() string {
	switch a {
	case ActionUpdated:
		return "updated"
	case ActionCreated:
		return "created"
	case ActionRecreated:
		return "recreated"
	case ActionUnchanged:
		return "unchanged"
	}
	return "unknown"
}

// Workdir is the checkout of a single package repository under out/.
type Workdir struct {
	Dir     string
	Pkgbase string
	Remote  Remote
	Git     *gitx.Runner
	// MkSrcinfo produces .SRCINFO content when makepkg is unavailable.
	MkSrcinfo func() string

	commitMsg string
}

// Files is the set of files written into the repository before committing.
type Files struct {
	Pkgbuild string
	Srcinfo  string
	Lock     string // JSON content for auto-aur.lock.json
}

// Files to stage in every case. The AUR requires PKGBUILD and .SRCINFO.
var trackedFiles = []string{"PKGBUILD", ".SRCINFO", "auto-aur.lock.json"}

// gitignoreBody excludes everything so that building locally never leaves the
// repository dirty; tracked files are added with -f.
const gitignoreBody = "*\n"

// Push writes files into the working tree and publishes them.
//
// The three outcomes that matter:
//   - the repository already exists on the AUR: commit on the existing branch;
//   - the package does not exist yet (requirement: auto-create): the AUR accepts
//     a push to a non-existent pkgbase exactly like a normal push, so a fresh
//     local repository with the right remote is enough;
//   - the package was deleted but its repository still clones: push --force so
//     the leftover content is replaced instead of merged.
func (w *Workdir) Push(files Files) (Action, error) {
	if err := w.write(files); err != nil {
		return ActionUnchanged, err
	}

	// A clone of an empty repository still has .git but no commit, so "fresh"
	// means "nothing to build on" rather than ".git is absent".
	fresh := !fsx.IsDir(filepath.Join(w.Dir, ".git")) || !w.hasCommit()
	if fresh {
		if err := w.initRepo(); err != nil {
			return ActionUnchanged, err
		}
	}

	action := ActionUpdated
	if fresh {
		action = ActionCreated
	}

	addArgs := append([]string{"add", "-f"}, trackedFiles...)
	if _, err := w.Git.RunIn(w.Dir, addArgs...); err != nil {
		return action, err
	}
	// .gitignore participates in the first commit only.
	if fsx.Exists(filepath.Join(w.Dir, ".gitignore")) {
		if _, err := w.Git.RunIn(w.Dir, "add", "-f", ".gitignore"); err != nil {
			return action, err
		}
	}

	if w.stagedClean() {
		return ActionUnchanged, nil
	}

	if _, err := w.Git.RunIn(w.Dir, "commit", "-q", "-m", w.commitMessage()); err != nil {
		return action, err
	}

	if _, err := w.Git.RunIn(w.Dir, "push", "origin", w.Remote.Branch); err != nil {
		kind := gitx.Classify(err)
		if kind == gitx.KindNonFastForward || kind == gitx.KindHookDeclined {
			// A deleted package can leave a repository that clones but whose
			// history is unrelated. Replacing it is the only way to publish.
			if _, ferr := w.Git.RunIn(w.Dir, "push", "--force", "origin", w.Remote.Branch); ferr == nil {
				return ActionRecreated, nil
			}
		}
		return action, w.explain(err)
	}
	return action, nil
}

func (w *Workdir) commitMessage() string {
	if w.commitMsg != "" {
		return w.commitMsg
	}
	return "auto-aur: update " + w.Pkgbase
}

// SetCommitMessage overrides the default commit message.
func (w *Workdir) SetCommitMessage(msg string) { w.commitMsg = msg }

func (w *Workdir) stagedClean() bool {
	out, err := w.Git.RunIn(w.Dir, "status", "--porcelain")
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) == ""
}

func (w *Workdir) write(files Files) error {
	if err := os.MkdirAll(w.Dir, 0o755); err != nil {
		return fmt.Errorf("aur: create %s: %w", w.Dir, err)
	}
	if err := fsx.WriteFile(filepath.Join(w.Dir, "PKGBUILD"), files.Pkgbuild, 0o644); err != nil {
		return err
	}
	if err := fsx.WriteFile(filepath.Join(w.Dir, ".SRCINFO"), files.Srcinfo, 0o644); err != nil {
		return err
	}
	if err := fsx.WriteFile(filepath.Join(w.Dir, "auto-aur.lock.json"), files.Lock, 0o644); err != nil {
		return err
	}
	gi := filepath.Join(w.Dir, ".gitignore")
	// Only seed .gitignore when absent so a hand-edited one survives.
	if !fsx.Exists(gi) {
		if err := fsx.WriteFile(gi, gitignoreBody, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// initRepo prepares a repository for a package the AUR does not have yet, or
// for one whose clone failed.
func (w *Workdir) initRepo() error {
	if _, err := w.Git.RunIn(w.Dir, "init", "-q"); err != nil {
		return err
	}
	// The AUR only accepts pushes to master. gitx pins init.defaultbranch, and
	// the branch is renamed defensively in case an inherited config wins.
	if _, err := w.Git.RunIn(w.Dir, "symbolic-ref", "HEAD", "refs/heads/"+w.Remote.Branch); err != nil {
		return err
	}
	out, err := w.Git.RunIn(w.Dir, "remote", "get-url", "origin")
	if err == nil && strings.TrimSpace(out) != "" {
		return nil
	}
	_, err = w.Git.RunIn(w.Dir, "remote", "add", "origin", w.Remote.URL(w.Pkgbase))
	return err
}

// Clone prepares Dir for pkgbase. It reports whether the remote repository was
// empty (a brand new package) so the caller can log accordingly.
func (w *Workdir) Clone() (empty bool, err error) {
	if fsx.IsDir(filepath.Join(w.Dir, ".git")) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(w.Dir), 0o755); err != nil {
		return false, fmt.Errorf("aur: create %s: %w", filepath.Dir(w.Dir), err)
	}
	// A stale directory without .git would make clone fail; clear it first.
	if fsx.IsDir(w.Dir) {
		if err := os.RemoveAll(w.Dir); err != nil {
			return false, fmt.Errorf("aur: clear %s: %w", w.Dir, err)
		}
	}
	_, err = w.Git.RunIn(filepath.Dir(w.Dir), "clone", "-q", w.Remote.URL(w.Pkgbase), w.Dir)
	if err == nil {
		// Cloning an empty repository succeeds and prints only a warning, so
		// emptiness has to be detected from the checkout, not from exit codes.
		return !w.hasCommit(), nil
	}
	switch gitx.Classify(err) {
	case gitx.KindEmptyRepository:
		// The AUR created the repository on first contact; it is now empty.
		return true, nil
	case gitx.KindRepositoryNotFound:
		if w.RemoteExists() {
			return false, errors.New("aur: repository exists but was rejected; check AUR_SSH_KEY permissions")
		}
		return true, nil
	}
	return false, w.explain(err)
}

// hasCommit reports whether the checkout has a HEAD commit yet.
func (w *Workdir) hasCommit() bool {
	_, err := w.Git.RunIn(w.Dir, "rev-parse", "--verify", "--quiet", "HEAD")
	return err == nil
}

// RemoteExists asks git whether the repository has any refs at all. It is used
// to tell "package never existed" from "package was deleted but left a repo".
func (w *Workdir) RemoteExists() bool {
	out, err := w.Git.RunIn(filepath.Dir(w.Dir), "ls-remote", "--heads", w.Remote.URL(w.Pkgbase))
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) != ""
}

// explain attaches an actionable hint to frequent failures. It deliberately
// does not guess *which* credential is wrong; it points at the ones the
// workflow installs.
func (w *Workdir) explain(err error) error {
	switch gitx.Classify(err) {
	case gitx.KindPermissionDenied:
		return fmt.Errorf("%w\nhint: AUR rejected the SSH key. In CI this key is written from the AUR_SSH_KEY secret to ~/.ssh/aur_key and referenced by ~/.ssh/config (Host %s, User %s)", err, w.Remote.Host, w.Remote.User)
	case gitx.KindRepositoryNotFound:
		return fmt.Errorf("%w\nhint: the AUR has no repository for %q and the key was not allowed to create it", err, w.Pkgbase)
	case gitx.KindHookDeclined:
		return fmt.Errorf("%w\nhint: the AUR rejected the push; PKGBUILD and .SRCINFO must both be present and the branch must be %q", err, w.Remote.Branch)
	}
	return err
}
