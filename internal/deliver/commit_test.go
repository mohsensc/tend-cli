package deliver

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommitFastForward(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	head := commit(t, wt.Dir, "package.json", `{"v":2}`, "Bump a to 2.0.0")
	got, err := Commit(bg, wt, r.push, "main")
	if err != nil {
		t.Fatal(err)
	}
	if got != head || r.tip(t, "main") != head {
		t.Errorf("Commit = %s, remote main = %s, want %s", got, r.tip(t, "main"), head)
	}
}

func TestCommitToNewBranch(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	head := commit(t, wt.Dir, "package.json", `{"v":2}`, "bump")
	got, err := Commit(bg, wt, r.push, "deps")
	if err != nil || got != head || r.tip(t, "deps") != head {
		t.Fatalf("Commit = %q, %v; remote deps = %q", got, err, r.tip(t, "deps"))
	}
}

// A branch that moved while tend worked gets nothing: the tree on top of its
// new tip was never proven.
func TestCommitMoved(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	mine := commit(t, wt.Dir, "package.json", `{"v":2}`, "Bump a to 2.0.0")

	other := r.elsewhere(t)
	theirs := commit(t, other, "README.md", "hello\n", "docs")
	must(t, other, "push", "origin", "main")

	_, err := Commit(bg, wt, r.push, "main")
	if !errors.Is(err, ErrMoved) {
		t.Fatalf("err = %v, want ErrMoved", err)
	}
	if r.tip(t, "main") != theirs {
		t.Error("the remote changed")
	}
	if head := must(t, wt.Dir, "rev-parse", "HEAD"); head != mine {
		t.Errorf("the worktree was left at %s, want %s", head, mine)
	}
}

func TestCommitOtherFailuresAreNotRetried(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	commit(t, wt.Dir, "package.json", `{"v":2}`, "bump")
	hook := filepath.Join(r.bare, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'no pushes today' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Commit(bg, wt, r.push, "deps")
	if !errors.Is(err, ErrRefused) || errors.Is(err, ErrMoved) || !strings.Contains(err.Error(), "no pushes today") {
		t.Fatalf("err = %v", err)
	}
}
