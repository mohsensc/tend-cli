package patch

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/gitx"
)

var update = flag.Bool("update", false, "rewrite golden files")

// repo creates a git repository with files committed and returns its path.
func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run(t, dir, "init", "-q")
	run(t, dir, "add", "-A")
	run(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "base")
	return dir
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitx.Raw(context.Background(), dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// staged stages everything and returns the diff against the base commit.
func staged(t *testing.T, dir string) string {
	t.Helper()
	run(t, dir, "add", "-A")
	diff, _, err := Diff(t.Context(), dir, head)
	if err != nil {
		t.Fatal(err)
	}
	return diff
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
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
		t.Fatalf("%v (run with -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s mismatch\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

// fakeIndex answers Imports and Fields from fixed records. Imports matches by plain
// string prefix, looser than a real index, so callers must filter.
type fakeIndex struct {
	imports []core.Import
	fields  []core.Site
	files   []string
}

func (x *fakeIndex) Uses([]string) []core.Site        { return nil }
func (x *fakeIndex) Endpoints([]string) []core.URLUse { return nil }
func (x *fakeIndex) URLs() []core.URLUse              { return nil }
func (x *fakeIndex) Files() []string                  { return x.files }

func (x *fakeIndex) Imports(pkg string) []core.Import {
	var res []core.Import
	for _, imp := range x.imports {
		if strings.HasPrefix(imp.Package, pkg) {
			res = append(res, imp)
		}
	}
	return res
}

func (x *fakeIndex) Fields(name string, files []string) []core.Site {
	var res []core.Site
	for _, s := range x.fields {
		if s.Chain[0] == name && (files == nil || slices.Contains(files, s.File)) {
			res = append(res, s)
		}
	}
	return res
}

// useIndex makes every rescan return idx and counts the rescans.
func useIndex(t *testing.T, idx core.ScanIndex) *int {
	t.Helper()
	prev := rescan
	builds := 0
	rescan = func(string) (core.ScanIndex, error) {
		builds++
		return idx, nil
	}
	t.Cleanup(func() { rescan = prev })
	return &builds
}

// fakeEco bumps by replacing the dep's version text in its manifest and records calls.
type fakeEco struct {
	name    string
	calls   []string
	imports []string
	err     error
	note    string
	locked  []core.Dep // what Deps reads back after a bump
}

func (e *fakeEco) Name() string                                   { return e.name }
func (e *fakeEco) Detect(string) ([]core.Manifest, error)         { return nil, nil }
func (e *fakeEco) Deps(string, core.Manifest) ([]core.Dep, error) { return e.locked, nil }
func (e *fakeEco) Commands(string, core.Manifest) core.Commands   { return core.Commands{} }
func (e *fakeEco) Traits() core.Traits                            { return core.Traits{} }

func (e *fakeEco) Imports(d core.Dep) []string { return e.imports }
func (e *fakeEco) Meta(context.Context, core.Fetcher, string) (*core.Meta, error) {
	return nil, core.ErrNotSupported
}

func (e *fakeEco) Bump(ctx context.Context, root string, d core.Dep, to, toName string) (*core.Bump, error) {
	e.calls = append(e.calls, strings.TrimSpace(d.Name+" "+to+" "+toName))
	if e.err != nil {
		if _, ok := e.err.(core.ToolMissing); !ok {
			return nil, e.err
		}
	}
	p := filepath.Join(root, d.Manifest.Path)
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	name := d.Name
	if toName != "" {
		name = toName
	}
	s := strings.Replace(string(b), `"`+d.Name+`": "`+d.Req+`"`, `"`+name+`": "^`+to+`"`, 1)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		return nil, err
	}
	b2 := &core.Bump{Files: []string{d.Manifest.Path}, Locked: e.note == "" && e.err == nil, Note: e.note}
	return b2, e.err
}

func npmDep(name, version string) core.Dep {
	return core.Dep{
		Ecosystem: core.Npm,
		Name:      name,
		Version:   version,
		Req:       "^" + version,
		Manifest:  core.Manifest{Ecosystem: core.Npm, Path: "package.json", Tool: "npm"},
		Direct:    true,
	}
}
