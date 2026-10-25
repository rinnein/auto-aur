// Package app orchestrates one auto-aur run: resolve releases, render files,
// publish to the AUR, and emit the update.sh the workflow executes.
package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rinnein/auto-aur/internal/aur"
	"github.com/rinnein/auto-aur/internal/conf"
	"github.com/rinnein/auto-aur/internal/fsx"
	"github.com/rinnein/auto-aur/internal/github"
	"github.com/rinnein/auto-aur/internal/gitx"
	"github.com/rinnein/auto-aur/internal/pkgbuild"
	"github.com/rinnein/auto-aur/repo"
)

// Options configure a run.
type Options struct {
	ConfPath string
	OutDir   string
	Only     string // restrict to one task
	// Trigger narrows the run to the tasks fed by the upstream an external
	// caller named. A zero Trigger runs every configured task.
	Trigger  repo.Trigger
	Force    bool // ignore the lock file and metadata diff
	DryRun   bool // print instead of writing/pushing
	NoCreate bool // refuse to create missing AUR packages
	Verbose  bool
}

// Fetcher resolves an upstream release.
type Fetcher interface {
	Fetch(spec github.Wanted) (*github.Release, error)
}

// BranchReader resolves a branch head, used by VCS (-git) tasks.
type BranchReader interface {
	BranchHead(repo, branch string) (*github.Commit, error)
}

// FileReader resolves a file at a ref, used for the version of VCS packages.
type FileReader interface {
	FileVersion(repo, ref, path string) (string, error)
}

// RepoReader resolves repository metadata, used for the About description.
type RepoReader interface {
	RepoInfo(repo string) (*github.Repository, error)
}

// Lookup reports whether an AUR package exists.
type Lookup interface {
	Lookup(pkgbase string) (aur.Info, bool, error)
}

// Publisher prepares and publishes one package repository.
type Publisher interface {
	// Prepare makes the package checkout available so the previous lock file
	// and PKGBUILD can be inspected before rendering.
	Prepare(pkgbase string) error
	Publish(req PublishRequest) (aur.Action, error)
}

// PublishRequest is everything needed to push one package.
type PublishRequest struct {
	Pkgbase string
	Pkgver  string
	Files   aur.Files
}

// Deps are the collaborators a run needs. All are injectable for testing.
type Deps struct {
	Fetch    Fetcher
	Branches BranchReader
	Files    FileReader
	Repos    RepoReader
	Lookup   Lookup
	Publish  Publisher
	Checksum repo.Checksum
	Digest   repo.Digest
	Log      func(format string, args ...any)
}

// Result summarises one task.
type Result struct {
	Task     string
	Pkgbase  string
	Pkgver   string
	Action   string // skip|created|updated|recreated|unchanged|disabled|failed
	Reason   string
	Err      error
	Pushed   bool
	Upstream repo.Generated
}

