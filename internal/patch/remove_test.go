package patch

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/model"
)

// pruneEco is fakeEco that can also remove: it drops the dep's line from
// its manifest, and says the lockfile tool was not there when told to.
type pruneEco struct {
	fakeEco
	removed [][]string
	missing bool
}

func (e *pruneEco) Remove(ctx context.Context, root string, deps []core.Dep) (*core.Bump, error) {
	var names []string
	p := filepath.Join(root, deps[0].Manifest.Path)
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(b), "\n")
	for _, d := range deps {
		names = append(names, d.Name)
		lines = slices.DeleteFunc(lines, func(l string) bool { return strings.Contains(l, `"`+d.Name+`"`) })
	}
	e.removed = append(e.removed, names)
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		return nil, err
	}
	if e.missing {
		return &core.Bump{Files: []string{deps[0].Manifest.Path}, Note: "npm not found"}, nil
	}
	return &core.Bump{Files: []string{deps[0].Manifest.Path, deps[0].Manifest.Lock}, Locked: true}, nil
}

// idleEco removes nothing and says so with what it returns.
type idleEco struct {
	fakeEco
	b   *core.Bump
	err error
}

func (e *idleEco) Remove(context.Context, string, []core.Dep) (*core.Bump, error) {
	return e.b, e.err
}

func TestRemove(t *testing.T) {
	dir := repo(t, map[string]string{
		"package.json":     "{\n  \"a\": \"1\",\n  \"b\": \"1\"\n}\n",
		"web/package.json": "{\n  \"c\": \"1\"\n}\n",
	})
	in := func(manifest, name string) core.Dep {
		return core.Dep{Ecosystem: core.Npm, Name: name, Direct: true, Manifest: core.Manifest{Ecosystem: core.Npm, Path: manifest, Lock: filepath.ToSlash(filepath.Join(filepath.Dir(manifest), "package-lock.json"))}}
	}
	e := &pruneEco{fakeEco: fakeEco{name: core.Npm}}
	p := &core.Patch{}
	o := Options{Ecos: map[string]core.Ecosystem{core.Npm: e}}
	if err := Remove(context.Background(), dir, []core.Dep{in("package.json", "a"), in("web/package.json", "c"), in("package.json", "b")}, o, p); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"a", "b"}, {"c"}}; !slices.EqualFunc(e.removed, want, slices.Equal) {
		t.Errorf("removed %q, want one call per manifest: %q", e.removed, want)
	}
	want := []string{"removed a, b (package-lock.json, package.json)", "removed c (web/package-lock.json, web/package.json)"}
	if !slices.Equal(p.Applied, want) {
		t.Errorf("applied %q, want %q", p.Applied, want)
	}

	e.missing = true
	p = &core.Patch{}
	if err := Remove(context.Background(), dir, []core.Dep{in("web/package.json", "c")}, o, p); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(p.Applied, "lockfile not regenerated, npm not found") {
		t.Errorf("applied %q", p.Applied)
	}

	for _, e := range []*idleEco{
		{err: core.ToolMissing{Tool: "go"}},
		{},
		{b: &core.Bump{Note: "nothing to do"}},
	} {
		o.Ecos[core.Npm] = e
		p := &core.Patch{}
		if err := Remove(context.Background(), dir, []core.Dep{in("package.json", "a")}, o, p); err == nil || len(p.Applied) > 0 {
			t.Errorf("a removal that changed nothing (%v, %v): err %v, applied %q", e.b, e.err, err, p.Applied)
		}
	}

	o.Ecos[core.Npm] = &fakeEco{name: core.Npm}
	if err := Remove(context.Background(), dir, []core.Dep{in("package.json", "a")}, o, &core.Patch{}); err == nil {
		t.Error("an ecosystem that cannot remove removed")
	}
}

// A move made once is made again on another tree from its bump and the
// edits it put on top, with no model.
func TestRedo(t *testing.T) {
	f := newFixture(t)
	var bumped string
	o := f.opts(&model.Script{Replies: []string{f.edit(t, unmigrated, migrated)}})
	o.Bumped = func(context.Context) error {
		bumped = readFile(t, f.dir, "package.json")
		return nil
	}
	pt, err := Apply(context.Background(), f.dir, f.ch, o)
	if err != nil || pt.Decision {
		t.Fatalf("apply = %+v, %v", pt, err)
	}
	if !strings.Contains(bumped, `"^2.0.0"`) || readFile(t, f.dir, "src/billing.js") == billingJS {
		t.Fatalf("Bumped saw package.json as\n%s", bumped)
	}
	code := run(t, f.dir, "diff", "--cached", "--src-prefix=a/", "--dst-prefix=b/", "--", "src/billing.js")
	run(t, f.dir, "reset", "-q", "--hard", "HEAD")
	if code == "" || readFile(t, f.dir, "src/billing.js") != billingJS {
		t.Fatal("the worktree is not back on its base")
	}

	if err := Redo(context.Background(), f.dir, f.ch, code, f.opts(nil), &core.Patch{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, f.dir, "package.json"), `"^2.0.0"`) || !strings.Contains(readFile(t, f.dir, "src/billing.js"), "paymentIntents") {
		t.Errorf("redo left\n%s", staged(t, f.dir))
	}

	run(t, f.dir, "reset", "-q", "--hard", "HEAD")
	writeFile(t, f.dir, "src/billing.js", "// rewritten since\n")
	if err := Redo(context.Background(), f.dir, f.ch, code, f.opts(nil), &core.Patch{}); err == nil || !strings.Contains(err.Error(), "no longer apply") {
		t.Errorf("redo onto a file that moved on: %v", err)
	}
}
