package plan

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
)

// A repo with one package nothing uses and a queue of every kind: the first
// PR carries the fixes and the removal, and plans the rest stage by stage:
// the base image and the runtime minimum next, then the major, then the
// migration past Spread call sites and the choice between replacements.
func TestFirst(t *testing.T) {
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
	site := func(file string, line int) core.Site { return core.Site{File: file, Line: line, Chain: []string{"x"}} }
	var many []core.Site
	for i := range Spread + 1 {
		many = append(many, site("src/report.js", i+1))
	}
	lodash := core.Dep{Ecosystem: core.Npm, Name: "lodash", Version: "4.17.20", Direct: true, Manifest: core.Manifest{Path: "package.json"}}
	node := core.Dep{Ecosystem: core.Docker, Name: "library/node", Direct: true, Manifest: core.Manifest{Path: "Dockerfile"}}
	engines := core.Dep{Ecosystem: core.Toolchain, Name: "nodejs", Direct: true, Min: true, Manifest: core.Manifest{Path: "package.json"}}
	vitest := core.Dep{Ecosystem: core.Npm, Name: "vitest", Version: "1.6.0", Direct: true, Manifest: core.Manifest{Path: "package.json"}}
	moment := core.Dep{Ecosystem: core.Npm, Name: "moment", Version: "2.30.1", Direct: true, Manifest: core.Manifest{Path: "package.json"}}
	request := core.Dep{Ecosystem: core.Npm, Name: "request", Version: "2.88.2", Direct: true, Manifest: core.Manifest{Path: "package.json"}}
	p := &Plan{Queue: []core.Change{
		{ID: "npm/lodash@4.17.21", Kind: core.Security, Deps: []core.Dep{lodash}, To: "4.17.21", Sites: []core.Site{site("src/a.js", 1)}},
		{ID: "npm/@fixture/pay@2.0.0", Kind: core.Deprecation, Deps: []core.Dep{pay}, To: "2.0.0", Sites: []core.Site{site("src/billing.js", 2)}},
		{ID: "docker/library/node@22", Kind: core.Deprecation, Deps: []core.Dep{node}, To: "22", Sites: []core.Site{{File: "Dockerfile", Line: 1}}},
		{ID: "npm/@fixture/pay@1.4.0", Kind: core.Deprecation, Deps: []core.Dep{pay}, To: "1.4.0"},
		{ID: "toolchain/nodejs@20", Kind: core.Deprecation, Deps: []core.Dep{engines}, To: "20"},
		{ID: "npm/moment@", Kind: core.Deprecation, Deps: []core.Dep{moment}, Options: []core.Option{{Title: "Migrate to dayjs"}}},
		{ID: "npm/request->undici@6.19.8", Kind: core.Deprecation, Deps: []core.Dep{request}, ToName: "undici", To: "6.19.8", Sites: many},
		{ID: "npm/vitest@3.2.4", Kind: core.Breaking, Level: "major", Deps: []core.Dep{vitest}, To: "3.2.4", Sites: []core.Site{site("test/a.test.js", 1)}},
		{ID: "npm/left-pad@1.3.0", Kind: core.Deprecation, Deps: []core.Dep{pad}, To: "1.3.0", Evidence: []core.Evidence{{Source: "registry"}}},
	}}

	ch, err := First(inv, p, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	b := ch.Batch
	if ch.ID != core.FirstID || ch.Kind != core.First || len(b.Unused) != 1 || b.Unused[0].Name != "left-pad" {
		t.Fatalf("first = %+v", ch)
	}
	var made []string
	for _, c := range b.Changes {
		made = append(made, c.ID)
	}
	if want := []string{"npm/lodash@4.17.21", "npm/@fixture/pay@2.0.0"}; !slices.Equal(made, want) || b.Stage != Fixes {
		t.Errorf("stage %d made %q, want %q", b.Stage, made, want)
	}
	stage := map[string]int{}
	why := map[string]string{}
	for _, l := range b.Next {
		stage[l.Change.ID], why[l.Change.ID] = l.Stage, l.Why
	}
	for id, want := range map[string]int{
		"docker/library/node@22":     Runtimes,
		"npm/@fixture/pay@1.4.0":     Runtimes,
		"toolchain/nodejs@20":        Runtimes,
		"npm/vitest@3.2.4":           Majors,
		"npm/moment@":                Migrations,
		"npm/request->undici@6.19.8": Migrations,
	} {
		if stage[id] != want {
			t.Errorf("%s waits for stage %d, want %d", id, stage[id], want)
		}
	}
	if w := why["npm/@fixture/pay@1.4.0"]; w != "another change here moves the same package, so this one comes after it" || why["npm/vitest@3.2.4"] != "" {
		t.Errorf("why = %q", why)
	}
	if len(b.Next) != 6 || After(b) != Runtimes {
		t.Errorf("next = %+v, after %d", b.Next, After(b))
	}
	if names := core.Names(ch); !slices.Equal(names, []string{"left-pad", "lodash", "@fixture/pay"}) || len(ch.Sites) != 2 {
		t.Errorf("deps %q, sites %+v", names, ch.Sites)
	}

	cfg.Ignore = []config.Ignore{{Name: "left-pad", Versions: []string{">=2"}}}
	if ch, _ := First(inv, p, cfg, false); len(ch.Batch.Unused) != 0 {
		t.Errorf("an ignore entry did not hold left-pad back: %+v", ch.Batch.Unused)
	}
	if ch, _ := First(inv, &Plan{Queue: p.Queue[4:8]}, cfg, true); ch == nil || ch.Batch.Stage != Runtimes || len(ch.Batch.Changes) != 1 || len(ch.Batch.Next) != 3 {
		t.Errorf("with nothing to fix, the first PR is the next stage: %+v", ch)
	}
	if ch, _ := First(inv, &Plan{}, cfg, true); ch != nil {
		t.Errorf("a first PR with nothing to make and a tend.yml already there: %+v", ch)
	}
	if ch, _ := First(inv, &Plan{}, cfg, false); ch == nil || len(ch.Deps) != 0 {
		t.Errorf("a repo with nothing to catch up on still gets its welcome: %+v", ch)
	}
}

// A swap brings its new package into the change, where vetting looks for
// the new calls and the lockfile's new entries.
func TestRefit(t *testing.T) {
	req := core.Dep{Ecosystem: core.Npm, Name: "request", Version: "2.88.0", Line: 4, Direct: true}
	ch := &core.Change{Kind: core.First, Batch: &core.Batch{
		Unused:  []core.Dep{{Ecosystem: core.Npm, Name: "left-pad"}},
		Changes: []core.Change{{Deps: []core.Dep{req}, ToName: "undici", To: "6.19.8", Sites: []core.Site{{File: "b.js", Line: 3}, {File: "a.js", Line: 9}}}},
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

// Each stage takes what is left of the ones before it, and none is left
// once nothing past the last stage that landed waits.
func TestNext(t *testing.T) {
	dep := func(name string) core.Dep {
		return core.Dep{Ecosystem: core.Npm, Name: name, Version: "1.0.0", Direct: true, Manifest: core.Manifest{Path: "package.json"}}
	}
	fix := core.Change{ID: "npm/a@1.0.1", Kind: core.Security, Deps: []core.Dep{dep("a")}, To: "1.0.1"}
	major := core.Change{ID: "npm/b@3.0.0", Kind: core.Breaking, Level: "major", Deps: []core.Dep{dep("b")}, To: "3.0.0", Sites: []core.Site{{File: "src/b.js", Line: 1}}}
	choice := core.Change{ID: "npm/c@", Kind: core.Deprecation, Deps: []core.Dep{dep("c")}, Options: []core.Option{{Title: "Switch to d", ToName: "d", To: "2.0.0"}}}
	p := &Plan{Queue: []core.Change{fix, major, choice}}

	ch := Next(p, Fixes)
	if ch == nil || ch.ID != core.StageID(Majors) || ch.Batch.Stage != Majors {
		t.Fatalf("after the fixes: %+v", ch)
	}
	var made []string
	for _, c := range ch.Batch.Changes {
		made = append(made, c.ID)
	}
	if !slices.Equal(made, []string{"npm/a@1.0.1", "npm/b@3.0.0"}) || len(ch.Batch.Next) != 1 || ch.Batch.Next[0].Stage != Migrations {
		t.Errorf("stage 3 makes %q and leaves %+v", made, ch.Batch.Next)
	}
	if ch := Next(p, Majors); ch == nil || ch.Batch.Stage != Migrations || len(ch.Batch.Changes) != 3 || After(ch.Batch) != 0 {
		t.Errorf("the last stage: %+v", ch)
	}
	if ch := Next(&Plan{Queue: []core.Change{fix}}, Majors); ch != nil {
		t.Errorf("only a fix left after stage 3: %+v", ch)
	}
}

// The rules put each change in one stage, the first that matches winning.
func TestTier(t *testing.T) {
	site := func(file string) core.Site { return core.Site{File: file, Line: 1} }
	lib := core.Dep{Ecosystem: core.Npm, Name: "lib", Version: "1.0.0", Direct: true}
	tool := core.Dep{Ecosystem: core.Npm, Name: "webpack", Version: "4.0.0", Direct: true, Dev: true}
	runner := core.Dep{Ecosystem: core.Npm, Name: "vitest", Version: "1.6.0", Direct: true, Dev: true}
	var many, tests []core.Site
	for i := range Spread + 1 {
		many = append(many, core.Site{File: "src/a.js", Line: i + 1})
		tests = append(tests, core.Site{File: fmt.Sprintf("test/%d.test.js", i), Line: 1})
	}
	for _, tt := range []struct {
		name string
		ch   core.Change
		want int
	}{
		{"security", core.Change{Kind: core.Security, Deps: []core.Dep{lib}, To: "1.0.1"}, Fixes},
		{"security past Spread", core.Change{Kind: core.Security, Level: "major", Deps: []core.Dep{lib}, To: "2.0.0", Sites: many}, Migrations},
		{"security patch of a wide package", core.Change{Kind: core.Security, Level: "patch", Deps: []core.Dep{lib}, To: "1.0.1", Sites: many}, Fixes},
		{"minor of a wide package", core.Change{Kind: core.Update, Level: "minor", Deps: []core.Dep{lib}, To: "1.1.0", Sites: many}, Majors},
		{"test runner minor in many tests", core.Change{Kind: core.Update, Level: "minor", Deps: []core.Dep{runner}, To: "1.7.0", Sites: tests}, Runtimes},
		{"test runner major in many tests", core.Change{Kind: core.Breaking, Level: "major", Deps: []core.Dep{runner}, To: "3.0.0", Sites: tests}, Migrations},
		{"unsafe option past Spread", core.Change{Kind: core.Security, Level: "patch", Deps: []core.Dep{lib}, To: "1.0.1", Sites: many, Options: []core.Option{{Title: "Rewrite the calls"}}}, Migrations},
		{"api", core.Change{Kind: core.Breaking, API: &core.APIChange{Provider: "x"}}, Fixes},
		{"deprecated version", core.Change{Kind: core.Deprecation, Deps: []core.Dep{lib}, To: "2.0.0"}, Fixes},
		{"no single replacement", core.Change{Kind: core.Deprecation, Deps: []core.Dep{lib}}, Migrations},
		{"runtime pin", core.Change{Kind: core.Deprecation, Deps: []core.Dep{{Ecosystem: core.Toolchain, Name: "nodejs"}}, To: "24"}, Runtimes},
		{"base image", core.Change{Kind: core.Deprecation, Deps: []core.Dep{{Ecosystem: core.Docker, Name: "library/node"}}, To: "24"}, Runtimes},
		{"build tool", core.Change{Kind: core.Breaking, Level: "major", Deps: []core.Dep{tool}, To: "5.0.0", Sites: []core.Site{site("test/build.test.js")}}, Runtimes},
		{"dev dep the code calls", core.Change{Kind: core.Breaking, Level: "major", Deps: []core.Dep{tool}, To: "5.0.0", Sites: []core.Site{site("src/build.js")}}, Majors},
		{"major", core.Change{Kind: core.Breaking, Level: "major", Deps: []core.Dep{lib}, To: "3.0.0", Sites: []core.Site{site("src/a.js")}}, Majors},
	} {
		if got := Tier(&tt.ch); got != tt.want {
			t.Errorf("%s: stage %d, want %d", tt.name, got, tt.want)
		}
	}
}

// tend tries a dependency the project has first, then the catalog's order,
// and never staying put or a replacement with no release to move to.
func TestRanked(t *testing.T) {
	ch := &core.Change{Deps: []core.Dep{{Name: "request", Version: "2.88.2"}}, Options: []core.Option{
		{Title: "Switch to undici", ToName: "undici", To: "7.16.0"},
		{Title: "Switch to gone", ToName: "gone"},
		{Title: "Switch to axios", ToName: "axios", To: "1.12.2"},
		{Title: "Pin to 2.88.2 and ignore", To: "2.88.2"},
	}}
	var got []string
	for _, x := range Ranked(ch, nil) {
		got = append(got, fmt.Sprintf("%d %s", x.N, x.Why))
	}
	if want := []string{"1 the catalog's recommended replacement", "3 the next replacement in the catalog's order"}; !slices.Equal(got, want) {
		t.Errorf("ranked %q, want %q", got, want)
	}
	got = nil
	for _, x := range Ranked(ch, []core.Dep{{Name: "axios", Direct: true}}) {
		got = append(got, fmt.Sprintf("%d %s", x.N, x.Why))
	}
	if want := []string{"3 the project already depends on axios", "1 the catalog's recommended replacement"}; !slices.Equal(got, want) {
		t.Errorf("with axios in use, ranked %q, want %q", got, want)
	}
	rename := &core.Change{Deps: []core.Dep{{Name: "pay", Version: "1.4.0"}}, To: "2.0.0", Options: []core.Option{
		{Title: "Apply the rename", To: "2.0.0", Codemod: &core.Codemod{Renames: map[string]string{"a": "b"}}},
		{Title: "Pin to 1.4.0 and ignore", To: "1.4.0"},
	}}
	if r := Ranked(rename, nil); len(r) != 1 || r[0].N != 1 {
		t.Errorf("rename ranked %+v", r)
	}
}

// A major two or more behind can climb through the newest release of each
// major in between; a runtime never does, and one major behind has no step.
func TestSteps(t *testing.T) {
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	old := now.AddDate(-1, 0, 0)
	m := &core.Meta{Releases: []core.Release{
		{Version: "1.0.0", Time: old}, {Version: "2.0.0", Time: old}, {Version: "2.4.1", Time: old}, {Version: "3.0.0", Time: old},
		{Version: "3.1.0", Time: old}, {Version: "4.0.0-rc.1", Time: old, Pre: true}, {Version: "4.0.0", Time: old},
	}}
	web := core.Dep{Ecosystem: core.Npm, Name: "web", Version: "1.0.0", Direct: true}
	b := &builder{in: Input{Cfg: config.Default(), Now: now}, meta: map[pkg]*core.Meta{{core.Npm, "web"}: m}}
	if got := b.steps(&core.Change{Deps: []core.Dep{web}, To: "4.0.0"}); !slices.Equal(got, []string{"2.4.1", "3.1.0"}) {
		t.Errorf("steps = %q", got)
	}
	if got := b.steps(&core.Change{Deps: []core.Dep{web}, To: "2.4.1"}); got != nil {
		t.Errorf("one major: %q", got)
	}
	node := core.Dep{Ecosystem: core.Toolchain, Name: "nodejs", Version: "16.20.2"}
	b.meta[pkg{core.Toolchain, "nodejs"}] = &core.Meta{Releases: []core.Release{{Version: "18.0.0"}, {Version: "20.0.0"}, {Version: "24.0.0"}}}
	if got := b.steps(&core.Change{Deps: []core.Dep{node}, To: "24.0.0"}); got != nil {
		t.Errorf("a runtime climbs: %q", got)
	}
}
