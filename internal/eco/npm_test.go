package eco

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/fetch"
)

func TestNpmDetect(t *testing.T) {
	root := copyTestdata(t, "npm/mono")
	got := manifestsIn(t, npm{}, root)
	want := []core.Manifest{
		{Ecosystem: "npm", Path: "legacy/package.json", Tool: "yarn"},
		{Ecosystem: "npm", Path: "package.json", Lock: "package-lock.json", Tool: "npm"},
		{Ecosystem: "npm", Path: "packages/a/package.json", Lock: "package-lock.json", Tool: "npm"},
		{Ecosystem: "npm", Path: "packages/b/package.json", Lock: "package-lock.json", Tool: "npm"},
		{Ecosystem: "npm", Path: "tools/site/package.json", Lock: "tools/site/pnpm-lock.yaml", Tool: "pnpm"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Detect = %+v\nwant %+v", got, want)
	}
}

func TestNpmShrinkwrap(t *testing.T) {
	root := t.TempDir()
	lock := `{"lockfileVersion": 3, "packages": {"": {}, "node_modules/left-pad": {"version": "1.3.0"}}}`
	write(t, root, map[string]string{
		"package.json":        `{"dependencies": {"left-pad": "^1.0.0"}}`,
		"npm-shrinkwrap.json": lock,
		"package-lock.json":   lock,
	})
	ms := manifestsIn(t, npm{}, root)
	if len(ms) != 1 || ms[0].Lock != "npm-shrinkwrap.json" {
		t.Fatalf("Detect = %+v", ms)
	}
	if ds := depsIn(t, npm{}, root, "package.json"); len(ds) != 1 || ds[0].Version != "1.3.0" {
		t.Errorf("Deps = %+v", ds)
	}
}

func TestNpmDeps(t *testing.T) {
	for _, tt := range []struct{ name, fixture, manifest string }{
		{"lock-v1", "npm/lock-v1", "package.json"},
		{"lock-v3", "npm/lock-v3", "package.json"},
		{"yarn-v1", "npm/yarn-v1", "package.json"},
		{"yarn-berry", "npm/yarn-berry", "package.json"},
		{"pnpm-v9", "npm/pnpm-v9", "package.json"},
		{"mono-root", "npm/mono", "package.json"},
		{"mono-a", "npm/mono", "packages/a/package.json"},
		{"mono-b", "npm/mono", "packages/b/package.json"},
		{"mono-site", "npm/mono", "tools/site/package.json"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := copyTestdata(t, tt.fixture)
			goldenCheck(t, "npm/deps/"+tt.name+".golden.json", jsonOf(t, depsIn(t, npm{}, root, tt.manifest)))
		})
	}
}

func TestPnpmKey(t *testing.T) {
	tests := []struct {
		key           string
		major         int
		name, version string
		ok            bool
	}{
		{"/left-pad/1.3.0", 5, "left-pad", "1.3.0", true},
		{"/@babel/core/7.24.0", 5, "@babel/core", "7.24.0", true},
		{"/react-dom/18.2.0_react@18.2.0", 5, "react-dom", "18.2.0", true},
		{"/left-pad@1.3.0", 6, "left-pad", "1.3.0", true},
		{"/@babel/core@7.24.0(supports-color@9.0.0)", 6, "@babel/core", "7.24.0", true},
		{"left-pad@1.3.0", 9, "left-pad", "1.3.0", true},
		{"@isaacs/string-locale-compare@1.1.0", 9, "@isaacs/string-locale-compare", "1.1.0", true},
		{"left-pad@link:../left-pad", 9, "", "", false},
		{"@scope/only", 9, "", "", false},
	}
	for _, tt := range tests {
		name, version, ok := pnpmKey(tt.key, tt.major)
		if ok != tt.ok || ok && (name != tt.name || version != tt.version) {
			t.Errorf("pnpmKey(%q, %d) = %q, %q, %v", tt.key, tt.major, name, version, ok)
		}
	}
}

func TestPnpmLockV5(t *testing.T) {
	src := []byte(`lockfileVersion: 5.4

specifiers:
  react: ^18.2.0
  react-dom: ^18.2.0
  local: link:../local

dependencies:
  local: link:../local
  react: 18.2.0
  react-dom: 18.2.0_react@18.2.0

packages:

  /loose-envify/1.4.0:
    resolution: {integrity: sha512-x}
    dev: false

  /react-dom/18.2.0_react@18.2.0:
    resolution: {integrity: sha512-x}
    dev: false

  /react/18.2.0:
    resolution: {integrity: sha512-x}
    dev: false
`)
	lk, err := pnpmLock(src, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := lk.resolve("react-dom", "^18.2.0"); got != "18.2.0" {
		t.Errorf("react-dom = %q", got)
	}
	if !lk.links["local"] || lk.links["react"] {
		t.Errorf("links = %v", lk.links)
	}
	if got := npmSortLocked(lk.all); len(got) != 3 || got[0].name != "loose-envify" {
		t.Errorf("all = %+v", got)
	}
}

// Lockfile 9 has no dev flag on packages: what only devDependencies reach,
// through any depth, aliases and peer suffixes, is dev; what both reach is not.
func TestPnpmLockV9Dev(t *testing.T) {
	src := []byte(`lockfileVersion: '9.0'

importers:

  .:
    dependencies:
      debug:
        specifier: ^4.3.4
        version: 4.4.3
    devDependencies:
      jest:
        specifier: ^29.0.0
        version: 29.7.0(react@18.2.0)

  packages/web:
    devDependencies:
      sw:
        specifier: npm:string-width@^4.2.3
        version: string-width@4.2.3

packages:

  debug@4.4.3:
    resolution: {integrity: sha512-x}

  jest@29.7.0:
    resolution: {integrity: sha512-x}

  ms@2.1.3:
    resolution: {integrity: sha512-x}

  pretty-format@29.7.0:
    resolution: {integrity: sha512-x}

  react-is@18.2.0:
    resolution: {integrity: sha512-x}

  string-width@4.2.3:
    resolution: {integrity: sha512-x}

  strip-ansi@6.0.1:
    resolution: {integrity: sha512-x}

  orphan@1.0.0:
    resolution: {integrity: sha512-x}

snapshots:

  debug@4.4.3:
    dependencies:
      ms: 2.1.3

  jest@29.7.0(react@18.2.0):
    dependencies:
      ms: 2.1.3
      pretty-format: 29.7.0

  pretty-format@29.7.0:
    dependencies:
      react-is: 18.2.0

  react-is@18.2.0: {}

  ms@2.1.3: {}

  string-width@4.2.3:
    dependencies:
      strip-ansi: 6.0.1

  strip-ansi@6.0.1: {}

  orphan@1.0.0: {}
`)
	lk, err := pnpmLock(src, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range lk.all {
		got[p.name] = p.dev
	}
	want := map[string]bool{
		"debug": false, "ms": false, "orphan": false,
		"jest": true, "pretty-format": true, "react-is": true, "string-width": true, "strip-ansi": true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dev = %v\nwant %v", got, want)
	}
}

func TestYarnAlias(t *testing.T) {
	lk := yarnLock([]byte("\"pad@npm:left-pad@^1.3.0\":\n  version: 1.3.0\n"))
	if got := lk.resolve("pad", "npm:left-pad@^1.3.0"); got != "1.3.0" {
		t.Errorf("resolve = %q", got)
	}
	if len(lk.all) != 1 || lk.all[0].name != "left-pad" {
		t.Errorf("all = %+v", lk.all)
	}
}

func TestNpmrc(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TEND_TEST_REGISTRY", "https://npm.corp.example")
	root := t.TempDir()
	home, _ := os.UserHomeDir()
	write(t, home, map[string]string{".npmrc": "registry=https://home.example/\n@home:registry=https://home-scope.example\n"})
	write(t, root, map[string]string{
		".npmrc":           "; comment\nregistry = \"https://project.example/\"\n@corp:registry=${TEND_TEST_REGISTRY}/\n//project.example/:_authToken=secret\n",
		"web/.npmrc":       "@web:registry=https://web.example\n",
		"web/package.json": "{}",
	})
	tests := []struct{ dir, name, want string }{
		{".", "left-pad", "https://project.example"},
		{".", "@corp/ui", "https://npm.corp.example"},
		{".", "@home/x", "https://home-scope.example"},
		{".", "@other/x", "https://project.example"},
		{"web", "@web/x", "https://web.example"},
		{"web", "left-pad", "https://project.example"},
	}
	for _, tt := range tests {
		if got := npmrc(root, tt.dir).registry(tt.name); got != tt.want {
			t.Errorf("registry(%s, %s) = %q, want %q", tt.dir, tt.name, got, tt.want)
		}
	}
	if got := npmrc("", "").registry("left-pad"); got != "https://home.example" {
		t.Errorf("home registry = %q", got)
	}
}

// npmMeta runs Meta against the recordings with no user .npmrc in the way.
func npmMeta(t *testing.T, name string) (*core.Meta, error) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	npmRegistries.Clear()
	return npm{}.Meta(context.Background(), fetch.Replay(recordedDir("npm")), name)
}

func TestNpmMeta(t *testing.T) {
	for _, name := range []string{"is-number", "left-pad", "@isaacs/string-locale-compare"} {
		t.Run(name, func(t *testing.T) {
			meta, err := npmMeta(t, name)
			if err != nil {
				t.Fatal(err)
			}
			goldenCheck(t, "npm/meta/"+strings.ReplaceAll(name, "/", "_")+".golden.json", jsonOf(t, meta))
		})
	}
	t.Run("not found", func(t *testing.T) {
		if _, err := npmMeta(t, "@tend-fixture/does-not-exist"); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("err = %v, want not found", err)
		}
	})
}

