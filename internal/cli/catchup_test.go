package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/deliver"
	"github.com/mohsensc/tend-cli/internal/gitx"
	"github.com/mohsensc/tend-cli/internal/plan"
	"github.com/mohsensc/tend-cli/internal/scan"
	"github.com/mohsensc/tend-cli/internal/state"
	"github.com/mohsensc/tend-cli/internal/verify"
)

// Tests are weak with no test command, or when a file the catch-up changes
// is reached by no test; a test of every such file is enough.
func TestWeak(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{
		"src/app.js":       "module.exports = {}\n",
		"src/client.js":    "module.exports = {}\n",
		"test/app.test.js": "require('../src/app')\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	idx, err := scan.Build(scan.Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	tests := core.Commands{Test: []string{"npm test"}}
	for _, tt := range []struct {
		name  string
		cmds  core.Commands
		files []string
		want  string
	}{
		{"no test command", core.Commands{}, []string{"src/app.js"}, "there is no test command"},
		{"every file reached", tests, []string{"src/app.js"}, ""},
		{"one not reached", tests, []string{"src/app.js", "src/client.js"}, "no test reaches `src/client.js`, which the catch-up changes"},
		{"nothing changes", tests, nil, ""},
	} {
		if got := weak(tt.cmds, idx, tt.files); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
	b := &core.Batch{
		Changes: []core.Change{{Deps: []core.Dep{{Manifest: core.Manifest{Path: "package.json"}}}, Sites: []core.Site{{File: "package.json", Line: 3}, {File: "src/app.js", Line: 1}, {File: "test/app.test.js", Line: 1}}}},
		Next:    []core.Later{{Change: core.Change{Sites: []core.Site{{File: "src/client.js", Line: 2}, {File: "Dockerfile", Line: 1}}}}},
	}
	if got := touched(b); strings.Join(got, " ") != "src/app.js src/client.js" {
		t.Errorf("touched = %q", got)
	}
}

// Once the first PR merged, each run opens the next stage, and the one that
// finds none left ends the catch-up. A state from before stages, or one
// whose catch-up is over, opens none.
func TestStaged(t *testing.T) {
	major := core.Change{ID: "npm/b@3.0.0", Kind: core.Breaking, Level: "major", Deps: []core.Dep{{Ecosystem: core.Npm, Name: "b", Version: "1.0.0"}}, To: "3.0.0"}
	p := &plan.Plan{Queue: []core.Change{major}}
	for _, tt := range []struct {
		name      string
		stage     int
		queue     *plan.Plan
		want      string
		stageThen int
	}{
		{"next stage", plan.Fixes, p, core.StageID(plan.Majors), plan.Fixes},
		{"nothing left", plan.Majors, p, "", plan.Last},
		{"before stages", 0, p, "", 0},
		{"over", plan.Last, p, "", plan.Last},
	} {
		s := &session{e: Env{Stderr: &bytes.Buffer{}}, st: state.New()}
		s.st.Stage = tt.stage
		ch := s.staged(tt.queue)
		got := ""
		if ch != nil {
			got = ch.ID
		}
		if got != tt.want || s.st.Stage != tt.stageThen {
			t.Errorf("%s: %q, stage %d; want %q, %d", tt.name, got, s.st.Stage, tt.want, tt.stageThen)
		}
	}
}

// The tracking issue lists what failed on this base, what was reverted and
// what has no known replacement; it is opened, left alone while nothing
// changes, and closed once the list is empty.
func TestTrack(t *testing.T) {
	ctx := context.Background()
	f := deliver.NewFake("acme", "shop", "main")
	var errs bytes.Buffer
	s := &session{e: Env{Stderr: &errs}, cfg: config.Default(), st: state.New(), forge: f}
	now := time.Now()
	s.st.Fail("npm/a@2.0.0", "npm test failed (exit 1)\nmore", "base1", now)
	s.st.Fail("npm/old@2.0.0", "npm test failed", "base0", now)
	s.st.Parked["npm/b@3.0.0"] = state.Parked{Reason: "reverted", Detail: "reverted in abc1234: CI failed on def5678 (test)"}
	s.st.Parked["npm/c@1.0.0"] = state.Parked{Reason: "rejected"}
	p := &plan.Plan{Queue: []core.Change{{ID: "npm/moment@", Kind: core.Deprecation, Deps: []core.Dep{{Name: "moment"}}}}}

	s.track(ctx, p, "base1")
	is := f.Issues()
	if len(is) != 1 || !is[0].Open {
		t.Fatalf("issues = %+v", is)
	}
	for _, want := range []string{
		"- `npm/a@2.0.0`: could not be made or proven on base1: npm test failed (exit 1)\n",
		"- `npm/b@3.0.0`: reverted in abc1234: CI failed on def5678 (test)\n",
		"- `moment`: deprecated, and tend knows no replacement for it, so it stays\n",
		trackMarker,
	} {
		if !strings.Contains(is[0].Body, want) {
			t.Errorf("the issue lacks %q:\n%s", want, is[0].Body)
		}
	}
	if strings.Contains(is[0].Body, "npm/old") || strings.Contains(is[0].Body, "npm/c@") {
		t.Errorf("the issue lists what failed on another base or a maintainer closed:\n%s", is[0].Body)
	}

	// Nothing new: the forge is not asked, so its failing goes unnoticed.
	f.Err = context.Canceled
	s.track(ctx, p, "base1")
	f.Err = nil
	if strings.Contains(errs.String(), "cannot") {
		t.Errorf("a run with nothing new asked the forge: %s", errs.String())
	}

	delete(s.st.Seen, "npm/a@2.0.0")
	delete(s.st.Parked, "npm/b@3.0.0")
	s.track(ctx, &plan.Plan{}, "base1")
	if is := f.Issues(); len(is) != 1 || is[0].Open || !strings.HasPrefix(is[0].Body, "Everything tend listed here is done") {
		t.Errorf("after the list emptied: %+v", is)
	}
	if !strings.Contains(errs.String(), "closed #1000") {
		t.Errorf("logged %q", errs.String())
	}

	// Lost state finds the issue by its marker and opens it again.
	s.st = state.New()
	s.st.Fail("npm/a@2.0.0", "npm test failed", "base2", now)
	s.track(ctx, &plan.Plan{}, "base2")
	if is := f.Issues(); len(is) != 1 || !is[0].Open || !strings.Contains(is[0].Body, "npm/a@2.0.0") {
		t.Errorf("after losing state: %+v", is)
	}
}

// Each part of a catch-up PR is made on top of the baseline the PR adds, so
// the baseline's test line finds its files; and the baseline is staged, so
// the clean-up after a run leaves it.
func TestRestart(t *testing.T) {
	need := func(tool string) {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not found")
		}
	}
	need("git")
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	for _, args := range [][]string{{"init", "-q"}, {"commit", "-q", "--allow-empty", "-m", "base"}} {
		if _, err := gitx.Run(ctx, dir, append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...); err != nil {
			t.Fatal(err)
		}
	}
	base, err := gitx.Run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	r := &runner{wt: &verify.Worktree{Dir: dir, Base: base}}
	floor := "diff --git a/test/tend/smoke.js b/test/tend/smoke.js\nnew file mode 100644\n--- /dev/null\n+++ b/test/tend/smoke.js\n@@ -0,0 +1 @@\n+require('node:test')\n"
	if err := os.WriteFile(filepath.Join(dir, "left-over.js"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.restart(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "left-over.js")); err == nil {
		t.Error("restart left a file behind")
	}
	r.floor = floor
	if err := r.restart(ctx); err != nil {
		t.Fatal(err)
	}
	if err := discard(ctx, r.wt); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "test", "tend", "smoke.js")); err != nil || string(b) != "require('node:test')\n" {
		t.Errorf("the baseline is not there after a run's clean-up: %q, %v", b, err)
	}
}