// Run executes every enabled task in conf order.
func Run(opts Options, deps Deps) ([]Result, error) {
	logf := deps.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}

	cfg, err := conf.Load(opts.ConfPath)
	if err != nil {
		return nil, err
	}
	if err := cfg.KnownTask(repo.Has, repo.SortedNames, repo.Pkgname); err != nil {
		return nil, err
	}

	remote := aur.Remote{Host: cfg.AUR.Host, User: cfg.AUR.User, Branch: cfg.AUR.Branch}
	create := cfg.CreateMissing() && !opts.NoCreate

	// A trigger from another workflow narrows the run to the packages it names.
	// Nothing matching is not an error: a dispatch for a release that no
	// configured package consumes must leave the AUR untouched rather than
	// guess. The empty update.sh below is what keeps the workflow's
	// unconditional "bash update.sh" a no-op.
	selected := map[string]bool{}
	if !opts.Trigger.IsZero() {
		names, unknown := opts.Trigger.Select()
		for _, n := range names {
			selected[n] = true
		}
		if len(unknown) > 0 {
			logf("trigger: no registered task matches %v", unknown)
		}
		if len(selected) == 0 {
			logf("trigger: nothing matches repo=%q tasks=%v; nothing to do",
				opts.Trigger.Source, opts.Trigger.Tasks)
			if !opts.DryRun {
				if err := writeUpdateScript(cfg, opts.OutDir, nil); err != nil {
					return nil, err
				}
			}
			return nil, nil
		}
	}

	var results []Result
	for _, p := range cfg.Packages {
		if opts.Only != "" && p.Task != opts.Only {
			continue
		}
		if len(selected) > 0 && !selected[p.Task] {
			logf("skip %s: not selected by the trigger", p.Task)
			continue
		}
		if reason := cfg.DisabledReason(p); reason != "" {
			logf("skip %s: %s", p.Task, reason)
			results = append(results, Result{Task: p.Task, Action: "disabled", Reason: reason})
			continue
		}
		res := runTask(cfg, p, opts, remote, create, deps)
		results = append(results, res)
		if res.Err != nil {
			logf("fail %s: %v", res.Task, res.Err)
		} else {
			logf("%s %s (%s) %s", res.Action, res.Task, res.Pkgbase, res.Pkgver)
		}
	}

	if opts.DryRun {
		fmt.Fprintf(os.Stderr, "dry-run: no files written, no pushes performed\n")
		return results, firstFailure(results)
	}

	// A failure must still produce a script so the workflow's unconditional
	// "bash update.sh" publishes whatever succeeded; the non-zero exit is what
	// marks the run red.
	if err := writeUpdateScript(cfg, opts.OutDir, results); err != nil {
		return results, err
	}
	return results, firstFailure(results)
}

// firstFailure reports the tasks that failed, so a caller cannot look at
// results only and conclude the run succeeded.
func firstFailure(results []Result) error {
	var names []string
	for _, r := range results {
		if r.Err != nil {
			names = append(names, r.Task)
		}
	}
	if len(names) == 0 {
		return nil
	}
	return fmt.Errorf("failed tasks: %s", strings.Join(names, ", "))
}

