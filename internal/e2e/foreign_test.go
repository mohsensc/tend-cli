package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/deliver"
)

// State that lags the forge, here one restored from before tend's last push,
// does not make tend call its own PR foreign: the branch and the marker say
// whose it is. A commit someone else pushes on top still does.
func TestStaleStateKeepsTheOwnPR(t *testing.T) {
	need(t, "node", "npm")
	w := setup(t, "node-sdk")
	w.model = script(t, "node-sdk")
	w.tend("run").want(t, 0)
	old := must(os.ReadFile(w.statePath()))

	w.commit("Touch the readme", map[string]string{"README.md": "one\n"})
	w.model = script(t, "node-sdk")
	if res := w.tend("run").want(t, 0); !strings.HasPrefix(res.stdout, "tend: updated #1 ") {
		t.Fatalf("second run: %q", res.stdout)
	}

	if err := os.WriteFile(w.statePath(), old, 0o644); err != nil {
		t.Fatal(err)
	}
	w.commit("Touch the readme again", map[string]string{"README.md": "two\n"})
	w.model = script(t, "node-sdk")
	res := w.tend("run").want(t, 0)
	if !strings.HasPrefix(res.stdout, "tend: updated #1 ") {
		t.Fatalf("run on stale state: %q\n%s", res.stdout, res.stderr)
	}
	if cs, _ := w.forge.Comments(t.Context(), 1); deliver.Foreign(cs) {
		t.Errorf("tend called its own PR foreign: %+v", cs)
	}
	if got, want := w.git(w.origin, "rev-parse", "tend/npm-fixture-pay-2.0.0^"), w.base; got != want {
		t.Errorf("the branch sits on %s, want %s", got, want)
	}

	other := filepath.Join(t.TempDir(), "other")
	w.git(w.dir, "clone", "--quiet", "--branch", "tend/npm-fixture-pay-2.0.0", w.origin, other)
	w.git(other, "-c", "user.name=Bea", "-c", "user.email=bea@example.com", "commit", "--quiet", "--allow-empty", "-m", "Fix by hand")
	w.git(other, "push", "--quiet", "origin", "HEAD")
	theirs := w.git(w.origin, "rev-parse", "tend/npm-fixture-pay-2.0.0")
	w.commit("Touch the readme a third time", map[string]string{"README.md": "three\n"})
	w.model = script(t, "node-sdk")
	res = w.tend("run").want(t, 0)
	if !strings.Contains(res.stdout, "commits tend did not push") {
		t.Errorf("run over a hand-made commit: %q", res.stdout)
	}
	if got := w.git(w.origin, "rev-parse", "tend/npm-fixture-pay-2.0.0"); got != theirs {
		t.Error("the hand-made commit was overwritten")
	}
}
