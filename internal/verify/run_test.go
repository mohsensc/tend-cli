package verify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
)

func TestRunPass(t *testing.T) {
	dir, logs := t.TempDir(), t.TempDir()
	res, err := Run(context.Background(), &Worktree{Dir: dir}, Options{
		Commands: core.Commands{
			Install: []string{"echo installing > installed"},
			Build:   []string{"test -f installed && echo built"},
			Test:    []string{"echo ok; echo oops >&2"},
		},
		LogDir: logs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pass || len(res.Steps) != 3 {
		t.Fatalf("Run = %+v", res)
	}
	for _, st := range res.Steps {
		if st.Exit != 0 || st.Skipped != "" || st.Took <= 0 {
			t.Errorf("step %+v", st)
		}
	}
	last := res.Steps[2]
	if last.Tail != "ok\noops" {
		t.Errorf("Tail = %q", last.Tail)
	}
	if want := filepath.Join(logs, "3-echo-ok-echo-oops-2.log"); last.Log != want {
		t.Errorf("Log = %q, want %q", last.Log, want)
	}
	b, err := os.ReadFile(last.Log)
	if err != nil || string(b) != "ok\noops\n" {
		t.Errorf("log = %q, %v", b, err)
	}
}

func TestRunStopsAtFailure(t *testing.T) {
	res, err := Run(context.Background(), &Worktree{Dir: t.TempDir()}, Options{
		Commands: core.Commands{
			Install: []string{"true"},
			Build:   []string{"echo broken; exit 3"},
			Test:    []string{"echo never > ran", "true"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pass {
		t.Error("Pass after a failing step")
	}
	want := []core.Step{
		{Cmd: "true", Phase: core.PhaseInstall},
		{Cmd: "echo broken; exit 3", Phase: core.PhaseBuild, Exit: 3, Tail: "broken"},
		{Cmd: "echo never > ran", Phase: core.PhaseTest, Skipped: "previous step failed"},
		{Cmd: "true", Phase: core.PhaseTest, Skipped: "previous step failed"},
	}
	for i, st := range res.Steps {
		st.Took = 0
		if st != want[i] {
			t.Errorf("step %d = %+v, want %+v", i, st, want[i])
		}
	}
}

func TestRunEmpty(t *testing.T) {
	res, err := Run(context.Background(), &Worktree{Dir: t.TempDir()}, Options{})
	if err != nil || !res.Pass || len(res.Steps) != 0 {
		t.Errorf("Run = %+v, %v", res, err)
	}
}

func TestRunTail(t *testing.T) {
	res, err := Run(context.Background(), &Worktree{Dir: t.TempDir()}, Options{
		Commands: core.Commands{Test: []string{"i=1; while [ $i -le 100 ]; do echo line $i; i=$((i+1)); done; printf partial"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for i := 62; i <= 100; i++ {
		want = append(want, fmt.Sprintf("line %d", i))
	}
	want = append(want, "partial")
	if got := res.Steps[0].Tail; got != strings.Join(want, "\n") {
		t.Errorf("Tail =\n%s", got)
	}
}

func TestTailBounds(t *testing.T) {
	var tl tail
	fmt.Fprintf(&tl, "%s\nshort\n", strings.Repeat("x", 3*lineMax))
	lines := strings.Split(tl.String(), "\n")
	if len(lines) != 2 || len(lines[0]) != lineMax || lines[1] != "short" {
		t.Errorf("tail kept %d lines, first %d bytes", len(lines), len(lines[0]))
	}
	var empty tail
	if empty.String() != "" {
		t.Error("empty tail not empty")
	}
}

func TestTailKeepsFirstFailure(t *testing.T) {
	var tl tail
	fmt.Fprintln(&tl, "TAP version 13")
	fmt.Fprintln(&tl, "not ok 1 - charge")
	fmt.Fprintln(&tl, "  error: reading 'create'")
	for i := range 100 {
		fmt.Fprintf(&tl, "    at frame %d\n", i)
	}
	lines := strings.Split(tl.String(), "\n")
	if lines[0] != "not ok 1 - charge" || lines[1] != "  error: reading 'create'" || lines[firstLines] != "..." {
		t.Errorf("tail lost the first failure:\n%s", strings.Join(lines[:firstLines+1], "\n"))
	}
	if len(lines) != firstLines+1+tailLines || lines[len(lines)-1] != "    at frame 99" {
		t.Errorf("tail kept %d lines, last %q", len(lines), lines[len(lines)-1])
	}

	var near tail
	fmt.Fprint(&near, "ok\nnot ok 1\nmore")
	if got := near.String(); got != "ok\nnot ok 1\nmore" {
		t.Errorf("a failure inside the window was repeated: %q", got)
	}
}

func TestRunStream(t *testing.T) {
	var buf bytes.Buffer
	_, err := Run(context.Background(), &Worktree{Dir: t.TempDir()}, Options{
		Commands: core.Commands{Build: []string{"echo one"}, Test: []string{"echo two"}},
		Stream:   &buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "$ echo one\none\n$ echo two\ntwo\n"; buf.String() != want {
		t.Errorf("stream = %q, want %q", buf.String(), want)
	}
}

func TestRunEnv(t *testing.T) {
	t.Setenv("STRIPE_API_KEY", "sk_live")
	t.Setenv("TEND_HOME", "/somewhere")
	t.Setenv("DB_PASSWORD", "hunter2")
	t.Setenv("NPM_TOKEN", "npm_abc")
	res, err := Run(context.Background(), &Worktree{Dir: t.TempDir()}, Options{
		Commands: core.Commands{Test: []string{`echo "$STRIPE_API_KEY|$TEND_HOME|$DB_PASSWORD|$CI|$TEND"`, `test "$NPM_TOKEN" = npm_abc && echo "token $NPM_TOKEN"`}},
		Env:      []string{"NPM_TOKEN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Steps[0].Tail; got != "|||1|1" {
		t.Errorf("command saw %q", got)
	}
	// The command gets the value; the tail that leaves the machine does not.
	if got := res.Steps[1]; got.Exit != 0 || got.Tail != "token ***" {
		t.Errorf("passed through: %+v", got)
	}
}

func TestRunCommit(t *testing.T) {
	repo := newRepo(t, map[string]string{"a": "a\n"})
	res, err := Run(context.Background(), &Worktree{Dir: repo}, Options{Commands: core.Commands{Test: []string{"true"}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := run(t, repo, "git", "rev-parse", "HEAD"); res.Commit != want {
		t.Errorf("Commit = %q, want %q", res.Commit, want)
	}
}

func TestRunCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	res, err := Run(ctx, &Worktree{Dir: t.TempDir()}, Options{
		Commands: core.Commands{Test: []string{"sleep 30", "true"}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if res == nil || res.Pass || res.Steps[1].Skipped != "canceled" {
		t.Errorf("Run = %+v", res)
	}
}

func TestLocalPaths(t *testing.T) {
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	in := "at charge (" + real + "/src/billing.js:12:22)\nat " + dir + "/test/a.js:1\nkeep /usr/lib/node\n"
	want := "at charge (src/billing.js:12:22)\nat test/a.js:1\nkeep /usr/lib/node\n"
	if got := local(in, dir); got != want {
		t.Errorf("local =\n%s\nwant\n%s", got, want)
	}
}

// A step that writes git config in the worktree, as husky install does,
// leaves the repository's own config as it was.
func TestRunKeepsGitConfig(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "--quiet", "-b", "main"}, {"commit", "--quiet", "--allow-empty", "-m", "x"}} {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	cfg := filepath.Join(repo, ".git", "config")
	was, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := Checkout(context.Background(), repo, filepath.Join(t.TempDir(), "wt"), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), wt, Options{Commands: core.Commands{Install: []string{"git config core.hooksPath .husky && echo $HUSKY"}}})
	if err != nil || !res.Pass {
		t.Fatalf("run = %+v, %v", res, err)
	}
	if now, _ := os.ReadFile(cfg); string(now) != string(was) {
		t.Errorf("the repository's config changed:\n%s", now)
	}
	if !strings.Contains(res.Steps[0].Tail, "0") {
		t.Errorf("HUSKY is not 0: %q", res.Steps[0].Tail)
	}
}
