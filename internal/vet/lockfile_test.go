package vet

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

const npmBase = `{"name": "app", "lockfileVersion": 3, "requires": true, "packages": {
  "": {"name": "app", "dependencies": {"a": "^1.0.0", "c": "^1.0.0"}},
  "node_modules/a": {"version": "1.0.0", "dependencies": {"b": "^1.0.0", "old": "^1.0.0"}},
  "node_modules/b": {"version": "1.0.0"},
  "node_modules/c": {"version": "1.0.0"},
  "node_modules/old": {"version": "1.0.0"}
}}`

// The bump of a pulls in d, moves b, nests a second b and drops old: all
// explained. c moving is not.
const npmPatched = `{"name": "app", "lockfileVersion": 3, "requires": true, "packages": {
  "": {"name": "app", "dependencies": {"a": "^2.0.0", "c": "^1.0.0"}},
  "node_modules/a": {"version": "2.0.0", "dependencies": {"b": "^1.1.0", "d": "^1.0.0"}},
  "node_modules/a/node_modules/b": {"version": "2.0.0"},
  "node_modules/b": {"version": "1.1.0"},
  "node_modules/c": {"version": "1.1.0"},
  "node_modules/d": {"version": "1.0.0"}
}}`

const npmRelocked = `{"name": "app", "lockfileVersion": 3, "requires": true, "packages": {
  "": {"name": "app", "dependencies": {"a": "^2.0.0", "c": "^1.0.0"}},
  "node_modules/a": {"version": "2.0.0", "dependencies": {"b": "^1.1.0", "d": "^1.0.0"}},
  "node_modules/b": {"version": "1.1.0"},
  "node_modules/c": {"version": "1.0.0"},
  "node_modules/d": {"version": "1.0.0"}
}}`

func TestLockfileNpm(t *testing.T) {
	dep := core.Dep{Ecosystem: core.Npm, Name: "a", Direct: true, Manifest: core.Manifest{Ecosystem: core.Npm, Path: "package.json", Lock: "package-lock.json", Tool: "npm"}}
	tests := []struct {
		name   string
		relock func(dir string) error // writes what the package manager would
		want   string
		lock   string
		dirty  bool
	}{
		{
			"relock removes the churn",
			func(dir string) error {
				return os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte(npmRelocked), 0o644)
			},
			"fixed package-lock.json:0 relocked from base to drop unrelated churn: c",
			npmRelocked, true,
		},
		{
			"package manager missing",
			func(string) error { return core.ToolMissing{Tool: "npm"} },
			"flagged package-lock.json:0 unrelated lockfile churn: c (relock failed: npm not found in PATH)",
			npmPatched, false,
		},
		{
			"relock keeps the churn",
			func(dir string) error {
				return os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte(npmPatched), 0o644)
			},
			"flagged package-lock.json:0 unrelated lockfile churn: c (relocking did not remove it)",
			npmPatched, false,
		},
		{
			"relock undoes the bump",
			func(string) error { return nil },
			"flagged package-lock.json:0 unrelated lockfile churn: c (relocking lost the bump)",
			npmPatched, false,
		},
	}
	defer func(f func(context.Context, string, core.Manifest, []core.Dep, bool) error) { relock = f }(relock)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, sha, diff := worktree(t,
				map[string]string{"package.json": "{}\n", "package-lock.json": npmBase},
				map[string]string{"package.json": "{\"a\": 2}\n", "package-lock.json": npmPatched})
			var calls []string
			relock = func(ctx context.Context, wt string, m core.Manifest, deps []core.Dep, berry bool) error {
				calls = append(calls, relockLine(m.Tool, deps, berry))
				return tt.relock(wt)
			}
			p := newPass(Input{Dir: dir, Base: sha, Change: &core.Change{Deps: []core.Dep{dep}, To: "2.0.0"}, Patch: &core.Patch{Diff: diff}})
			got, skip := lockfile(context.Background(), p)
			if skip != "" {
				t.Fatalf("skipped: %s", skip)
			}
			eq(t, got, tt.want)
			if lock := read(t, dir, "package-lock.json"); lock != tt.lock {
				t.Errorf("lockfile left as:\n%s", lock)
			}
			if p.dirty != tt.dirty {
				t.Errorf("dirty = %v", p.dirty)
			}
			if !slices.Equal(calls, []string{"npm install --package-lock-only --ignore-scripts --no-audit --no-fund"}) {
				t.Errorf("relock calls = %q", calls)
			}
		})
	}
}

