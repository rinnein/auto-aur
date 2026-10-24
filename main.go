// Command auto-aur publishes PKGBUILDs to the AUR for releases published on
// GitHub. Run it with the "run" subcommand:
//
//	auto-aur run [-conf conf.json] [-out out] [-only task] [-force] [-dry-run]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/rinnein/auto-aur/internal/app"
	"github.com/rinnein/auto-aur/internal/aur"
	"github.com/rinnein/auto-aur/internal/conf"
	"github.com/rinnein/auto-aur/internal/github"
	"github.com/rinnein/auto-aur/repo"
	_ "github.com/rinnein/auto-aur/repo/szulife" // register tasks
)

const usage = `auto-aur - publish GitHub releases to the AUR

usage:
  auto-aur run [flags]
  auto-aur tasks          list registered tasks

flags:
  -conf string   configuration file (default "conf.json")
  -out string    working directory holding one checkout per package (default "out")
  -only string   run a single task
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
	force := fs.Bool("force", false, "republish unchanged versions")
	dryRun := fs.Bool("dry-run", false, "render without writing or pushing")
	noCreate := fs.Bool("no-create", false, "do not create missing AUR packages")
	verbose := fs.Bool("v", false, "verbose output")
	if err := fs.Parse(args); err != nil {
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
	deps := app.Deps{
		Fetch:    gh,
		Branches: gh,
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
