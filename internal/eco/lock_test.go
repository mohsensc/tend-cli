package eco

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

func TestReadLock(t *testing.T) {
	tests := []struct {
		file, src string
		name      string
		vers      []string
		reqs      []string
	}{
		{"package-lock.json", `{"lockfileVersion": 1, "dependencies": {"a": {"version": "1.0.0", "requires": {"b": "^1"}, "dependencies": {"b": {"version": "1.0.1"}}}}}`,
			"a", []string{"1.0.0"}, []string{"b"}},
		{"package-lock.json", `{"lockfileVersion": 3, "packages": {"": {"dependencies": {"a": "^1"}}, "node_modules/a": {"version": "1.0.0", "dependencies": {"b": "^1"}, "peerDependencies": {"c": "*"}}, "node_modules/a/node_modules/b": {"version": "1.0.1"}, "node_modules/x": {"version": "1.0.0", "link": true}}}`,
			"a", []string{"1.0.0"}, []string{"b", "c"}},
		{"pnpm-lock.yaml", `lockfileVersion: '9.0'
packages:
  '@s/a@2.0.0':
    resolution: {integrity: x}
  b@1.1.0:
    resolution: {integrity: y}
snapshots:
  '@s/a@2.0.0(react@18.0.0)':
    dependencies:
      b: 1.1.0
  '@s/a@2.0.0(react@19.0.0)':
    dependencies:
      b: 1.1.0
      c: 1.0.0
  b@1.1.0: {}
`, "@s/a", []string{"2.0.0"}, []string{"b", "c"}},
		{"pnpm-lock.yaml", `lockfileVersion: '6.0'
packages:
  /a@1.0.0:
    dependencies:
      b: 1.0.0
  /a@0.9.0:
    dev: false
`, "a", []string{"0.9.0", "1.0.0"}, []string{"b"}},
		{"pnpm-lock.yaml", `lockfileVersion: 5.4
packages:
  /a/1.0.0_react@18.0.0:
    dependencies:
      b: 1.0.0
`, "a", []string{"1.0.0"}, []string{"b"}},
		{"yarn.lock", `# yarn lockfile v1


"@s/a@^2.0.0", "@s/a@^2.1.0":
  version "2.1.0"
  resolved "https://registry.yarnpkg.com/@s/a/-/a-2.1.0.tgz"
  dependencies:
    b "^1.0.0"
    "@s/c" "^1"

b@^1.0.0:
  version "1.1.0"
`, "@s/a", []string{"2.1.0"}, []string{"b", "@s/c"}},
		{"yarn.lock", `__metadata:
  version: 6

"a@npm:^2.0.0, a@npm:^2.1.0":
  version: 2.1.0
  dependencies:
    b: "npm:^1.0.0"
  peerDependencies:
    c: "*"
  languageName: node
  linkType: hard
`, "a", []string{"2.1.0"}, []string{"b", "c"}},
		{"Cargo.lock", `[[package]]
name = "serde"
version = "1.0.0"
source = "registry+https://github.com/rust-lang/crates.io-index"
dependencies = ["serde_derive 1.0.0 (registry+https://x)", "itoa"]
`, "serde", []string{"1.0.0"}, []string{"serde_derive", "itoa"}},
		{"composer.lock", `{"packages": [{"name": "Monolog/Monolog", "version": "3.0.0", "require": {"php": ">=8.1", "ext-json": "*", "psr/log": "^3"}}], "packages-dev": []}`,
			"monolog/monolog", []string{"3.0.0"}, []string{"psr/log"}},
		{"Gemfile.lock", `GEM
  remote: https://rubygems.org/
  specs:
    rack (2.2.8)
    rails (7.0.0)
      actionpack (= 7.0.0)
      rack (>= 2)

PLATFORMS
  ruby

DEPENDENCIES
  rails (~> 7.0)
`, "rails", []string{"7.0.0"}, []string{"actionpack", "rack"}},
		{"packages.lock.json", `{"version": 1, "dependencies": {
  "net8.0": {"Newtonsoft.Json": {"type": "Direct", "resolved": "13.0.3", "dependencies": {"System.Memory": "4.5.0"}}},
  "net6.0": {"Newtonsoft.Json": {"type": "Direct", "resolved": "13.0.3", "dependencies": {"System.Buffers": "4.5.0"}}}}}`,
			"newtonsoft.json", []string{"13.0.3"}, []string{"system.buffers", "system.memory"}},
		{"poetry.lock", `[[package]]
name = "Requests"
version = "2.31.0"

[package.dependencies]
urllib3 = ">=1.21"
charset_normalizer = "*"
`, "requests", []string{"2.31.0"}, []string{"charset-normalizer", "urllib3"}},
		{"uv.lock", `version = 1

[[package]]
name = "httpx"
version = "0.27.0"
source = { registry = "https://pypi.org/simple" }
dependencies = [{ name = "anyio" }, { name = "h11" }]
`, "httpx", []string{"0.27.0"}, []string{"anyio", "h11"}},
	}
	for _, tt := range tests {
		t.Run(tt.file+" "+tt.name, func(t *testing.T) {
			ls, err := ReadLock("sub/"+tt.file, []byte(tt.src))
			if err != nil {
				t.Fatal(err)
			}
			var vers, reqs []string
			for _, l := range ls {
				if l.Name == tt.name {
					vers = append(vers, l.Version)
					reqs = union(reqs, l.Requires)
				}
			}
			slices.Sort(reqs)
			want := slices.Sorted(slices.Values(tt.reqs))
			if !slices.Equal(vers, tt.vers) || !slices.Equal(reqs, want) {
				t.Errorf("%s = %q requiring %q, want %q requiring %q", tt.name, vers, reqs, tt.vers, want)
			}
		})
	}
	if _, err := ReadLock("go.sum", nil); !errors.Is(err, core.ErrNotSupported) {
		t.Errorf("go.sum: err = %v", err)
	}
}

