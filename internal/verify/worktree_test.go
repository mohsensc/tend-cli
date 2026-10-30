package verify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/gitx"
)

// isolate keeps the developer's git config (hooks, signing, identity) out of tests.
func isolate(t *testing.T) {
	t.Helper()
	empty := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", empty)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func newRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	isolate(t)
	dir := t.TempDir()
	run(t, dir, "git", "init", "-q", "-b", "main")
	write(t, dir, files)
	run(t, dir, "git", "add", "-A")
	run(t, dir, "git", "-c", "user.name=a", "-c", "user.email=a@b", "commit", "-q", "-m", "init")
	return dir
}

func TestWorktreeLifecycle(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t, map[string]string{
		"main.go":    "package main\n",
		".gitignore": "node_modules/\n.venv/\n",
	})
	base := run(t, repo, "git", "rev-parse", "HEAD")
	dir := filepath.Join(t.TempDir(), "wt", "x")

	w, err := Checkout(ctx, repo, dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if w.Base != base {
		t.Errorf("Base = %q, want the resolved sha %q", w.Base, base)
	}
	write(t, dir, map[string]string{
		"main.go":             "package main\n\nfunc main() {}\n",
		"new.txt":             "hello\n",
		"node_modules/x/x.js": "junk\n",
		".venv/bin/python":    "junk\n",
	})

	sha, err := w.Commit(ctx, "Bump x", Author{"Jane", "jane@example.com"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if sha == base || len(sha) != 40 {
		t.Errorf("Commit sha = %q", sha)
	}
	if got := run(t, dir, "git", "log", "-1", "--format=%an <%ae>|%s"); got != "Jane <jane@example.com>|Bump x" {
		t.Errorf("commit = %q", got)
	}
	if got := run(t, dir, "git", "show", "--name-only", "--format=", "HEAD"); got != "main.go\nnew.txt" {
		t.Errorf("committed files = %q", got)
	}

	if err := w.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("worktree dir still exists: %v", err)
	}
	if got := run(t, repo, "git", "worktree", "list", "--porcelain"); strings.Count(got, "worktree ") != 1 {
		t.Errorf("worktree still registered:\n%s", got)
	}
}

func TestCommitEmpty(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t, map[string]string{"a": "a\n"})
	w, err := Checkout(ctx, repo, filepath.Join(t.TempDir(), "wt"), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.Remove(ctx); err != nil {
			t.Error(err)
		}
	})
	if _, err := w.Commit(ctx, "nothing", fallbackAuthor, false); err == nil {
		t.Error("commit with nothing staged succeeded without allowEmpty")
	}
	sha, err := w.Commit(ctx, "Decide: migrate off x", fallbackAuthor, true)
	if err != nil {
		t.Fatal(err)
	}
	if sha == w.Base {
		t.Error("empty commit did not move HEAD")
	}
	if got := run(t, w.Dir, "git", "diff", "--name-only", w.Base, "HEAD"); got != "" {
		t.Errorf("the empty commit changed %q", got)
	}
}

func TestCheckoutStale(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t, map[string]string{"a": "a\n"})
	dir := filepath.Join(t.TempDir(), "wt")

	if _, err := Checkout(ctx, repo, dir, "main"); err != nil {
		t.Fatal(err)
	}
	write(t, dir, map[string]string{"junk": "left by a crash\n"})
	w, err := Checkout(ctx, repo, dir, "main")
	if err != nil {
		t.Fatalf("checkout over a registered worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "junk")); !os.IsNotExist(err) {
		t.Error("stale file survived")
	}
	if err := w.Remove(ctx); err != nil {
		t.Fatal(err)
	}

	write(t, dir, map[string]string{"junk": "not a worktree\n"})
	if _, err := Checkout(ctx, repo, dir, "main"); err != nil {
		t.Fatalf("checkout over a plain dir: %v", err)
	}
	if _, err := Checkout(ctx, repo, filepath.Join(t.TempDir(), "x"), "no-such-ref"); err == nil {
		t.Error("checkout of a missing ref succeeded")
	}
}