func TestLockfileCargo(t *testing.T) {
	lock := func(serde, rand string) string {
		return `version = 3

[[package]]
name = "app"
version = "0.1.0"
dependencies = ["serde", "rand"]

[[package]]
name = "serde"
version = "` + serde + `"
source = "registry+https://github.com/rust-lang/crates.io-index"
dependencies = ["serde_derive ` + serde + ` (registry+https://github.com/rust-lang/crates.io-index)"]

[[package]]
name = "serde_derive"
version = "` + serde + `"
source = "registry+https://github.com/rust-lang/crates.io-index"

[[package]]
name = "rand"
version = "` + rand + `"
source = "registry+https://github.com/rust-lang/crates.io-index"
`
	}
	dep := core.Dep{Ecosystem: core.Cargo, Name: "serde", Manifest: core.Manifest{Path: "Cargo.toml", Lock: "Cargo.lock", Tool: "cargo"}}
	tests := []struct {
		name, rand string
		want       []string
	}{
		{"only serde and its derive", "0.8.5", nil},
		{"rand moved too", "0.8.6", []string{"flagged Cargo.lock:0 unrelated lockfile churn: rand"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, sha, diff := worktree(t, map[string]string{"Cargo.lock": lock("1.0.100", "0.8.5")}, map[string]string{"Cargo.lock": lock("1.0.200", tt.rand)})
			p := newPass(Input{Dir: dir, Base: sha, Change: &core.Change{Deps: []core.Dep{dep}, To: "1.0.200"}, Patch: &core.Patch{Diff: diff}})
			got, skip := lockfile(context.Background(), p)
			if skip != "" {
				t.Fatalf("skipped: %s", skip)
			}
			eq(t, got, tt.want...)
		})
	}
}

func TestLockfileSkips(t *testing.T) {
	dir, sha, diff := worktree(t, map[string]string{"go.mod": "module x\n"}, map[string]string{"go.mod": "module x\n\ngo 1.22\n"})
	dep := core.Dep{Ecosystem: core.Go, Name: "rsc.io/quote", Manifest: core.Manifest{Path: "go.mod"}}
	p := newPass(Input{Dir: dir, Base: sha, Change: &core.Change{Deps: []core.Dep{dep}}, Patch: &core.Patch{Diff: diff}})
	if _, skip := lockfile(context.Background(), p); skip != na {
		t.Errorf("no lockfile: skip = %q", skip)
	}

	dir, sha, diff = worktree(t, map[string]string{"x.lock": "one\n"}, map[string]string{"x.lock": "two\n"})
	dep = core.Dep{Ecosystem: core.Npm, Name: "x", Manifest: core.Manifest{Path: "package.json", Lock: "x.lock"}}
	p = newPass(Input{Dir: dir, Base: sha, Change: &core.Change{Deps: []core.Dep{dep}}, Patch: &core.Patch{Diff: diff}})
	if _, skip := lockfile(context.Background(), p); skip != "tend cannot read x.lock" {
		t.Errorf("unknown lockfile: skip = %q", skip)
	}
}

func TestRelockLine(t *testing.T) {
	direct := []core.Dep{{Name: "a", Direct: true}}
	deep := []core.Dep{{Name: "@s/b"}}
	tests := []struct {
		tool  string
		deps  []core.Dep
		berry bool
		want  string
	}{
		{"npm", direct, false, "npm install --package-lock-only --ignore-scripts --no-audit --no-fund"},
		{"npm", deep, false, "npm update '@s/b' --package-lock-only --ignore-scripts --no-audit --no-fund"},
		{"pnpm", direct, false, "pnpm install --lockfile-only --ignore-scripts"},
		{"pnpm", deep, false, "pnpm update '@s/b' --lockfile-only --ignore-scripts"},
		{"yarn", direct, true, "yarn install --mode update-lockfile"},
		{"yarn", deep, true, "yarn up -R '@s/b' --mode update-lockfile"},
		{"yarn", direct, false, "yarn install --ignore-scripts --non-interactive"},
		{"yarn", deep, false, "yarn upgrade '@s/b' --ignore-scripts --non-interactive"},
	}
	for _, tt := range tests {
		if got := relockLine(tt.tool, tt.deps, tt.berry); got != tt.want {
			t.Errorf("relockLine(%s, %v) = %q", tt.tool, tt.berry, got)
		}
	}
}

func TestParseLock(t *testing.T) {
	src := `{"lockfileVersion": 3, "packages": {"node_modules/a": {"version": "1.0.0", "dependencies": {"b": "^1"}}, "node_modules/b": {"version": "1.0.1"}}}`
	g, err := parseLock("sub/package-lock.json", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if !sameSet(g.vers["a"], []string{"1.0.0"}) || !sameSet(g.deps["a"], []string{"b"}) || !g.closure([]string{"a"})["b"] {
		t.Errorf("graph = %+v", g)
	}
	if _, err := parseLock("go.sum", nil); err != core.ErrNotSupported {
		t.Errorf("go.sum: err = %v", err)
	}
}
