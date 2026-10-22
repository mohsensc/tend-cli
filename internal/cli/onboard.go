package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/deliver"
	"github.com/mohsensc/tend-cli/internal/gitx"
	"github.com/mohsensc/tend-cli/internal/patch"
	"github.com/mohsensc/tend-cli/internal/plan"
	"github.com/mohsensc/tend-cli/internal/state"
	"github.com/mohsensc/tend-cli/internal/verify"
)

// due is st.Onboard while tend's first PR is coming or open.
const due = "due"

// onboard puts tend's first PR on the repo, the tidy-up, into the queue when
// this run should carry it. Only security work goes ahead of it.
func (s *session) onboard(ctx context.Context, inv *core.Inventory, p *plan.Plan, base string) error {
	ch, err := s.onboarding(ctx, inv, p, base)
	if err != nil || ch == nil {
		return err
	}
	p.Queue = append(p.Queue, *ch)
	plan.Rank(p.Queue, s.e.Now())
	return nil
}

// onboarding returns the tidy-up when it is due, nil when not, and settles
// st.Onboard once the first PR is behind the repo: it merged or was closed,
// tend.yml asks for none, there was nothing to tidy, or tend had been at
// work here before. The forge is the truth: a lost state still finds the PR.
func (s *session) onboarding(ctx context.Context, inv *core.Inventory, p *plan.Plan, base string) (*core.Change, error) {
	st := s.st
	switch {
	case st.Onboard != "" && st.Onboard != due:
		return nil, nil
	case s.cfg.Onboarding != "pr" || s.cfg.Deliver.Mode != "pr":
		if st.Onboard == "" {
			s.quiet(inv, p)
		}
		st.Onboard = s.cfg.Onboarding
		if s.cfg.Onboarding == "pr" {
			st.Onboard = "commit"
		}
		return nil, nil
	}
	if f := st.Seen[core.TidyID]; f.Verdict == "unsupported" && base != "" && f.Base == base {
		return nil, nil
	}
	var pr *core.PR
	owed := false
	if s.forge != nil {
		var err error
		if pr, err = s.forge.FindPR(ctx, deliver.TidyBranch); err != nil {
			return nil, fmt.Errorf("forge: %w", err)
		}
		if pr == nil && st.Onboard == "" {
			if owed, err = s.owed(ctx); err != nil {
				return nil, fmt.Errorf("forge: %w", err)
			}
		}
		switch {
		case owed:
		case pr == nil && st.Onboard == "":
			had, err := s.forge.HadPR(ctx, "tend/")
			if err != nil {
				return nil, fmt.Errorf("forge: %w", err)
			}
			if had {
				st.Onboard = "before"
				return nil, nil
			}
		case pr == nil || pr.State == "open":
		case pr.State == "merged":
			st.Onboard = "merged"
			return nil, nil
		default:
			back, err := deliver.Displaced(ctx, s.forge, pr)
			if err != nil {
				return nil, fmt.Errorf("forge: %w", err)
			}
			if !back {
				st.Onboard = "closed"
				return nil, nil
			}
		}
	}
	// State that only remembers the first PR itself, as a lost state does
	// once the open PR is read back from the forge, is no sign of earlier work.
	if st.Onboard == "" && pr == nil && !owed && tended(st) {
		st.Onboard = "before"
		return nil, nil
	}
	ch, err := plan.Tidy(inv, p, s.cfg, s.configured(inv.Root))
	if err != nil {
		return nil, err
	}
	if ch == nil {
		st.Onboard = "clean"
		return nil, nil
	}
	// Without a forge to ask whether tend was here before, a run that has one
	// must still ask: a tend check offline does not settle it.
	if s.forge != nil {
		st.Onboard = due
	}
	return ch, nil
}

// owed reports whether an open tend PR went ahead of the first PR, as its
// marker says: a state lost meanwhile still owes the tidy-up.
func (s *session) owed(ctx context.Context) (bool, error) {
	prs, err := s.forge.OpenPRs(ctx, "tend/")
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(prs, func(pr core.PR) bool { return deliver.TidyDue(pr.Body) }), nil
}

// untidy says why tend's first PR, open as id, is no longer wanted, or ""
// while it is: tend.yml asks for none now, or nothing is left to tidy.
func (s *session) untidy(p *plan.Plan, id string) string {
	if id != core.TidyID || slices.ContainsFunc(p.Queue, func(ch core.Change) bool { return ch.Kind == core.Tidy }) {
		return ""
	}
	switch s.st.Onboard {
	case "quiet", "off":
		return "tend.yml sets onboarding: " + s.st.Onboard + " now"
	case "commit":
		return "tend.yml delivers by commit now"
	case "clean":
		return "nothing is left to tidy up"
	}
	return ""
}

