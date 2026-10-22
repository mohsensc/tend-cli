package vet

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/gitx"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if body == "\x00delete" {
			os.Remove(p)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// worktree commits base, then writes patched on top and returns the dir, the
// base sha and the diff the patch stage would have produced.
func worktree(t *testing.T, base, patched map[string]string) (string, string, string) {
	t.Helper()
	empty := filepath.Join(t.TempDir(), "gitconfig")
	write(t, filepath.Dir(empty), map[string]string{"gitconfig": ""})
	t.Setenv("GIT_CONFIG_GLOBAL", empty)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	sh(t, dir, "git", "init", "-q", "-b", "main")
	write(t, dir, base)
	sh(t, dir, "git", "add", "-A")
	sh(t, dir, "git", "-c", "user.name=a", "-c", "user.email=a@b", "commit", "-q", "--allow-empty", "-m", "base")
	sha := sh(t, dir, "git", "rev-parse", "HEAD")
	write(t, dir, patched)
	sh(t, dir, "git", "add", "-A")
	diff, err := gitx.Raw(context.Background(), dir, "diff", "--no-color", "--no-renames", sha)
	if err != nil {
		t.Fatal(err)
	}
	return dir, sha, diff
}

// index is a ScanIndex over the files on disk, good enough to drive the checks:
// imports come from a table, uses are chains rooted at an import binding.
type index struct {
	dir       string
	imports   []core.Import
	endpoints []core.URLUse
}

var chainRe = regexp.MustCompile(`[A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)*`)

func (x *index) Uses(names []string) []core.Site {
	var res []core.Site
	for _, imp := range x.imports {
		if !slices.Contains(names, imp.Package) {
			continue
		}
		roots := append([]string{imp.Binding}, imp.Names...)
		x.eachLine(imp.File, func(n int, l string) {
			for _, loc := range chainRe.FindAllStringIndex(l, -1) {
				if loc[0] > 0 && l[loc[0]-1] == '.' {
					continue
				}
				chain := strings.Split(l[loc[0]:loc[1]], ".")
				if slices.Contains(roots, chain[0]) && chain[0] != "" && n != imp.Line {
					res = append(res, core.Site{File: imp.File, Line: n, Col: loc[0] + 1, Chain: chain, Call: true, Text: strings.TrimSpace(l)})
				}
			}
		})
	}
	return res
}

func (x *index) Fields(name string, files []string) []core.Site {
	re := regexp.MustCompile(`\.` + name + `\b|["']` + name + `["']|\b` + name + `\s*:`)
	var res []core.Site
	for _, f := range x.Files() {
		if files != nil && !slices.Contains(files, f) {
			continue
		}
		x.eachLine(f, func(n int, l string) {
			if code := strings.TrimSpace(l); !strings.HasPrefix(code, "#") && !strings.HasPrefix(code, "//") && re.MatchString(l) {
				res = append(res, core.Site{File: f, Line: n, Chain: []string{name}, Text: code})
			}
		})
	}
	return res
}

func (x *index) Endpoints(hosts []string) []core.URLUse { return x.endpoints }

func (x *index) URLs() []core.URLUse { return x.endpoints }

func (x *index) Imports(pkg string) []core.Import {
	var res []core.Import
	for _, imp := range x.imports {
		if imp.Package == pkg {
			res = append(res, imp)
		}
	}
	return res
}

func (x *index) Files() []string {
	var res []string
	err := filepath.WalkDir(x.dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == ".venv") {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(x.dir, p)
			res = append(res, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		panic(err)
	}
	return res
}

func (x *index) eachLine(file string, fn func(int, string)) {
	b, err := os.ReadFile(filepath.Join(x.dir, file))
	if err != nil {
		return
	}
	for i, l := range strings.Split(string(b), "\n") {
		fn(i+1, l)
	}
}

// fakeEco imports a dep by its own name, or by the names in the table.
type fakeEco struct {
	name    string
	imports map[string][]string
}

func (e fakeEco) Name() string                                   { return e.name }
func (e fakeEco) Detect(string) ([]core.Manifest, error)         { return nil, nil }
func (e fakeEco) Deps(string, core.Manifest) ([]core.Dep, error) { return nil, nil }
func (e fakeEco) Commands(string, core.Manifest) core.Commands   { return core.Commands{} }
func (e fakeEco) Meta(context.Context, core.Fetcher, string) (*core.Meta, error) {
	return nil, core.ErrNotSupported
}
func (e fakeEco) Bump(context.Context, string, core.Dep, string, string) (*core.Bump, error) {
	return nil, core.ErrNotSupported
}
func (e fakeEco) Traits() core.Traits { return core.Traits{} }

func (e fakeEco) Imports(d core.Dep) []string {
	if names, ok := e.imports[d.Name]; ok {
		return names
	}
	return []string{d.Name}
}

func ecos(names ...string) map[string]core.Ecosystem {
	res := map[string]core.Ecosystem{}
	for _, n := range names {
		res[n] = fakeEco{name: n}
	}
	return res
}

// lines renders findings one per line for comparison in tests.
func lines(found []core.Finding) []string {
	var res []string
	for _, f := range found {
		res = append(res, fmt.Sprintf("%s %s:%d %s", f.Action, f.File, f.Line, f.Msg))
	}
	return res
}

func eq(t *testing.T, got []core.Finding, want ...string) {
	t.Helper()
	if g := lines(got); !slices.Equal(g, want) {
		t.Errorf("findings:\n%s\nwant:\n%s", strings.Join(g, "\n"), strings.Join(want, "\n"))
	}
}
