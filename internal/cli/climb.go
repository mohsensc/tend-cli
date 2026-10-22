package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/deliver"
	"github.com/mohsensc/tend-cli/internal/gitx"
	"github.com/mohsensc/tend-cli/internal/patch"
	"github.com/mohsensc/tend-cli/internal/plan"
	"github.com/mohsensc/tend-cli/internal/scan"
	"github.com/mohsensc/tend-cli/internal/ver"
	"github.com/mohsensc/tend-cli/internal/verify"
)

// shareMax caps how many times its configured budget a model gets for one
// piece of work.
const shareMax = 5

// shares is the budget a change asks for: one share, and one more for each
// Spread call sites it moves.
func shares(ch *core.Change) int {
	if ch == nil {
		return 1
	}
	return min(1+len(core.Calls(ch))/plan.Spread, shareMax)
}

// scale multiplies a model's turns and tokens by n; no limit stays none.
func scale(o *patch.Options, n int) {
	o.Turns *= n
	o.Tokens *= n
}

// decide makes ch by each option plan.Ranked lets tend take on its own, in
// that order, and keeps the first its proof passes: the change with the
// option it took and why, and its patch. Each option before it that failed
// is named in the why. It returns nil when none passed, with the worktree
// back on the base.
func (r *runner) decide(ctx context.Context, ch *core.Change, cmds core.Commands, proof *core.Proof, left *int) (*core.Change, *core.Patch, error) {
	var failed []string
	for _, t := range plan.Ranked(ch, r.inv.Deps) {
		if left != nil && *left <= 0 {
			break
		}
		if err := r.restart(ctx); err != nil {
			return nil, nil, err
		}
		c := *ch
		c.Choice = &core.Choice{N: t.N, Why: t.Why}
		r.s.logf("trying option %d of %s on its own: %s", t.N, name(&part{ch: ch}), c.Options[t.N-1].Title)
		pt, err := r.attempt(ctx, &c, cmds, proof)
		if left != nil && err == nil && !pt.Decision && !proof.Nothing {
			*left--
		}
		switch {
		case err != nil && ctx.Err() != nil:
			return nil, nil, ctx.Err()
		case err != nil:
			first, _, _ := strings.Cut(err.Error(), "\n")
			failed = append(failed, fmt.Sprintf("option %d could not be made (%s)", t.N, first))
		case pt.Decision:
			failed = append(failed, fmt.Sprintf("option %d could not be made (%s)", t.N, deliver.Clause(pt.Note)))
		case len(pt.Files) == 0:
			failed = append(failed, fmt.Sprintf("option %d changes no files", t.N))
		case !proof.Nothing && !plan.Proven(proof):
			failed = append(failed, fmt.Sprintf("with option %d, %s", t.N, broke(proof.Patched)))
		default:
			if len(failed) > 0 {
				c.Choice.Why = "the first its proof passed, " + t.Why + "; " + strings.Join(failed, ", ")
			}
			return &c, pt, nil
		}
	}
	return nil, nil, r.restart(ctx)
}

