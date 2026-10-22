package vet

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

func TestScope(t *testing.T) {
	base := map[string]string{
		"package.json":        "{\"a\": 1}\n",
		"src/billing.js":      "pay.charges.create()\n",
		"src/my model, v2.js": "x()\n",
		"README.md":           "# app\n",
	}
	patched := map[string]string{
		"package.json":        "{\"a\": 2}\n",
		"package-lock.json":   "{}\n",
		"src/billing.js":      "pay.paymentIntents.create()\n",
		"src/my model, v2.js": "y()\n",
		"README.md":           "# app, now on pay 2\n",
		"notes.txt":           "scratch\n",
	}
	dir, sha, diff := worktree(t, base, patched)
	ch := &core.Change{
		Deps:  []core.Dep{{Ecosystem: core.Npm, Name: "pay", Manifest: core.Manifest{Path: "package.json"}}},
		Sites: []core.Site{{File: "src/billing.js", Line: 1}},
	}
	patch := &core.Patch{Diff: diff, Model: true, Applied: []string{"bumped pay 1.0.0 -> 2.0.0 (package.json, package-lock.json)"}, Edited: []string{"src/my model, v2.js"}}
	p := newPass(Input{Dir: dir, Base: sha, Change: ch, Patch: patch})
	got, skip := scope(context.Background(), p)
	if skip != "" {
		t.Fatalf("skipped: %s", skip)
	}
	eq(t, got,
		"dropped README.md:0 not explained by this change; reverted",
		"dropped notes.txt:0 not explained by this change; reverted",
	)
	if got := read(t, dir, "README.md"); got != base["README.md"] {
		t.Errorf("README.md = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); !os.IsNotExist(err) {
		t.Errorf("notes.txt still there: %v", err)
	}
	if err := p.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := p.changed(); len(got) != 4 {
		t.Errorf("after revert the diff touches %q", got)
	}
}