func TestNpmMetaRegistryFromDeps(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	npmRegistries.Clear()
	root := t.TempDir()
	write(t, root, map[string]string{
		".npmrc":       "@corp:registry=http://127.0.0.1:4873/\n",
		"package.json": `{"dependencies": {"@corp/ui": "^1.0.0"}}`,
	})
	depsIn(t, npm{}, root, "package.json")
	f := replayCanned(t, canned{url: "http://127.0.0.1:4873/@corp%2Fui", body: `{"dist-tags":{"latest":"1.0.0"},"versions":{"1.0.0":{}},"repository":"corp/ui"}`})
	meta, err := npm{}.Meta(context.Background(), f, "@corp/ui")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Latest != "1.0.0" || len(meta.Releases) != 1 || meta.Repo != "https://github.com/corp/ui" {
		t.Errorf("meta = %+v", meta)
	}
}

// Two manifests whose .npmrc files send one scope to different registries
// each keep theirs: Meta tries both rather than the one read last.
func TestNpmMetaRegistryPerDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	npmRegistries.Clear()
	root := t.TempDir()
	write(t, root, map[string]string{
		"a/.npmrc":       "@corp:registry=http://127.0.0.1:4873/\n",
		"a/package.json": `{"dependencies": {"@corp/ui": "^1.0.0"}}`,
		"b/.npmrc":       "@corp:registry=http://127.0.0.1:4874/\n",
		"b/package.json": `{"dependencies": {"@corp/db": "^1.0.0", "left-pad": "^1.0.0"}}`,
	})
	depsIn(t, npm{}, root, "a/package.json")
	depsIn(t, npm{}, root, "b/package.json")
	doc := `{"dist-tags":{"latest":"1.0.0"},"versions":{"1.0.0":{}}}`
	f := replayCanned(t,
		canned{url: "http://127.0.0.1:4873/@corp%2Fui", body: doc},
		canned{url: "http://127.0.0.1:4874/@corp%2Fui", status: 404, body: `{}`},
		canned{url: "http://127.0.0.1:4873/@corp%2Fdb", status: 404, body: `{}`},
		canned{url: "http://127.0.0.1:4874/@corp%2Fdb", body: doc},
		canned{url: "http://127.0.0.1:4873/@corp%2Fnone", status: 404, body: `{}`},
		canned{url: "http://127.0.0.1:4874/@corp%2Fnone", status: 404, body: `{}`})
	for _, name := range []string{"@corp/ui", "@corp/db"} {
		if meta, err := (npm{}).Meta(context.Background(), f, name); err != nil || meta.Latest != "1.0.0" {
			t.Errorf("%s: meta = %+v, %v", name, meta, err)
		}
	}
	_, err := npm{}.Meta(context.Background(), f, "@corp/none")
	if err == nil || !strings.Contains(err.Error(), "not found at http://127.0.0.1:4873, http://127.0.0.1:4874") {
		t.Errorf("err = %v", err)
	}
	if got := npmRegistriesFor("left-pad"); !reflect.DeepEqual(got, []string{npmDefaultRegistry}) {
		t.Errorf("unscoped registries = %v", got)
	}
}

func TestNpmMetaRecord(t *testing.T) {
	wantRecord(t)
	t.Setenv("HOME", t.TempDir())
	npmRegistries.Clear()
	f := liveClient(t, "npm")
	for _, name := range []string{"is-number", "left-pad", "@isaacs/string-locale-compare", "@tend-fixture/does-not-exist"} {
		if _, err := (npm{}).Meta(context.Background(), f, name); err != nil {
			t.Log(err)
		}
	}
}

func TestNpmBump(t *testing.T) {
	bumpGolden(t, npm{}, "npm/lock-v3", []bumpCase{
		{"caret-major", "package.json", "is-number", true, "8.0.0", ""},
		{"exact", "package.json", "left-pad", true, "1.3.1", ""},
		{"swap-sorted", "package.json", "is-number", true, "2.1.0", "@sindresorhus/is"},
	})
	bumpGolden(t, npm{}, "npm/lock-v1", []bumpCase{
		{"caret-v1", "package.json", "debug", true, "4.4.3", ""},
		{"tilde", "package.json", "send", true, "1.2.0", ""},
	})
	bumpGolden(t, npm{}, "npm/mono", []bumpCase{
		{"member", "packages/a/package.json", "is-number", true, "7.0.0", ""},
		{"no-lock", "legacy/package.json", "left-pad", true, "1.3.0", ""},
	})
}

