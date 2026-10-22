package vet

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

func TestVerdict(t *testing.T) {
	pass := &core.Run{Pass: true}
	fail := &core.Run{Pass: false}
	f := func(check, action string) core.Finding { return core.Finding{Check: check, Action: action} }
	tests := []struct {
		name     string
		findings []core.Finding
		proof    *core.Proof
		want     string
	}{
		{"clean", nil, &core.Proof{Base: pass, Patched: pass}, "pass"},
		{"no proof at all", nil, nil, "pass"},
		{"fixed and dropped do not count", []core.Finding{f("lockfile", "fixed"), f("scope", "dropped"), f("debug", "dropped")}, &core.Proof{Patched: pass}, "pass"},
		{"flags outside 1-4 do not count", []core.Finding{f("dead", "flagged"), f("question", "flagged"), f("debug", "flagged")}, &core.Proof{Patched: pass}, "pass"},
		{"symbol flag drafts", []core.Finding{f("symbols", "flagged")}, &core.Proof{Patched: pass}, "draft"},
		{"leftover flag drafts", []core.Finding{f("leftovers", "flagged")}, &core.Proof{Patched: fail}, "draft"},
		{"lockfile flag drafts", []core.Finding{f("lockfile", "flagged")}, &core.Proof{Patched: pass}, "draft"},
		{"failing baseline drafts", nil, &core.Proof{Base: fail, Patched: pass}, "draft"},
		{"symbol flag and failing proof rejects", []core.Finding{f("symbols", "flagged")}, &core.Proof{Base: pass, Patched: fail}, "reject"},
		{"field flag and failing proof rejects", []core.Finding{f("fields", "flagged")}, &core.Proof{Patched: fail}, "reject"},
		{"scope revert failure and failing proof rejects", []core.Finding{f("scope", "flagged")}, &core.Proof{Patched: fail}, "reject"},
		{"scope revert failure alone passes", []core.Finding{f("scope", "flagged")}, &core.Proof{Patched: pass}, "pass"},
		{"failing proof alone is not vetting's call", nil, &core.Proof{Patched: fail}, "pass"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Verdict(&core.Vetting{Findings: tt.findings}, tt.proof); got != tt.want {
				t.Errorf("Verdict = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSection(t *testing.T) {
	v := &core.Vetting{
		Checks: []string{
			"symbols: 1/1 changed calls resolve in a@2.0.0 typings", "fields: not applicable", "leftovers", "lockfile", "debug", "comments", "dead",
			"scope: 3 files, all explained by this change", "question",
		},
		Findings: []core.Finding{
			{Check: "leftovers", File: "src/billing.js", Line: 88, Msg: "still calls `pay.charges.retrieve`", Action: "flagged"},
			{Check: "lockfile", File: "package-lock.json", Msg: "relocked from base to drop unrelated churn: c", Action: "fixed"},
			{Check: "comments", File: "src/billing.js", Line: 41, Msg: "removed comment `// use v2`", Action: "dropped"},
			{Check: "comments", File: "src/billing.js", Line: 50, Msg: "removed comment `// done`", Action: "dropped"},
			{Check: "dead", File: "src/billing.js", Line: 120, Msg: "`legacyCharge` is now unreferenced", Action: "flagged"},
			{Check: "question", File: "src/billing.js", Line: 42, Msg: "the old call passed capture: false", Action: "flagged"},
		},
	}
	golden(t, "section.golden", Section(v))
	if Section(nil) != "" {
		t.Error("nil vetting renders something")
	}
}

func TestCheck(t *testing.T) {
	base := map[string]string{
		"package.json":      "{\"dependencies\": {\"a\": \"^1.0.0\"}}\n",
		"package-lock.json": npmBase,
		"README.md":         "# app\n",
		"src/billing.js":    "const a = require('a')\n\nfunction run() {\n  return a.charges.create({})\n}\nmodule.exports = { run }\n",
	}
	patched := map[string]string{
		"package.json":      "{\"dependencies\": {\"a\": \"^2.0.0\"}}\n",
		"package-lock.json": npmPatched,
		"README.md":         "# app on a@2\n",
		"src/scratch.js":    "console.log('scratch')\n",
		"src/billing.js":    "const a = require('a')\n\nfunction run() {\n  // moved to payment intents\n  console.log('migrating')\n  return a.paymentIntents.create({})\n}\nmodule.exports = { run }\n",
	}
	dir, sha, diff := worktree(t, base, patched)
	defer func(f func(context.Context, string, core.Manifest, []core.Dep, bool) error) { relock = f }(relock)
	relock = func(ctx context.Context, wt string, m core.Manifest, deps []core.Dep, berry bool) error {
		return os.WriteFile(filepath.Join(wt, "package-lock.json"), []byte(npmRelocked), 0o644)
	}
	dep := core.Dep{Ecosystem: core.Npm, Name: "a", Direct: true, Manifest: core.Manifest{Ecosystem: core.Npm, Path: "package.json", Lock: "package-lock.json", Tool: "npm"}}
	in := Input{
		Dir: dir, Base: sha,
		Change: &core.Change{Kind: core.Breaking, Deps: []core.Dep{dep}, To: "2.0.0", Sites: []core.Site{{File: "src/billing.js", Line: 4}}},
		Patch:  &core.Patch{Diff: diff, Model: true, Applied: []string{"bumped a 1.0.0 -> 2.0.0 (package.json, package-lock.json)"}, Edited: []string{"src/billing.js"}},
		Proof:  &core.Proof{Base: &core.Run{Pass: true}, Patched: &core.Run{Pass: true}},
		Index:  &index{dir: dir, imports: []core.Import{{File: "src/billing.js", Line: 1, Package: "a", Binding: "a"}}},
		Ecos:   ecos(core.Npm),
	}
	v, err := Check(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	wantChecks := []string{
		"scope: 3 files, all explained by this change", "symbols: 0/1 changed calls resolve", "fields: not applicable",
		"leftovers: not applicable", "lockfile", "debug", "comments", "dead", "question: not run: no model configured",
	}
	if !slices.Equal(v.Checks, wantChecks) {
		t.Errorf("Checks = %q", v.Checks)
	}
	if !v.Changed || v.Verdict != "draft" {
		t.Errorf("Changed = %v, Verdict = %q", v.Changed, v.Verdict)
	}
	golden(t, "check.golden", Section(v))

	wantBilling := "const a = require('a')\n\nfunction run() {\n  return a.paymentIntents.create({})\n}\nmodule.exports = { run }\n"
	if got := read(t, dir, "src/billing.js"); got != wantBilling {
		t.Errorf("billing.js =\n%s", got)
	}
	if got := read(t, dir, "README.md"); got != base["README.md"] {
		t.Errorf("README.md = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "src/scratch.js")); !os.IsNotExist(err) {
		t.Errorf("src/scratch.js still there: %v", err)
	}
	if got := read(t, dir, "package-lock.json"); got != npmRelocked {
		t.Errorf("lockfile not relocked")
	}
}

func TestShift(t *testing.T) {
	res := []core.Finding{
		{File: "a.js", Line: 2}, {File: "a.js", Line: 5}, {File: "a.js", Line: 9}, {File: "b.js", Line: 9}, {File: "a.js"},
	}
	shift(res, map[string][]int{"a.js": {2, 4, 7}})
	var got []int
	for _, f := range res {
		got = append(got, f.Line)
	}
	if want := []int{2, 3, 6, 9, 0}; !slices.Equal(got, want) {
		t.Errorf("lines = %v, want %v", got, want)
	}
}

func TestCheckNeedsChange(t *testing.T) {
	if _, err := Check(context.Background(), Input{}); err == nil {
		t.Error("Check without a change succeeded")
	}
}
