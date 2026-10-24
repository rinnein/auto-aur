// Package app orchestrates one auto-aur run: resolve releases, render files,
// publish to the AUR, and emit the update.sh the workflow executes.
package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	Force    bool   // ignore the lock file and metadata diff
	DryRun   bool   // print instead of writing/pushing
	NoCreate bool   // refuse to create missing AUR packages
	Verbose  bool
}

// Fetcher resolves an upstream release.
type Fetcher interface {
	Fetch(spec github.Wanted) (*github.Release, error)
}

// Lookup reports whether an AUR package exists.
type Lookup interface {
	Lookup(pkgbase string) (aur.Info, bool, error)
}

// Publisher publishes one package repository.
type Publisher interface {
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
	Lookup   Lookup
	Publish  Publisher
	Checksum repo.Checksum
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

	var results []Result
	for _, p := range cfg.Packages {
		if opts.Only != "" && p.Task != opts.Only {
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

	gen := repo.MustGet(p.Task)
	dir := filepath.Join(opts.OutDir, base)
	lockPath := filepath.Join(dir, "auto-aur.lock.json")

	prevPkgver, err := fsx.ReadLock(lockPath)
	if err != nil {
		res.Action, res.Err = "failed", err
		return res
	}

	wanted := gen.Wanted()
	rel, err := deps.Fetch.Fetch(github.Wanted{Repo: wanted.Repo, Tag: wanted.Tag, Search: wanted.Search})
	if err != nil {
		res.Action, res.Err = "failed", err
		return res
	}

	gen2 := repo.WithOverrides(gen, cfg.Maintainer, p.Overrides)
	sum := deps.Checksum
	if sum == nil {
		sum = func(string) (string, error) { return "", fmt.Errorf("no checksum fetcher configured") }
	}
	up, err := gen2.Render(repo.Release{TagName: rel.TagName, Assets: toRepoAssets(rel.Assets)}, sum)
	if err != nil {
		res.Action, res.Err = "failed", err
		return res
	}
	res.Pkgver = up.Pkgver
	res.Upstream = up

	if !opts.Force && prevPkgver == up.Pkgver {
		if diffs := metadataDiff(filepath.Join(dir, "PKGBUILD"), up.Fields); len(diffs) == 0 {
			res.Action = "skip"
			return res
		} else {
			// The version did not move but the package definition did; republish
			// so dependency or description fixes are not stuck locally.
			res.Reason = strings.Join(diffs, "; ")
		}
	}

	srcinfo := gen2.Srcinfo(up)

	if opts.DryRun {
		res.Action = "dry-run"
		fmt.Printf("=== %s (%s) ===\n%s\n", p.Task, up.Pkgver, up.PKGBUILD)
		return res
	}

	if err := fsx.WriteFile(filepath.Join(dir, "PKGBUILD"), up.PKGBUILD, 0o644); err != nil {
		res.Action, res.Err = "failed", err
		return res
	}
	if err := fsx.WriteLock(lockPath, up.Pkgver); err != nil {
		res.Action, res.Err = "failed", err
		return res
	}
	if err := fsx.WriteFile(filepath.Join(dir, ".SRCINFO"), srcinfo, 0o644); err != nil {
		res.Action, res.Err = "failed", err
		return res
	}

	if deps.Publish == nil {
		res.Action = "written"
		return res
	}

	action, err := deps.Publish.Publish(PublishRequest{
		Pkgbase: base,
		Pkgver:  up.Pkgver,
		Files: aur.Files{
			Pkgbuild: up.PKGBUILD,
			Srcinfo:  srcinfo,
			Lock:     fmt.Sprintf("{\"pkgver\":%q}\n", up.Pkgver),
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
		out = append(out, repo.Asset{Name: a.Name, URL: a.BrowserDownloadURL})
	}
	return out
}

// writeUpdateScript emits out/update.sh. The workflow runs it unconditionally,
// so an empty run must still produce a valid, successful script.
func writeUpdateScript(cfg *conf.Config, outDir string, results []Result) error {
	var b strings.Builder
	b.WriteString("# generated by auto-aur; executed by the CI workflow\n")
	b.WriteString("set -e\n")

	pushed := 0
	for _, r := range results {
		if !r.Pushed {
			continue
		}
		pushed++
		fmt.Fprintf(&b, "cd %s\n", shellQuote(filepath.Join(outDir, r.Pkgbase)))
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