func TestNpmBumpRefused(t *testing.T) {
	hideTools(t)
	ctx := context.Background()
	root := copyTestdata(t, "npm/lock-v3")
	m := core.Manifest{Ecosystem: "npm", Path: "package.json", Lock: "package-lock.json", Tool: "npm"}
	tests := []struct {
		name string
		dep  core.Dep
		to   string
		want error
	}{
		{"file dependency", core.Dep{Name: "local-lib", Manifest: m, Direct: true}, "1.0.0", core.ErrNotSupported},
		{"inside range needs the tool", depIn(t, npm{}, root, "package.json", "debug", true), "4.9.0", core.ToolMissing{Tool: "npm"}},
		{"transitive needs the tool", depIn(t, npm{}, root, "package.json", "ms", false), "2.1.4", core.ToolMissing{Tool: "npm"}},
		{"a new range needs the tool", depIn(t, npm{}, root, "package.json", "is-number", true), "8.0.0", core.ToolMissing{Tool: "npm"}},
	}
	ms := depIn(t, npm{}, root, "package.json", "ms", false)
	if _, err := (npm{}).Bump(ctx, root, ms, "1.0.0", "tiny-ms"); !errors.Is(err, core.ErrNotSupported) {
		t.Errorf("swap of a transitive dep: err = %v", err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := treeSnapshot(t, root)
			_, err := npm{}.Bump(ctx, root, tt.dep, tt.to, "")
			if !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
			if !reflect.DeepEqual(before, treeSnapshot(t, root)) {
				t.Error("a refused bump changed files")
			}
		})
	}
	berry := copyTestdata(t, "npm/yarn-berry")
	shared := core.Dep{Name: "shared", Manifest: manifestsIn(t, npm{}, berry)[0], Direct: true}
	if _, err := (npm{}).Bump(ctx, berry, shared, "1.0.0", ""); !errors.Is(err, core.ErrNotSupported) {
		t.Errorf("workspace: bump err = %v", err)
	}
}

func TestNpmBumpCRLF(t *testing.T) {
	hideTools(t)
	root := t.TempDir()
	src := "{\r\n  \"name\": \"x\",\r\n  \"dependencies\": {\r\n    \"@scope/pkg\": \"^1.2.0\",\r\n    \"left-pad\": \"^1.2.0\"\r\n  }\r\n}\r\n"
	write(t, root, map[string]string{"package.json": src})
	d := depIn(t, npm{}, root, "package.json", "left-pad", true)
	if d.Line != 5 {
		t.Fatalf("line = %d", d.Line)
	}
	res, err := npm{}.Bump(context.Background(), root, d, "2.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "package.json"))
	if want := strings.Replace(src, `"left-pad": "^1.2.0"`, `"left-pad": "^2.0.0"`, 1); string(got) != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if res.Locked || res.Note != "no lockfile" || !reflect.DeepEqual(res.Files, []string{"package.json"}) {
		t.Errorf("bump = %+v", res)
	}
}

func TestNpmSwapUnsorted(t *testing.T) {
	src := []byte(`{"dependencies": {"b": "^1.0.0", "a": "^1.0.0"}}`)
	blocks, err := npmParse(src)
	if err != nil {
		t.Fatal(err)
	}
	out, err := npmSwap(src, &blocks[0], &blocks[0].entries[0], "c", "2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"dependencies": {"c": "^2.0.0", "a": "^1.0.0"}}`; string(out) != want {
		t.Errorf("got %s", out)
	}
	if _, err := npmSwap(src, &blocks[0], &blocks[0].entries[0], "a", "2.0.0"); err == nil {
		t.Error("swapped onto a name the block already has")
	}
}

func TestNpmBumpCommands(t *testing.T) {
	ctx := context.Background()
	// A transitive bump is checked against the lock, so its tool moves ms there.
	tests := []struct {
		fixture, tool, dep string
		direct             bool
		to                 string
		want               string
	}{
		{"npm/lock-v3", "npm", "is-number", true, "8.0.0", ". install --package-lock-only --ignore-scripts --no-audit --no-fund"},
		{"npm/lock-v3", "npm", "debug", true, "4.9.0", ". install --package-lock-only --ignore-scripts --no-audit --no-fund"},
		{"npm/lock-v3", "npm", "ms", false, "2.1.4", ". update ms --package-lock-only --ignore-scripts --no-audit --no-fund"},
		{"npm/pnpm-v9", "pnpm", "is-number", true, "8.0.0", ". install --lockfile-only --ignore-scripts"},
		{"npm/pnpm-v9", "pnpm", "ms", false, "2.1.4", ". update ms --lockfile-only"},
		{"npm/yarn-berry", "yarn", "@isaacs/string-locale-compare", true, "2.0.0", ". install --mode=update-lockfile"},
		{"npm/yarn-berry", "yarn", "ms", false, "2.1.4", ". up -R ms --mode=update-lockfile"},
		{"npm/yarn-v1", "yarn", "debug", true, "5.0.0", ". install --ignore-scripts --non-interactive"},
	}
	for _, tt := range tests {
		t.Run(tt.fixture+"/"+tt.dep, func(t *testing.T) {
			logged := lockTool(t, tt.tool, "2.1.3", "2.1.4")
			root := copyTestdata(t, tt.fixture)
			d := depIn(t, npm{}, root, "package.json", tt.dep, tt.direct)
			if _, err := (npm{}).Bump(ctx, root, d, tt.to, ""); err != nil {
				t.Fatal(err)
			}
			want := []string{tt.want}
			if tt.direct {
				want = append(want, tt.want)
			}
			if got := logged(root); !reflect.DeepEqual(got, want) {
				t.Errorf("ran %q, want %q", got, want)
			}
		})
	}
	t.Run("yarn v1 transitive", func(t *testing.T) {
		logged := toolScript(t, "yarn", `grep -q '^ms@' yarn.lock || printf '\nms@^2.1.3:\n  version "2.1.4"\n' >> yarn.lock`+"\n")
		root := copyTestdata(t, "npm/yarn-v1")
		d := depIn(t, npm{}, root, "package.json", "ms", false)
		res, err := (npm{}).Bump(ctx, root, d, "2.1.4", "")
		if err != nil || !res.Locked {
			t.Fatalf("bump = %+v, %v", res, err)
		}
		if got, want := logged(root), []string{". install --ignore-scripts --non-interactive"}; !reflect.DeepEqual(got, want) {
			t.Errorf("ran %q, want %q", got, want)
		}
		lock, _ := os.ReadFile(filepath.Join(root, "yarn.lock"))
		if strings.Contains(string(lock), `version "2.1.3"`) || !strings.Contains(string(lock), "debug@^4.3.4:") {
			t.Errorf("yarn.lock:\n%s", lock)
		}
	})
}

func TestYarnLockEdits(t *testing.T) {
	root := t.TempDir()
	src := "# yarn lockfile v1\n\n\n\"@s/a@^1.0.0\":\n  version \"1.0.0\"\n\nms@2.1.2, ms@^2.1.3:\n  version \"2.1.3\"\n\nms@^1.0.0:\n  version \"1.0.0\"\n\npad@npm:left-pad@^1.3.0:\n  version \"1.3.0\"\n"
	write(t, root, map[string]string{"yarn.lock": src})
	if err := yarnDrop(root, "yarn.lock", "ms"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "yarn.lock"))
	if want := "# yarn lockfile v1\n\n\n\"@s/a@^1.0.0\":\n  version \"1.0.0\"\n\npad@npm:left-pad@^1.3.0:\n  version \"1.3.0\"\n"; string(got) != want {
		t.Errorf("after drop:\n%s", got)
	}
	if err := yarnDrop(root, "yarn.lock", "left-pad"); err != nil {
		t.Errorf("an alias of left-pad is not its entry: %v", err)
	}
	if err := yarnDrop(root, "yarn.lock", "nope"); err == nil {
		t.Error("dropping what is not there succeeded")
	}
	write(t, root, map[string]string{"yarn.lock": src})
	if ok, err := yarnAlias(root, "yarn.lock", "@s/a@^1.0.0", "@s/a@1.0.0"); !ok || err != nil {
		t.Fatalf("alias = %v, %v", ok, err)
	}
	if ok, err := yarnAlias(root, "yarn.lock", "ms@2.1.2", "ms@^2.0.0"); !ok || err != nil {
		t.Fatalf("alias = %v, %v", ok, err)
	}
	got, _ = os.ReadFile(filepath.Join(root, "yarn.lock"))
	for _, want := range []string{"\"@s/a@^1.0.0\", \"@s/a@1.0.0\":\n", "ms@2.1.2, ms@^2.1.3, ms@^2.0.0:\n"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("yarn.lock lacks %q:\n%s", want, got)
		}
	}
}

