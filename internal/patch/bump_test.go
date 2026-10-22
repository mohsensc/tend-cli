package patch

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

const manifest = `{
  "dependencies": {
    "@fixture/pay": "^1.4.0",
    "@fixture/util": "^1.0.0"
  }
}
`

func TestBump(t *testing.T) {
	pay, util := npmDep("@fixture/pay", "1.4.0"), npmDep("@fixture/util", "1.0.0")
	tests := []struct {
		name    string
		deps    []core.Dep
		to      string
		toName  string
		err     error
		note    string
		calls   []string
		applied []string
		fail    string
	}{
		{
			name:    "one",
			deps:    []core.Dep{pay},
			to:      "2.0.0",
			calls:   []string{"@fixture/pay 2.0.0"},
			applied: []string{"bumped @fixture/pay 1.4.0 -> 2.0.0 (package.json)"},
		},
		{
			name:  "group bumps every dep",
			deps:  []core.Dep{pay, util},
			to:    "2.0.0",
			calls: []string{"@fixture/pay 2.0.0", "@fixture/util 2.0.0"},
			applied: []string{
				"bumped @fixture/pay 1.4.0 -> 2.0.0 (package.json)",
				"bumped @fixture/util 1.0.0 -> 2.0.0 (package.json)",
			},
		},
		{
			name:    "swap",
			deps:    []core.Dep{pay},
			to:      "3.1.0",
			toName:  "@fixture/payments",
			calls:   []string{"@fixture/pay 3.1.0 @fixture/payments"},
			applied: []string{"swapped @fixture/pay for @fixture/payments (package.json)"},
		},
		{
			name:  "unlocked note is said once",
			deps:  []core.Dep{pay, util},
			to:    "2.0.0",
			note:  "pnpm not found",
			calls: []string{"@fixture/pay 2.0.0", "@fixture/util 2.0.0"},
			applied: []string{
				"bumped @fixture/pay 1.4.0 -> 2.0.0 (package.json)",
				"lockfile not regenerated, pnpm not found",
				"bumped @fixture/util 1.0.0 -> 2.0.0 (package.json)",
			},
		},
		{
			name:  "missing tool is not fatal",
			deps:  []core.Dep{pay},
			to:    "2.0.0",
			err:   core.ToolMissing{Tool: "npm"},
			calls: []string{"@fixture/pay 2.0.0"},
			applied: []string{
				"bumped @fixture/pay 1.4.0 -> 2.0.0 (package.json)",
				"lockfile not regenerated, npm not found in PATH",
			},
		},
		{
			name:  "other errors are",
			deps:  []core.Dep{pay},
			to:    "2.0.0",
			err:   errors.New("registry down"),
			calls: []string{"@fixture/pay 2.0.0"},
			fail:  "patch: @fixture/pay: registry down",
		},
		{
			name:  "a refusal is passed on as the ecosystem said it",
			deps:  []core.Dep{pay},
			to:    "2.0.0",
			err:   fmt.Errorf("package.json: cannot move it: %w", core.ErrNotSupported),
			calls: []string{"@fixture/pay 2.0.0"},
			fail:  "package.json: cannot move it: not supported",
		},
		{
			name: "unknown ecosystem",
			deps: []core.Dep{{Ecosystem: "cobol", Name: "x"}},
			to:   "1.0.0",
			fail: "patch: x: no cobol ecosystem",
		},
		{
			name: "the version in use bumps nothing",
			deps: []core.Dep{pay},
			to:   pay.Version,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := repo(t, map[string]string{"package.json": manifest})
			eco := &fakeEco{name: core.Npm, err: tt.err, note: tt.note}
			o := Options{Ecos: map[string]core.Ecosystem{core.Npm: eco}}
			p := &core.Patch{}
			files, err := bump(context.Background(), dir, tt.deps, tt.to, tt.toName, o, p)
			if !slices.Equal(eco.calls, tt.calls) {
				t.Errorf("calls = %q, want %q", eco.calls, tt.calls)
			}
			if tt.fail != "" {
				if err == nil || err.Error() != tt.fail {
					t.Fatalf("err = %v, want %q", err, tt.fail)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(p.Applied, tt.applied) {
				t.Errorf("applied:\n%s\nwant:\n%s", strings.Join(p.Applied, "\n"), strings.Join(tt.applied, "\n"))
			}
			if want := []string{"package.json"}; len(tt.calls) > 0 && !slices.Equal(files, want) || len(tt.calls) == 0 && files != nil {
				t.Errorf("files = %q", files)
			}
		})
	}
}

// groupEco is fakeEco that can also bump several deps of a manifest at once.
type groupEco struct {
	*fakeEco
	groups [][]string
}

func (e *groupEco) BumpGroup(ctx context.Context, root string, deps []core.Dep, to string) (*core.Bump, error) {
	e.groups = append(e.groups, core.Names(&core.Change{Deps: deps}))
	return &core.Bump{Files: []string{deps[0].Manifest.Path}, Locked: true}, nil
}

func TestBumpTogether(t *testing.T) {
	dir := repo(t, map[string]string{"package.json": manifest, "web/package.json": manifest})
	pay, util := npmDep("@fixture/pay", "1.4.0"), npmDep("@fixture/util", "1.4.0")
	other := npmDep("@fixture/pay", "1.4.0")
	other.Manifest.Path = "web/package.json"
	e := &groupEco{fakeEco: &fakeEco{name: core.Npm}}
	p := &core.Patch{}
	files, err := bump(context.Background(), dir, []core.Dep{pay, util, other}, "2.0.0", "", Options{Ecos: map[string]core.Ecosystem{core.Npm: e}}, p)
	if err != nil {
		t.Fatal(err)
	}
	// the two of package.json in one pass; web/package.json has one, bumped alone
	if want := [][]string{{"@fixture/pay", "@fixture/util"}}; len(e.groups) != 1 || !slices.Equal(e.groups[0], want[0]) {
		t.Errorf("groups = %q, want %q", e.groups, want)
	}
	if !slices.Equal(e.calls, []string{"@fixture/pay 2.0.0"}) {
		t.Errorf("single bumps = %q", e.calls)
	}
	want := []string{"bumped @fixture/pay, @fixture/util 1.4.0 -> 2.0.0 together (package.json)"}
	if len(p.Applied) < 1 || p.Applied[0] != want[0] || !slices.Contains(files, "package.json") {
		t.Errorf("applied = %q, files = %q", p.Applied, files)
	}
}

func TestBumpWrapsError(t *testing.T) {
	dir := repo(t, map[string]string{"package.json": manifest})
	eco := &fakeEco{name: core.Npm, err: core.ErrNotSupported}
	_, err := bump(context.Background(), dir, []core.Dep{npmDep("@fixture/pay", "1.4.0")}, "2.0.0", "",
		Options{Ecos: map[string]core.Ecosystem{core.Npm: eco}}, &core.Patch{})
	if !errors.Is(err, core.ErrNotSupported) {
		t.Fatalf("err = %v, want it to wrap ErrNotSupported", err)
	}
}

func TestBumpLanded(t *testing.T) {
	pay := npmDep("@fixture/pay", "1.4.0")
	ms := core.Dep{Ecosystem: core.Npm, Name: "ms", Version: "2.0.0", Manifest: pay.Manifest}
	tests := []struct {
		name   string
		dep    core.Dep
		locked []core.Dep
		fail   string
	}{
		{"direct at the target", pay, []core.Dep{{Name: "@fixture/pay", Version: "2.0.0", Direct: true}}, ""},
		{"direct past the target", pay, []core.Dep{{Name: "@fixture/pay", Version: "2.1.0", Direct: true}}, "patch: @fixture/pay resolved to 2.1.0, not 2.0.0"},
		{"direct, with an old copy nested elsewhere", pay, []core.Dep{{Name: "@fixture/pay", Version: "2.0.0", Direct: true}, {Name: "@fixture/pay", Version: "1.4.0"}}, ""},
		{"transitive moved", ms, []core.Dep{{Name: "ms", Version: "2.1.3"}}, ""},
		{"transitive held by its parent", ms, []core.Dep{{Name: "ms", Version: "2.0.0"}, {Name: "ms", Version: "2.1.3"}}, "patch: ms is still at 2.0.0 after the bump to 2.1.3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := repo(t, map[string]string{"package.json": manifest})
			eco := &fakeEco{name: core.Npm, locked: tt.locked}
			to := "2.0.0"
			if !tt.dep.Direct {
				to = "2.1.3"
			}
			_, err := bump(context.Background(), dir, []core.Dep{tt.dep}, to, "", Options{Ecos: map[string]core.Ecosystem{core.Npm: eco}}, &core.Patch{})
			if got := fmt.Sprint(err); tt.fail == "" && err != nil || tt.fail != "" && got != tt.fail {
				t.Errorf("err = %v, want %q", err, tt.fail)
			}
		})
	}
}