// tended reports whether state remembers tend delivering here before, which
// a commit-mode repo's forge would not.
func tended(st *state.State) bool {
	if st.Open != nil || len(st.Parked) > 0 {
		return true
	}
	for _, s := range st.Seen {
		if s.Verdict == "delivered" {
			return true
		}
	}
	return false
}

// configured reports whether the repo, or the checkout tend runs in, has a
// tend.yml already, which a first PR with nothing else to do would add.
func (s *session) configured(root string) bool {
	if s.o.config != "" || s.e.Getenv("TEND_CONFIG") != "" {
		return true
	}
	// The checkout counts too: tend init writes tend.yml there before it is
	// committed, and a PR adding another would clash with it.
	for _, dir := range []string{root, s.root} {
		for _, name := range []string{"tend.yml", filepath.Join(".github", "tend.yml")} {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				return true
			}
		}
	}
	return false
}

// quiet says once what a first PR would have removed, when tend.yml asks
// for no first PR: the rest of the tidy-up comes as PRs of its own anyway.
func (s *session) quiet(inv *core.Inventory, p *plan.Plan) {
	if s.cfg.Onboarding != "quiet" {
		return
	}
	ch, err := plan.Tidy(inv, p, s.cfg, true)
	if err != nil || ch == nil || len(ch.Cleanup.Unused) == 0 {
		return
	}
	var names []string
	for _, d := range ch.Cleanup.Unused {
		names = append(names, d.Name)
	}
	s.logf("onboarding is quiet, so there is no tidy-up PR; nothing in the project uses %s", strings.Join(names, ", "))
}

// part is one piece of a tidy-up that its proof keeps or drops: a dependency
// nothing uses, to remove, or a deprecated library to move off.
type part struct {
	n    int // its place among the parts, for naming a set of them
	gone *core.Dep
	move *core.Change
	pt   *core.Patch // the move as it was first made
	code string      // the move's edits on top of its bump, as a diff
}

// tidy carries tend's first PR: it makes each move once, then proves every
// removal and move together, keeps what passes when the whole does not, and
// vets and delivers the result like any patch.
func (r *runner) tidy(ctx context.Context, ch *core.Change, preempt *core.PR) (*outcome, error) {
	s, wt := r.s, r.wt
	r.slug = core.Slug(ch.ID)
	cmds := verify.Detect(wt.Dir, r.inv.Manifests, r.ecos, s.cfg.Verify)
	nothing := len(cmds.Install)+len(cmds.Build)+len(cmds.Test) == 0
	proof := &core.Proof{Container: s.cfg.Verify.Container, Nothing: nothing}
	if !nothing {
		proof.Base = r.baseline(ctx, cmds)
	}
	c := &core.Cleanup{Kept: slices.Clone(ch.Cleanup.Kept)}
	var parts []*part
	for i := range ch.Cleanup.Unused {
		parts = append(parts, &part{n: len(parts), gone: &ch.Cleanup.Unused[i]})
	}
	for i := range ch.Cleanup.Moves {
		m := &ch.Cleanup.Moves[i]
		x, why, err := r.first(ctx, m, cmds, proof)
		if err != nil {
			return nil, err
		}
		if why != "" {
			c.Left = append(c.Left, core.Aside{Dep: m.Deps[0], Why: why, Options: m.Options})
			continue
		}
		x.n = len(parts)
		parts = append(parts, x)
	}

	t := &trial{r: r, cmds: cmds, proof: proof, runs: map[string]*core.Run{}}
	t.check = t.try
	kept := parts
	if !nothing {
		var err error
		if kept, err = t.keep(ctx, nil, parts, nil); err != nil {
			return nil, err
		}
	}
	for _, d := range t.dropped {
		s.logf("left %s out of the tidy-up: %s", name(d.x), d.why())
		if d.x.gone != nil {
			c.Kept = append(c.Kept, core.Aside{Dep: *d.x.gone, Why: d.why()})
			continue
		}
		c.Left = append(c.Left, core.Aside{Dep: d.x.move.Deps[0], Why: d.why(), Options: d.x.move.Options})
	}
	for _, x := range kept {
		if x.gone != nil {
			c.Unused = append(c.Unused, *x.gone)
		} else {
			c.Moves = append(c.Moves, *x.move)
		}
	}
	out := *ch
	out.Cleanup = c
	plan.Refit(&out)

	pt, err := t.final(ctx, kept, !s.configured(wt.Dir))
	if err != nil {
		return nil, err
	}
	if len(pt.Files) == 0 {
		s.st.Onboard = "clean"
		return &outcome{Action: "nothing", line: "nothing to tidy up: no removal or move passed its proof, and tend.yml is there already"}, nil
	}
	vt, err := r.vet(ctx, &out, pt, proof)
	if err != nil {
		return nil, err
	}
	switch {
	case vt.Verdict == "reject":
		return r.unproven(&out, "vetting rejected the patch: "+findings(vt)), nil
	case !nothing && !plan.Proven(proof):
		return r.unproven(&out, "the patch failed its tests after vetting: "+failure(proof.Patched)), nil
	}
	return r.deliver(ctx, &out, pt, proof, vt, preempt)
}

