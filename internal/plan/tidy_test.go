package plan

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
)

// A repo with one package nothing uses and one deprecated package the code
// calls, next to the deprecations a tidy-up leaves to PRs of their own.
func TestTidy(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{
		"package.json":      `{"dependencies": {"left-pad": "^1.0.0", "@fixture/pay": "^1.4.0"}}`,
		"package-lock.json": `{"lockfileVersion": 3, "packages": {"": {}, "node_modules/left-pad": {"version": "1.3.0"}, "node_modules/@fixture/pay": {"version": "1.4.0"}}}`,
		"src/billing.js":    "const pay = require('@fixture/pay')\npay.charges.create({})\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	inv, _, err := Inventory(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var pay, pad core.Dep
	for _, d := range inv.Deps {
		switch d.Name {
		case "@fixture/pay":
			pay = d
		case "left-pad":
			pad = d
		}
	}
	registry := []core.Evidence{{Source: "registry", Title: "@fixture/pay 1.4.0 is deprecated"}}
	move := core.Change{ID: "npm/@fixture/pay@2.0.0", Kind: core.Deprecation, Deps: []core.Dep{pay}, To: "2.0.0", Evidence: registry,
		Sites: []core.Site{{File: "src/billing.js", Line: 2, Chain: []string{"pay", "charges", "create"}}}}
	node := core.Dep{Ecosystem: core.Docker, Name: "library/node", Direct: true}
	p := &Plan{Queue: []core.Change{
		{ID: "npm/@fixture/pay@1.4.0", Kind: core.Deprecation, Deps: []core.Dep{pay}, To: "1.4.0", Evidence: []core.Evidence{{Source: "runtime"}}},
		move,
		{ID: "docker/library/node@22", Kind: core.Deprecation, Deps: []core.Dep{node}, To: "22", Evidence: []core.Evidence{{Source: "eol"}}},
		{ID: "npm/@fixture/pay@1.4.1", Kind: core.Security, Deps: []core.Dep{pay}, To: "1.4.1"},
		// deprecated and unused: the tidy-up removes it rather than moves off it
		{ID: "npm/left-pad@1.3.0", Kind: core.Deprecation, Deps: []core.Dep{pad}, To: "1.3.0", Evidence: []core.Evidence{{Source: "registry"}}},
	}}

	ch, err := Tidy(inv, p, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	c := ch.Cleanup
	if ch.ID != core.TidyID || ch.Kind != core.Tidy || len(c.Unused) != 1 || c.Unused[0].Name != "left-pad" {
		t.Fatalf("tidy = %+v", ch)
	}
	if len(c.Moves) != 1 || c.Moves[0].ID != move.ID {
		t.Errorf("moves = %+v, want only the registry deprecation", c.Moves)
	}
	if names := core.Names(ch); !slices.Equal(names, []string{"left-pad", "@fixture/pay"}) || len(ch.Sites) != 1 {
		t.Errorf("deps %q, sites %+v", names, ch.Sites)
	}

	cfg.Ignore = []config.Ignore{{Name: "left-pad", Versions: []string{">=2"}}}
	if ch, _ := Tidy(inv, p, cfg, false); len(ch.Cleanup.Unused) != 0 {
		t.Errorf("an ignore entry did not hold left-pad back: %+v", ch.Cleanup.Unused)
	}
	if ch, _ := Tidy(inv, &Plan{}, cfg, true); ch != nil {
		t.Errorf("a tidy-up with nothing to change and a tend.yml already there: %+v", ch)
	}
	if ch, _ := Tidy(inv, &Plan{}, cfg, false); ch == nil || len(ch.Deps) != 0 {
		t.Errorf("a repo with nothing to tidy still gets its welcome: %+v", ch)
	}
}

// A swap brings its new package into the change, where vetting looks for
// the new calls and the lockfile's new entries.
func TestRefit(t *testing.T) {
	req := core.Dep{Ecosystem: core.Npm, Name: "request", Version: "2.88.0", Line: 4, Direct: true}
	ch := &core.Change{Kind: core.Tidy, Cleanup: &core.Cleanup{
		Unused: []core.Dep{{Ecosystem: core.Npm, Name: "left-pad"}},
		Moves:  []core.Change{{Deps: []core.Dep{req}, ToName: "undici", To: "6.19.8", Sites: []core.Site{{File: "b.js", Line: 3}, {File: "a.js", Line: 9}}}},
	}}
	Refit(ch)
	if names := core.Names(ch); !slices.Equal(names, []string{"left-pad", "request", "undici"}) {
		t.Errorf("deps %q", names)
	}
	if u := ch.Deps[2]; u.Version != "6.19.8" || u.Line != 0 || !u.Direct {
		t.Errorf("undici = %+v", u)
	}
	if ch.Sites[0].File != "a.js" {
		t.Errorf("sites %+v are not sorted", ch.Sites)
	}
}
