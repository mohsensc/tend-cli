package eco

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/fetch"
	"github.com/mohsensc/tend-cli/internal/ver"
)

// copyFixture copies testdata/name into a fresh temp dir and returns it.
func copyTestdata(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join("testdata", filepath.FromSlash(name))
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// goldenCheck compares got with testdata/name. TEND_UPDATE=1 rewrites the file.
func goldenCheck(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", filepath.FromSlash(name))
	if os.Getenv("TEND_UPDATE") != "" {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (TEND_UPDATE=1 writes it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	res := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		rel, _ := filepath.Rel(root, p)
		res[filepath.ToSlash(rel)] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// treeDiff lists, per changed file, the lines that differ at the same position.
func treeDiff(before, after map[string]string) string {
	var names []string
	for n := range after {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		if before[n] == after[n] {
			continue
		}
		fmt.Fprintf(&b, "--- %s\n", n)
		old, cur := strings.Split(before[n], "\n"), strings.Split(after[n], "\n")
		for i := 0; i < len(old) || i < len(cur); i++ {
			var o, c string
			if i < len(old) {
				o = old[i]
			}
			if i < len(cur) {
				c = cur[i]
			}
			if o != c {
				fmt.Fprintf(&b, "%d: -%s\n%d: +%s\n", i+1, o, i+1, c)
			}
		}
	}
	return b.String()
}

// withoutTools makes every native tool look absent.
func hideTools(t *testing.T) {
	t.Helper()
	old := have
	have = func(string) bool { return false }
	t.Cleanup(func() { have = old })
}

type canned struct {
	url    string
	status int
	header map[string][]string
	body   string
	file   string // body from testdata
}

// replayOf serves the stubs through fetch.Replay.
func replayCanned(t *testing.T, stubs ...canned) core.Fetcher {
	t.Helper()
	dir := t.TempDir()
	for _, s := range stubs {
		if s.status == 0 {
			s.status = 200
		}
		if s.file != "" {
			b, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(s.file)))
			if err != nil {
				t.Fatal(err)
			}
			s.body = string(b)
		}
		rec := map[string]any{"url": s.url, "status": s.status, "header": s.header, "body": s.body}
		if !utf8.ValidString(s.body) {
			delete(rec, "body")
			rec["body_b64"] = base64.StdEncoding.EncodeToString([]byte(s.body))
		}
		b, _ := json.Marshal(rec)
		name := fetch.RecordName("GET", s.url, nil)
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return fetch.Replay(dir)
}

func recordedDir(eco string) string { return filepath.Join("testdata", eco, "recorded") }

func wantRecord(t *testing.T) {
	t.Helper()
	if os.Getenv("TEND_RECORD") == "" {
		t.Skip("set TEND_RECORD=1 to record against the live registry")
	}
}

func liveClient(t *testing.T, eco string) core.Fetcher {
	t.Helper()
	dir := recordedDir(eco)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return fetch.Record(fetch.New("", "record"), dir)
}

func manifestsIn(t *testing.T, e core.Ecosystem, root string) []core.Manifest {
	t.Helper()
	ms, err := e.Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func depsIn(t *testing.T, e core.Ecosystem, root, manifest string) []core.Dep {
	t.Helper()
	for _, m := range manifestsIn(t, e, root) {
		if m.Path == manifest {
			ds, err := e.Deps(root, m)
			if err != nil {
				t.Fatal(err)
			}
			return ds
		}
	}
	t.Fatalf("no manifest %s", manifest)
	return nil
}

func depIn(t *testing.T, e core.Ecosystem, root, manifest, name string, direct bool) core.Dep {
	t.Helper()
	for _, d := range depsIn(t, e, root, manifest) {
		if d.Name == name && d.Direct == direct {
			return d
		}
	}
	t.Fatalf("no dep %s (direct %v) in %s", name, direct, manifest)
	return core.Dep{}
}

type bumpCase struct {
	name     string
	manifest string
	dep      string
	direct   bool
	to       string
	toName   string
}

// bumpGolden bumps a copy of the fixture with the native tools hidden and
// compares what changed with the golden file.
func bumpGolden(t *testing.T, e core.Ecosystem, fixture string, tests []bumpCase) {
	t.Helper()
	hideTools(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := copyTestdata(t, fixture)
			d := depIn(t, e, root, tt.manifest, tt.dep, tt.direct)
			before := treeSnapshot(t, root)
			res, err := e.Bump(context.Background(), root, d, tt.to, tt.toName)
			if err != nil {
				t.Fatal(err)
			}
			out := fmt.Sprintf("files: %v\nlocked: %v\nnote: %s\n%s", res.Files, res.Locked, res.Note, treeDiff(before, treeSnapshot(t, root)))
			goldenCheck(t, fmt.Sprintf("%s/bump/%s.golden", e.Name(), tt.name), out)
		})
	}
}

func TestCargoDetect(t *testing.T) {
	root := copyTestdata(t, "cargo/workspace")
	write(t, root, map[string]string{"target/debug/Cargo.toml": "[package]\n", "vendor/x/Cargo.toml": "[package]\n"})
	got := manifestsIn(t, cargo{}, root)
	want := []core.Manifest{
		{Ecosystem: "cargo", Path: "Cargo.toml", Lock: "Cargo.lock", Tool: "cargo"},
		{Ecosystem: "cargo", Path: "crates/app/Cargo.toml", Lock: "Cargo.lock", Tool: "cargo"},
		{Ecosystem: "cargo", Path: "crates/shared/Cargo.toml", Lock: "Cargo.lock", Tool: "cargo"},
		{Ecosystem: "cargo", Path: "standalone/Cargo.toml", Tool: "cargo"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Detect = %+v\nwant %+v", got, want)
	}
}

func TestCargoDeps(t *testing.T) {
	root := copyTestdata(t, "cargo/workspace")
	for _, tt := range []struct{ name, manifest string }{
		{"root", "Cargo.toml"},
		{"app", "crates/app/Cargo.toml"},
		{"shared", "crates/shared/Cargo.toml"},
		{"standalone", "standalone/Cargo.toml"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			goldenCheck(t, "cargo/deps/"+tt.name+".golden.json", jsonOf(t, depsIn(t, cargo{}, root, tt.manifest)))
		})
	}
}

func TestCargoBump(t *testing.T) {
	bumpGolden(t, cargo{}, "cargo/workspace", []bumpCase{
		{"caret-implicit", "crates/app/Cargo.toml", "rand", true, "0.9.1", ""},
		{"table-form", "crates/app/Cargo.toml", "regex", true, "2.0.0", ""},
		{"renamed", "crates/app/Cargo.toml", "serde_yaml", true, "0.10.0", ""},
		{"workspace-entry", "crates/app/Cargo.toml", "serde", true, "2.0.1", ""},
		{"workspace-dotted", "crates/shared/Cargo.toml", "anyhow", true, "2.0.0", ""},
		{"workspace-root", "Cargo.toml", "tokio", true, "2.0.0", ""},
		{"exact", "crates/shared/Cargo.toml", "serde_json", true, "1.0.120", ""},
		{"target-table", "crates/app/Cargo.toml", "libc", true, "1.0.0", ""},
		{"inside-range", "crates/app/Cargo.toml", "libc", true, "0.2.160", ""},
		{"transitive", "Cargo.toml", "syn", false, "2.0.70", ""},
		{"swap", "crates/app/Cargo.toml", "cc", true, "1.2.0", "cc2"},
		{"no-lock", "standalone/Cargo.toml", "reqwest", true, "0.12.4", ""},
	})
}

func TestCargoBumpErrors(t *testing.T) {
	hideTools(t)
	root := copyTestdata(t, "cargo/workspace")
	ctx := context.Background()
	rand := depIn(t, cargo{}, root, "crates/app/Cargo.toml", "rand", true)
	if _, err := (cargo{}).Bump(ctx, root, rand, "not-a-version", ""); err == nil {
		t.Error("bumped to a version that does not parse")
	}
	syn := depIn(t, cargo{}, root, "Cargo.toml", "syn", false)
	if _, err := (cargo{}).Bump(ctx, root, syn, "2.0.70", "other"); err == nil {
		t.Error("swapped a transitive crate")
	}
	regex := depIn(t, cargo{}, root, "crates/app/Cargo.toml", "regex", true)
	if _, err := (cargo{}).Bump(ctx, root, regex, "2.0.0", "regex2"); err == nil {
		t.Error("swapped a crate that has its own table")
	}
	rand.Line = 0
	if _, err := (cargo{}).Bump(ctx, root, rand, "0.9.1", ""); err == nil {
		t.Error("bumped a dep with no line")
	}
}

// fakeTool puts a script called name first on PATH that appends its working
// directory and arguments to a log. It returns a function that reads the log
// with root cut out of the paths.
func fakeTool(t *testing.T, name string) func(root string) []string {
	t.Helper()
	return toolScript(t, name, "")
}

// toolScript is fakeTool running more shell after it logs.
func toolScript(t *testing.T, name, more string) func(root string) []string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	script := "#!/bin/sh\necho \"$PWD $*\" >> " + log + "\n" + more
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

// lockingTool is fakeTool that also stands in for the lock the tool writes:
// every run leaves the file at lock under root as relock made it from the
// lock as it was when the test began.
func lockingTool(t *testing.T, name, root, lock string, relock func(string) string) func(root string) []string {
	t.Helper()
	return toolScript(t, name, relockLine(t, root, lock, relock))
}

// relockLine is the shell line that sets the lock as lockingTool does.
func relockLine(t *testing.T, root, lock string, relock func(string) string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, lock))
	if err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(t.TempDir(), "next")
	if err := os.WriteFile(next, []byte(relock(string(b))), 0o644); err != nil {
		t.Fatal(err)
	}
	return "cp " + shArg(next) + " " + shArg(filepath.Join(root, lock)) + "\n"
}

