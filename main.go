// Command auto-aur publishes PKGBUILDs to the AUR for releases published on
// GitHub. Run it with the "run" subcommand:
//
//	auto-aur run [-conf conf.json] [-out out] [-only task] [-force] [-dry-run]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/rinnein/auto-aur/internal/app"
	"github.com/rinnein/auto-aur/internal/aur"
	"github.com/rinnein/auto-aur/internal/conf"
	"github.com/rinnein/auto-aur/internal/github"
	"github.com/rinnein/auto-aur/repo"
	_ "github.com/rinnein/auto-aur/repo/your-szu-life" // register tasks
)

const usage = `auto-aur - publish GitHub releases to the AUR

usage:
  auto-aur run [flags]
  auto-aur tasks          list registered tasks

flags:
  -conf string   configuration file (default "conf.json")
  -out string    working directory holding one checkout per package (default "out")
  -only string   run a single task
  -source string upstream repository (owner/name) selected by an external trigger
  -tasks string  comma separated task names or prefixes selected by a trigger
  -tag string    release tag the trigger fired for
  -search string release name substring the trigger fired for
  -from-event    read the trigger from a repository_dispatch payload on stdin
  -force         ignore auto-aur.lock.json and republish unchanged versions
  -dry-run       render and report without writing or pushing
  -no-create     fail instead of creating AUR packages that do not exist
  -v             verbose output
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "run":
		err = runCommand(args)
	case "trigger":
		err = triggerCommand(args)
	case "tasks":
		for _, n := range repo.Names() {
			fmt.Println(n)
		}
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "auto-aur: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "auto-aur: %v\n", err)
		os.Exit(1)
	}
}

func runCommand(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	confPath := fs.String("conf", "conf.json", "configuration file")
	outDir := fs.String("out", "out", "working directory for package checkouts")
	only := fs.String("only", "", "run a single task")
	source := fs.String("source", "", "upstream repository selected by an external trigger")
	tasks := fs.String("tasks", "", "comma separated task names selected by a trigger")
	tag := fs.String("tag", "", "release tag a trigger fired for")
	search := fs.String("search", "", "release name a trigger fired for")
	fromEvent := fs.Bool("from-event", false, "read the trigger from a dispatch payload on stdin")
	force := fs.Bool("force", false, "republish unchanged versions")
	dryRun := fs.Bool("dry-run", false, "render without writing or pushing")
	noCreate := fs.Bool("no-create", false, "do not create missing AUR packages")
	verbose := fs.Bool("v", false, "verbose output")
	if err := fs.Parse(args); err != nil {
		return err
	}

	trigger := repo.Trigger{
		Source: *source,
		Tasks:  splitTasks(*tasks),
		Tag:    *tag,
		Search: *search,
	}
	if *fromEvent {
		ev, err := readDispatch(os.Stdin)
		if err != nil {
			return err
		}
		// Flags win over the payload, so a workflow can add a task or a tag by
		// hand while debugging a dispatch.
		merged, err := ev.merge(trigger)
		if err != nil {
			return err
		}
		trigger = merged
		if ev.Client.Force && !*force {
			*force = true
		}
	}
	trigger.Source = repo.NormalizeRepo(trigger.Source)
	if err := repo.ValidateTrigger(trigger); err != nil {
		return err
	}

	cfg, err := conf.Load(*confPath)
	if err != nil {
		return err
	}

	logf := func(format string, a ...any) {}
	if *verbose || !*dryRun {
		logf = func(format string, a ...any) {
			fmt.Fprintf(os.Stderr, format+"\n", a...)
		}
	}

	publisher := &app.AURPublisher{
		OutDir: *outDir,
		Remote: aur.Remote{Host: cfg.AUR.Host, User: cfg.AUR.User, Branch: cfg.AUR.Branch},
		Name:   cfg.MaintainerName(),
		Email:  cfg.MaintainerEmail(),
		Log:    logf,
	}

	token := githubToken()
	gh := github.New(token)
	// A run started by another workflow only ever selected tasks by upstream, so
	// the token is not needed to read anything private. Still say plainly when
	// it is missing: the unauthenticated quota is exhausted by a busy day.
	if token == "" && !*dryRun && !trigger.IsZero() {
		logf("no GH_TOKEN/GITHUB_TOKEN: GitHub reads use the anonymous quota (60/hour)")
	}
	deps := app.Deps{
		Fetch:    gh,
		Branches: gh,
		Files:    gh,
		Repos:    gh,
		Lookup:   aur.NewClient(cfg.AUR.Host),
		Publish:  publisher,
		Checksum: app.HTTPChecksum(token),
		Digest:   app.HTTPDigest(token),
		Log:      logf,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	results, err := app.RunContext(ctx, app.Options{
		ConfPath: *confPath,
		OutDir:   *outDir,
		Only:     *only,
		Trigger:  trigger,
		Force:    *force,
		DryRun:   *dryRun,
		NoCreate: *noCreate,
		Verbose:  *verbose,
	}, deps)

	report, sumErr := app.Summary(results)
	if report != "" {
		fmt.Print(report)
	}
	if err != nil {
		return err
	}
	return sumErr
}

// githubToken prefers GH_TOKEN, matching the variable the previous
// implementation read, and falls back to the token CI provides automatically.
func githubToken() string {
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// dispatchEvent is the subset of a GitHub repository_dispatch payload that
// selects work. Everything here is untrusted input: it only ever narrows the
// set of packages a run looks at, and the payload can never introduce a task,
// an upstream or a flag that conf.json does not already allow.
type dispatchEvent struct {
	Action string `json:"action"`
	Type   string `json:"event_type"`
	Client struct {
		Source string   `json:"source"`
		Repo   string   `json:"repo"`
		Tasks  []string `json:"tasks"`
		Task   string   `json:"task"`
		Tag    string   `json:"tag"`
		Search string   `json:"search"`
		Force  bool     `json:"force"`
	} `json:"client_payload"`
}

// merge fills the trigger from the payload without overriding explicit flags.
func (e dispatchEvent) merge(flags repo.Trigger) (repo.Trigger, error) {
	if e.Action != "" && e.Action != "aur-release" {
		return flags, fmt.Errorf("unnamed dispatch action %q: expected \"aur-release\"", e.Action)
	}
	out := flags
	if out.Source == "" {
		out.Source = firstNonEmpty(e.Client.Source, e.Client.Repo)
	}
	if len(out.Tasks) == 0 {
		out.Tasks = splitTasks(e.Client.Task)
		out.Tasks = append(out.Tasks, e.Client.Tasks...)
	}
	if out.Tag == "" {
		out.Tag = e.Client.Tag
	}
	if out.Search == "" {
		out.Search = e.Client.Search
	}
	if out.Source == "" && len(out.Tasks) == 0 && out.Tag == "" && out.Search == "" {
		return out, fmt.Errorf("dispatch payload names no source, tasks or tag")
	}
	return out, nil
}

// triggerCommand prints what a trigger would select, which makes a dispatch
// from another workflow inspectable without pushing anything.
func triggerCommand(args []string) error {
	fs := flag.NewFlagSet("trigger", flag.ExitOnError)
	source := fs.String("source", "", "upstream repository")
	tasks := fs.String("tasks", "", "comma separated task names or prefixes")
	tag := fs.String("tag", "", "release tag the trigger fired for")
	search := fs.String("search", "", "release name the trigger fired for")
	fromEvent := fs.Bool("from-event", false, "read the event from a dispatch payload on stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}

	trigger := repo.Trigger{Source: *source, Tasks: splitTasks(*tasks), Tag: *tag, Search: *search}
	if *fromEvent {
		ev, err := readDispatch(os.Stdin)
		if err != nil {
			return err
		}
		if trigger, err = ev.merge(trigger); err != nil {
			return err
		}
	}
	trigger.Source = repo.NormalizeRepo(trigger.Source)
	if err := repo.ValidateTrigger(trigger); err != nil {
		return err
	}
	names, unknown := trigger.Select()
	for _, n := range names {
		fmt.Println(n)
	}
	for _, n := range unknown {
		fmt.Fprintf(os.Stderr, "trigger: %q matches no registered task\n", n)
	}
	return nil
}

// readDispatch decodes the GITHUB_EVENT_PATH file when CI points at one, and
// the payload on r otherwise.
func readDispatch(r io.Reader) (dispatchEvent, error) {
	var raw []byte
	var err error
	if path := strings.TrimSpace(os.Getenv("GITHUB_EVENT_PATH")); path != "" {
		raw, err = os.ReadFile(path)
	} else {
		raw, err = io.ReadAll(r)
	}
	if err != nil {
		return dispatchEvent{}, fmt.Errorf("read dispatch payload: %w", err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return dispatchEvent{}, fmt.Errorf("dispatch payload is empty")
	}
	var ev dispatchEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return dispatchEvent{}, fmt.Errorf("parse dispatch payload: %w", err)
	}
	return ev, nil
}

func splitTasks(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
