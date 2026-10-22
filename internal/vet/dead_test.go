package vet

import (
	"context"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

const billing = `function legacyCharge(x) {
  return x
}

function helper() {
  return 1
}

function run() {
  helper()
  return legacyCharge(1)
}
module.exports = { run }
`

const billingAfter = `function legacyCharge(x) {
  return x
}

function helper() {
  return 1
}

function run() {
  helper()
  return 2
}
module.exports = { run }
`

func TestDead(t *testing.T) {
	tests := []struct {
		name  string
		other string
		want  []string
	}{
		{"last call removed", "// legacyCharge is old\nconst s = \"legacyCharge\"\n", []string{"flagged src/billing.js:1 `legacyCharge` is now unreferenced"}},
		{"still called elsewhere", "legacyCharge(2)\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, sha, diff := worktree(t,
				map[string]string{"src/billing.js": billing, "src/other.js": tt.other},
				map[string]string{"src/billing.js": billingAfter})
			got, skip := dead(context.Background(), newPass(Input{Dir: dir, Base: sha, Change: &core.Change{}, Patch: &core.Patch{Diff: diff}}))
			if skip != "" {
				t.Fatalf("skipped: %s", skip)
			}
			eq(t, got, tt.want...)
		})
	}
}

func TestDeadGoAndPython(t *testing.T) {
	dir, sha, diff := worktree(t,
		map[string]string{
			"lib.go":  "package lib\n\nfunc oldSign() {}\n\nfunc Sign() { oldSign() }\n\nfunc init() {}\n",
			"util.py": "def legacy():\n    pass\n\ndef main():\n    legacy()\n",
		},
		map[string]string{
			"lib.go":  "package lib\n\nfunc oldSign() {}\n\nfunc Sign() {}\n\nfunc init() {}\n",
			"util.py": "def legacy():\n    pass\n\ndef main():\n    pass\n",
		})
	got, _ := dead(context.Background(), newPass(Input{Dir: dir, Base: sha, Change: &core.Change{}, Patch: &core.Patch{Diff: diff}}))
	eq(t, got,
		"flagged lib.go:3 `oldSign` is now unreferenced",
		"flagged util.py:1 `legacy` is now unreferenced",
	)
}

func TestDeadNothingRemoved(t *testing.T) {
	dir, sha, diff := worktree(t, map[string]string{"a.js": "a()\n"}, map[string]string{"a.js": "a()\nb()\n"})
	if _, skip := dead(context.Background(), newPass(Input{Dir: dir, Base: sha, Change: &core.Change{}, Patch: &core.Patch{Diff: diff}})); skip != "nothing was removed" {
		t.Errorf("skip = %q", skip)
	}
}

func TestManifestOnly(t *testing.T) {
	dir, sha, diff := worktree(t, map[string]string{"go.mod": "module x\n\ngo 1.24.0\n", "Dockerfile": "FROM node:18\n"},
		map[string]string{"go.mod": "module x\n\ngo 1.26.0\n", "Dockerfile": "FROM node:22\n"})
	p := newPass(Input{Dir: dir, Base: sha, Change: &core.Change{}, Patch: &core.Patch{Diff: diff}})
	for name, fn := range map[string]func(context.Context, *pass) ([]core.Finding, string){"debug": debug, "dead": dead} {
		if _, skip := fn(context.Background(), p); skip != na {
			t.Errorf("%s: skip = %q, want %q", name, skip, na)
		}
	}
}