// first makes a move once, on the base commit, the way its own PR would make
// it: the bump, the codemods, then the model. It learns whether the move can
// be made at all, and what it edits on top of the bump, to build it again
// next to the other parts without asking the model twice. why says what
// stands in the way when it cannot be made.
func (r *runner) first(ctx context.Context, m *core.Change, cmds core.Commands, proof *core.Proof) (*part, string, error) {
	defer func(slug string) { r.slug = slug }(r.slug)
	r.slug = core.Slug(m.ID)
	if err := reset(ctx, r.wt); err != nil {
		return nil, "", err
	}
	o := r.options(cmds, proof)
	o.Strict = true
	bumped := ""
	o.Bumped = func(ctx context.Context) error {
		if bumped = r.tree(ctx); bumped == "" {
			return errors.New("cannot read the worktree after the bump")
		}
		return nil
	}
	r.s.logf("moving off %s on its own first", m.Deps[0].Name)
	pt, err := patch.Apply(ctx, r.wt.Dir, m, o)
	switch {
	case err != nil && ctx.Err() != nil:
		return nil, "", ctx.Err()
	case err != nil:
		why, _, _ := strings.Cut(err.Error(), "\n")
		return nil, why, nil
	case pt.Decision:
		return nil, pt.Note, nil
	case len(pt.Files) == 0:
		return nil, "moving off it changes no files", nil
	}
	code := ""
	if after := r.tree(ctx); after != "" && after != bumped {
		if code, err = gitx.Raw(ctx, r.wt.Dir, "diff", "--binary", "--no-renames", "--no-color", "--no-ext-diff", "--no-textconv", "--src-prefix=a/", "--dst-prefix=b/", bumped, after); err != nil {
			return nil, "", err
		}
	}
	return &part{move: m, pt: pt, code: code}, "", nil
}

// trial builds sets of parts on the base commit and proves them, keeping
// each passing run by the set it proved.
type trial struct {
	r       *runner
	cmds    core.Commands
	proof   *core.Proof
	runs    map[string]*core.Run
	dropped []drop
	check   func(context.Context, []*part) (*drop, error) // try, or a test's stand-in
}

// drop is a part a tidy-up leaves out: the run that failed with it, or why
// it could not be made at all.
type drop struct {
	x     *part
	run   *core.Run
	build string
}

func (d drop) why() string {
	build := strings.TrimPrefix(d.build, "patch: ")
	switch {
	case build != "" && d.x.gone != nil:
		if m := d.x.gone.Manifest.Path; m != "" {
			if rest, ok := strings.CutPrefix(build, m+": "); ok {
				return "removing it from " + m + " fails: " + rest
			}
		}
		return "removing it fails: " + build
	case build != "":
		return "the move cannot be made next to the rest: " + build
	case d.x.gone != nil:
		return "with it removed, " + broke(d.run)
	}
	return "with the move made, " + broke(d.run)
}

func key(set []*part) string {
	var ns []string
	for _, x := range set {
		ns = append(ns, strconv.Itoa(x.n))
	}
	slices.Sort(ns)
	return strings.Join(ns, ",")
}