// lockTool is fakeTool that also rewrites from to to in whichever lockfile
// sits in its working directory, the way a lock update moves a package.
func lockTool(t *testing.T, name, from, to string) func(root string) []string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	script := "#!/bin/sh\necho \"$PWD $*\" >> " + log + "\n" +
		"for f in package-lock.json pnpm-lock.yaml yarn.lock poetry.lock uv.lock; do\n" +
		"  [ -f \"$f\" ] && sed 's/" + from + "/" + to + "/g' \"$f\" > \"$f.tmp\" && mv \"$f.tmp\" \"$f\"\n" +
		"done\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func(root string) []string {
		b, _ := os.ReadFile(log)
		real, _ := filepath.EvalSymlinks(root)
		var res []string
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if l != "" {
				res = append(res, strings.NewReplacer(real, ".", root, ".").Replace(l))
			}
		}
		return res
	}
}

// A targeted update may lock the chosen release or a later one of its
// major, and says which; anything else fails the bump and leaves the files
// as they were.
func TestNpmTargetedLockChecked(t *testing.T) {
	tests := []struct {
		fixture, tool, to, locks string
		landed, err              string
	}{
		{"npm/lock-v3", "npm", "2.1.4", "2.1.4", "", ""},
		{"npm/lock-v3", "npm", "2.1.4", "2.1.9", "2.1.9", ""},
		{"npm/lock-v3", "npm", "2.1.4", "3.0.0", "", "locked ms at 3.0.0, not 2.1.4 or a later 2.x"},
		{"npm/lock-v3", "npm", "2.1.4", "2.1.3", "", "did not lock ms at 2.1.4 or a later 2.x"},
		{"npm/pnpm-v9", "pnpm", "2.1.4", "2.1.9", "2.1.9", ""},
		{"npm/pnpm-v9", "pnpm", "2.1.4", "3.0.0", "", "locked ms at 3.0.0, not 2.1.4 or a later 2.x"},
		{"npm/yarn-berry", "yarn", "2.1.4", "2.1.9", "2.1.9", ""},
	}
	for _, tt := range tests {
		t.Run(tt.fixture+"/"+tt.locks, func(t *testing.T) {
			lockTool(t, tt.tool, "2.1.3", tt.locks)
			root := copyTestdata(t, tt.fixture)
			d := depIn(t, npm{}, root, "package.json", "ms", false)
			before := treeSnapshot(t, root)
			res, err := (npm{}).Bump(context.Background(), root, d, tt.to, "")
			if tt.err == "" {
				if err != nil || !res.Locked || res.Landed != tt.landed {
					t.Fatalf("bump = %+v, %v; want landed %q", res, err, tt.landed)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Fatalf("err = %v, want %q", err, tt.err)
			}
			if !reflect.DeepEqual(before, treeSnapshot(t, root)) {
				t.Error("a refused bump changed files")
			}
		})
	}
}

func TestNpmNewReq(t *testing.T) {
	tests := []struct {
		req, to, want string
		err           bool
	}{
		{"^1.2.0", "2.0.0", "^2.0.0", false},
		{"^1.2.0", "1.5.0", "^1.2.0", false},
		{"~1.2.0", "1.3.1", "~1.3.1", false},
		{"~1.2.0", "1.2.9", "~1.2.0", false},
		{">=1.2.0", "3.0.0", ">=1.2.0", false},
		{">2.0.0", "2.0.0", ">=2.0.0", false},
		{"1.2.3", "2.0.0", "2.0.0", false},
		{"=1.2.3", "2.0.0", "=2.0.0", false},
		{"v1.2.3", "2.0.0", "v2.0.0", false},
		{"1.x", "2.3.1", "2.x", false},
		{"1.2.x", "2.3.1", "2.3.x", false},
		{"1.*", "3.0.0", "3.*", false},
		{"1", "2.3.1", "2", false},
		{"^1.2", "2.3.1", "^2.3", false},
		{"~1", "2.0.0", "~2", false},
		{">=1.0.0 <2.0.0", "2.1.0", ">=2.1.0 <3.0.0", false},
		{">= 1.0.0 < 2.0.0", "2.1.0", ">=2.1.0 <3.0.0", false},
		{"^1.0.0 || ^2.0.0", "3.0.0", "^1.0.0 || ^2.0.0 || ^3.0.0", false},
		{"1.0.0 - 1.5.0", "2.0.0", "1.0.0 - 2.0.0", false},
		{"^1.2.0", "2.0.0-rc.1", "^2.0.0-rc.1", false},
		{"1.x", "2.0.0-rc.1", "2.0.0-rc.1", false},
		{"npm:other@^1.0.0", "2.0.0", "npm:other@^2.0.0", false},
		{"npm:@scope/other@~1.0.0", "1.1.0", "npm:@scope/other@~1.1.0", false},
		{"latest", "2.0.0", "latest", false},
		{"*", "9.9.9", "*", false},
		{"", "9.9.9", "", false},
		{"<2.0.0", "3.0.0", "", true},
		{"workspace:*", "2.0.0", "", true},
		{"file:../lib", "2.0.0", "", true},
		{"^1.0.0", "two", "", true},
	}
	for _, tt := range tests {
		got, err := npmNewReq(tt.req, tt.to)
		if (err != nil) != tt.err || got != tt.want {
			t.Errorf("npmNewReq(%q, %q) = %q, %v; want %q", tt.req, tt.to, got, err, tt.want)
		}
	}
}

func TestNpmSatisfies(t *testing.T) {
	tests := []struct {
		rng, v string
		want   bool
	}{
		{"^1.2.3", "1.2.3", true},
		{"^1.2.3", "1.9.0", true},
		{"^1.2.3", "2.0.0", false},
		{"^1.2.3", "1.2.2", false},
		{"^0.2.3", "0.2.9", true},
		{"^0.2.3", "0.3.0", false},
		{"^0.0.3", "0.0.3", true},
		{"^0.0.3", "0.0.4", false},
		{"^0.0", "0.0.9", true},
		{"^0.0", "0.1.0", false},
		{"^0", "0.9.0", true},
		{"^0", "1.0.0", false},
		{"^1.x", "1.5.0", true},
		{"~1.2.3", "1.2.9", true},
		{"~1.2.3", "1.3.0", false},
		{"~1.2", "1.2.0", true},
		{"~1", "1.9.9", true},
		{"~1", "2.0.0", false},
		{"~>1.2.3", "1.2.4", true},
		{"1.2.3", "1.2.3", true},
		{"1.2.3", "1.2.4", false},
		{"=1.2.3", "1.2.3", true},
		{"v1.2.3", "1.2.3", true},
		{"1.2.3", "v1.2.3", true},
		{"1.x", "1.0.0", true},
		{"1.x", "2.0.0", false},
		{"1.2.X", "1.2.7", true},
		{"1.2.*", "1.3.0", false},
		{"1", "1.4.0", true},
		{"1.2", "1.2.5", true},
		{"1.2", "1.3.0", false},
		{"*", "3.4.5", true},
		{"", "3.4.5", true},
		{"x", "0.0.1", true},
		{">=1.2.3", "1.2.3", true},
		{">=1.2.3", "1.2.2", false},
		{">1.2.3", "1.2.3", false},
		{">1.2", "1.2.9", false},
		{">1.2", "1.3.0", true},
		{">1", "2.0.0", true},
		{"<1.2.3", "1.2.2", true},
		{"<1.2.3", "1.2.3", false},
		{"<1.2", "1.1.9", true},
		{"<1.2", "1.2.0", false},
		{"<=1.2", "1.2.9", true},
		{"<=1.2", "1.3.0", false},
		{"<=1.2.3", "1.2.3", true},
		{">=1.0.0 <2.0.0", "1.5.0", true},
		{">=1.0.0 <2.0.0", "2.0.0", false},
		{">= 1.0.0", "1.0.0", true},
		{"1.0.0 - 2.0.0", "2.0.0", true},
		{"1.0.0 - 2.0.0", "2.0.1", false},
		{"1.2 - 2.3", "2.3.9", true},
		{"1.2 - 2.3", "2.4.0", false},
		{"1.2 - 2", "2.9.9", true},
		{"1.2 - 2", "1.1.9", false},
		{"^1.0.0 || ^3.0.0", "3.1.0", true},
		{"^1.0.0 || ^3.0.0", "2.1.0", false},
		{"<1.0.0 || >=2.0.0", "2.0.0", true},
		{"^1.2.3", "1.3.0-beta.1", false},
		{"^1.2.3-beta.1", "1.2.3-beta.2", true},
		{"^1.2.3-beta.1", "1.2.4-beta.1", false},
		{"^1.2.3-beta.1", "1.2.3", true},
		{"<2.0.0", "2.0.0-rc.1", false},
		{">=1.0.0-rc.1", "1.0.0-rc.2", true},
		{"^1.2.3", "1.5.0+build.7", true},
		{"^1.2.3-beta.1", "1.2.3-beta.2+build.7", true},
		{"latest", "1.0.0", false},
		{"workspace:*", "1.0.0", false},
		{"^1.2.3", "1.2", false},
		{"1.2.3.4", "1.2.3", false},
	}
	for _, tt := range tests {
		if got := npmSatisfies(tt.rng, tt.v); got != tt.want {
			t.Errorf("npmSatisfies(%q, %q) = %v, want %v", tt.rng, tt.v, got, tt.want)
		}
	}
}

func TestNpmCommands(t *testing.T) {
	tests := []struct {
		fixture, manifest string
		want              core.Commands
	}{
		{"npm/lock-v3", "package.json", core.Commands{Install: []string{"npm ci"}, Build: []string{"npm run build"}, Test: []string{"npm test"}}},
		{"npm/lock-v1", "package.json", core.Commands{Install: []string{"npm ci"}}},
		{"npm/pnpm-v9", "package.json", core.Commands{Install: []string{"pnpm install --frozen-lockfile"}, Build: []string{"pnpm run build"}, Test: []string{"pnpm test"}}},
		{"npm/yarn-v1", "package.json", core.Commands{Install: []string{"yarn install --frozen-lockfile"}, Build: []string{"yarn run build"}, Test: []string{"yarn test"}}},
		{"npm/yarn-berry", "package.json", core.Commands{Install: []string{"yarn install --immutable"}, Test: []string{"yarn test"}}},
		{"npm/mono", "packages/a/package.json", core.Commands{Install: []string{"npm ci"}}},
		{"npm/mono", "tools/site/package.json", core.Commands{Install: []string{"cd tools/site && pnpm install --frozen-lockfile"}}},
		{"npm/mono", "legacy/package.json", core.Commands{Install: []string{"cd legacy && yarn install"}}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture+"/"+tt.manifest, func(t *testing.T) {
			root := copyTestdata(t, tt.fixture)
			for _, m := range manifestsIn(t, npm{}, root) {
				if m.Path != tt.manifest {
					continue
				}
				if got := (npm{}).Commands(root, m); !reflect.DeepEqual(got, tt.want) {
					t.Errorf("Commands = %+v\nwant %+v", got, tt.want)
				}
				return
			}
			t.Fatalf("no manifest %s", tt.manifest)
		})
	}
}

func TestNpmImports(t *testing.T) {
	for _, name := range []string{"stripe", "@aws-sdk/client-s3"} {
		if got := (npm{}).Imports(core.Dep{Name: name}); !reflect.DeepEqual(got, []string{name}) {
			t.Errorf("Imports(%s) = %v", name, got)
		}
	}
}

func TestGithubRepo(t *testing.T) {
	tests := []struct{ in, want string }{
		{"git+https://github.com/jonschlinkert/is-number.git", "https://github.com/jonschlinkert/is-number"},
		{"git+ssh://git@github.com/stevemao/left-pad.git", "https://github.com/stevemao/left-pad"},
		{"git://github.com/o/r.git", "https://github.com/o/r"},
		{"git@github.com:o/r.git", "https://github.com/o/r"},
		{"github:o/r", "https://github.com/o/r"},
		{"o/r", "https://github.com/o/r"},
		{"https://www.github.com/o/r/tree/main/packages/x", "https://github.com/o/r"},
		{"scm:git:git://github.com/google/gson.git", "https://github.com/google/gson"},
		{"scm:git:https://github.com/o/r", "https://github.com/o/r"},
		{"https://github.com/sponsors/hynek", ""},
		{"https://gitlab.com/o/r", ""},
		{"https://github.com/o", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := githubRepo(tt.in); got != tt.want {
			t.Errorf("githubRepo(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// npmRegistry is a registry with one package whose versions the test can
// add to, serving packuments and real tarballs so npm runs without the network.
type npmRegistry struct {
	*httptest.Server
	mu       sync.Mutex
	name     string
	peer     string // a package each version pins as a peer at its own version
	versions []string
	tarballs map[string][]byte
}

func newNpmRegistry(t *testing.T, name string, versions ...string) *npmRegistry {
	t.Helper()
	r := &npmRegistry{name: name, tarballs: map[string][]byte{}}
	r.Server = httptest.NewServer(r)
	t.Cleanup(r.Close)
	for _, v := range versions {
		r.publish(t, v)
	}
	return r
}

func (r *npmRegistry) publish(t *testing.T, v string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := fmt.Sprintf(`{"name":%q,"version":%q,"main":"index.js"}`, r.name, v)
	if r.peer != "" {
		body = fmt.Sprintf(`{"name":%q,"version":%q,"main":"index.js","peerDependencies":{%q:%q}}`, r.name, v, r.peer, v)
	}
	for _, f := range []struct{ name, body string }{{"package/package.json", body}, {"package/index.js", "module.exports = 1\n"}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.versions = append(r.versions, v)
	r.tarballs[v] = buf.Bytes()
}

func (r *npmRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	base := r.name[strings.LastIndexByte(r.name, '/')+1:]
	if v, ok := strings.CutPrefix(strings.ReplaceAll(req.URL.Path, "%2f", "/"), "/"+r.name+"/-/"+base+"-"); ok {
		_, _ = w.Write(r.tarballs[strings.TrimSuffix(v, ".tgz")])
		return
	}
	if p, _ := url.PathUnescape(req.URL.Path); p != "/"+r.name {
		http.NotFound(w, req)
		return
	}
	versions := map[string]any{}
	for _, v := range r.versions {
		tgz := r.tarballs[v]
		s1, s512 := sha1.Sum(tgz), sha512.Sum512(tgz)
		ver := map[string]any{
			"name":    r.name,
			"version": v,
			"dist": map[string]string{
				"tarball":   r.URL + "/" + r.name + "/-/" + base + "-" + v + ".tgz",
				"shasum":    hex.EncodeToString(s1[:]),
				"integrity": "sha512-" + base64.StdEncoding.EncodeToString(s512[:]),
			},
		}
		if r.peer != "" {
			ver["peerDependencies"] = map[string]string{r.peer: v}
		}
		versions[v] = ver
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name":      r.name,
		"dist-tags": map[string]string{"latest": r.versions[len(r.versions)-1]},
		"versions":  versions,
	})
}

func TestNpmBumpReal(t *testing.T) {
	if !have("npm") {
		t.Skip("npm not found")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("npm_config_cache", t.TempDir())
	npmRegistries.Clear()
	ctx := context.Background()
	reg := newNpmRegistry(t, "tend-fixture", "1.0.0")
	root := t.TempDir()
	write(t, root, map[string]string{
		".npmrc":       "registry=" + reg.URL + "/\n",
		"package.json": "{\n  \"name\": \"app\",\n  \"version\": \"1.0.0\",\n  \"dependencies\": {\n    \"tend-fixture\": \"^1.0.0\"\n  }\n}\n",
	})
	if out, err := run(ctx, root, nil, "npm install --package-lock-only --ignore-scripts --no-audit --no-fund"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	d := depIn(t, npm{}, root, "package.json", "tend-fixture", true)
	if d.Version != "1.0.0" {
		t.Fatalf("dep = %+v", d)
	}
	reg.publish(t, "1.1.0")
	reg.publish(t, "2.0.0")
	if meta, err := (npm{}).Meta(ctx, fetch.New("", "test"), "tend-fixture"); err != nil || meta.Latest != "2.0.0" || len(meta.Releases) != 3 {
		t.Fatalf("Meta = %+v, %v", meta, err)
	}

	res, err := npm{}.Bump(ctx, root, d, "1.1.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Locked || !reflect.DeepEqual(res.Files, []string{"package-lock.json"}) {
		t.Errorf("in-range bump = %+v", res)
	}
	if d = depIn(t, npm{}, root, "package.json", "tend-fixture", true); d.Version != "1.1.0" || d.Req != "^1.0.0" {
		t.Errorf("after in-range bump: %+v", d)
	}

	res, err = npm{}.Bump(ctx, root, d, "2.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Locked || !reflect.DeepEqual(res.Files, []string{"package-lock.json", "package.json"}) {
		t.Errorf("major bump = %+v", res)
	}
	if d = depIn(t, npm{}, root, "package.json", "tend-fixture", true); d.Version != "2.0.0" || d.Req != "^2.0.0" {
		t.Errorf("after major bump: %+v", d)
	}
}

// A package that pins another as a peer at its own version moves with it in
// one lock pass; moved alone, either fails to resolve.
func TestNpmBumpGroupReal(t *testing.T) {
	if !have("npm") {
		t.Skip("npm not found")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("npm_config_cache", t.TempDir())
	npmRegistries.Clear()
	ctx := context.Background()
	runner := newNpmRegistry(t, "tend-runner", "1.0.0", "1.0.1")
	cov := &npmRegistry{name: "@tend-runner/cov", peer: "tend-runner", tarballs: map[string][]byte{}}
	cov.Server = httptest.NewServer(cov)
	t.Cleanup(cov.Close)
	cov.publish(t, "1.0.0")
	cov.publish(t, "1.0.1")
	root := t.TempDir()
	manifest := "{\n  \"name\": \"app\",\n  \"version\": \"1.0.0\",\n  \"devDependencies\": {\n    \"@tend-runner/cov\": \"1.0.0\",\n    \"tend-runner\": \"1.0.0\"\n  }\n}\n"
	write(t, root, map[string]string{".npmrc": "registry=" + runner.URL + "/\n@tend-runner:registry=" + cov.URL + "/\n", "package.json": manifest})
	if out, err := run(ctx, root, nil, "npm install --package-lock-only --ignore-scripts --no-audit --no-fund"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	deps := []core.Dep{depIn(t, npm{}, root, "package.json", "tend-runner", true), depIn(t, npm{}, root, "package.json", "@tend-runner/cov", true)}
	if _, err := (npm{}).Bump(ctx, root, deps[0], "1.0.1", ""); err == nil {
		t.Fatal("tend-runner moved alone past the version its peer pins")
	}
	write(t, root, map[string]string{"package.json": manifest})
	res, err := npm{}.BumpGroup(ctx, root, deps, "1.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Locked || !reflect.DeepEqual(res.Files, []string{"package-lock.json", "package.json"}) {
		t.Errorf("bump = %+v", res)
	}
	for _, name := range []string{"tend-runner", "@tend-runner/cov"} {
		if d := depIn(t, npm{}, root, "package.json", name, true); d.Version != "1.0.1" || d.Req != "1.0.1" {
			t.Errorf("after the bump: %+v", d)
		}
	}
}

// A direct dep is locked at the release tend chose, not the newest its range
// allows: the lock tool sees the exact version first, then the range again.
func TestNpmLockAt(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	script := "#!/bin/sh\ngrep '\"debug\"' package.json >> " + log + "\n"
	if err := os.WriteFile(filepath.Join(dir, "npm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	root := copyTestdata(t, "npm/lock-v3")
	before, _ := os.ReadFile(filepath.Join(root, "package.json"))
	d := depIn(t, npm{}, root, "package.json", "debug", true)
	res, err := (npm{}).Bump(context.Background(), root, d, "4.3.7", "")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(log)
	if want := "    \"debug\": \"4.3.7\",\n    \"debug\": \"^4.3.4\",\n"; string(got) != want {
		t.Errorf("package.json as each lock pass saw it:\n%s\nwant\n%s", got, want)
	}
	after, _ := os.ReadFile(filepath.Join(root, "package.json"))
	if string(after) != string(before) || slices.Contains(res.Files, "package.json") {
		t.Errorf("a range that admits the target was rewritten:\n%s", after)
	}
}

func depLines(ds []core.Dep) []string {
	var res []string
	for _, d := range ds {
		res = append(res, fmt.Sprintf("%s %q direct=%v", d.Name, d.Version, d.Direct))
	}
	slices.Sort(res)
	return res
}

// A yarn workspace sibling required by a plain range is no registry package,
// and the root does not repeat what the members require directly.
func TestYarnWorkspaces(t *testing.T) {
	tests := []struct {
		name, lock string
	}{
		{"classic", "# yarn lockfile v1\n\n\nleft-pad@^1.3.0:\n  version \"1.3.0\"\n  resolved \"https://registry.yarnpkg.com/left-pad/-/left-pad-1.3.0.tgz\"\n\nms@^2.1.3:\n  version \"2.1.3\"\n"},
		{"berry", "__metadata:\n  version: 8\n\n\"app@workspace:packages/app\":\n  version: 0.0.0-use.local\n  resolution: \"app@workspace:packages/app\"\n  dependencies:\n    left-pad: \"npm:^1.3.0\"\n    shared: \"npm:^1.0.0\"\n\n\"left-pad@npm:^1.3.0\":\n  version: 1.3.0\n  resolution: \"left-pad@npm:1.3.0\"\n\n\"ms@npm:^2.1.3\":\n  version: 2.1.3\n  resolution: \"ms@npm:2.1.3\"\n\n\"root@workspace:.\":\n  version: 0.0.0-use.local\n  resolution: \"root@workspace:.\"\n\n\"shared@npm:^1.0.0, shared@workspace:packages/shared\":\n  version: 1.0.0\n  resolution: \"shared@workspace:packages/shared\"\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, map[string]string{
				"package.json":                 `{"name": "root", "private": true, "workspaces": ["packages/*"], "dependencies": {"ms": "^2.1.3"}}`,
				"yarn.lock":                    tt.lock,
				"packages/app/package.json":    `{"name": "app", "dependencies": {"shared": "^1.0.0", "left-pad": "^1.3.0"}}`,
				"packages/shared/package.json": `{"name": "shared", "version": "1.0.0"}`,
			})
			if got, want := depLines(depsIn(t, npm{}, root, "packages/app/package.json")), []string{`left-pad "1.3.0" direct=true`}; !reflect.DeepEqual(got, want) {
				t.Errorf("app deps = %q, want %q", got, want)
			}
			if got, want := depLines(depsIn(t, npm{}, root, "package.json")), []string{`ms "2.1.3" direct=true`}; !reflect.DeepEqual(got, want) {
				t.Errorf("root deps = %q, want %q", got, want)
			}
			if got := depLines(depsIn(t, npm{}, root, "packages/shared/package.json")); len(got) != 0 {
				t.Errorf("shared deps = %q", got)
			}
		})
	}
	lk := yarnLock([]byte("\"shared@npm:^1.0.0, shared@workspace:packages/shared\":\n  version: 1.0.0\n"))
	if len(lk.all) != 0 || !lk.links["shared"] {
		t.Errorf("berry workspace block: all = %+v, links = %v", lk.all, lk.links)
	}
}

// Git, file and other tarball installs have a version in package-lock.json
// but no registry release; registry tarballs, custom registries included,
// and entries with no resolved URL stay.
func TestPackageLockSources(t *testing.T) {
	lock := `{"lockfileVersion": 3, "packages": {
		"": {"dependencies": {"mylib": "github:acme/mylib#v1", "tar": "https://example.com/t/tarlib-2.0.0.tgz", "vendored": "file:vendor/vendored-1.0.0.tgz", "@corp/ui": "^1.0.0", "left-pad": "^1.3.0", "is-number": "^7.0.0"}},
		"node_modules/mylib": {"version": "1.0.0", "resolved": "git+ssh://git@github.com/acme/mylib.git#0123456789abcdef"},
		"node_modules/tar": {"version": "2.0.0", "resolved": "https://example.com/t/tarlib-2.0.0.tgz"},
		"node_modules/vendored": {"version": "1.0.0", "resolved": "file:vendor/vendored-1.0.0.tgz"},
		"node_modules/@corp/ui": {"version": "1.2.0", "resolved": "https://artifactory.corp.example/api/npm/npm/@corp/ui/-/ui-1.2.0.tgz"},
		"node_modules/left-pad": {"version": "1.3.0", "resolved": "https://registry.npmjs.org/left-pad/-/left-pad-1.3.0.tgz"},
		"node_modules/is-number": {"version": "7.0.0"},
		"node_modules/mylib/node_modules/ms": {"version": "2.1.3", "resolved": "https://registry.npmjs.org/ms/-/ms-2.1.3.tgz"}
	}}`
	root := t.TempDir()
	write(t, root, map[string]string{
		"package.json":      `{"dependencies": {"mylib": "github:acme/mylib#v1", "tar": "https://example.com/t/tarlib-2.0.0.tgz", "vendored": "file:vendor/vendored-1.0.0.tgz", "@corp/ui": "^1.0.0", "left-pad": "^1.3.0", "is-number": "^7.0.0"}}`,
		"package-lock.json": lock,
	})
	want := []string{`@corp/ui "1.2.0" direct=true`, `is-number "7.0.0" direct=true`, `left-pad "1.3.0" direct=true`, `ms "2.1.3" direct=false`}
	if got := depLines(depsIn(t, npm{}, root, "package.json")); !reflect.DeepEqual(got, want) {
		t.Errorf("deps = %q\nwant %q", got, want)
	}
	ls, err := ReadLock("package-lock.json", []byte(lock))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, l := range ls {
		names = append(names, l.Name)
	}
	if want := []string{"@corp/ui", "is-number", "left-pad", "ms"}; !reflect.DeepEqual(names, want) {
		t.Errorf("ReadLock = %q, want %q", names, want)
	}
}

// An aliased pnpm dependency resolves to the version of the package it
// installs, in every lockfile format, and is reported once.
func TestPnpmAlias(t *testing.T) {
	tests := []struct {
		name, req, lock string
	}{
		{"v5", "npm:bar@^1.0.0", "lockfileVersion: 5.4\n\nspecifiers:\n  foo: npm:bar@^1.0.0\n\ndependencies:\n  foo: /bar/1.2.3\n\npackages:\n\n  /bar/1.2.3:\n    resolution: {integrity: sha512-x}\n    dev: false\n"},
		{"v6", "npm:bar@^1.0.0", "lockfileVersion: '6.0'\n\ndependencies:\n  foo:\n    specifier: npm:bar@^1.0.0\n    version: /bar@1.2.3\n\npackages:\n\n  /bar@1.2.3:\n    resolution: {integrity: sha512-x}\n    dev: false\n"},
		{"v9", "npm:bar@^1.0.0", "lockfileVersion: '9.0'\n\nimporters:\n\n  .:\n    dependencies:\n      foo:\n        specifier: npm:bar@^1.0.0\n        version: bar@1.2.3\n\npackages:\n\n  bar@1.2.3:\n    resolution: {integrity: sha512-x}\n\nsnapshots:\n\n  bar@1.2.3: {}\n"},
		{"v9 scoped", "npm:@s/bar@^1.0.0", "lockfileVersion: '9.0'\n\nimporters:\n\n  .:\n    dependencies:\n      foo:\n        specifier: npm:@s/bar@^1.0.0\n        version: '@s/bar@1.2.3(react@18.2.0)'\n\npackages:\n\n  '@s/bar@1.2.3':\n    resolution: {integrity: sha512-x}\n\nsnapshots:\n\n  '@s/bar@1.2.3(react@18.2.0)': {}\n"},
		{"v5 scoped", "npm:@s/bar@^1.0.0", "lockfileVersion: 5.4\n\nspecifiers:\n  foo: npm:@s/bar@^1.0.0\n\ndependencies:\n  foo: /@s/bar/1.2.3_react@18.2.0\n\npackages:\n\n  /@s/bar/1.2.3_react@18.2.0:\n    resolution: {integrity: sha512-x}\n    dev: false\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lk, err := pnpmLock([]byte(tt.lock), "")
			if err != nil {
				t.Fatal(err)
			}
			if got := lk.resolve("foo", tt.req); got != "1.2.3" {
				t.Errorf("resolve = %q", got)
			}
			root := t.TempDir()
			write(t, root, map[string]string{
				"package.json":   `{"dependencies": {"foo": "` + tt.req + `"}}`,
				"pnpm-lock.yaml": tt.lock,
			})
			name, _ := npmRegistryName("foo", tt.req)
			if got, want := depLines(depsIn(t, npm{}, root, "package.json")), []string{name + ` "1.2.3" direct=true`}; !reflect.DeepEqual(got, want) {
				t.Errorf("deps = %q, want %q", got, want)
			}
		})
	}
	for _, ref := range []pnpmRef{"link:../x", "file:../x", "1.2.3_react@18.2.0", "1.2.3(react@18.2.0)"} {
		want := ""
		if strings.HasPrefix(string(ref), "1.2.3") {
			want = "1.2.3"
		}
		if got := ref.version(); got != want {
			t.Errorf("version(%q) = %q, want %q", ref, got, want)
		}
	}
}

// A package.json saved with a byte order mark reads like any other, and a
// bump keeps the mark.
func TestNpmBOM(t *testing.T) {
	hideTools(t)
	root := t.TempDir()
	src := "\xef\xbb\xbf{\"packageManager\": \"pnpm@9.0.0\", \"dependencies\": {\"left-pad\": \"^1.2.0\"}}"
	write(t, root, map[string]string{"package.json": src})
	if ms := manifestsIn(t, npm{}, root); len(ms) != 1 || ms[0].Tool != "pnpm" {
		t.Errorf("Detect = %+v", ms)
	}
	d := depIn(t, npm{}, root, "package.json", "left-pad", true)
	if _, err := (npm{}).Bump(context.Background(), root, d, "2.0.0", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "package.json"))
	if want := strings.Replace(src, "^1.2.0", "^2.0.0", 1); string(got) != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	lock := "\xef\xbb\xbf{\"lockfileVersion\": 3, \"packages\": {\"node_modules/ms\": {\"version\": \"2.1.3\"}}}"
	if ls, err := ReadLock("package-lock.json", []byte(lock)); err != nil || len(ls) != 1 {
		t.Errorf("ReadLock = %+v, %v", ls, err)
	}
}

// tend cannot read or relock bun's lockfiles, so it changes nothing a bun
// lockfile locks and runs bun, not npm, to check a project.
func TestBunLockRefused(t *testing.T) {
	for _, lock := range []string{"bun.lock", "bun.lockb"} {
		t.Run(lock, func(t *testing.T) {
			fakeTool(t, "bun")
			root := t.TempDir()
			write(t, root, map[string]string{
				"package.json": `{"packageManager": "bun@1.2.0", "scripts": {"test": "bun test"}, "dependencies": {"left-pad": "^1.2.0"}}`,
				lock:           "{}",
			})
			ms := manifestsIn(t, npm{}, root)
			if len(ms) != 1 || ms[0].Lock != lock || ms[0].Tool != "bun" {
				t.Fatalf("Detect = %+v", ms)
			}
			c := (npm{}).Commands(root, ms[0])
			if !reflect.DeepEqual(c.Install, []string{"bun install --frozen-lockfile"}) || !reflect.DeepEqual(c.Test, []string{"bun run test"}) {
				t.Errorf("commands = %+v", c)
			}
			d := depIn(t, npm{}, root, "package.json", "left-pad", true)
			before := treeSnapshot(t, root)
			if _, err := (npm{}).Bump(context.Background(), root, d, "2.0.0", ""); !errors.Is(err, core.ErrNotSupported) {
				t.Errorf("bump err = %v", err)
			}
			if _, err := (npm{}).BumpGroup(context.Background(), root, []core.Dep{d}, "2.0.0"); !errors.Is(err, core.ErrNotSupported) {
				t.Errorf("group err = %v", err)
			}
			if _, err := (npm{}).Remove(context.Background(), root, []core.Dep{d}); !errors.Is(err, core.ErrNotSupported) {
				t.Errorf("remove err = %v", err)
			}
			if !reflect.DeepEqual(before, treeSnapshot(t, root)) {
				t.Error("a refused change wrote files")
			}
		})
	}
}

// An npm that writes its own lockfile format is run again with the old one.
func TestNpmKeepsLockFormat(t *testing.T) {
	logged := toolScript(t, "npm", `case "$*" in *--lockfile-version=1*) sed 's/"lockfileVersion": 3/"lockfileVersion": 1/' package-lock.json > x ;; *) sed 's/"lockfileVersion": 1/"lockfileVersion": 3/' package-lock.json > x ;; esac; mv x package-lock.json`+"\n")
	root := copyTestdata(t, "npm/lock-v1")
	d := depIn(t, npm{}, root, "package.json", "debug", true)
	if _, err := (npm{}).Bump(context.Background(), root, d, "4.9.0", ""); err != nil {
		t.Fatal(err)
	}
	m := d.Manifest
	if v := npmLockVersion(root, m); v != 1 {
		t.Errorf("lockfileVersion = %d, want 1", v)
	}
	if got := logged(root); len(got) == 0 || !strings.HasSuffix(got[len(got)-1], "--lockfile-version=1") {
		t.Errorf("ran %q", got)
	}
}