func TestCargoBumpCommands(t *testing.T) {
	ctx := context.Background()
	root := copyTestdata(t, "cargo/workspace")
	logged := lockingTool(t, "cargo", root, "Cargo.lock", strings.NewReplacer(
		"name = \"rand\"\nversion = \"0.8.5\"", "name = \"rand\"\nversion = \"0.9.1\"",
		"name = \"syn\"\nversion = \"1.0.109\"", "name = \"syn\"\nversion = \"1.0.110\"",
		"name = \"cc\"\nversion = \"1.0.95\"", "name = \"cc2\"\nversion = \"1.2.0\"",
	).Replace)
	rand := depIn(t, cargo{}, root, "crates/app/Cargo.toml", "rand", true)
	syn := depIn(t, cargo{}, root, "Cargo.toml", "syn", false)
	cc := depIn(t, cargo{}, root, "crates/app/Cargo.toml", "cc", true)
	if syn.Version != "1.0.109" {
		t.Fatalf("syn = %+v", syn)
	}
	res, err := (cargo{}).Bump(ctx, root, rand, "0.9.1", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Locked || !reflect.DeepEqual(res.Files, []string{"crates/app/Cargo.toml", "Cargo.lock"}) {
		t.Errorf("bump = %+v", res)
	}
	if res, err = (cargo{}).Bump(ctx, root, syn, "1.0.110", ""); err != nil || !res.Locked || !reflect.DeepEqual(res.Files, []string{"Cargo.lock"}) {
		t.Errorf("transitive bump = %+v, %v", res, err)
	}
	if _, err = (cargo{}).Bump(ctx, root, cc, "1.2.0", "cc2"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"./crates/app update -p rand --precise 0.9.1",
		". update -p syn@1.0.109 --precise 1.0.110",
		"./crates/app update --workspace",
		"./crates/app update -p cc2 --precise 1.2.0",
	}
	if got := logged(root); !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %q\nwant %q", got, want)
	}
}