func runTask(cfg *conf.Config, p conf.Package, opts Options, remote aur.Remote, create bool, deps Deps) Result {
	res := Result{Task: p.Task}

	base, ok := repo.Pkgname(p.Task)
	if !ok {
		res.Action, res.Err = "failed", fmt.Errorf("unknown task %q", p.Task)
		return res
	}
	if p.Overrides.Pkgname != nil {
		base = *p.Overrides.Pkgname
	}
	res.Pkgbase = base

	gen := repo.WithOverrides(repo.MustGet(p.Task), cfg.Maintainer, p.Overrides)
	dir := filepath.Join(opts.OutDir, base)
	lockPath := filepath.Join(dir, "auto-aur.lock.json")

	// Check out the published repository first: skip detection and pkgrel
	// bumping both depend on the lock file that lives in it.
	if !opts.DryRun && deps.Publish != nil {
		if err := deps.Publish.Prepare(base); err != nil {
			res.Action, res.Err = "failed", err
			return res
		}
	}

	prev, err := fsx.ReadLock(lockPath)
	if err != nil {
		res.Action, res.Err = "failed", err
		return res
	}

	up, err := resolveUpstream(gen.Wanted(), deps)
	if err != nil {
		res.Action, res.Err = "failed", err
		return res
	}

	// Cheap skip: the version is derived from upstream metadata alone, so an
	// unchanged package costs no asset download at all.
	pkgver, err := gen.Pkgver(up)
	if err != nil {
		res.Action, res.Err = "failed", err
		return res
	}
	res.Pkgver = pkgver

	// Rendering happens twice in the worst case: once to learn what the package
	// would look like with the currently published pkgrel, and again if that
	// content turns out to differ. Network results are memoised so the .deb is
	// still downloaded only once.
	net := repo.Net{
		Checksum: memoChecksum(deps.Checksum),
		Digest:   memoDigest(deps.Digest),
	}

	// Continue the previous pkgrel when the version is unchanged, so a series of
	// rebuilds keeps counting up instead of restarting at 1.
	rel := 1
	if prev.Pkgver == pkgver && prev.Pkgrel > 0 {
		rel = prev.Pkgrel
	}

	genned, err := gen.Render(repo.Context{Upstream: up, Pkgrel: rel, Net: net})
	if err != nil {
		res.Action, res.Err = "failed", err
		return res
	}

	// A change that keeps the same version must still bump pkgrel, otherwise
	// pacman considers the installed copy up to date and users never receive the
	// fix. Two causes count: rebuilt upstream content (fingerprint) and an
	// edited package definition (dependency fix, description, ...).
	bump, why := false, ""
	if prev.Pkgver == pkgver && prev.Pkgrel > 0 {
		publishedPath := filepath.Join(dir, "PKGBUILD")
		switch diffs := metadataDiff(publishedPath, genned.Fields); {
		case prev.Fingerprint != up.Fingerprint:
			bump, why = true, "upstream content changed for the same version"
		case len(diffs) > 0:
			bump, why = true, "package definition changed: "+strings.Join(diffs, "; ")
		}
	}
	if bump {
		rel++
		res.Reason = why
		genned, err = gen.Render(repo.Context{Upstream: up, Pkgrel: rel, Net: net})
		if err != nil {
			res.Action, res.Err = "failed", err
			return res
		}
	}

	if genned.Pkgver != "" {
		res.Pkgver = genned.Pkgver
	}
	if genned.Pkgrel == 0 {
		genned.Pkgrel = rel
	}
	res.Upstream = genned

	// Same version, same fingerprint: only republish when the package
	// definition itself changed (dependency fix, description edit).
	if !opts.Force && prev.Pkgver == pkgver && prev.Fingerprint == up.Fingerprint {
		if diffs := metadataDiff(filepath.Join(dir, "PKGBUILD"), genned.Fields); len(diffs) == 0 {
			res.Action = "skip"
			return res
		}
		res.Reason = strings.Join(metadataDiff(filepath.Join(dir, "PKGBUILD"), genned.Fields), "; ")
	}

	lockRec := fsx.LockFile{
		Pkgver:      genned.Pkgver,
		Pkgrel:      genned.Pkgrel,
		Fingerprint: up.Fingerprint,
	}

	if opts.DryRun {
		res.Action = "dry-run"
		fmt.Printf("=== %s (%s-%d) ===\n%s\n%s\n", p.Task, genned.Pkgver, genned.Pkgrel, genned.PKGBUILD, genned.Srcinfo)
		return res
	}

	if err := fsx.WriteFile(filepath.Join(dir, "PKGBUILD"), genned.PKGBUILD, 0o644); err != nil {
		res.Action, res.Err = "failed", err
		return res
	}
	if err := fsx.WriteLock(lockPath, lockRec); err != nil {
		res.Action, res.Err = "failed", err
		return res
	}
	if err := fsx.WriteFile(filepath.Join(dir, ".SRCINFO"), genned.Srcinfo, 0o644); err != nil {
		res.Action, res.Err = "failed", err
		return res
	}

	if deps.Publish == nil {
		res.Action = "written"
		return res
	}

	lockJSON, err := fsx.MarshalLock(lockRec)
	if err != nil {
		res.Action, res.Err = "failed", err
		return res
	}
	action, err := deps.Publish.Publish(PublishRequest{
		Pkgbase: base,
		Pkgver:  genned.Pkgver,
		Files: aur.Files{
			Pkgbuild: genned.PKGBUILD,
			Srcinfo:  genned.Srcinfo,
			Lock:     lockJSON,
		},
	})
	if err != nil {
		res.Action, res.Err = "failed", err
		return res
	}
	res.Action = action.String()
	res.Pushed = action != aur.ActionUnchanged
	return res
}

