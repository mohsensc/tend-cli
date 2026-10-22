//go:build unix

package verify

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
)

func TestRunTimeoutKillsTree(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()
	res, err := Run(context.Background(), &Worktree{Dir: dir}, Options{
		Commands: core.Commands{
			Test: []string{"(sleep 5; echo late > late) & echo $! > child; echo started; sleep 30", "true"},
		},
		Timeout: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Fatalf("Run took %s after a 300ms timeout", took)
	}
	st := res.Steps[0]
	if res.Pass || st.Exit != -1 || !strings.HasSuffix(st.Tail, "started\ntimed out after 300ms") {
		t.Errorf("step = %+v", st)
	}
	if res.Steps[1].Skipped != "timed out" {
		t.Errorf("second step = %+v", res.Steps[1])
	}
	gone(t, readPid(t, filepath.Join(dir, "child")))
}

func TestRunReapsStragglers(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()
	res, err := Run(context.Background(), &Worktree{Dir: dir}, Options{
		Commands: core.Commands{Test: []string{"sleep 30 & echo $! > child; echo done"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Fatalf("a background child held the step for %s", took)
	}
	if !res.Pass || res.Steps[0].Tail != "done" {
		t.Errorf("Run = %+v", res)
	}
	gone(t, readPid(t, filepath.Join(dir, "child")))
}

// gone waits for pid to disappear. A killed orphan lingers as a zombie until
// init reaps it, which on a busy machine takes a moment.
func gone(t *testing.T, pid int) {
	t.Helper()
	var err error
	for range 100 {
		if err = syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("background child %d still alive: %v", pid, err)
}

func readPid(t *testing.T, p string) int {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// Inside a container the commands print /work paths; the warnings and the
// tail still read as paths in the worktree.
func TestRunContainerPaths(t *testing.T) {
	bin := t.TempDir()
	docker := "#!/bin/sh\necho '/work/app/report.py:7: DeprecationWarning: fetch_all() is deprecated'\necho 'at /work/src/a.js:3'\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(docker), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	res, err := Run(context.Background(), &Worktree{Dir: t.TempDir()}, Options{
		Commands:  core.Commands{Test: []string{"python3 -m pytest"}},
		Container: "python:3.13",
		LogDir:    filepath.Join(t.TempDir(), "logs"),
		Warnings:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || res.Warnings[0].File != "app/report.py" || res.Warnings[0].Line != 7 {
		t.Errorf("warnings = %+v", res.Warnings)
	}
	if want := "at src/a.js:3"; !strings.HasSuffix(res.Steps[0].Tail, want) {
		t.Errorf("tail = %q, want it to end in %q", res.Steps[0].Tail, want)
	}
}
