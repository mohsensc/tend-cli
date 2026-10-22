package verify

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

func TestScanWarnings(t *testing.T) {
	dir := "/w/repo"
	log := `
/w/repo/app/report.py:7: DeprecationWarning: fetch_all() is deprecated; use iter_rows()
  rows = fixturelib.fetch_all()
/w/repo/.venv/lib/python3.13/site-packages/other/x.py:3: DeprecationWarning: inside a package
app/views.py:12: PendingDeprecationWarning: render() will change
(node:4242) [DEP0005] DeprecationWarning: Buffer() is deprecated due to security and usability issues.
    at showFlaggedDeprecation (node:buffer:178:11)
    at new Buffer (node:buffer:274:3)
    at encode (/w/repo/node_modules/legacy-b64/index.js:3:10)
    at Object.<anonymous> (/w/repo/src/upload.js:21:15)
(node:4242) DeprecationWarning: no stack for this one
next line
/w/repo/lib/thing.rb:4: warning: URI.escape is obsolete and deprecated
PHP Deprecated: Creation of dynamic property A::$b is deprecated in /w/repo/src/A.php on line 9
/w/repo/Api/Client.cs(31,13): warning CS0618: 'WebClient' is obsolete: 'Use HttpClient instead.' [/w/repo/Api/Api.csproj]
[WARNING] /w/repo/src/main/java/x/Main.java:[18,9] [deprecation] Date(String) in Date has been deprecated
/w/repo/pkg/a.go:9:2: ioutil.ReadAll is deprecated: As of Go 1.16, this function simply calls io.ReadAll. (SA1019)
pkg/b.go:11:14: strings.Title has been deprecated since Go 1.18 and an alternative has been available since Go 1.0: Use golang.org/x/text/cases instead.  (SA1019)
(node:4242) [DEP0005] DeprecationWarning: Buffer() is deprecated due to security and usability issues.
    at showFlaggedDeprecation (node:buffer:178:11)
    at Object.<anonymous> (/w/repo/src/legacy.js:4:9)
/usr/lib/python3/dist-packages/y.py:1: DeprecationWarning: the system's
`
	got := scanWarnings(bufio.NewScanner(strings.NewReader(log)), dir)
	var have []string
	for _, w := range got {
		have = append(have, w.File+":"+strconv.Itoa(w.Line)+" ["+w.Pkg+"] "+w.Msg)
	}
	want := []string{
		"app/report.py:7 [] fetch_all() is deprecated; use iter_rows()",
		"app/views.py:12 [] render() will change",
		"src/upload.js:21 [legacy-b64] Buffer() is deprecated due to security and usability issues.",
		"lib/thing.rb:4 [] URI.escape is obsolete and deprecated",
		"src/A.php:9 [] Creation of dynamic property A::$b is deprecated",
		"Api/Client.cs:31 [] 'WebClient' is obsolete: 'Use HttpClient instead.'",
		"src/main/java/x/Main.java:18 [] Date(String) in Date has been deprecated",
		"pkg/a.go:9 [] ioutil.ReadAll is deprecated: As of Go 1.16, this function simply calls io.ReadAll.",
		"pkg/b.go:11 [runtime:go] strings.Title has been deprecated since Go 1.18 and an alternative has been available since Go 1.0: Use golang.org/x/text/cases instead.",
		"src/legacy.js:4 [runtime:nodejs] Buffer() is deprecated due to security and usability issues.",
	}
	if !slices.Equal(have, want) {
		t.Errorf("warnings =\n%s\nwant\n%s", strings.Join(have, "\n"), strings.Join(want, "\n"))
	}
}

func TestWarningsOn(t *testing.T) {
	got := warningsOn([]string{"PATH=/bin", "NODE_OPTIONS=--max-old-space-size=4096", "PYTHONWARNINGS=error::UserWarning"})
	want := []string{
		"PATH=/bin",
		"NODE_OPTIONS=--max-old-space-size=4096 --trace-deprecation",
		"PYTHONWARNINGS=error::UserWarning,default::DeprecationWarning,default::PendingDeprecationWarning",
		"RUBYOPT=-W:deprecated",
	}
	if !slices.Equal(got, want) {
		t.Errorf("env = %q", got)
	}
}

