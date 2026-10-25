// Command gh is the thin client used by another workflow to start a run of
// auto-aur: it posts a repository_dispatch event and lets GitHub do the rest.
//
// It is a separate main package so the publishing tool keeps its "run" surface
// small; build it with:
//
//	go build -o gh ./cmd/gh
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/rinnein/auto-aur/internal/github"
	"github.com/rinnein/auto-aur/repo"
)

const usage = `auto-aur gh - start an auto-aur run from another workflow

usage:
  gh dispatch [flags]

flags:
  -repo string      this auto-aur repository, owner/name (default from GH_REPOSITORY)
  -source string    upstream repository whose release fired the run, owner/name
  -tasks string     comma separated task names or prefixes, e.g. your-szu-life-bin
  -tag string       release tag the caller is publishing, e.g. canary
  -search string    release name substring, when the caller knows no tag
  -event string     dispatch event type (default "aur-release")
  -force            republish even when the lock file says nothing changed
  -dry-run          print the payload instead of sending it
  -v                verbose output

The GitHub token comes from GH_TOKEN or GITHUB_TOKEN and needs the "Actions:
write" permission on the auto-aur repository.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch cmd := os.Args[1]; cmd {
	case "dispatch":
		if err := dispatchCmd(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "auto-aur gh: %v\n", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "auto-aur gh: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}

func dispatchCmd(args []string) error {
	fs := flag.NewFlagSet("dispatch", flag.ExitOnError)
	target := fs.String("repo", env("GH_REPOSITORY"), "auto-aur repository, owner/name")
	source := fs.String("source", "", "upstream repository, owner/name")
	tasks := fs.String("tasks", "", "comma separated task names or prefixes")
	tag := fs.String("tag", "", "release tag")
	search := fs.String("search", "", "release name substring")
	event := fs.String("event", "aur-release", "dispatch event type")
	force := fs.Bool("force", false, "republish regardless of the lock file")
	dryRun := fs.Bool("dry-run", false, "print the payload instead of sending it")
	if err := fs.Parse(args); err != nil {
		return err
	}

	trigger := repo.Trigger{
		Source: repo.NormalizeRepo(*source),
		Tasks:  splitTasks(*tasks),
		Tag:    strings.TrimSpace(*tag),
		Search: strings.TrimSpace(*search),
	}
	if trigger.IsZero() {
		return fmt.Errorf("nothing to dispatch: pass at least -source or -tasks")
	}
	if err := repo.ValidateTrigger(trigger); err != nil {
		return err
	}
	repoName := repo.NormalizeRepo(*target)
	if repoName == "" {
		return fmt.Errorf("no target repository: pass -repo or set GH_REPOSITORY")
	}

	payload := map[string]any{
		"source": trigger.Source,
		"tasks":  trigger.Tasks,
		"tag":    trigger.Tag,
		"search": trigger.Search,
		"force":  *force,
	}
	if *dryRun {
		raw, err := json.MarshalIndent(map[string]any{
			"event_type":     *event,
			"client_payload": payload,
		}, "", "  ")
		if err != nil {
			return err
		}
		fmt.Printf("# POST https://api.github.com/repos/%s/dispatches\n%s\n", repoName, raw)
		return nil
	}

	token := token()
	if token == "" {
		return fmt.Errorf("no token: set GH_TOKEN or GITHUB_TOKEN")
	}
	client := github.New(token)
	if err := client.Dispatch(repoName, *event, payload); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "dispatched %s to %s (source=%s tasks=%v tag=%s)\n",
		*event, repoName, trigger.Source, trigger.Tasks, trigger.Tag)
	return nil
}

// splitTasks accepts both repeated and comma separated input, which keeps
// shell snippets and workflow "inputs" equally pleasant to write.
func splitTasks(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func env(key string) string { return strings.TrimSpace(os.Getenv(key)) }

func token() string {
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := env(k); v != "" {
			return v
		}
	}
	return ""
}
