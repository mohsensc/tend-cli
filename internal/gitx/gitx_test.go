package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func repo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "Ada"}, {"config", "user.email", "ada@example.test"}} {
		if _, err := Run(context.Background(), dir, args...); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// git never sees tend's tokens, so nothing it runs can hand them on.
func TestRunScrubsTheEnvironment(t *testing.T) {
	dir := repo(t)
	t.Setenv("GITHUB_TOKEN", "ghs_secret")
	t.Setenv("TEND_MODEL_KEY", "sk_secret")
	if _, err := Run(context.Background(), dir, "config", "alias.env", "!env"); err != nil {
		t.Fatal(err)
	}
	out, err := Run(context.Background(), dir, "env")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "secret") || !strings.Contains(out, "GIT_TERMINAL_PROMPT=0") {
		t.Errorf("git ran with:\n%s", out)
	}
}

// A caller's GIT_DIR and friends, as a hook that runs tend leaves them, never
// point git away from the directory tend asked for.
func TestRunIgnoresTheCallersRepository(t *testing.T) {
	dir := repo(t)
	other := repo(t)
	ctx := context.Background()
	for k, v := range map[string]string{
		"GIT_DIR":              filepath.Join(other, ".git"),
		"GIT_WORK_TREE":        other,
		"GIT_INDEX_FILE":       filepath.Join(other, ".git", "index"),
		"GIT_OBJECT_DIRECTORY": filepath.Join(other, ".git", "objects"),
		"GIT_COMMON_DIR":       filepath.Join(other, ".git"),
	} {
		t.Setenv(k, v)
	}
	top, err := Run(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(dir)
	if got, _ := filepath.EvalSymlinks(top); got != want {
		t.Errorf("toplevel = %q, want %q", top, dir)
	}
	if _, err := Run(ctx, dir, "commit", "-q", "--allow-empty", "-m", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, other, "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
		t.Error("the commit landed in the caller's repository")
	}
}

func TestRunSkipsHooks(t *testing.T) {
	dir := repo(t)
	marker := filepath.Join(t.TempDir(), "ran")
	hook := filepath.Join(dir, ".git", "hooks", "post-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), dir, "commit", "-q", "--allow-empty", "-m", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a hook ran")
	}
}

func TestFilesListsAConflictOnce(t *testing.T) {
	dir := repo(t)
	ctx := context.Background()
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(dir, "f"), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		if _, err := Run(ctx, dir, args...); err != nil {
			t.Fatal(err)
		}
	}
	write("a\n")
	git("add", "f")
	git("commit", "-qm", "a")
	git("checkout", "-qb", "side")
	write("b\n")
	git("commit", "-qam", "b")
	git("checkout", "-q", "main")
	write("c\n")
	git("commit", "-qam", "c")
	if _, err := Run(ctx, dir, "merge", "-q", "side"); err == nil {
		t.Fatal("merge did not conflict")
	}
	if err := os.WriteFile(filepath.Join(dir, "g"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Files(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if want := []string{"f", "g"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Files = %q, want %q", got, want)
	}
}
