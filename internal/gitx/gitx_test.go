package gitx

import (
	"errors"
	"strings"
	"testing"
)

func wrap(output string) error {
	return &Error{Args: []string{"push"}, Output: output, Err: errors.New("exit status 1")}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		out  string
		want Kind
	}{
		{"aur@aur.archlinux.org: Permission denied (publickey).", KindPermissionDenied},
		{"ERROR: Repository not found.\nfatal: 无法读取远程仓库", KindRepositoryNotFound},
		{"warning: You appear to have cloned an empty repository.", KindEmptyRepository},
		{"! [rejected] master -> master (non-fast-forward)", KindNonFastForward},
		{"remote: error: hook declined to update refs/heads/master", KindHookDeclined},
		{"something else entirely", KindOther},
	}
	for _, tc := range cases {
		if got := Classify(wrap(tc.out)); got != tc.want {
			t.Errorf("Classify(%q)=%v want %v", tc.out, got, tc.want)
		}
	}
}

func TestClassifyNonGitError(t *testing.T) {
	if got := Classify(errors.New("plain")); got != KindOther {
		t.Fatalf("got %v", got)
	}
}

func TestErrorMessageIsTheLastUsefulLine(t *testing.T) {
	err := wrap("hint: Updates were rejected\nfatal: repository 'x' not found\nhint: retry later\n")
	msg := err.Error()
	if !strings.Contains(msg, "repository 'x' not found") {
		t.Fatalf("message lost the real cause: %q", msg)
	}
	if strings.Contains(msg, "hint:") {
		t.Fatalf("message should not include hints: %q", msg)
	}
}

func TestRunRejectsMissingBinary(t *testing.T) {
	r := &Runner{Dir: ".", Name: "t", Email: "t@e.i"}
	// A bogus subcommand must surface a classified error, not panic.
	if _, err := r.Run("definitely-not-a-git-subcommand"); err == nil {
		t.Fatal("expected an error")
	}
}