// resolveUpstream turns a task's Wanted into a concrete Upstream, either a
// release or a branch head.
func resolveUpstream(w repo.Wanted, deps Deps) (repo.Upstream, error) {
	if w.Branch != "" {
		if deps.Branches == nil {
			return repo.Upstream{}, fmt.Errorf("no branch reader configured")
		}
		head, err := deps.Branches.BranchHead(w.Repo, w.Branch)
		if err != nil {
			return repo.Upstream{}, err
		}
		up := repo.Upstream{
			Commit:      head.SHA,
			Date:        utcDate(head.Info.Committer.Date),
			Fingerprint: head.SHA,
		}
		// A branch has no release to name a version, so take it from the tree.
		if w.VersionFile != "" {
			if deps.Files == nil {
				return repo.Upstream{}, fmt.Errorf("task tracks %s but no file reader is configured", w.VersionFile)
			}
			version, verr := deps.Files.FileVersion(w.Repo, w.Branch, w.VersionFile)
			if verr != nil {
				return repo.Upstream{}, verr
			}
			up.Version = version
		}
		if err := addDescription(&up, w, deps); err != nil {
			return repo.Upstream{}, err
		}
		return up, nil
	}
	if deps.Fetch == nil {
		return repo.Upstream{}, fmt.Errorf("no release fetcher configured")
	}
	rel, err := deps.Fetch.Fetch(github.Wanted{Repo: w.Repo, Tag: w.Tag, Search: w.Search})
	if err != nil {
		return repo.Upstream{}, err
	}
	// The version comes from the release itself. Deriving it from an asset
	// filename would freeze the package whenever upstream forgets to rename.
	version, err := rel.Version()
	if err != nil {
		return repo.Upstream{}, err
	}
	up := repo.Upstream{
		Tag:         rel.TagName,
		Commit:      commitFromRelease(rel),
		Date:        utcDate(rel.PublishedAt),
		Assets:      toRepoAssets(rel.Assets),
		Version:     version,
		Fingerprint: ReleaseFingerprint(rel),
	}
	if err := addDescription(&up, w, deps); err != nil {
		return repo.Upstream{}, err
	}
	return up, nil
}

// addDescription fills Upstream.Description when the task asks for it. A failure
// is reported rather than silently falling back: a default description would
// look like a recipe change and trigger a needless pkgrel bump.
func addDescription(up *repo.Upstream, w repo.Wanted, deps Deps) error {
	if !w.RepoDescription {
		return nil
	}
	if deps.Repos == nil {
		return fmt.Errorf("task %q wants the repository description but no repo reader is configured", w.Repo)
	}
	info, err := deps.Repos.RepoInfo(w.Repo)
	if err != nil {
		return err
	}
	up.Description = info.Description
	return nil
}

// ReleaseFingerprint identifies the exact upstream state behind a release. It is
// exported so tests can build the same string instead of hardcoding a format.
//
// A rolling tag keeps its name, so neither the tag nor (for a rebuilt release)
// the publication time is a reliable signal. The commit recorded in the body
// and the per-asset metadata are what actually move.
func ReleaseFingerprint(rel *github.Release) string {
	parts := []string{rel.TagName, rel.PublishedAt, commitFromRelease(rel)}
	for _, a := range rel.Assets {
		parts = append(parts, fmt.Sprintf("%s:%d:%s", a.Name, a.Size, a.UpdatedAt))
	}
	return strings.Join(parts, "|")
}

// commitFromRelease extracts a commit SHA from the release body, where CI often
// records the built revision.
func commitFromRelease(rel *github.Release) string {
	for _, line := range strings.Split(rel.Body, "\n") {
		for _, field := range strings.FieldsFunc(line, func(r rune) bool {
			return !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f'))
		}) {
			if len(field) == 40 {
				return field
			}
		}
	}
	return ""
}

// utcDate formats an RFC3339 timestamp as YYYYMMDD in UTC, or "" when unset.
func utcDate(ts string) string {
	if ts == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ""
	}
	return t.UTC().Format("20060102")
}

// metadataDiff compares the published PKGBUILD against the freshly rendered
// one. A missing file counts as "no diff" so first runs behave like the old
// lock-file-only check.
func metadataDiff(path string, fresh pkgbuild.Fields) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return pkgbuild.Diff(pkgbuild.Parse(string(raw)), fresh)
}

func toRepoAssets(in []github.Asset) []repo.Asset {
	out := make([]repo.Asset, 0, len(in))
	for _, a := range in {
		out = append(out, repo.Asset{
			Name:   a.Name,
			URL:    a.BrowserDownloadURL,
			Size:   a.Size,
			SHA256: a.SHA256(),
		})
	}
	return out
}