// A run asked for warnings comes back with them, read from its own logs.
func TestRunCollectsWarnings(t *testing.T) {
	dir := t.TempDir()
	script := `import os, warnings
warnings.warn("old_api() is deprecated", DeprecationWarning)
assert "DeprecationWarning" in os.environ["PYTHONWARNINGS"]
`
	if err := os.WriteFile(filepath.Join(dir, "t.py"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not found in PATH")
	}
	res, err := Run(context.Background(), &Worktree{Dir: dir}, Options{
		Commands: core.Commands{Test: []string{"python3 t.py"}},
		LogDir:   filepath.Join(t.TempDir(), "logs"),
		Warnings: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pass {
		t.Skipf("python3 could not run the script: %+v", res.Steps)
	}
	if len(res.Warnings) != 1 || res.Warnings[0].File != "t.py" || res.Warnings[0].Line != 2 || res.Warnings[0].Msg != "old_api() is deprecated" {
		t.Errorf("warnings = %+v", res.Warnings)
	}
}

// uses is a core.ScanIndex that knows only the sites of each import name.
type uses map[string][]core.Site

func (u uses) Uses(names []string) []core.Site {
	var res []core.Site
	for _, n := range names {
		res = append(res, u[n]...)
	}
	return res
}
func (u uses) Fields(string, []string) []core.Site { return nil }
func (u uses) Endpoints([]string) []core.URLUse    { return nil }
func (u uses) URLs() []core.URLUse                 { return nil }
func (u uses) Imports(string) []core.Import        { return nil }
func (u uses) Files() []string                     { return nil }

func TestRuntime(t *testing.T) {
	lib := core.Dep{Ecosystem: core.Pip, Name: "fixturelib", Version: "1.0.0", Direct: true}
	b64 := core.Dep{Ecosystem: core.Npm, Name: "legacy-b64", Version: "1.2.0", Direct: true}
	quiet := core.Dep{Ecosystem: core.Npm, Name: "quiet", Version: "2.0.0", Direct: true}
	gover := core.Dep{Ecosystem: core.Toolchain, Name: "go", Version: "1.26", Req: "1.26", Direct: true, Min: true}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(strings.Repeat("\n", 10)+"\treturn strings.Title(s)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inv := &core.Inventory{Root: root, Deps: []core.Dep{lib, b64, quiet, gover}, Index: uses{
		"fixturelib": {{File: "app/report.py", Line: 7, Chain: []string{"fixturelib", "fetch_all"}, Call: true, Text: "rows = fixturelib.fetch_all()"}},
	}}
	r := &Runtime{Warnings: []core.Warning{
		{File: "app/report.py", Line: 7, Msg: "fetch_all() is deprecated; use iter_rows()"},
		{File: "app/report.py", Line: 7, Msg: "fetch_all(limit=) is deprecated"},
		{File: "src/upload.js", Line: 21, Pkg: "legacy-b64", Msg: "Buffer() is deprecated"},
		{File: "src/upload.js", Line: 30, Pkg: "legacy-b64", Msg: "Buffer() is deprecated"},
		{File: "app/other.py", Line: 3, Msg: "nobody's"},
		{File: "main.go", Line: 11, Pkg: "runtime:go", Msg: "strings.Title has been deprecated since Go 1.18"},
		{File: "src/legacy.js", Line: 4, Pkg: "runtime:nodejs", Msg: "Buffer() is deprecated"},
	}}
	got, err := r.Changes(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("changes = %+v", got)
	}
	py, js, std := got[0], got[1], got[2]
	if std.ID != "toolchain/go@1.26" || std.To != "1.26" || len(std.Sites) != 1 || std.Sites[0].Text != "return strings.Title(s)" {
		t.Errorf("standard library change = %+v; a project with no Node declared gets nothing for Node's own warnings", std)
	}
	if py.ID != "pip/fixturelib@1.0.0" || py.Kind != core.Deprecation || py.To != "1.0.0" || len(py.Sites) != 1 || py.Sites[0].Chain[1] != "fetch_all" {
		t.Errorf("python change = %+v", py)
	}
	if len(py.Evidence) != 2 || py.Evidence[0].Source != "runtime" || py.Evidence[0].Text != "fetch_all() is deprecated; use iter_rows()" {
		t.Errorf("evidence = %+v", py.Evidence)
	}
	if js.ID != "npm/legacy-b64@1.2.0" || len(js.Sites) != 2 || len(js.Evidence) != 1 {
		t.Errorf("node change = %+v", js)
	}
}

func TestListen(t *testing.T) {
	bin, dir := t.TempDir(), t.TempDir()
	script := "#!/bin/sh\npwd > ran-in\necho 'main.go:5:2: io/ioutil has been deprecated since Go 1.19: As of Go 1.16, use package [io] or package [os].  (SA1019)'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "staticcheck"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "svc"), 0o755); err != nil {
		t.Fatal(err)
	}
	lookPath = func(name string) (string, error) { return filepath.Join(bin, name), nil }
	t.Cleanup(func() { lookPath = exec.LookPath })
	ls := Listeners([]core.Manifest{{Ecosystem: core.Npm, Path: "package.json"}, {Ecosystem: core.Go, Path: "svc/go.mod"}})
	if len(ls) != 1 || ls[0].Dir != "svc" {
		t.Fatalf("listeners = %+v", ls)
	}
	got := listen(context.Background(), dir, []string{"PATH=" + bin + ":/bin:/usr/bin"}, ls)
	want := []core.Warning{{File: "svc/main.go", Line: 5, Pkg: "runtime:go", Msg: "io/ioutil has been deprecated since Go 1.19: As of Go 1.16, use package [io] or package [os]."}}
	if !slices.Equal(got, want) {
		t.Errorf("warnings = %+v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "svc", "ran-in")); !strings.HasSuffix(strings.TrimSpace(string(b)), "/svc") {
		t.Errorf("staticcheck ran in %q", b)
	}
}
