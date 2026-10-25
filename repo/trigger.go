package repo

import (
	"fmt"
	"regexp"
	"strings"
)

// Trigger selects tasks from the upstream a caller names, so another workflow
// can say "your-szu-life published tag canary" and have only the packages fed
// by that upstream republished.
//
// Selection is opt-in: an empty Trigger matches every task, which is what a
// plain scheduled run wants. Names are matched against the registered task
// names (Pkgname); an entry may end in "*" to select a package family, e.g.
// "your-szu-life-" selects the -bin and -git packages alike.
type Trigger struct {
	// Source is the GitHub repository (owner/name) the caller cares about.
	// Tasks tracking a different repository are skipped: their upstream did not
	// change, so republishing them would be both pointless and dangerous.
	Source string
	// Task names, exact or "prefix*".
	Tasks []string
	// Tag/Search pick which release of Source to look at. They never replace a
	// task's own release spec; they only make the app verify that this is the
	// release the task actually packages, so a dispatch fired for an unrelated
	// release of the same repository becomes a no-op instead of a surprise
	// publish.
	Tag    string
	Search string
}

// IsZero reports whether the trigger would select every task.
func (t Trigger) IsZero() bool {
	return t.Source == "" && len(t.Tasks) == 0 && t.Tag == "" && t.Search == ""
}

// NormalizeRepo accepts "owner/name", ".git" suffixed and full GitHub URLs and
// returns the canonical "owner/name".
func NormalizeRepo(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "https://github.com/")
	s = strings.TrimPrefix(s, "http://github.com/")
	s = strings.Trim(s, "/")
	s = strings.TrimSuffix(s, ".git")
	return s
}

// matchesTask reports whether name is selected. A trailing "*" is a prefix
// match, so "pkg-" covers every package built from the same upstream.
func (t Trigger) matchesTask(name string) bool {
	if len(t.Tasks) == 0 {
		return true
	}
	for _, pattern := range t.Tasks {
		if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
			if strings.HasPrefix(name, prefix) {
				return true
			}
		} else if pattern == name {
			return true
		}
	}
	return false
}

// matchesUpstream reports whether g maps to the repository the caller named.
func (t Trigger) matchesUpstream(g Generator) bool {
	if t.Source == "" {
		return true
	}
	return NormalizeRepo(g.Wanted().Repo) == NormalizeRepo(t.Source)
}

// releaseMatches reports whether the release an event fired for is the one the
// task packages.
//
// Matching uses the same precedence as github.Wanted: an explicit tag first,
// then a substring, then (with neither given but the caller naming a tag or a
// rolling tag being involved) plain containment. A task that is not
// release-driven, such as a -git package, has nothing to match against and is
// accepted: whether it needs republishing is then decided by its lock file.
func (t Trigger) releaseMatches(g Generator) bool {
	w := g.Wanted()
	if w.Branch != "" {
		return true
	}
	get := t.Tag
	if get == "" {
		get = t.Search
	}
	if get == "" {
		return true
	}
	if w.Tag != "" {
		return w.Tag == get
	}
	if w.Search != "" {
		return strings.Contains(w.Search, get) || strings.Contains(get, w.Search)
	}
	return true
}

// Select returns the registered names the trigger applies to, best effort:
// unregistered patterns are reported through unknown instead of failing, so a
// dispatch that names a package this repository does not build yet still runs
// the ones it does.
func (t Trigger) Select() (selected, unknown []string) {
	seen := map[string]bool{}
	known := Names()
	for _, name := range known {
		g, ok := Get(name)
		if !ok {
			continue
		}
		if !t.matchesTask(name) || !t.matchesUpstream(g) || !t.releaseMatches(g) {
			continue
		}
		selected = append(selected, name)
		seen[name] = true
	}
	for _, pattern := range t.Tasks {
		exact := strings.TrimSuffix(pattern, "*")
		hit := false
		for name := range seen {
			if prefix, ok := strings.CutSuffix(pattern, "*"); ok && strings.HasPrefix(name, prefix) {
				hit = true
			} else if name == pattern {
				hit = true
			}
		}
		if !hit {
			unknown = append(unknown, exact)
		}
	}
	return selected, unknown
}

// upstreamTask is accepted by RequireUpstream for the packages whose upstream
// this repository can verify.
type upstreamTask struct {
	name string
	g    Generator
}

var upstreamTasks = map[string]upstreamTask{}

// RequireUpstream returns a Generator that fails unless the repository it
// packages is the one the caller named.
//
// The GitHub token that dispatches a workflow must naturally be read-only for
// the dispatcher, but the payload it carries is still attacker-controlled input
// for an event that can push to the AUR. So the payload is never trusted as a
// source of truth: a dispatch for "attacker/fork" simply finds no package.
func RequireUpstream(name, repo string) Generator {
	g := MustGet(name)
	if NormalizeRepo(g.Wanted().Repo) != NormalizeRepo(repo) {
		return failingGen{name: name, repo: repo, g: g}
	}
	return g
}

type failingGen struct {
	name, repo string
	g          Generator
}

func (f failingGen) Pkgname() string { return f.g.Pkgname() }
func (f failingGen) Wanted() Wanted  { return f.g.Wanted() }
func (f failingGen) Render(c Context) (Generated, error) {
	return Generated{}, f.err()
}
func (f failingGen) Pkgver(Upstream) (string, error) { return "", f.err() }

func (f failingGen) err() error {
	return fmt.Errorf("task %q packages %s, not %s", f.name, f.g.Wanted().Repo, f.repo)
}

// TaskPattern matches task names for validation of external input.
var TaskPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\*?$`)

// ValidateTrigger rejects payloads that could not possibly name a task, so a
// malformed external event fails loudly instead of silently publishing nothing.
func ValidateTrigger(t Trigger) error {
	if t.Source != "" && !strings.Contains(t.Source, "/") {
		return fmt.Errorf("trigger: source %q is not an owner/name repository", t.Source)
	}
	for _, name := range t.Tasks {
		if !TaskPattern.MatchString(name) {
			return fmt.Errorf("trigger: invalid task name %q", name)
		}
	}
	return nil
}
