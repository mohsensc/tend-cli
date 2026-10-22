package verify

import (
	"context"
	"reflect"
	"testing"

	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/eco"
)

// fakeEco answers Commands from a table keyed by manifest path.
type fakeEco struct {
	name string
	cmds map[string]core.Commands
}

func (f fakeEco) Name() string                                   { return f.name }
func (f fakeEco) Detect(string) ([]core.Manifest, error)         { return nil, nil }
func (f fakeEco) Deps(string, core.Manifest) ([]core.Dep, error) { return nil, nil }
func (f fakeEco) Meta(context.Context, core.Fetcher, string) (*core.Meta, error) {
	return nil, core.ErrNotSupported
}
func (f fakeEco) Bump(context.Context, string, core.Dep, string, string) (*core.Bump, error) {
	return nil, core.ErrNotSupported
}
func (f fakeEco) Commands(root string, m core.Manifest) core.Commands { return f.cmds[m.Path] }
func (f fakeEco) Traits() core.Traits                                 { return core.Traits{} }

func (f fakeEco) Imports(core.Dep) []string { return nil }

func TestDetect(t *testing.T) {
	npmCmds := core.Commands{Install: []string{"npm ci"}, Build: []string{"npm run build"}, Test: []string{"npm test"}}
	goCmds := core.Commands{Build: []string{"go build ./..."}, Test: []string{"go vet ./... && go test ./..."}}
	ecos := map[string]core.Ecosystem{
		core.Npm: fakeEco{core.Npm, map[string]core.Commands{
			"package.json":            npmCmds,
			"packages/a/package.json": {Install: []string{"npm ci"}, Test: []string{"cd packages/a && npm test"}},
			"web/package.json":        {Install: []string{"cd web && npm ci"}, Build: []string{"cd web && npm run build"}, Test: []string{"cd web && npm test"}},
			"site docs/package.json":  {Test: []string{"cd 'site docs' && npm test"}},
		}},
		core.Go: fakeEco{core.Go, map[string]core.Commands{
			"go.mod":           goCmds,
			"tools/gen/go.mod": {Build: []string{"cd tools/gen && go build ./..."}, Test: []string{"cd tools/gen && go vet ./... && go test ./..."}},
		}},
		core.Pip: fakeEco{core.Pip, map[string]core.Commands{
			"requirements.txt":     {Install: []string{"python -m venv .venv && .venv/bin/pip install -r requirements.txt"}, Test: []string{"python -m unittest"}},
			"requirements-dev.txt": {Install: []string{"python -m venv .venv && .venv/bin/pip install -r requirements-dev.txt"}, Test: []string{"python -m unittest"}},
		}},
		core.Docker:  eco.ByName(core.Docker),
		core.Actions: eco.ByName(core.Actions),
	}
	npmRoot := core.Manifest{Ecosystem: core.Npm, Path: "package.json", Lock: "package-lock.json"}
	goNested := core.Manifest{Ecosystem: core.Go, Path: "tools/gen/go.mod"}
	docker := core.Manifest{Ecosystem: core.Docker, Path: "Dockerfile"}
	actions := core.Manifest{Ecosystem: core.Actions, Path: ".github/workflows/ci.yml"}

	tests := []struct {
		name      string
		manifests []core.Manifest
		cfg       config.Verify
		want      core.Commands
	}{
		{
			"root npm and nested go, go listed first is still ordered after npm",
			[]core.Manifest{goNested, npmRoot},
			config.Verify{},
			core.Commands{
				Install: []string{"npm ci"},
				Build:   []string{"npm run build", "cd tools/gen && go build ./..."},
				Test:    []string{"npm test", "cd tools/gen && go vet ./... && go test ./..."},
			},
		},
		{
			"config replaces a category, the rest is detected",
			[]core.Manifest{npmRoot},
			config.Verify{Test: []string{"make check"}},
			core.Commands{Install: []string{"npm ci"}, Build: []string{"npm run build"}, Test: []string{"make check"}},
		},
		{
			"docker and actions contribute nothing",
			[]core.Manifest{docker, actions},
			config.Verify{},
			core.Commands{},
		},
		{
			"workspace member without its own lock is installed by the root",
			[]core.Manifest{npmRoot, {Ecosystem: core.Npm, Path: "packages/a/package.json", Lock: "package-lock.json"}},
			config.Verify{},
			core.Commands{
				Install: []string{"npm ci"},
				Build:   []string{"npm run build"},
				Test:    []string{"npm test", "cd packages/a && npm test"},
			},
		},
		{
			"nested project with its own lock installs itself",
			[]core.Manifest{npmRoot, {Ecosystem: core.Npm, Path: "web/package.json", Lock: "web/package-lock.json"}},
			config.Verify{},
			core.Commands{
				Install: []string{"npm ci", "cd web && npm ci"},
				Build:   []string{"npm run build", "cd web && npm run build"},
				Test:    []string{"npm test", "cd web && npm test"},
			},
		},
		{
			"two manifests in one dir, identical lines once",
			[]core.Manifest{{Ecosystem: core.Pip, Path: "requirements.txt"}, {Ecosystem: core.Pip, Path: "requirements-dev.txt"}},
			config.Verify{},
			core.Commands{
				Install: []string{
					"python -m venv .venv && .venv/bin/pip install -r requirements-dev.txt",
					"python -m venv .venv && .venv/bin/pip install -r requirements.txt",
				},
				Test: []string{"python -m unittest"},
			},
		},
		{
			"lines come from the ecosystem as written",
			[]core.Manifest{{Ecosystem: core.Npm, Path: "site docs/package.json"}},
			config.Verify{},
			core.Commands{Test: []string{"cd 'site docs' && npm test"}},
		},
		{
			"unknown ecosystem is ignored",
			[]core.Manifest{{Ecosystem: core.Cargo, Path: "Cargo.toml"}},
			config.Verify{},
			core.Commands{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Detect("/repo", tt.manifests, ecos, tt.cfg)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Detect =\n%#v\nwant\n%#v", got, tt.want)
			}
		})
	}
}