// vendored builds a crate that gets its dependency from a directory source, so
// cargo resolves it without the network.
func vendored(t *testing.T) string {
	root := t.TempDir()
	files := map[string]string{
		"Cargo.toml":  "[package]\nname = \"app\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[dependencies]\nfixture-dep = \"1.0\"\n",
		"src/main.rs": "fn main() {}\n",
		"Cargo.lock": "version = 4\n\n[[package]]\nname = \"app\"\nversion = \"0.1.0\"\ndependencies = [\n \"fixture-dep\",\n]\n\n" +
			"[[package]]\nname = \"fixture-dep\"\nversion = \"1.0.0\"\nsource = \"registry+https://github.com/rust-lang/crates.io-index\"\n",
		".cargo/config.toml": "[source.crates-io]\nreplace-with = \"local\"\n\n[source.local]\ndirectory = \"vendor\"\n",
	}
	for _, v := range []string{"1.0.0", "1.1.0", "2.0.0"} {
		dir := "vendor/fixture-dep-" + v
		files[dir+"/Cargo.toml"] = fmt.Sprintf("[package]\nname = \"fixture-dep\"\nversion = \"%s\"\nedition = \"2021\"\n", v)
		files[dir+"/src/lib.rs"] = "\n"
		files[dir+"/.cargo-checksum.json"] = `{"files":{},"package":null}`
	}
	write(t, root, files)
	return root
}