// climb moves ch up its steps a major at a time, for when the jump to its
// target fails: each step is bumped on top of the last, migrated, proven,
// and marked with a commit in the worktree, so the next step reads its
// call sites from it. It stops at the first step that fails and returns
// the change as far as it got, with the steps it climbed through in Via,
// its patch staged against the base and the run that proved it, or a nil
// change when not even the first step passed. why says where it stopped.
func (r *runner) climb(ctx context.Context, ch *core.Change, cmds core.Commands, proof *core.Proof, left *int) (*core.Change, *core.Patch, *core.Run, string, error) {
	wt := r.wt
	if err := r.restart(ctx); err != nil {
		return nil, nil, nil, "", err
	}
	var done []string
	var run *core.Run
	res := &core.Patch{}
	why := ""
	deps, sites, idx := ch.Deps, ch.Sites, r.inv.Index
	for _, to := range append(slices.Clone(ch.Steps), ch.To) {
		if left != nil && *left <= 0 {
			why = "the proofs ran out"
			break
		}
		c := rung(ch, deps, sites, to)
		r.s.logf("climbing %s to %s", name(&part{ch: ch}), to)
		o := r.options(&c, cmds, proof)
		o.Index = idx
		pt, err := patch.Apply(ctx, wt.Dir, &c, o)
		switch {
		case err != nil && ctx.Err() != nil:
			return nil, nil, nil, "", ctx.Err()
		case err != nil:
			why, _, _ = strings.Cut(err.Error(), "\n")
		case pt.Decision:
			why = pt.Note
		case len(pt.Files) == 0:
			why = "the step changes no files"
		}
		if why != "" {
			why = to + ": " + deliver.Clause(why)
			break
		}
		rr, err := r.prove(ctx, cmds, "patched")
		if err != nil {
			return nil, nil, nil, "", err
		}
		if left != nil {
			*left--
		}
		if !plan.Proven(&core.Proof{Base: proof.Base, Patched: rr}) {
			why = "with " + to + ", " + broke(rr)
			break
		}
		if _, err := wt.Commit(ctx, "climb to "+to, verify.Author{Name: "tend", Email: "tend@localhost"}, true); err != nil {
			return nil, nil, nil, "", err
		}
		done, run = append(done, to), rr
		res.Applied = append(res.Applied, pt.Applied...)
		res.Model = res.Model || pt.Model
		res.Edited = append(res.Edited, pt.Edited...)
		res.Note = pt.Note
		if deps, err = r.reread(deps); err != nil {
			return nil, nil, nil, "", err
		}
		if x, err := scan.Build(scan.Options{Root: wt.Dir}); err == nil {
			idx = x
			sites = r.uses(x, deps)
		}
	}
	// What failed goes; what climbed stays staged, on the base.
	for _, args := range [][]string{{"reset", "--quiet", "--hard", "HEAD"}, {"clean", "--quiet", "--force", "-d"}, {"reset", "--quiet", "--soft", wt.Base}} {
		if _, err := gitx.Run(ctx, wt.Dir, args...); err != nil {
			return nil, nil, nil, "", err
		}
	}
	if len(done) == 0 {
		return nil, nil, nil, why, r.restart(ctx)
	}
	var err error
	if res.Diff, res.Files, err = patch.Diff(ctx, wt.Dir, wt.Base); err != nil {
		return nil, nil, nil, "", err
	}
	slices.Sort(res.Edited)
	res.Edited = slices.Compact(res.Edited)
	c := *ch
	c.To, c.Via = done[len(done)-1], done[:len(done)-1]
	c.Level = ver.Step(plan.Lowest(ch.Deps), c.To)
	return &c, res, run, why, nil
}

// rung is ch as one step of a climb: from deps, as the last step left them,
// to to, with the call sites as they read now and only the release notes of
// the releases the step crosses.
func rung(ch *core.Change, deps []core.Dep, sites []core.Site, to string) core.Change {
	c := *ch
	from := plan.Lowest(deps)
	c.Deps, c.Sites, c.To, c.Steps = deps, sites, to, nil
	c.Level = ver.Step(from, to)
	c.Evidence = nil
	for _, ev := range ch.Evidence {
		if v, ok := noted(ev); !ok || ver.Less(from, v) && !ver.Less(to, v) {
			c.Evidence = append(c.Evidence, ev)
		}
	}
	return c
}

// noted is the release a piece of release notes is about, from its title:
// "v16.0.0", "stripe@16.0.0 Big release".
func noted(ev core.Evidence) (string, bool) {
	if ev.Source != "releases" && ev.Source != "changelog" {
		return "", false
	}
	f := strings.Fields(ev.Title)
	if len(f) == 0 {
		return "", false
	}
	v := f[0][strings.LastIndexByte(f[0], '@')+1:]
	_, ok := ver.Parse(v)
	return v, ok
}

// reread reads deps again from their manifests in the worktree, as a bump
// left them.
func (r *runner) reread(deps []core.Dep) ([]core.Dep, error) {
	var res []core.Dep
	for _, d := range deps {
		e := r.ecos[d.Ecosystem]
		if e == nil {
			res = append(res, d)
			continue
		}
		all, err := e.Deps(r.wt.Dir, d.Manifest)
		if err != nil {
			return nil, err
		}
		i := slices.IndexFunc(all, func(x core.Dep) bool { return x.Name == d.Name && x.Direct == d.Direct })
		if i < 0 {
			res = append(res, d)
			continue
		}
		res = append(res, all[i])
	}
	return res, nil
}

// uses are the call sites of deps in idx.
func (r *runner) uses(idx core.ScanIndex, deps []core.Dep) []core.Site {
	var res []core.Site
	for _, d := range deps {
		if e := r.ecos[d.Ecosystem]; e != nil {
			if imps := e.Imports(d); len(imps) > 0 {
				res = append(res, idx.Uses(imps)...)
			}
		}
	}
	return res
}
