package patch

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/ver"
)

// bump moves every dep to `to` (or swaps it for toName) with its ecosystem's native
// tooling and returns the files that changed. A missing tool is not fatal: the
// manifest still moves and the proof shows what the lockfile could not.
func bump(ctx context.Context, wt string, deps []core.Dep, to, toName string, o Options, p *core.Patch) ([]string, error) {
	var res []string
	done := map[int]bool{}
	for _, set := range together(deps, to, toName, o) {
		d := deps[set[0]]
		var group []core.Dep
		for _, i := range set {
			group = append(group, deps[i])
			done[i] = true
		}
		b, err := o.Ecos[d.Ecosystem].(core.GroupBumper).BumpGroup(ctx, wt, group, to)
		if errors.Is(err, core.ErrNotSupported) {
			return nil, err
		}
		if err != nil {
			return nil, fmt.Errorf("patch: %s: %w", strings.Join(core.Names(&core.Change{Deps: group}), ", "), err)
		}
		for _, x := range group {
			if !b.Locked {
				break
			}
			if err := landed(o.Ecos[d.Ecosystem], wt, x, to, ""); err != nil {
				return nil, fmt.Errorf("patch: %w", err)
			}
		}
		files := uniq(b.Files)
		res = append(res, files...)
		names := core.Names(&core.Change{Deps: group})
		o.logf("bumped %s in %s", strings.Join(names, ", "), strings.Join(files, ", "))
		p.Applied = append(p.Applied, fmt.Sprintf("bumped %s %s -> %s together (%s)", strings.Join(names, ", "), from(d), to, strings.Join(files, ", ")))
		if note := "lockfile not regenerated, " + b.Note; !b.Locked && b.Note != "" && !slices.Contains(p.Applied, note) {
			p.Applied = append(p.Applied, note)
		}
	}
	for i, d := range deps {
		// A change that keeps the version, such as moving off calls the
		// installed version warns about, bumps nothing.
		if done[i] || toName == "" && to == from(d) {
			continue
		}
		e := o.Ecos[d.Ecosystem]
		if e == nil {
			return nil, fmt.Errorf("patch: %s: no %s ecosystem", d.Name, d.Ecosystem)
		}
		b, err := e.Bump(ctx, wt, d, to, toName)
		var tm core.ToolMissing
		switch {
		case errors.Is(err, core.ErrNotSupported):
			return nil, err
		case err != nil && !errors.As(err, &tm):
			return nil, fmt.Errorf("patch: %s: %w", d.Name, err)
		}
		if b == nil {
			b = &core.Bump{}
		}
		if err != nil && b.Note == "" {
			b.Note = err.Error()
		}
		if b.Locked {
			if err := landed(e, wt, d, to, toName); err != nil {
				return nil, fmt.Errorf("patch: %w", err)
			}
		}
		files := uniq(b.Files)
		res = append(res, files...)
		o.logf("bumped %s in %s", d.Name, strings.Join(files, ", "))

		line := fmt.Sprintf("bumped %s %s -> %s", d.Name, from(d), to)
		if toName != "" {
			line = fmt.Sprintf("swapped %s for %s", d.Name, toName)
		}
		if len(files) > 0 {
			line += " (" + strings.Join(files, ", ") + ")"
		}
		p.Applied = append(p.Applied, line)
		if note := "lockfile not regenerated, " + b.Note; !b.Locked && b.Note != "" && !slices.Contains(p.Applied, note) {
			p.Applied = append(p.Applied, note)
		}
	}
	return uniq(res), nil
}

// together finds the deps one lock pass must move: several direct packages
// of one manifest going to the same version, in an ecosystem that can bump
// them at once. Each set is indexes into deps.
func together(deps []core.Dep, to, toName string, o Options) [][]int {
	if toName != "" {
		return nil
	}
	at := map[string][]int{}
	var keys []string
	for i, d := range deps {
		if _, ok := o.Ecos[d.Ecosystem].(core.GroupBumper); !ok || !d.Direct || to == from(d) {
			continue
		}
		k := d.Ecosystem + " " + d.Manifest.Path
		if at[k] == nil {
			keys = append(keys, k)
		}
		at[k] = append(at[k], i)
	}
	var res [][]int
	for _, k := range keys {
		set := at[k]
		names := map[string]bool{}
		for _, i := range set {
			names[deps[i].Name] = true
		}
		if len(names) > 1 {
			res = append(res, set)
		}
	}
	return res
}

func from(d core.Dep) string {
	if d.Version != "" {
		return d.Version
	}
	return d.Req
}

// landed checks that the lockfile took the move: a direct dep sits at
// exactly to, the release tend chose and proves; a transitive one has no
// copy left at the version it moved from.
func landed(e core.Ecosystem, wt string, d core.Dep, to, toName string) error {
	deps, err := e.Deps(wt, d.Manifest)
	if err != nil {
		return err
	}
	name := cmp.Or(toName, d.Name)
	for _, x := range deps {
		if x.Name != name || x.Version == "" {
			continue
		}
		switch {
		case !d.Direct && same(x.Version, d.Version) && !same(d.Version, to):
			return fmt.Errorf("%s is still at %s after the bump to %s", name, x.Version, to)
		case d.Direct && x.Direct && !same(x.Version, to):
			return fmt.Errorf("%s resolved to %s, not %s", name, x.Version, to)
		}
	}
	return nil
}

// same compares versions by value, so 2.31 and 2.31.0 match.
func same(a, b string) bool {
	va, okA := ver.Parse(a)
	vb, okB := ver.Parse(b)
	if okA && okB {
		return ver.Compare(va, vb) == 0
	}
	return a == b
}
