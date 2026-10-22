package vet

import (
	"context"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

// A patch that rewrites what the project's tests run, as a model rewriting
// a failing test script to one that passes would, is flagged and makes the
// PR a draft; one that leaves the lines alone says nothing about them.
func TestCommands(t *testing.T) {
	base := core.Commands{Install: []string{"npm ci"}, Test: []string{"npm test"}}
	dir, sha, diff := worktree(t,
		map[string]string{"package.json": `{"scripts": {"test": "node --test"}}` + "\n"},
		map[string]string{"package.json": `{"scripts": {"test": "echo ok"}}` + "\n"})
	in := Input{Dir: dir, Base: sha, Change: &core.Change{Kind: core.Breaking}, Patch: &core.Patch{Diff: diff, Files: []string{"package.json"}},
		Index: &index{dir: dir}, Commands: base}
	for _, tt := range []struct {
		name  string
		now   core.Commands
		flag  string
		check bool
	}{
		{"the same lines", base, "", false},
		{"a test line changed", core.Commands{Install: []string{"npm ci"}, Test: []string{"echo ok"}}, `the patch changes the test lines from ["npm test"] to ["echo ok"]; the proof ran the first`, true},
	} {
		in.Detect = func() core.Commands { return tt.now }
		vt, err := Check(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		listed := strings.Contains(strings.Join(vt.Checks, "\n"), "commands")
		var flags []string
		for _, f := range vt.Findings {
			if f.Check == "commands" {
				flags = append(flags, f.Msg)
			}
		}
		if listed != tt.check || strings.Join(flags, "") != tt.flag {
			t.Errorf("%s: checks %q, flags %q", tt.name, vt.Checks, flags)
		}
		if tt.check && vt.Verdict != "draft" {
			t.Errorf("%s: verdict %s", tt.name, vt.Verdict)
		}
	}
}
