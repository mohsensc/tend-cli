package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/deliver"
	"github.com/mohsensc/tend-cli/internal/state"
)

// A check while a run holds the lock still reports, from the state that run
// last saved, and saves nothing over it.
func TestCheckWhileLocked(t *testing.T) {
	dir := repo(t, map[string]string{"package.json": `{"dependencies": {"lodash": "4.17.0"}}`})
	home := t.TempDir()
	if r := tend(t, dir, home, offline{}, "check"); r.code != exitOK {
		t.Fatalf("first check = %d: %s", r.code, r.stderr)
	}
	sdir := state.Dir(home, state.Key("", dir))
	before, err := os.ReadFile(sdir + "/state.json")
	if err != nil {
		t.Fatal(err)
	}
	release, err := state.Lock(sdir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	r := tend(t, dir, home, offline{}, "check")
	if r.code != exitOK || !strings.Contains(r.out, "queued") || !strings.Contains(r.stderr, "this check saves nothing") {
		t.Errorf("check while locked = %d\nstdout: %s\nstderr: %s", r.code, r.out, r.stderr)
	}
	if after, _ := os.ReadFile(sdir + "/state.json"); !bytes.Equal(before, after) {
		t.Error("a check while locked saved state")
	}
	if r := tend(t, dir, home, offline{}, "check", "--reset"); r.code != exitErr || !strings.Contains(r.stderr, "holds the lock") {
		t.Errorf("check --reset while locked = %d: %s", r.code, r.stderr)
	}
	if r := tend(t, dir, home, offline{}, "run", "--dry-run"); r.code != exitErr || !strings.Contains(r.stderr, "holds the lock") {
		t.Errorf("run while locked = %d: %s", r.code, r.stderr)
	}
}

// A repo GitHub does not show the token is said once, in words, not as
// three raw API errors; check goes on without the forge and run stops.
func TestNoRepo(t *testing.T) {
	dir := repo(t, map[string]string{"package.json": `{"dependencies": {"lodash": "4.17.0"}}`})
	missing := deliver.NewFake("acme", "shop", "main")
	missing.Err = &deliver.NoRepo{Repo: "acme/shop"}
	call := func(args ...string) tendRun {
		var out, errs bytes.Buffer
		e := DefaultEnv()
		e.Dir, e.Home, e.Stdout, e.Stderr, e.Stdin = dir, t.TempDir(), &out, &errs, strings.NewReader("")
		e.Fetch, e.Forge, e.Getenv = offline{}, missing, func(string) string { return "" }
		return tendRun{Main(args, e), out.String(), errs.String()}
	}
	r := call("check")
	head, _, _ := strings.Cut(r.out, "\n")
	if r.code != exitOK || !strings.HasSuffix(head, "open PRs unknown (GitHub repo acme/shop not found, or the token cannot see it)") || strings.Contains(r.out, "/repos/") {
		t.Errorf("check = %d\nstdout: %s\nstderr: %s", r.code, r.out, r.stderr)
	}
	r = call("run")
	if r.code != exitErr || r.stderr != "tend: GitHub repo acme/shop not found, or the token cannot see it\n" {
		t.Errorf("run = %d\nstdout: %s\nstderr: %s", r.code, r.out, r.stderr)
	}
}

// update on a build it cannot replace says so and fails; update --check,
// which was asked to change nothing, does not.
func TestUpdateDevBuild(t *testing.T) {
	for _, tt := range []struct {
		args []string
		code int
	}{{[]string{"update"}, exitErr}, {[]string{"update", "--check"}, exitOK}} {
		var out, errs bytes.Buffer
		e := DefaultEnv()
		e.Dir, e.Home, e.Stdout, e.Stderr, e.Version = t.TempDir(), t.TempDir(), &out, &errs, "v1.0.0-2-gf5ba9bf"
		e.Fetch, e.Getenv = offline{}, func(string) string { return "" }
		if code := Main(tt.args, e); code != tt.code || !strings.Contains(out.String()+errs.String(), "development build") {
			t.Errorf("%v = %d: %s%s", tt.args, code, out.String(), errs.String())
		}
	}
}

// A dry run that prepares nothing leaves no worktree, and a later run
// clears the one a dry run on an older base kept.
func TestWorktreesSwept(t *testing.T) {
	dir := repo(t, map[string]string{"README.md": "hi\n"})
	home := t.TempDir()
	if r := tend(t, dir, home, offline{}, "run", "--dry-run"); r.code != exitOK || len(worktrees(t, home)) != 0 {
		t.Fatalf("dry run = %d %q, worktrees %q", r.code, r.out, worktrees(t, home))
	}
	if r := tend(t, dir, home, offline{}, "run", "--keep"); r.code != exitOK || len(worktrees(t, home)) != 1 {
		t.Fatalf("run --keep = %d %q", r.code, r.stderr)
	}
	old := worktrees(t, home)[0]
	writeFiles(t, dir, map[string]string{"README.md": "hello\n"})
	gitIn(t, dir, "commit", "--quiet", "-am", "Next")
	if r := tend(t, dir, home, offline{}, "run"); r.code != exitOK {
		t.Fatalf("run = %d %q", r.code, r.stderr)
	}
	if wts := worktrees(t, home); len(wts) != 0 {
		t.Errorf("worktrees left: %q", wts)
	}
	if list := gitIn(t, dir, "worktree", "list"); strings.Contains(list, old) {
		t.Errorf("git still lists %s:\n%s", old, list)
	}
}

// An origin that is a local path is no GitHub repo, even with two path
// segments, and GITHUB_REPOSITORY does not stand in for it: only a repo
// with no origin reads that.
func TestCheckNotGit(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"package.json": `{"dependencies": {"lodash": "4.17.0"}}`})
	var out, errs bytes.Buffer
	e := DefaultEnv()
	e.Dir, e.Home, e.Stdout, e.Stderr, e.Stdin = dir, t.TempDir(), &out, &errs, strings.NewReader("")
	e.Fetch = offline{}
	e.Getenv = func(string) string { return "" }
	if code := Main([]string{"check"}, e); code != exitOK || !strings.Contains(out.String(), "open PRs unknown (not a git repository)") {
		t.Errorf("check = %d\nstdout: %s\nstderr: %s", code, out.String(), errs.String())
	}
}

