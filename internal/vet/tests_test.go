package vet

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/gitx"
)

func TestTests(t *testing.T) {
	base := map[string]string{
		"package.json":                  "{\"a\": 1}\n",
		"src/billing.js":                "pay.charges.create()\n",
		"test/billing.test.js":          "test('total', () => {\n  assert.equal(total(), 42)\n})\n",
		"test/refund.test.js":           "test('refund', () => {\n  assert.equal(refund(), 1)\n})\ntest('void', () => {\n  assert.ok(voided())\n})\n",
		"test/unrelated.test.js":        "test('other', () => {\n  assert.equal(other(), 1)\n})\n",
		"test/skipped.test.js":          "test('slow', () => {\n  expect(slow()).toBe(2)\n})\n",
		"test/tend/characterization.js": "test('pins', () => {\n  assert.equal(total(), 42)\n})\n",
	}
	patched := map[string]string{
		"package.json":           "{\"a\": 2}\n",
		"src/billing.js":         "pay.paymentIntents.create()\n",
		"test/billing.test.js":   "test('total', () => {\n  assert.ok(true)\n})\n",
		"test/refund.test.js":    "test('refund', () => {\n  assert.equal(refund(), 1)\n  assert.ok(refunded())\n})\n",
		"test/unrelated.test.js": "test('other', () => {\n  assert.equal(other(), 2)\n})\n",
		"test/skipped.test.js":   "test.skip('slow', () => {\n  expect(slow()).toBe(3)\n})\n",
	}
	dir, sha, _ := worktree(t, base, patched)
	if err := os.Remove(filepath.Join(dir, "test", "tend", "characterization.js")); err != nil {
		t.Fatal(err)
	}
	sh(t, dir, "git", "add", "-A")
	diff, err := gitx.Raw(context.Background(), dir, "diff", "--no-color", "--no-renames", sha)
	if err != nil {
		t.Fatal(err)
	}
	ch := &core.Change{
		Deps:  []core.Dep{{Ecosystem: core.Npm, Name: "pay", Manifest: core.Manifest{Path: "package.json"}}},
		Sites: []core.Site{{File: "src/billing.js", Line: 1}},
	}
	edited := []string{"src/billing.js", "test/billing.test.js", "test/refund.test.js", "test/unrelated.test.js", "test/skipped.test.js", "test/tend/characterization.js"}
	idx := &index{dir: dir, reach: map[string][]string{}}
	for _, f := range edited[1:] {
		if f != "test/unrelated.test.js" {
			idx.reach[f] = []string{"src/billing.js"}
		}
	}
	patch := &core.Patch{Diff: diff, Model: true, Edited: edited}
	p := newPass(Input{Dir: dir, Base: sha, Change: ch, Patch: patch, Index: idx})
	got, skip := tests(context.Background(), p)
	if skip != "" {
		t.Fatalf("skipped: %s", skip)
	}
	eq(t, got,
		"flagged test/billing.test.js:0 a model removed 1 assertion from this test without adding as many back",
		"flagged test/refund.test.js:0 a model removed 1 test case from this test",
		"flagged test/skipped.test.js:0 a model skipped 1 test in this test",
		"flagged test/tend/characterization.js:0 a model deleted this test",
		"flagged test/unrelated.test.js:0 a model edited this test, which reaches none of the files this change explains",
	)

	got, _ = scope(context.Background(), p)
	eq(t, got)
	if p.note != "6 files, all explained by this change" {
		t.Errorf("scope note = %q", p.note)
	}

	p = newPass(Input{Dir: dir, Base: sha, Change: ch, Patch: &core.Patch{Diff: diff, Edited: edited}, Index: idx})
	if _, skip := tests(context.Background(), p); skip != na {
		t.Errorf("a patch no model wrote: skip = %q", skip)
	}
}
