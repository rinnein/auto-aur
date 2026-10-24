// Package gitx wraps every git invocation used by auto-aur.
//
// The only reason this package exists is that a developer machine and CI do not
// share a git configuration: global settings such as commit.gpgsign=true or
// init.defaultbranch=<name> would otherwise change behaviour of the generated
// commands. Every call therefore injects the settings it depends on explicitly.
package gitx

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Runner executes git in Dir. Env and StdoutStdout are optional.
type Runner struct {
	Dir    string
	Name   string // commit author name
	Email  string // commit author email
	Stdout io.Writer
	Stderr io.Writer
}

// ErrNotFound is returned when the git binary is unavailable.
var ErrNotFound = errors.New("git: executable not found")

func (r *Runner) baseArgs() []string {
	return []string{
		"-c", "commit.gpgsign=false",
		"-c", "tag.gpgsign=false",
		"-c", "init.defaultbranch=master",
		"-c", "advice.detachedHead=false",
		"-c", "user.name=" + r.Name,
		"-c", "user.email=" + r.Email,
	}
}

// Run executes git with args and returns combined output. A non-zero exit is
// reported as an error whose text keeps git's own message, so callers can
// classify it (see Classify).
func (r *Runner) Run(args ...string) (string, error) {
	return r.run(r.Dir, args...)
}

// RunIn executes git in an explicit directory, ignoring r.Dir.
func (r *Runner) RunIn(dir string, args ...string) (string, error) {
	return r.run(dir, args...)
}

func (r *Runner) run(dir string, args ...string) (string, error) {
	full := append(r.baseArgs(), args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	// Keep the developer's ssh config, HOME and GIT_SSH_COMMAND visible; only
	// strip prompts so a broken key fails instead of hanging in CI.
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GIT_CONFIG_NOSYSTEM=0",
	)
	var out bytes.Buffer
	cmd.Stdout = io.MultiWriter(&out, writerOrDiscard(r.Stdout))
	cmd.Stderr = io.MultiWriter(&out, writerOrDiscard(r.Stderr))
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return out.String(), ErrNotFound
		}
		return out.String(), &Error{Args: args, Output: out.String(), Err: err}
	}
	return out.String(), nil
}

func writerOrDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// Error carries git's output alongside the exit status.
type Error struct {
	Args   []string
	Output string
	Err    error
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Output)
	if msg == "" {
		return "git " + strings.Join(e.Args, " ") + ": " + e.Err.Error()
	}
	// Keep only the last meaningful line; git tends to print hints after the
	// real failure, and full output is noisy in CI logs.
	lines := strings.Split(msg, "\n")
	last := lines[len(lines)-1]
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" || strings.HasPrefix(l, "hint:") {
			continue
		}
		last = l
		break
	}
	return "git " + strings.Join(e.Args, " ") + ": " + last
}

func (e *Error) Unwrap() error { return e.Err }

// Kind classifies a git failure so callers can attach a useful hint.
type Kind int

const (
	KindOther Kind = iota
	KindPermissionDenied
	KindRepositoryNotFound
	KindEmptyRepository
	KindNonFastForward
	KindHookDeclined
)

// Classify inspects git output. Matching is intentionally narrow: only the
// phrases git itself emits without localisation-sensitive wrapping are used,
// so the caller must treat KindOther as "unknown" rather than "success".
func Classify(err error) Kind {
	var ge *Error
	if !errors.As(err, &ge) {
		return KindOther
	}
	out := strings.ToLower(ge.Output)
	switch {
	case strings.Contains(out, "permission denied (publickey)"):
		return KindPermissionDenied
	case strings.Contains(out, "repository not found"):
		return KindRepositoryNotFound
	case strings.Contains(out, "empty repository"),
		strings.Contains(out, "you appear to have cloned an empty"):
		return KindEmptyRepository
	case strings.Contains(out, "non-fast-forward"),
		strings.Contains(out, "fetch first"),
		strings.Contains(out, "stale info"):
		return KindNonFastForward
	case strings.Contains(out, "pre-receive hook declined"),
		strings.Contains(out, "hook declined"):
		return KindHookDeclined
	}
	return KindOther
}
