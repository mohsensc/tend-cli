package eco

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
)

var customDeps = []config.Dep{
	{Name: "protoc", File: "scripts/*.sh", Match: `PROTOC_VERSION=(?P<v>v?[0-9.]+)`, Source: "github:protocolbuffers/protobuf"},
	{Name: "esbuild", File: "Makefile", Match: `esbuild@(?P<v>[0-9.]+)`, Source: "npm:esbuild"},
	{Name: "cosign", File: "Makefile", Match: `COSIGN=(?P<v>[0-9.]+)`, Source: "url:https://downloads.example.test/{name}/index.txt"},
}

func TestCustomDeps(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"scripts/install.sh": "#!/bin/sh\nset -e\nPROTOC_VERSION=v25.1\ncurl -L .../protoc-$PROTOC_VERSION.zip\n",
		"scripts/ci.sh":      "echo nothing pinned here\n",
		"Makefile":           "lint:\n\tnpx esbuild@0.19.2 --version\n\nCOSIGN=2.2.0\n",
	})
	e := Custom(customDeps)
	var got []string
	for _, m := range manifestsIn(t, e, root) {
		ds, err := e.Deps(root, m)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range ds {
			got = append(got, fmt.Sprintf("%s:%d %s %s", d.Manifest.Path, d.Line, d.Name, d.Req))
		}
	}
	want := []string{"Makefile:2 esbuild 0.19.2", "Makefile:4 cosign 2.2.0", "scripts/install.sh:3 protoc v25.1"}
	if !slices.Equal(got, want) {
		t.Errorf("deps = %q, want %q", got, want)
	}
	up, ok := Upstream(customDeps)(core.Dep{Ecosystem: core.Custom, Name: "esbuild", Version: "0.19.2"})
	if !ok || up.Ecosystem != core.Npm || up.Name != "esbuild" || up.Version != "0.19.2" {
		t.Errorf("Upstream = %+v, %v", up, ok)
	}
	if _, ok := Upstream(customDeps)(core.Dep{Ecosystem: core.Custom, Name: "protoc"}); ok {
		t.Error("a GitHub source has an advisory upstream")
	}
	if _, ok := Upstream(customDeps)(core.Dep{Ecosystem: core.Npm, Name: "esbuild"}); ok {
		t.Error("an npm dep was mapped")
	}
}

func TestCustomMeta(t *testing.T) {
	f := replayCanned(t,
		canned{url: "https://api.github.com/repos/protocolbuffers/protobuf/tags?per_page=100", body: `[{"name": "v25.1", "commit": {"sha": "a"}}, {"name": "v26.0-rc1", "commit": {"sha": "b"}}, {"name": "v25.3", "commit": {"sha": "c"}}]`},
		canned{url: "https://api.github.com/repos/protocolbuffers/protobuf/releases?per_page=100", body: `[{"tag_name": "v25.3", "published_at": "2026-02-01T00:00:00Z"}]`},
		canned{url: "https://api.github.com/repos/protocolbuffers/protobuf", body: `{"archived": false}`},
		canned{url: "https://downloads.example.test/cosign/index.txt", body: "cosign 2.2.0\ncosign v2.4.1 (latest)\ncosign 2.10.0-rc.1 beta\n"},
	)
	e := Custom(customDeps)
	m, err := e.Meta(context.Background(), f, "protoc")
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "protoc" || m.Latest != "25.3" || len(m.Releases) != 3 || m.Releases[1].Version != "25.3" || m.Releases[1].Time.IsZero() || !m.Releases[2].Pre {
		t.Errorf("github meta = %+v", m)
	}
	m, err = e.Meta(context.Background(), f, "cosign")
	if err != nil {
		t.Fatal(err)
	}
	var vs []string
	for _, r := range m.Releases {
		vs = append(vs, r.Version)
	}
	if !slices.Equal(vs, []string{"2.2.0", "2.4.1", "2.10.0"}) {
		t.Errorf("url releases = %q", vs)
	}
	if _, err := e.Meta(context.Background(), f, "nope"); err == nil {
		t.Error("Meta of a name tend.yml does not list")
	}
}

func TestCustomBump(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"scripts/install.sh": "#!/bin/sh\nPROTOC_VERSION=v25.1\n"})
	e := Custom(customDeps)
	ds := depsIn(t, e, root, "scripts/install.sh")
	if _, err := e.Bump(context.Background(), root, ds[0], "25.3", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "scripts", "install.sh"))
	if string(got) != "#!/bin/sh\nPROTOC_VERSION=v25.3\n" {
		t.Errorf("install.sh = %q", got)
	}
	if _, err := e.Bump(context.Background(), root, ds[0], "latest", ""); err == nil {
		t.Error("bumped to a version that is not one")
	}
}