// writeUpdateScript emits out/update.sh. The workflow runs it unconditionally,
// so an empty run must still produce a valid, successful script.
func writeUpdateScript(cfg *conf.Config, outDir string, results []Result) error {
	var b strings.Builder
	b.WriteString("# generated by auto-aur; executed by the CI workflow\n")
	b.WriteString("set -e\n")
	// Checkouts are resolved against this script's own directory, not the
	// caller's: CI runs it from inside out/ ("cd ./out; bash ./update.sh"), so a
	// relative OutDir would otherwise be applied twice and point at out/out/.
	b.WriteString(`here=$(cd -- "$(dirname -- "$0")" && pwd)` + "\n")

	pushed := 0
	for _, r := range results {
		if !r.Pushed {
			continue
		}
		pushed++
		fmt.Fprintf(&b, "cd \"$here\"/%s\n", shellQuote(r.Pkgbase))
		fmt.Fprintf(&b, "git add -f PKGBUILD .SRCINFO auto-aur.lock.json\n")
		fmt.Fprintf(&b, "git add -f .gitignore 2>/dev/null || true\n")
		fmt.Fprintf(&b, "git commit -q -m %s || echo %s\n",
			shellQuote("auto-aur: "+r.Pkgbase+" "+r.Pkgver),
			shellQuote("nothing to commit for "+r.Pkgbase))
		fmt.Fprintf(&b, "git push origin %s\n", cfg.AUR.Branch)
	}
	if pushed == 0 {
		b.WriteString("echo 'auto-aur: nothing to publish'\n")
	}
	b.WriteString("exit 0\n")

	// 0755 so a download-and-run works; the workflow also chmods it.
	return fsx.WriteFile(filepath.Join(outDir, "update.sh"), b.String(), 0o755)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Summary renders a human readable report and the overall error.
func Summary(results []Result) (string, error) {
	var b strings.Builder
	var failed []string
	counts := map[string]int{}
	for _, r := range results {
		counts[r.Action]++
		line := fmt.Sprintf("%-10s %-24s %-26s %s", r.Action, r.Task, r.Pkgbase, r.Pkgver)
		if r.Reason != "" {
			line += "  (" + r.Reason + ")"
		}
		b.WriteString(strings.TrimRight(line, " ") + "\n")
		if r.Err != nil {
			failed = append(failed, r.Task)
		}
	}
	fmt.Fprintf(&b, "\n%d task(s): %s\n", len(results), formatCounts(counts))
	if len(failed) > 0 {
		return b.String(), fmt.Errorf("failed tasks: %s", strings.Join(failed, ", "))
	}
	return b.String(), nil
}

func formatCounts(m map[string]int) string {
	keys := []string{"created", "updated", "recreated", "skip", "disabled", "unchanged", "written", "dry-run", "failed"}
	var parts []string
	for _, k := range keys {
		if m[k] > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
		}
	}
	if len(parts) == 0 {
		return "nothing to do"
	}
	return strings.Join(parts, " ")
}

// RunContext is a convenience wrapper used by main.
func RunContext(ctx context.Context, opts Options, deps Deps) ([]Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return Run(opts, deps)
}

var _ = gitx.Runner{}

// memoChecksum caches checksum lookups for the duration of one task, so the
// two-phase render does not refetch them.
func memoChecksum(fn repo.Checksum) repo.Checksum {
	if fn == nil {
		return nil
	}
	cache := map[string]string{}
	return func(url string) (string, error) {
		if v, ok := cache[url]; ok {
			return v, nil
		}
		v, err := fn(url)
		if err != nil {
			return "", err
		}
		cache[url] = v
		return v, nil
	}
}

// memoDigest caches asset digests, which is what keeps a 40 MB .deb from being
// downloaded twice when the render has to be repeated.
func memoDigest(fn repo.Digest) repo.Digest {
	if fn == nil {
		return nil
	}
	cache := map[string]string{}
	return func(url, algo string) (string, error) {
		key := algo + " " + url
		if v, ok := cache[key]; ok {
			return v, nil
		}
		v, err := fn(url, algo)
		if err != nil {
			return "", err
		}
		cache[key] = v
		return v, nil
	}
}