func TestAuthorFrom(t *testing.T) {
	env := func(kv ...string) func(string) string {
		return func(k string) string {
			for i := 0; i < len(kv); i += 2 {
				if kv[i] == k {
					return kv[i+1]
				}
			}
			return ""
		}
	}
	configured := newRepo(t, map[string]string{"a": "a\n"})
	run(t, configured, "git", "config", "user.name", "Repo Owner")
	run(t, configured, "git", "config", "user.email", "owner@example.com")
	bare := newRepo(t, map[string]string{"a": "a\n"})
	half := newRepo(t, map[string]string{"a": "a\n"})
	run(t, half, "git", "config", "user.name", "Only Name")

	tests := []struct {
		name   string
		repo   string
		getenv func(string) string
		want   Author
	}{
		{"git config wins", configured, env("TEND_GIT_USER", "bot", "TEND_GIT_EMAIL", "bot@x"), Author{"Repo Owner", "owner@example.com"}},
		{"env when no git config", bare, env("TEND_GIT_USER", "bot", "TEND_GIT_EMAIL", "bot@x"), Author{"bot", "bot@x"}},
		{"half git config falls through", half, env("TEND_GIT_USER", "bot", "TEND_GIT_EMAIL", "bot@x"), Author{"bot", "bot@x"}},
		{"half env falls through", bare, env("TEND_GIT_USER", "bot"), fallbackAuthor},
		{"nothing set", bare, env(), Author{"tend", "tend@users.noreply.github.com"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AuthorFrom(tt.repo, tt.getenv); got != tt.want {
				t.Errorf("AuthorFrom = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// A command in the worktree points its .git at a git dir of its own, with a
// file system monitor, a hook and a clean filter in the config. None of them
// runs when tend uses git there afterwards, as it does to stage and commit.
func TestWorktreeIgnoresARewrittenGitDir(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t, map[string]string{"main.go": "package main\n"})
	w, err := Checkout(ctx, repo, filepath.Join(t.TempDir(), "wt"), "main")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := w.Remove(ctx); err != nil {
			t.Error(err)
		}
	}()
	pwned := filepath.Join(t.TempDir(), "pwned")
	step := `set -e
mkdir -p .x/hooks .x/objects .x/refs
printf 'ref: refs/heads/main\n' > .x/HEAD
printf '[core]\n\trepositoryformatversion = 0\n\tfsmonitor = "touch ` + pwned + `"\n[filter "x"]\n\tclean = "touch ` + pwned + `; cat"\n' > .x/config
printf '#!/bin/sh\ntouch ` + pwned + `\n' > .x/hooks/pre-commit
chmod +x .x/hooks/pre-commit
printf '* filter=x\n' > .gitattributes
rm -f .git
printf 'gitdir: .x\n' > .git
echo changed >> main.go
`
	res, err := Run(ctx, w, Options{Commands: core.Commands{Test: []string{step}}})
	if err != nil || !res.Pass {
		t.Fatalf("step: %+v, %v", res, err)
	}
	if _, err := w.Commit(ctx, "Change main", Author{"Jane", "jane@example.com"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pwned); err == nil {
		t.Fatal("a command from the rewritten git dir ran")
	}
}

// A project whose .gitignore does not list node_modules keeps its install
// across a clean, and never stages it, while a vendor dir it tracks stays
// tracked.
func TestWorktreeKeepsInstalls(t *testing.T) {
	repo := newRepo(t, map[string]string{"package.json": "{}\n", "vendor/venv/a.txt": "a\n"})
	ctx := context.Background()
	wt, err := Checkout(ctx, repo, filepath.Join(t.TempDir(), "wt"), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = wt.Remove(ctx) }()
	write(t, wt.Dir, map[string]string{"node_modules/lodash/index.js": "x\n", "src/__pycache__/a.pyc": "x", "vendor/venv/b.txt": "b\n", "built.txt": "x\n"})
	if _, err := gitx.Run(ctx, wt.Dir, "clean", "--quiet", "--force", "-d"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"node_modules/lodash/index.js", "src/__pycache__/a.pyc"} {
		if _, err := os.Stat(filepath.Join(wt.Dir, p)); err != nil {
			t.Errorf("clean took %s away: %v", p, err)
		}
	}
	for _, p := range []string{"built.txt", "vendor/venv/b.txt"} {
		if _, err := os.Stat(filepath.Join(wt.Dir, p)); err == nil {
			t.Errorf("clean left %s", p)
		}
	}
	write(t, wt.Dir, map[string]string{"vendor/venv/b.txt": "b\n"})
	if _, err := gitx.Run(ctx, wt.Dir, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	staged, _ := gitx.Run(ctx, wt.Dir, "diff", "--cached", "--name-only")
	if staged != "vendor/venv/b.txt" {
		t.Errorf("staged %q, want only vendor/venv/b.txt", staged)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "info", "exclude")); err == nil {
		if data, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude")); strings.Contains(string(data), "node_modules") {
			t.Error("the user's info/exclude was written")
		}
	}
}