// TestCustomBumpAtMatch rewrites the version where the regexp matched it,
// not the first copy of the same text on the line.
func TestCustomBumpAtMatch(t *testing.T) {
	deps := []config.Dep{
		{Name: "protoc", File: "*.sh", Match: `PROTOC_VERSION=(?P<v>v?[0-9.]+)`, Source: "github:protocolbuffers/protobuf"},
		{Name: "buf", File: "*.sh", Match: `BUF=(?P<v>[0-9.]+)`, Source: "github:bufbuild/buf"},
	}
	tests := []struct {
		name, src, dep, to, want string
	}{
		{"earlier copy", "echo v25.1; PROTOC_VERSION=v25.1\n", "protoc", "25.3", "echo v25.1; PROTOC_VERSION=v25.3\n"},
		{"other tool at the same version", "BUF=1.2.0 PROTOC_VERSION=1.2.0\n", "protoc", "1.3.0", "BUF=1.2.0 PROTOC_VERSION=1.3.0\n"},
		{"two pins on a line", "PROTOC_VERSION=v25.1 x PROTOC_VERSION=v25.1\n", "protoc", "26.0", "PROTOC_VERSION=v26.0 x PROTOC_VERSION=v26.0\n"},
		{"later line", "BUF=1.2.0\nX=1 BUF=1.2.0\n", "buf", "1.4.0", "BUF=1.2.0\nX=1 BUF=1.4.0\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, map[string]string{"get.sh": tt.src})
			e := Custom(deps)
			ds := depsIn(t, e, root, "get.sh")
			// A change carries every pin of the dep; bump the last line's only.
			last := 0
			for _, d := range ds {
				if d.Name == tt.dep {
					last = max(last, d.Line)
				}
			}
			for _, d := range ds {
				if d.Name != tt.dep || d.Line != last {
					continue
				}
				if _, err := e.Bump(context.Background(), root, d, tt.to, ""); err != nil {
					t.Fatal(err)
				}
			}
			if got := treeSnapshot(t, root)["get.sh"]; got != tt.want {
				t.Errorf("get.sh = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFor(t *testing.T) {
	cfg := config.Default()
	if n := len(For(cfg)); n != len(All()) {
		t.Errorf("For without deps = %d ecosystems", n)
	}
	cfg.Deps = customDeps[:1]
	if all := For(cfg); all[len(all)-1].Name() != core.Custom {
		t.Errorf("For with deps does not end in custom")
	}
	cfg.Ignore = []config.Ignore{{Name: "*", Ecosystem: core.Npm}, {Name: "*", Ecosystem: core.Custom}, {Name: "*", Ecosystem: core.Go, Versions: []string{">=2"}}}
	all := For(cfg)
	if len(all) != len(All())-1 || slices.ContainsFunc(all, func(e core.Ecosystem) bool { return e.Name() == core.Npm || e.Name() == core.Custom }) {
		t.Errorf("For with npm and custom ignored as a whole = %d ecosystems", len(all))
	}
}

func TestCheck(t *testing.T) {
	cfg := config.Default()
	cfg.Ignore = []config.Ignore{{Name: "x", Ecosystem: "npm"}, {Name: "s", Ecosystem: "api"}, {Name: "*", Ecosystem: "custom"}}
	cfg.Deps = []config.Dep{{Name: "a", Source: "github:o/r"}, {Name: "b", Source: "pypi:b"}, {Name: "c", Source: "npm:c"}, {Name: "d", Source: "url:https://x/{name}"}}
	cfg.Ecosystems = []string{"npm", "custom"}
	if err := Check(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Ignore = append(cfg.Ignore, config.Ignore{Name: "x", Ecosystem: "bower"})
	cfg.Deps = append(cfg.Deps, config.Dep{Name: "e", Source: "svn:x"})
	cfg.Ecosystems = append(cfg.Ecosystems, "yarn")
	err := Check(cfg)
	if err == nil || !strings.Contains(err.Error(), `ecosystems[2]: unknown ecosystem "yarn"`) || !strings.Contains(err.Error(), `ignore[3]: unknown ecosystem "bower"`) || !strings.Contains(err.Error(), `deps[4]: source wants`) {
		t.Errorf("err = %v", err)
	}
}