func TestCargoBumpReal(t *testing.T) {
	if !have("cargo") {
		t.Skip("cargo not found")
	}
	root := vendored(t)
	ctx := context.Background()
	e := cargo{}
	d := depIn(t, e, root, "Cargo.toml", "fixture-dep", true)
	if d.Version != "1.0.0" || d.Req != "1.0" {
		t.Fatalf("dep = %+v", d)
	}
	res, err := e.Bump(ctx, root, d, "2.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Locked || !reflect.DeepEqual(res.Files, []string{"Cargo.toml", "Cargo.lock"}) {
		t.Errorf("bump = %+v", res)
	}
	got := treeSnapshot(t, root)
	if !strings.Contains(got["Cargo.toml"], `fixture-dep = "2.0"`) {
		t.Errorf("Cargo.toml:\n%s", got["Cargo.toml"])
	}
	if !strings.Contains(got["Cargo.lock"], "name = \"fixture-dep\"\nversion = \"2.0.0\"") {
		t.Errorf("Cargo.lock:\n%s", got["Cargo.lock"])
	}
	if d := depIn(t, e, root, "Cargo.toml", "fixture-dep", true); d.Version != "2.0.0" {
		t.Errorf("locked version after the bump = %q", d.Version)
	}
}

func TestCargoMeta(t *testing.T) {
	ctx := context.Background()
	t.Run("recorded", func(t *testing.T) {
		m, err := (cargo{}).Meta(ctx, fetch.Replay(recordedDir("cargo")), "itoa")
		if err != nil {
			t.Fatal(err)
		}
		if m.Name != "itoa" || m.Repo != "https://github.com/dtolnay/itoa" || m.Latest == "" || len(m.Releases) < 3 {
			t.Fatalf("meta = %+v", m)
		}
		first := m.Releases[0]
		if first.Time.IsZero() || first.Pre || first.Yanked {
			t.Errorf("first release = %+v", first)
		}
		for i := 1; i < len(m.Releases); i++ {
			if m.Releases[i-1].Version == m.Releases[i].Version {
				t.Errorf("duplicate %s", m.Releases[i].Version)
			}
		}
	})
	t.Run("yanked and pre", func(t *testing.T) {
		f := replayCanned(t, canned{url: "https://crates.io/api/v1/crates/demo", body: `{
			"crate": {"id": "demo", "max_stable_version": "1.2.0", "repository": "https://github.com/x/demo"},
			"versions": [
				{"num": "2.0.0-rc.1", "yanked": false, "created_at": "2024-05-01T10:00:00.123456+00:00"},
				{"num": "1.2.0", "yanked": false, "created_at": "2024-04-01T10:00:00.000000+00:00"},
				{"num": "1.10.0", "yanked": true, "created_at": "2024-03-01T10:00:00.000000+00:00"},
				{"num": "1.9.0", "yanked": false, "created_at": "2024-02-01T10:00:00.000000+00:00"}
			]}`})
		m, err := (cargo{}).Meta(ctx, f, "demo")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range m.Releases {
			got = append(got, fmt.Sprintf("%s pre=%v yanked=%v", r.Version, r.Pre, r.Yanked))
		}
		// 1.9.0 sorts below 1.10.0, and the newest published (1.2.0) is not the highest
		want := []string{"1.2.0 pre=false yanked=false", "1.9.0 pre=false yanked=false", "1.10.0 pre=false yanked=true", "2.0.0-rc.1 pre=true yanked=false"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("releases = %q\nwant %q", got, want)
		}
		if m.Latest != "1.2.0" || m.Repo != "https://github.com/x/demo" {
			t.Errorf("latest %q repo %q", m.Latest, m.Repo)
		}
		if want := time.Date(2024, 2, 1, 10, 0, 0, 0, time.UTC); !m.Releases[1].Time.Equal(want) {
			t.Errorf("time = %v", m.Releases[1].Time)
		}
	})
	t.Run("not found", func(t *testing.T) {
		f := replayCanned(t, canned{url: "https://crates.io/api/v1/crates/nope", status: 404, body: `{"errors":[{"detail":"Not Found"}]}`})
		if _, err := (cargo{}).Meta(ctx, f, "nope"); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("server error", func(t *testing.T) {
		f := replayCanned(t, canned{url: "https://crates.io/api/v1/crates/down", status: 403, body: "forbidden"})
		if _, err := (cargo{}).Meta(ctx, f, "down"); err == nil || !strings.Contains(err.Error(), "403") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestCargoMetaRecord(t *testing.T) {
	wantRecord(t)
	if _, err := (cargo{}).Meta(context.Background(), liveClient(t, "cargo"), "itoa"); err != nil {
		t.Fatal(err)
	}
}

func TestCargoImports(t *testing.T) {
	tests := map[string]string{"serde": "serde", "serde_yaml": "serde_yaml", "tokio-util": "tokio_util", "a-b-c": "a_b_c"}
	for name, want := range tests {
		got := (cargo{}).Imports(core.Dep{Name: name})
		if !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("Imports(%s) = %v, want [%s]", name, got, want)
		}
	}
}

func TestCargoCommands(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		path string
		want core.Commands
	}{
		{"Cargo.toml", core.Commands{Build: []string{"cargo build"}, Test: []string{"cargo test"}}},
		{"crates/app/Cargo.toml", core.Commands{Build: []string{"cd crates/app && cargo build"}, Test: []string{"cd crates/app && cargo test"}}},
	}
	for _, tt := range tests {
		got := (cargo{}).Commands(root, core.Manifest{Path: tt.path})
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Commands(%s) = %+v, want %+v", tt.path, got, tt.want)
		}
	}
}

func TestRequirements(t *testing.T) {
	tests := []struct {
		dialect string
		req, to string
		want    string // "" means an error
	}{
		{"cargo", "1.2", "2.0.1", "2.0"},
		{"cargo", "1.2", "1.9.9", "1.2"},
		{"cargo", "1", "2.3.4", "2"},
		{"cargo", "^1.2", "2.5.0", "^2.5"},
		{"cargo", "^0.2.3", "0.5.1", "^0.5.1"},
		{"cargo", "0.2", "0.3.1", "0.3"},
		{"cargo", "0.0.3", "0.0.4", "0.0.4"},
		{"cargo", "~1.2.3", "1.2.9", "~1.2.3"},
		{"cargo", "~1.2.3", "1.3.0", "~1.3.0"},
		{"cargo", "~1", "1.9.0", "~1"},
		{"cargo", "=1.2.3", "2.0.0", "=2.0.0"},
		{"cargo", "=1.2", "2.0.0", "=2.0.0"},
		{"cargo", ">=1.2, <2", "3.1.0", ">=1.2, <4"},
		{"cargo", ">=1.2, <2", "1.5.0", ">=1.2, <2"},
		{"cargo", ">1.0", "2.0.0", ">1.0"},
		{"cargo", "*", "9.0.0", "*"},
		{"cargo", "1.*", "2.0.0", "2.*"},
		{"cargo", "1.2.*", "1.2.9", "1.2.*"},
		{"cargo", "1.2", "2.0.0-rc.1", "2.0.0-rc.1"},
		{"cargo", "1.2", "latest", ""},
		{"composer", "^1.2", "2.0.0", "^2.0"},
		{"composer", "^1.2", "1.9.0", "^1.2"},
		{"composer", "~1.2", "2.0.0", "~2.0"},
		{"composer", "~1.2", "1.9.0", "~1.2"},
		{"composer", "~1.2.3", "1.2.9", "~1.2.3"},
		{"composer", "~1.2.3", "1.3.0", "~1.3.0"},
		{"composer", "1.2.3", "2.0.0", "2.0.0"},
		{"composer", "v1.2.3", "2.0.0", "v2.0.0"},
		{"composer", ">=1.2", "3.0.0", ">=3.0.0"},
		{"composer", ">=1.2", "1.1.0", ">=1.1.0"},
		{"composer", ">=1.2 <2.0", "3.0.0", ">=1.2 <4.0"},
		{"composer", ">= 1.2, < 2.0", "3.0.0", ">= 1.2, < 4.0"},
		{"composer", "^1.0 || ^2.0", "3.1.0", "^1.0 || ^3.1"},
		{"composer", "^1.0 || ^2.0", "2.5.0", "^1.0 || ^2.0"},
		{"composer", "^1.0|^2.0", "3.1.0", "^1.0|^3.1"},
		{"composer", "1.2.*", "2.0.0", "2.0.*"},
		{"composer", "*", "2.0.0", "*"},
		{"composer", "^1.2@dev", "2.0.0", ""},
		{"composer", "1.0 - 2.0", "3.0.0", ""},
		{"composer", "dev-master", "3.0.0", ""},
		{"gem", "~> 1.2", "2.0.0", "~> 2.0"},
		{"gem", "~> 1.2", "1.9.0", "~> 1.2"},
		{"gem", "~> 1.2.3", "2.0.0", "~> 2.0.0"},
		{"gem", "~> 1.2.3", "1.3.0", "~> 1.3.0"},
		{"gem", "~> 1", "2.0.0", "~> 2"},
		{"gem", ">= 1.2", "2.0.0", ">= 2.0.0"},
		{"gem", ">= 1.2", "1.2.0", ">= 1.2"},
		{"gem", ">= 1.2, < 2", "3.0.0", ">= 1.2, < 4"},
		{"gem", "= 1.2.3", "1.2.4", "= 1.2.4"},
		{"gem", "1.2.3", "1.2.4", "1.2.4"},
		{"gem", "!= 1.2.3", "1.2.3", "!= 1.2.3"},
		{"gem", "~> 2.0.0.rc1", "2.0.0.rc2", "~> 2.0.0.rc1"},
	}
	for _, tt := range tests {
		t.Run(tt.dialect+" "+tt.req+" to "+tt.to, func(t *testing.T) {
			got, err := fixReq(tt.req, tt.to, tt.dialect == "cargo")
			if tt.want == "" {
				if err == nil {
					t.Fatalf("got %q, want an error", got)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("fixReq = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestRequirementsAdmit(t *testing.T) {
	tests := []struct {
		req, v string
		cargo  bool
		want   bool
	}{
		{"1.2", "1.9.0", true, true},
		{"1.2", "2.0.0", true, false},
		{"0.2.3", "0.2.9", true, true},
		{"0.2.3", "0.3.0", true, false},
		{"~1.2", "1.2.9", true, true},
		{"~1.2", "1.3.0", true, false},
		{"~1.2", "1.3.0", false, true},
		{"=1.2", "1.2.7", true, true},
		{"1.2", "1.2.7", false, false},
		{"1.2.0", "1.2", false, true},
		{"^1.2", "2.0.0-rc.1", true, false},
		{"^2.0.0-rc.1", "2.0.0-rc.2", true, true},
		{"*", "0.0.1", true, true},
		{">=1, <3 || ^5", "5.1.0", false, true},
		{"garbage", "1.0.0", true, false},
	}
	for _, tt := range tests {
		v, _ := ver.Parse(tt.v)
		if got := admits(tt.req, v, tt.cargo); got != tt.want {
			t.Errorf("admits(%q, %s, cargo=%v) = %v, want %v", tt.req, tt.v, tt.cargo, got, tt.want)
		}
	}
}

func TestPickVersion(t *testing.T) {
	tests := []struct {
		req  string
		have []string
		want string
	}{
		{"1.0", []string{"0.9.5", "1.0.4", "1.2.0"}, "1.2.0"},
		{"0.9", []string{"0.9.5", "1.0.4"}, "0.9.5"},
		{"3", []string{"1.0.0", "2.0.0"}, "2.0.0"},
		{"1", nil, ""},
	}
	for _, tt := range tests {
		if got := pickVersion(tt.req, tt.have); got != tt.want {
			t.Errorf("pickVersion(%q, %v) = %q, want %q", tt.req, tt.have, got, tt.want)
		}
	}
}

func TestCleanRepo(t *testing.T) {
	tests := map[string]string{
		"https://github.com/a/b":         "https://github.com/a/b",
		"https://github.com/a/b.git":     "https://github.com/a/b",
		"https://github.com/a/b/":        "https://github.com/a/b",
		"git+https://github.com/a/b.git": "https://github.com/a/b",
		"git://github.com/a/b.git":       "https://github.com/a/b",
		"git@github.com:a/b.git":         "https://github.com/a/b",
		"ssh://git@github.com/a/b.git":   "https://github.com/a/b",
		"http://github.com/a/b":          "https://github.com/a/b",
		"  https://gitlab.com/a/b  ":     "https://gitlab.com/a/b",
		"":                               "",
	}
	for in, want := range tests {
		if got := cleanRepo(in); got != want {
			t.Errorf("cleanRepo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShArg(t *testing.T) {
	tests := map[string]string{"serde": "serde", "a@1.2.3": "a@1.2.3", "a b": "'a b'", "it's": `'it'\''s'`, "$(x)": "'$(x)'"}
	for in, want := range tests {
		if got := shArg(in); got != want {
			t.Errorf("shArg(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitKey(t *testing.T) {
	tests := map[string][]string{
		"dependencies":                                    {"dependencies"},
		"dependencies.serde":                              {"dependencies", "serde"},
		`target.'cfg(unix)'.dependencies`:                 {"target", "cfg(unix)", "dependencies"},
		`target."x86_64-pc-windows-gnu".dev-dependencies`: {"target", "x86_64-pc-windows-gnu", "dev-dependencies"},
		`target.'cfg(target_os = "linux")'.dependencies`:  {"target", `cfg(target_os = "linux")`, "dependencies"},
	}
	for in, want := range tests {
		if got := splitKey(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitKey(%s) = %q, want %q", in, got, want)
		}
	}
}

// failingTool is a tool that prints a message and exits 1.
func failingTool(t *testing.T, name string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\necho 'boom: it broke' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestNativeToolFailure(t *testing.T) {
	tests := []struct {
		e             core.Ecosystem
		tool, fixture string
		manifest, dep string
		direct        bool
		to            string
	}{
		{cargo{}, "cargo", "cargo/workspace", "crates/app/Cargo.toml", "rand", true, "0.9.1"},
		{rubygems{}, "bundle", "rubygems/project", "Gemfile", "rails", true, "8.0.0"},
		{composer{}, "composer", "composer/project", "composer.json", "monolog/monolog", true, "4.0.0"},
		{nuget{}, "dotnet", "nuget/solution", "src/App/App.csproj", "newtonsoft.json", true, "13.0.3"},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			failingTool(t, tt.tool)
			root := copyTestdata(t, tt.fixture)
			d := depIn(t, tt.e, root, tt.manifest, tt.dep, tt.direct)
			_, err := tt.e.Bump(context.Background(), root, d, tt.to, "")
			if err == nil || !strings.Contains(err.Error(), "boom: it broke") || !strings.Contains(err.Error(), tt.dep) {
				t.Errorf("err = %v, want the tool's output and the package name", err)
			}
		})
	}
}

// TestLockNotLanded has the lock tool leave the package where it was or move
// it past the target: the bump fails rather than prove another release.
func TestLockNotLanded(t *testing.T) {
	tests := []struct {
		e             core.Ecosystem
		tool, fixture string
		manifest, dep string
		direct        bool
		to, lock      string
		relock        func(string) string
		want          string
	}{
		{cargo{}, "cargo", "cargo/workspace", "crates/app/Cargo.toml", "rand", true, "0.9.1", "Cargo.lock",
			nil, "rand locked at 0.8.5, not 0.9.1"},
		{cargo{}, "cargo", "cargo/workspace", "Cargo.toml", "syn", false, "1.0.110", "Cargo.lock",
			strings.NewReplacer(`version = "1.0.109"`, `version = "1.0.111"`).Replace, "syn locked at 1.0.111, 2.0.60, not 1.0.110"},
		{composer{}, "composer", "composer/project", "composer.json", "monolog/monolog", true, "4.0.0", "composer.lock",
			strings.NewReplacer(`"version": "3.5.0"`, `"version": "4.0.1"`).Replace, "monolog/monolog locked at 4.0.1, not 4.0.0"},
		{rubygems{}, "bundle", "rubygems/project", "Gemfile", "rails", true, "8.0.0", "Gemfile.lock",
			nil, "rails locked at 7.1.3, not 8.0.0"},
		{rubygems{}, "bundle", "rubygems/project", "Gemfile", "rack", false, "3.1.0", "Gemfile.lock",
			strings.NewReplacer("    rack (3.0.9)", "").Replace, "rack is not locked after the update to 3.1.0"},
	}
	for _, tt := range tests {
		t.Run(tt.tool+"/"+tt.dep, func(t *testing.T) {
			root := copyTestdata(t, tt.fixture)
			relock := tt.relock
			if relock == nil {
				relock = func(s string) string { return s }
			}
			lockingTool(t, tt.tool, root, tt.lock, relock)
			d := depIn(t, tt.e, root, tt.manifest, tt.dep, tt.direct)
			_, err := tt.e.Bump(context.Background(), root, d, tt.to, "")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestMalformedManifests(t *testing.T) {
	tests := []struct {
		e       core.Ecosystem
		path    string
		content string
	}{
		{cargo{}, "Cargo.toml", "[dependencies\nserde = 1"},
		{composer{}, "composer.json", `{"require": {"a/b": }`},
		{composer{}, "composer.json", `{"require": {"a/b": 1}}`},
		{nuget{}, "App.csproj", "<Project><ItemGroup>"},
		{docker{}, "compose.yaml", "services: [unclosed"},
		{actions{}, ".github/workflows/ci.yml", "jobs: {a: [}"},
	}
	for _, tt := range tests {
		t.Run(tt.e.Name()+" "+tt.content, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, map[string]string{tt.path: tt.content})
			ms, err := tt.e.Detect(root)
			if err != nil || len(ms) != 1 {
				t.Fatalf("Detect = %v, %v; a broken manifest still counts as one", ms, err)
			}
			if _, err := tt.e.Deps(root, ms[0]); err == nil {
				t.Error("Deps read a broken manifest without complaint")
			}
		})
	}
	root := t.TempDir()
	if _, err := (rubygems{}).Deps(root, core.Manifest{Path: "Gemfile"}); err == nil {
		t.Error("Deps read a Gemfile that is not there")
	}
	if _, err := (cargo{}).Deps(root, core.Manifest{Path: "Cargo.toml", Lock: "Cargo.lock"}); err == nil {
		t.Error("Deps read a Cargo.toml that is not there")
	}
}

func TestEmptyManifests(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"Cargo.toml":               "[package]\nname = \"x\"\n",
		"Gemfile":                  "source 'https://rubygems.org'\n",
		"composer.json":            "{}",
		"App.csproj":               "<Project/>",
		"Dockerfile":               "",
		"compose.yaml":             "",
		".github/workflows/ci.yml": "",
	})
	for _, e := range []core.Ecosystem{cargo{}, rubygems{}, composer{}, nuget{}, docker{}, actions{}} {
		ms := manifestsIn(t, e, root)
		if len(ms) == 0 {
			t.Errorf("%s: nothing detected", e.Name())
		}
		for _, m := range ms {
			if ds, err := e.Deps(root, m); err != nil || len(ds) != 0 {
				t.Errorf("%s %s: deps = %v, %v", e.Name(), m.Path, ds, err)
			}
		}
	}
}

func TestGetDoc(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write([]byte(`{"a": 7}`)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		status int
		body   string
		found  bool
		err    string
		a      int
	}{
		{"plain", 200, `{"a": 3}`, true, "", 3},
		{"gzip", 200, gz.String(), true, "", 7},
		{"missing", 404, ``, false, "", 0},
		{"server error", 502, `oops`, false, "HTTP 502", 0},
		{"bad json", 200, `{"a": `, false, "unexpected end", 0},
		{"bad gzip", 200, "\x1f\x8bnot gzip", false, "gzip", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := replayCanned(t, canned{url: "https://example.test/doc", status: tt.status, body: tt.body})
			var got struct{ A int }
			found, err := getDoc(context.Background(), f, "https://example.test/doc", &got)
			if found != tt.found || got.A != tt.a || (tt.err == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tt.err)) {
				t.Errorf("found %v, a %d, err %v; want %v, %d, %q", found, got.A, err, tt.found, tt.a, tt.err)
			}
		})
	}
	if _, err := getDoc(context.Background(), replayCanned(t), "https://example.test/unrecorded", &struct{}{}); err == nil {
		t.Error("a transport error must come back as an error")
	}
}

func TestSeen(t *testing.T) {
	var s learned[string]
	if got := s.get("k"); len(got) != 0 {
		t.Errorf("get on empty = %v", got)
	}
	s.add("k", "a", "b", "a")
	s.add("k", "b", "c")
	got := s.get("k")
	if !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("get = %v", got)
	}
	got[0] = "changed"
	if s.get("k")[0] != "a" {
		t.Error("get hands out its own storage")
	}
}

func TestSortReleases(t *testing.T) {
	rs := []core.Release{{Version: "v4.2.2"}, {Version: "v4"}, {Version: "1.10.0"}, {Version: "1.9.0"}, {Version: "v4.0.0"}, {Version: "junk"}, {Version: "2.0.0-rc.1"}, {Version: "2.0.0"}}
	sortReleases(rs)
	var got []string
	for _, r := range rs {
		got = append(got, r.Version)
	}
	want := []string{"junk", "1.9.0", "1.10.0", "2.0.0-rc.1", "2.0.0", "v4", "v4.0.0", "v4.2.2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sortReleases = %q, want %q", got, want)
	}
}

func TestLastLines(t *testing.T) {
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprint("line ", i))
	}
	got := lastLines(strings.Join(lines, "\n")+"\n\n", 20)
	if !strings.HasPrefix(got, "line 11\n") || !strings.HasSuffix(got, "line 30") || strings.Count(got, "\n") != 19 {
		t.Errorf("lastLines = %q", got)
	}
}

func TestCargoKeyLine(t *testing.T) {
	lines := strings.Split(`[package]
name = "x"

[dependencies]
serde = "1"
path = "2"
"quoted-name" = "3"
dotted.version = "4"

[[bin]]
name = "tool"
path = "src/x.rs"

[dependencies.regex]
version = "1"

[target.'cfg(unix)'.dependencies]
libc = "0.2"`, "\n")
	tests := []struct {
		tbl  []string
		key  string
		want int
	}{
		{[]string{"package"}, "name", 2},
		{[]string{"dependencies"}, "serde", 5},
		{[]string{"dependencies"}, "path", 6},
		{[]string{"dependencies"}, "quoted-name", 7},
		{[]string{"dependencies"}, "dotted", 8},
		{[]string{"dependencies"}, "regex", 14},
		{[]string{"dependencies"}, "name", 0},
		{[]string{"dev-dependencies"}, "serde", 0},
		{[]string{"target", "cfg(unix)", "dependencies"}, "libc", 18},
	}
	for _, tt := range tests {
		if got := cargoKeyLine(lines, tt.tbl, tt.key); got != tt.want {
			t.Errorf("cargoKeyLine(%v, %s) = %d, want %d", tt.tbl, tt.key, got, tt.want)
		}
	}
}
