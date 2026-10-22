package deliver

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/gitx"
	"github.com/mohsensc/tend-cli/internal/verify"
)

var bg = context.Background()

// TestMain keeps the user's git configuration out of the tests and fixes the
// identity commits are made with.
func TestMain(m *testing.M) {
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "Ada", "GIT_AUTHOR_EMAIL": "ada@example.test",
		"GIT_COMMITTER_NAME": "Ada", "GIT_COMMITTER_EMAIL": "ada@example.test",
	} {
		os.Setenv(k, v)
	}
	os.Exit(m.Run())
}

// needGit skips the test without git and lets it run beside the others.
func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	t.Parallel()
}

func must(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitx.Run(bg, dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// remote is a bare repository with main on it, a clone of it, and helpers.
type remote struct {
	bare  string // the "origin"
	clone string // the maintainer's checkout, origin pointing at bare
	base  string // sha of main
	push  *Remote
}

func newRemote(t *testing.T) *remote {
	t.Helper()
	needGit(t)
	root := t.TempDir()
	r := &remote{bare: filepath.Join(root, "origin.git"), clone: filepath.Join(root, "work")}
	must(t, root, "init", "--bare", "-b", "main", r.bare)
	must(t, root, "init", "-b", "main", r.clone)
	must(t, r.clone, "remote", "add", "origin", r.bare)
	write(t, r.clone, "app.txt", "one\ntwo\nthree\n")
	write(t, r.clone, "package.json", "{}\n")
	must(t, r.clone, "add", "-A")
	must(t, r.clone, "commit", "-m", "start")
	must(t, r.clone, "push", "origin", "main")
	r.base = must(t, r.clone, "rev-parse", "HEAD")
	r.push = r.remote(t, "origin")
	return r
}

func (r *remote) remote(t *testing.T, name string) *Remote {
	t.Helper()
	res, err := NewRemote(bg, r.clone, name, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// worktree is a detached worktree of base, as verify.Checkout makes one.
func (r *remote) worktree(t *testing.T) *verify.Worktree {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "wt")
	must(t, r.clone, "worktree", "add", "--detach", dir, r.base)
	t.Cleanup(func() { _, _ = gitx.Run(bg, r.clone, "worktree", "remove", "--force", dir) })
	return &verify.Worktree{Dir: dir, Repo: r.clone, Base: r.base}
}

// commit adds one commit on top of HEAD of dir.
func commit(t *testing.T, dir, file, body, msg string) string {
	t.Helper()
	write(t, dir, file, body)
	must(t, dir, "add", "-A")
	must(t, dir, "commit", "-m", msg)
	return must(t, dir, "rev-parse", "HEAD")
}

// elsewhere is another person's clone of origin.
func (r *remote) elsewhere(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "other")
	must(t, filepath.Dir(dir), "clone", "-q", r.bare, dir)
	return dir
}

func (r *remote) tip(t *testing.T, branch string) string {
	t.Helper()
	out, err := gitx.Run(bg, r.bare, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil {
		return ""
	}
	return out
}

func TestPushNewBranch(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	head := commit(t, wt.Dir, "package.json", `{"v":2}`, "bump")
	if err := Push(bg, wt, r.push, "tend/npm-a-2.0.0", false); err != nil {
		t.Fatal(err)
	}
	if got := r.tip(t, "tend/npm-a-2.0.0"); got != head {
		t.Errorf("remote branch = %q, want %q", got, head)
	}
}

func TestPushForceOnNewBranch(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	head := commit(t, wt.Dir, "package.json", `{"v":2}`, "bump")
	if err := Push(bg, wt, r.push, "tend/new", true); err != nil {
		t.Fatal(err)
	}
	if r.tip(t, "tend/new") != head {
		t.Error("branch was not created")
	}
}

func TestPushUpdateWithLease(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	commit(t, wt.Dir, "package.json", `{"v":2}`, "bump")
	if err := Push(bg, wt, r.push, "tend/b", false); err != nil {
		t.Fatal(err)
	}
	must(t, wt.Dir, "reset", "--hard", r.base)
	again := commit(t, wt.Dir, "package.json", `{"v":3}`, "bump again")
	if err := Push(bg, wt, r.push, "tend/b", false); !errors.Is(err, ErrRejected) {
		t.Fatalf("a diverged push without force = %v, want ErrRejected", err)
	}
	if err := Push(bg, wt, r.push, "tend/b", true); err != nil {
		t.Fatal(err)
	}
	if got := r.tip(t, "tend/b"); got != again {
		t.Errorf("remote branch = %q, want %q", got, again)
	}
}

func TestPushRejectedLease(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	commit(t, wt.Dir, "package.json", `{"v":2}`, "bump")
	if err := Push(bg, wt, r.push, "tend/c", false); err != nil {
		t.Fatal(err)
	}

	other := r.elsewhere(t)
	must(t, other, "checkout", "-q", "tend/c")
	theirs := commit(t, other, "notes.txt", "a human fixed something\n", "fix up")
	must(t, other, "push", "origin", "tend/c")

	must(t, wt.Dir, "reset", "--hard", r.base)
	commit(t, wt.Dir, "package.json", `{"v":3}`, "bump again")
	err := Push(bg, wt, r.push, "tend/c", true)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
	if !strings.Contains(err.Error(), "tend/c moved on origin") {
		t.Errorf("the error does not say what happened: %v", err)
	}
	if got := r.tip(t, "tend/c"); got != theirs {
		t.Errorf("the human's commit was overwritten: %q", got)
	}
}

func TestPushFreshCheckoutHasNoTrackingRef(t *testing.T) {
	r := newRemote(t)
	other := r.elsewhere(t)
	must(t, other, "checkout", "-q", "-b", "tend/d")
	commit(t, other, "package.json", `{"v":2}`, "yesterday's run")
	must(t, other, "push", "origin", "tend/d")

	wt := r.worktree(t)
	if _, err := gitx.Run(bg, wt.Dir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/tend/d"); err == nil {
		t.Fatal("test setup: the tracking ref already exists")
	}
	today := commit(t, wt.Dir, "package.json", `{"v":3}`, "today's run")
	if err := Push(bg, wt, r.push, "tend/d", true); err != nil {
		t.Fatal(err)
	}
	if got := r.tip(t, "tend/d"); got != today {
		t.Errorf("remote branch = %q, want %q", got, today)
	}
}

func TestPushAfterBranchDeletedOnRemote(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	commit(t, wt.Dir, "package.json", `{"v":2}`, "bump")
	if err := Push(bg, wt, r.push, "tend/e", false); err != nil {
		t.Fatal(err)
	}
	must(t, r.bare, "branch", "-D", "tend/e")
	must(t, wt.Dir, "reset", "--hard", r.base)
	again := commit(t, wt.Dir, "package.json", `{"v":3}`, "bump again")
	if err := Push(bg, wt, r.push, "tend/e", true); err != nil {
		t.Fatal(err)
	}
	if r.tip(t, "tend/e") != again {
		t.Error("branch was not recreated")
	}
}

func TestPushErrors(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	commit(t, wt.Dir, "package.json", `{"v":2}`, "bump")
	err := Push(bg, wt, r.remote(t, "nowhere"), "tend/f", false)
	if err == nil || errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v", err)
	}
	if err := Push(bg, wt, r.push, "tend/f", true); err != nil {
		t.Fatal(err)
	}
}

func TestBranchExists(t *testing.T) {
	r := newRemote(t)
	for branch, want := range map[string]bool{"main": true, "tend/none": false, "mai": false} {
		got, err := BranchExists(bg, r.push, branch)
		if err != nil || got != want {
			t.Errorf("BranchExists(%s) = %v, %v", branch, got, err)
		}
	}
	if _, err := BranchExists(bg, r.remote(t, "nowhere"), "main"); err == nil {
		t.Error("an unreachable remote was reported as a missing branch")
	}
}

func TestDefaultRemote(t *testing.T) {
	needGit(t)
	tests := []struct {
		name    string
		remotes []string
		want    string
		fail    bool
	}{
		{"origin", []string{"upstream", "origin"}, "origin", false},
		{"only one", []string{"github"}, "github", false},
		{"none", nil, "", true},
		{"several, no origin", []string{"a", "b"}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			must(t, dir, "init", "-q")
			for _, n := range tc.remotes {
				must(t, dir, "remote", "add", n, "https://example.test/"+n)
			}
			got, err := DefaultRemote(bg, dir)
			if (err != nil) != tc.fail || got != tc.want {
				t.Errorf("DefaultRemote = %q, %v", got, err)
			}
		})
	}
}

func TestRemoteURL(t *testing.T) {
	needGit(t)
	dir := t.TempDir()
	must(t, dir, "init", "-q")
	must(t, dir, "remote", "add", "origin", "git@github.com:acme/shop.git")
	got, err := RemoteURL(bg, dir, "origin")
	if err != nil || got != "git@github.com:acme/shop.git" {
		t.Fatalf("RemoteURL = %q, %v", got, err)
	}
	if owner, name, ok := RepoFromRemote(got); !ok || owner != "acme" || name != "shop" {
		t.Errorf("RepoFromRemote(%q) = %s %s %v", got, owner, name, ok)
	}
}

func TestSameTree(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	pushed := commit(t, wt.Dir, "package.json", `{"v":2}`, "bump")
	if err := Push(bg, wt, r.push, "tend/g", false); err != nil {
		t.Fatal(err)
	}
	if same, tip, err := SameTree(bg, wt, r.push, "tend/g"); err != nil || !same || tip != pushed {
		t.Fatalf("SameTree after a push = %v, %s, %v", same, tip, err)
	}

	must(t, wt.Dir, "commit", "--amend", "-m", "bump, reworded")
	if same, tip, err := SameTree(bg, wt, r.push, "tend/g"); err != nil || !same || tip != pushed {
		t.Errorf("a reworded commit has the same files: %v, %s, %v", same, tip, err)
	}

	commit(t, wt.Dir, "package.json", `{"v":3}`, "more")
	if same, _, _ := SameTree(bg, wt, r.push, "tend/g"); same {
		t.Error("different files reported as the same")
	}
	if _, _, err := SameTree(bg, wt, r.push, "tend/missing"); err == nil {
		t.Error("a branch that does not exist has no tree to compare")
	}
}

func TestPushLeaseOnAKnownTip(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	first := commit(t, wt.Dir, "package.json", `{"v":2}`, "bump")
	if err := Push(bg, wt, r.push, "tend/h", false); err != nil {
		t.Fatal(err)
	}

	// A fresh CI checkout has no tracking ref; the known tip is all there is
	// to tell tend's own commit from a human's.
	other := r.elsewhere(t)
	must(t, other, "checkout", "-q", "tend/h")
	theirs := commit(t, other, "notes.txt", "mine\n", "human fix")
	must(t, other, "push", "origin", "tend/h")

	must(t, wt.Dir, "reset", "--hard", r.base)
	commit(t, wt.Dir, "package.json", `{"v":3}`, "bump again")
	must(t, wt.Dir, "update-ref", "-d", "refs/remotes/origin/tend/h")
	if err := push(bg, wt, r.push, "tend/h", true, first); !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
	if r.tip(t, "tend/h") != theirs {
		t.Error("the human's commit was overwritten")
	}
	if err := push(bg, wt, r.push, "tend/h", true, theirs); err != nil {
		t.Fatalf("a lease on the real tip must pass: %v", err)
	}
}

// A command that rewrites the repository's git config, to send github.com
// traffic and the token with it somewhere else, stops every call to the remote.
func TestRemoteRefusesChangedSettings(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	commit(t, wt.Dir, "package.json", `{"v":2}`, "bump")
	must(t, r.clone, "config", "url.https://example.net/.insteadOf", "https://github.com/")
	err := Push(bg, wt, r.push, "tend/i", false)
	if err == nil || !strings.Contains(err.Error(), "git settings changed") {
		t.Fatalf("err = %v", err)
	}
	if r.tip(t, "tend/i") != "" {
		t.Error("the push went out")
	}
}

func TestRemoteAuth(t *testing.T) {
	r := newRemote(t)
	gh, err := NewRemote(bg, r.clone, "origin", "ghs_x", "acme", "shop")
	if err != nil {
		t.Fatal(err)
	}
	if gh.URL != "https://github.com/acme/shop.git" {
		t.Errorf("URL = %q", gh.URL)
	}
	env := strings.Join(gh.auth(), "\n")
	if !strings.Contains(env, "GIT_CONFIG_VALUE_2=AUTHORIZATION: basic eC1hY2Nlc3MtdG9rZW46Z2hzX3g=") || !strings.HasPrefix(env, "GIT_CONFIG_COUNT=4\n") {
		t.Errorf("auth:\n%s", env)
	}
	if r.push.URL != "origin" || r.push.auth() != nil {
		t.Errorf("without a token: %+v", r.push)
	}
}
