package eco

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/mohsensc/tend-cli/internal/core"
)

// needs: a module no .go file imports may still be named by a tool
// directive, a go:generate line or a script that runs it, or be required by
// other modules, which go mod tidy keeps it for at a version they pick.
func (gomod) needs(u *usage, d core.Dep) (need, doubt string) {
	if at := u.mention(d.Name); at != "" {
		return "named in " + at, ""
	}
	if at := u.configured(goTool(d.Name)); at != "" {
		return "configured in " + at, ""
	}
	if by := u.goGraph(d.Manifest.Path)[d.Name]; len(by) > 0 {
		return "required by " + strings.Join(by, ", "), ""
	}
	return "", ""
}

// goGraph maps each module in the build graph of the go.mod at rel to the
// modules that require it, the main module left out. It is empty when the
// go command cannot say.
func (u *usage) goGraph(rel string) map[string][]string {
	if g, ok := u.graphs[rel]; ok {
		return g
	}
	g := map[string][]string{}
	if u.graphs == nil {
		u.graphs = map[string]map[string][]string{}
	}
	u.graphs[rel] = g
	if !have("go") {
		return g
	}
	out, err := run(context.Background(), filepath.Join(u.root, filepath.FromSlash(path.Dir(rel))), goReadEnv(), "go mod graph")
	if err != nil {
		return g
	}
	for _, line := range strings.Split(out, "\n") {
		from, to, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || !strings.Contains(from, "@") {
			continue
		}
		mod, _, _ := strings.Cut(to, "@")
		if by, _, _ := strings.Cut(from, "@"); !slices.Contains(g[mod], by) {
			g[mod] = append(g[mod], by)
		}
	}
	return g
}

// goReadEnv is goEnv for a command that must not write go.mod or go.sum.
func goReadEnv() []string {
	var flags []string
	for _, f := range strings.Fields(os.Getenv("GOFLAGS")) {
		if !strings.HasPrefix(f, "-mod=") {
			flags = append(flags, f)
		}
	}
	return []string{"GOFLAGS=" + strings.Join(append(flags, "-mod=readonly"), " "), "GOWORK=off"}
}

// goTool is the last element of a module path that names the module rather
// than its major version: golangci-lint for github.com/golangci/golangci-lint/v2.
func goTool(mod string) string {
	prefix, _, ok := module.SplitPathVersion(mod)
	if !ok {
		prefix = mod
	}
	return path.Base(prefix)
}

// Remove drops the requirements with go mod edit and lets go mod tidy settle
// go.mod and go.sum. A module other modules still need stays, as indirect,
// at the version it was required at.
func (gomod) Remove(ctx context.Context, root string, deps []core.Dep) (*core.Bump, error) {
	if !have("go") {
		return nil, core.ToolMissing{Tool: "go"}
	}
	m := deps[0].Manifest
	dir := path.Dir(m.Path)
	snap := snapFiles(root, m.Path, path.Join(dir, "go.sum"))
	do := func(lines ...string) error {
		for _, line := range lines {
			if err := runIn(ctx, root, dir, goEnv(), line); err != nil {
				return errors.Join(err, snap.restore(root))
			}
		}
		return nil
	}
	edit := "go mod edit"
	for _, d := range deps {
		edit += " -droprequire=" + shArg(d.Name)
	}
	if err := do(edit, "go mod tidy"); err != nil {
		return nil, err
	}
	floors, err := goFloors(root, m.Path, deps)
	if err != nil {
		return nil, errors.Join(err, snap.restore(root))
	}
	if floors != "" {
		if err := do(floors, "go mod tidy"); err != nil {
			return nil, err
		}
	}
	vendored := onDisk(root, path.Join(dir, "vendor/modules.txt"))
	if vendored {
		if err := do("go mod vendor"); err != nil {
			return nil, err
		}
	}
	files := snap.changed(root)
	if vendored {
		files = append(files, path.Join(dir, "vendor"))
	}
	slices.Sort(files)
	return &core.Bump{Files: files, Locked: true}, nil
}

// goFloors is the go mod edit that puts back the versions of dropped modules
// go mod tidy brought back lower, because other modules still require them:
// a line that held one above what they ask for, often for a security fix,
// keeps holding it, marked indirect. It is "" when there are none. A module
// tidy brought back as a direct requirement is imported by code the scan
// did not read, and is an error: tidy would have moved it to its latest
// version under a removal's name.
func goFloors(root, rel string, deps []core.Dep) (string, error) {
	f, err := goParse(root, rel)
	if err != nil {
		return "", err
	}
	edit := ""
	for _, r := range f.Require {
		for _, d := range deps {
			switch {
			case r.Mod.Path != d.Name:
			case !r.Indirect:
				return "", fmt.Errorf("go mod tidy keeps %s: a package in the module imports it", d.Name)
			case semver.Compare(r.Mod.Version, d.Version) < 0:
				edit += " -require=" + shArg(d.Name+"@"+d.Version)
			}
		}
	}
	if edit == "" {
		return "", nil
	}
	return "go mod edit" + edit, nil
}