func TestCanon(t *testing.T) {
	tests := []struct{ eco, in, want string }{
		{core.Pip, "Charset_Normalizer", "charset-normalizer"},
		{core.Nuget, "Newtonsoft.Json", "newtonsoft.json"},
		{core.Composer, "Monolog/Monolog", "monolog/monolog"},
		{core.Npm, "@S/Pkg", "@S/Pkg"},
	}
	for _, tt := range tests {
		if got := Canon(tt.eco, tt.in); got != tt.want {
			t.Errorf("Canon(%s, %q) = %q, want %q", tt.eco, tt.in, got, tt.want)
		}
	}
}

func TestLockedToOtherLines(t *testing.T) {
	lock := func(vs ...string) string {
		var b strings.Builder
		b.WriteString(`{"lockfileVersion": 3, "packages": {"": {"name": "app"}`)
		for i, v := range vs {
			fmt.Fprintf(&b, `, "node_modules/%sansi-regex": {"version": %q}`, strings.Repeat("x/node_modules/", i), v)
		}
		b.WriteString("}}")
		return b.String()
	}
	tests := []struct {
		to     string
		after  []string
		landed string
		err    string
	}{
		{"5.0.1", []string{"4.1.1", "5.0.1"}, "", ""},
		{"4.1.1", []string{"4.1.1", "5.0.1"}, "", ""},
		{"5.0.1", []string{"3.0.0", "5.0.1"}, "", "locked ansi-regex at 3.0.0"},
		{"5.0.1", []string{"4.1.0", "5.0.2"}, "5.0.2", ""},
	}
	for _, tt := range tests {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "package-lock.json"), []byte(lock(tt.after...)), 0o644); err != nil {
			t.Fatal(err)
		}
		before := map[string]bool{"4.1.0": true, "5.0.0": true}
		got, err := lockedTo(root, "package-lock.json", core.Npm, "ansi-regex", tt.to, before)
		if tt.err == "" && (err != nil || got != tt.landed) {
			t.Errorf("lockedTo(%s, %v) = %q, %v; want %q", tt.to, tt.after, got, err, tt.landed)
		}
		if tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)) {
			t.Errorf("lockedTo(%s, %v) err = %v, want %q", tt.to, tt.after, err, tt.err)
		}
	}
}
