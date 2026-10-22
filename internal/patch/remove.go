package patch

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/gitx"
)

// Remove takes deps, which nothing in the project uses, out of their
// manifests with each ecosystem's native tool, one manifest at a time, and
// says what it did in p.
func Remove(ctx context.Context, wt string, deps []core.Dep, o Options, p *core.Patch) error {
	var order []string
	by := map[string][]core.Dep{}
	for _, d := range deps {
		k := d.Ecosystem + " " + d.Manifest.Path
		if by[k] == nil {
			order = append(order, k)
		}
		by[k] = append(by[k], d)
	}
	for _, k := range order {
		group := by[k]
		r, ok := o.Ecos[group[0].Ecosystem].(core.Remover)
		if !ok {
			return fmt.Errorf("patch: %s cannot remove dependencies", group[0].Ecosystem)
		}
		b, err := r.Remove(ctx, wt, group)
		var tm core.ToolMissing
		if err != nil && !errors.As(err, &tm) {
			return fmt.Errorf("patch: %s: %w", group[0].Manifest.Path, err)
		}
		if b == nil {
			b = &core.Bump{Note: err.Error()}
		}
		var names []string
		for _, d := range group {
			names = append(names, d.Name)
		}
		files := uniq(b.Files)
		o.logf("removed %s from %s", strings.Join(names, ", "), group[0].Manifest.Path)
		line := "removed " + strings.Join(names, ", ")
		if len(files) > 0 {
			line += " (" + strings.Join(files, ", ") + ")"
		}
		p.Applied = append(p.Applied, line)
		if note := "lockfile not regenerated, " + b.Note; !b.Locked && b.Note != "" && !slices.Contains(p.Applied, note) {
			p.Applied = append(p.Applied, note)
		}
	}
	return nil
}

// Redo makes ch on wt again: the bump through the native tool, then code,
// the edits its first making put on top of the bump, as a diff, and says
// what it did in p. It is how tend's first PR builds a change next to the
// others without asking a model a second time.
func Redo(ctx context.Context, wt string, ch *core.Change, code string, o Options, p *core.Patch) error {
	opt, err := choose(ch, o.Option)
	if err != nil {
		return err
	}
	to, toName := ch.To, ch.ToName
	if opt != nil {
		to, toName = cmp.Or(opt.To, to), cmp.Or(opt.ToName, toName)
	}
	if _, err := bump(ctx, wt, ch.Deps, to, toName, o, p); err != nil {
		return err
	}
	if code == "" {
		return nil
	}
	if _, err := (gitx.Cmd{Dir: wt, Stdin: code}).Run(ctx, "apply", "--whitespace=nowarn", "-"); err != nil {
		return fmt.Errorf("patch: the edits to the code no longer apply: %w", err)
	}
	return nil
}