// keep finds the parts that pass on top of kept: all of them when they pass
// together, else what passes of each half, the second half on top of what
// the first kept. A part that fails on its own is dropped with the run that
// showed it. failed, when set, is how kept and parts already failed
// together, which is not proven twice.
func (t *trial) keep(ctx context.Context, kept, parts []*part, failed *drop) ([]*part, error) {
	if len(parts) == 0 {
		return nil, nil
	}
	if failed == nil {
		f, err := t.check(ctx, append(slices.Clone(kept), parts...))
		if err != nil || f == nil {
			return parts, err
		}
		failed = f
	}
	if len(parts) == 1 {
		failed.x = parts[0]
		t.dropped = append(t.dropped, *failed)
		return nil, nil
	}
	h := len(parts) / 2
	a, err := t.keep(ctx, kept, parts[:h], nil)
	if err != nil {
		return nil, err
	}
	var again *drop
	if len(a) == h {
		again = failed
	}
	b, err := t.keep(ctx, append(slices.Clone(kept), a...), parts[h:], again)
	if err != nil {
		return nil, err
	}
	return append(a, b...), nil
}

// try builds set and proves it. It returns how the set failed, or nil when
// it passes.
func (t *trial) try(ctx context.Context, set []*part) (*drop, error) {
	if _, err := t.build(ctx, set); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		first, _, _ := strings.Cut(err.Error(), "\n")
		return &drop{build: first}, nil
	}
	run, err := t.r.prove(ctx, t.cmds, "patched")
	if err != nil {
		return nil, err
	}
	if !plan.Proven(&core.Proof{Base: t.proof.Base, Patched: run}) {
		return &drop{run: run}, nil
	}
	t.runs[key(set)] = run
	return nil, nil
}

// build puts the worktree on the base commit plus set: each move's bump made
// again and its edits applied, then the removals, one native run for each
// manifest. Moves go first, while each manifest line is where it was read.
func (t *trial) build(ctx context.Context, set []*part) (*core.Patch, error) {
	r := t.r
	if err := reset(ctx, r.wt); err != nil {
		return nil, err
	}
	o := r.options(t.cmds, t.proof)
	if t.runs[key(set)] != nil {
		// A set already proven is built again only to stage it; its steps
		// were logged the first time.
		o.Log = nil
	}
	p := &core.Patch{}
	var gone []core.Dep
	var notes []string
	for _, x := range set {
		if x.gone != nil {
			gone = append(gone, *x.gone)
			continue
		}
		if err := patch.Redo(ctx, r.wt.Dir, x.move, x.code, o); err != nil {
			return nil, err
		}
		p.Applied = append(p.Applied, x.pt.Applied...)
		p.Model = p.Model || x.pt.Model
		p.Edited = append(p.Edited, x.pt.Edited...)
		if x.pt.Note != "" {
			notes = append(notes, x.pt.Note)
		}
	}
	if len(notes) == 1 {
		p.Note = notes[0]
	}
	if len(gone) > 0 {
		if err := patch.Remove(ctx, r.wt.Dir, gone, o, p); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// final builds the tidy-up's patch from set, adds the starter tend.yml when
// nothing else changes and the repo asks for one, and stages and diffs it.
// The proof is the run that passed this very set when there was one.
func (t *trial) final(ctx context.Context, set []*part, starter bool) (*core.Patch, error) {
	r, wt := t.r, t.r.wt
	pt, err := t.build(ctx, set)
	if err != nil {
		return nil, err
	}
	if len(set) == 0 && starter {
		if err := os.WriteFile(filepath.Join(wt.Dir, "tend.yml"), config.Starter, 0o644); err != nil {
			return nil, err
		}
		pt.Applied = append(pt.Applied, "added tend.yml with every key commented out (tend.yml)")
	}
	if _, err := gitx.Run(ctx, wt.Dir, "add", "-A"); err != nil {
		return nil, err
	}
	if pt.Diff, pt.Files, err = patch.Diff(ctx, wt.Dir, wt.Base); err != nil {
		return nil, err
	}
	slices.Sort(pt.Edited)
	pt.Edited = slices.Compact(pt.Edited)
	if t.proof.Nothing || len(pt.Files) == 0 {
		return pt, nil
	}
	if run := t.runs[key(set)]; run != nil && len(set) > 0 {
		t.proof.Patched = run
		return pt, nil
	}
	t.proof.Patched, err = r.prove(ctx, t.cmds, "patched")
	return pt, err
}

func name(x *part) string {
	if x.gone != nil {
		return x.gone.Name
	}
	return x.move.Deps[0].Name
}

// broke says how a run failed, in a few words: "`npm test` fails (exit 1)".
func broke(run *core.Run) string {
	st := run.Last()
	switch {
	case st == nil:
		return "the proof does not run"
	case st.Exit == 0:
		return "`" + st.Cmd + "` does not finish"
	}
	return fmt.Sprintf("`%s` fails (exit %d)", st.Cmd, st.Exit)
}