func TestLocalOrigin(t *testing.T) {
	dir := repo(t, map[string]string{"package.json": `{"dependencies": {"lodash": "4.17.0"}}`})
	gitIn(t, dir, "remote", "add", "origin", "/srv/repo")
	var out, errs bytes.Buffer
	e := DefaultEnv()
	e.Dir, e.Home, e.Stdout, e.Stderr, e.Stdin = dir, t.TempDir(), &out, &errs, strings.NewReader("")
	e.Fetch = offline{}
	e.Getenv = func(k string) string {
		if k == "GITHUB_REPOSITORY" {
			return "acme/shop"
		}
		return ""
	}
	if code := Main([]string{"check"}, e); code != exitOK || !strings.Contains(out.String(), "open PRs unknown (no GitHub origin remote)") {
		t.Errorf("check = %d\nstdout: %s\nstderr: %s", code, out.String(), errs.String())
	}
}

func TestShowSteps(t *testing.T) {
	got := showSteps([]string{"go build ./...", "cd ci && go build ./...", "go vet ./... && go test ./..."})
	if want := "go build ./... && (cd ci && go build ./...) && go vet ./... && go test ./..."; got != want {
		t.Errorf("showSteps = %q, want %q", got, want)
	}
}

func TestListedUnproven(t *testing.T) {
	env := map[string]string{"GITHUB_ACTIONS": "true"}
	var out bytes.Buffer
	s := &session{e: Env{Stdout: &out, Getenv: func(k string) string { return env[k] }}, st: state.New(), forge: deliver.NewFake("acme", "shop", "main")}
	s.st.Issue.Number = 7
	res := &outcome{Action: "unproven", Summary: "could not prove npm/chalk@6.0.1: npm test failed (exit 1)", code: exitUnproven, parked: true}
	if code := s.report(res); code != exitOK || !strings.Contains(out.String(), "::warning title=tend::could not prove npm/chalk@6.0.1: npm test failed (exit 1); listed in #7") {
		t.Errorf("report = %d, %q", code, out.String())
	}
	env["TEND_FAIL_ON_UNPROVEN"] = "true"
	if code := s.report(res); code != exitUnproven {
		t.Errorf("with fail-on-unproven, report = %d", code)
	}
	delete(env, "TEND_FAIL_ON_UNPROVEN")
	if code := s.report(&outcome{Action: "unproven", code: exitUnproven}); code != exitUnproven {
		t.Errorf("a CI revert failure, report = %d", code)
	}
	delete(env, "GITHUB_ACTIONS")
	if code := s.report(res); code != exitUnproven {
		t.Errorf("run by hand, report = %d", code)
	}
}
