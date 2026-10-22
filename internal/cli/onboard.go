package cli

import (
	"cmp"
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
)

// due is st.Onboard while tend's first PR is coming or open.
const due = "due"

// onboard puts tend's first PR on the repo into the queue when this run
// should carry it. Nothing goes ahead of it: it carries the security fixes
// it can make, and the rest of the queue waits for it.
func (s *session) onboard(ctx context.Context, inv *core.Inventory, p *plan.Plan, base string) error {
	ch, err := s.onboarding(ctx, inv, p, base)
	if err != nil || ch == nil {
		return err
	}
	p.Queue = append(p.Queue, *ch)
	plan.Rank(p.Queue, s.e.Now())
	return nil
}

// onboarding returns the catch-up PR when one is due, nil when not: the
// first PR, then once it merged the stages after it. It settles st.Onboard
// once the first PR is behind the repo: it merged or was closed, tend.yml
// asks for none, there was nothing to catch up on, or tend had been at work
// here before. The forge is the truth: a lost state still finds the PR.
func (s *session) onboarding(ctx context.Context, inv *core.Inventory, p *plan.Plan, base string) (*core.Change, error) {
	st := s.st
	switch {
	case st.Onboard == "merged":
		return s.staged(p), nil
	case st.Onboard != "" && st.Onboard != due:
		return nil, nil
	case s.cfg.Onboarding != "pr":
		if st.Onboard == "" {
			s.quiet(inv, p)
		}
		st.Onboard = s.cfg.Onboarding
		return nil, nil
	}
	if f := st.Seen[core.FirstID]; f.Verdict == "unsupported" && base != "" && f.Base == base {
		return nil, nil
	}
	commit := s.cfg.Deliver.Mode == "commit"
	if commit && st.Open != nil && st.Open.PR != 0 {
		// The first PR was opened before tend.yml asked for commits.
		st.Onboard = "commit"
		return nil, nil
	}
	var pr *core.PR
	owed := false
	if s.forge != nil && !commit {
		var err error
		if pr, err = s.forge.FindPR(ctx, deliver.FirstBranch); err != nil {
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
			st.Stage = max(st.Stage, deliver.StageOf(pr.Body))
			return s.staged(p), nil
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
	// A commit is no place for a tend.yml that changes nothing.
	ch, err := plan.First(inv, p, s.cfg, commit || s.configured(inv.Root))
	if err != nil {
		return nil, err
	}
	if ch == nil {
		st.Onboard = "clean"
		return nil, nil
	}
	// Without a forge to ask whether tend was here before, a run that has one
	// must still ask: a tend check offline does not settle it.
	if commit && s.remote != "" || !commit && s.forge != nil {
		st.Onboard = due
	}
	return ch, nil
}

// staged is the catch-up PR for the next stage once the first PR has
// merged, nil when there is none: the catch-up is over, or tend had begun
// here before it had stages. A run that finds no stage left ends it.
func (s *session) staged(p *plan.Plan) *core.Change {
	st := s.st
	if st.Stage == 0 || st.Stage >= plan.Last {
		return nil
	}
	ch := plan.Next(p, st.Stage)
	if ch == nil {
		st.Stage = plan.Last
		s.logf("the catch-up is done; from here on tend works one change at a time")
	}
	return ch
}

// owed reports whether an open tend PR went ahead of the first PR, as its
// marker says: a state lost meanwhile still owes the first PR, which folds
// that PR in.
func (s *session) owed(ctx context.Context) (bool, error) {
	prs, err := s.forge.OpenPRs(ctx, "tend/")
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(prs, func(pr core.PR) bool { return deliver.FirstDue(pr.Body) }), nil
}

// unwanted says why a catch-up PR, open as id, is no longer wanted, or ""
// while it is: tend.yml asks for none now, or nothing is left to catch up on.
func (s *session) unwanted(p *plan.Plan, id string) string {
	if !core.CatchUp(id) || slices.ContainsFunc(p.Queue, func(ch core.Change) bool { return ch.Kind == core.First }) {
		return ""
	}
	switch {
	case s.st.Onboard == "quiet" || s.st.Onboard == "off":
		return "tend.yml sets onboarding: " + s.st.Onboard + " now"
	case s.st.Onboard == "commit":
		return "tend.yml delivers by commit now"
	case s.st.Onboard == "clean", core.StageOf(id) > 0 && s.st.Stage >= plan.Last:
		return "nothing is left to catch up on"
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
// for no first PR: the rest of what it carries comes as PRs of their own
// anyway.
func (s *session) quiet(inv *core.Inventory, p *plan.Plan) {
	if s.cfg.Onboarding != "quiet" {
		return
	}
	ch, err := plan.First(inv, p, s.cfg, true)
	if err != nil || ch == nil || len(ch.Batch.Unused) == 0 {
		return
	}
	var names []string
	for _, d := range ch.Batch.Unused {
		names = append(names, d.Name)
	}
	s.logf("onboarding is quiet, so there is no first PR; nothing in the project uses %s", strings.Join(names, ", "))
}

// part is one piece of a catch-up PR that its proof keeps or drops: a
// dependency nothing uses, to remove, a queued change to make, or the test
// baseline, which every set starts with and none drops.
type part struct {
	n    int // its place among the parts, for naming a set of them
	gone *core.Dep
	ch   *core.Change
	pt   *core.Patch    // the change as it was first made, nil for one that is its bump alone
	code string         // the change's edits on top of its bump, or the baseline's files, as a diff
	base *core.Baseline // set for the baseline
}

// proofMax is the least number of times a catch-up PR runs the project's
// commands to settle what it carries, the run of the whole batch included;
// one with more parts gets two runs for each. What is still unsettled when
// they run out waits for a later stage.
const proofMax = 8

// batch carries a catch-up PR: the first adds a test baseline when the
// project's tests are weak; then it makes each change that needs more than
// its bump once, on its own, proves every change and removal together,
// keeps what passes when the whole does not, and vets and delivers the
// result like any patch.
func (r *runner) batch(ctx context.Context, ch *core.Change, preempt *core.PR) (*outcome, error) {
	s, wt := r.s, r.wt
	r.slug = core.Slug(ch.ID)
	cmds := r.detect()
	nothing := len(cmds.Install)+len(cmds.Build)+len(cmds.Test) == 0
	proof := &core.Proof{Container: s.cfg.Verify.Container, Nothing: nothing}
	if !nothing {
		proof.Base = r.baseline(ctx, cmds)
	}
	runs := max(proofMax, 2*(len(ch.Batch.Changes)+len(ch.Batch.Unused)))
	t := &trial{r: r, cmds: cmds, proof: proof, runs: map[string]*core.Run{}, left: runs, budget: runs}
	t.check = t.try
	b := &core.Batch{Stage: ch.Batch.Stage, Kept: slices.Clone(ch.Batch.Kept), Next: slices.Clone(ch.Batch.Next)}
	var fixed []*part
	if ch.ID == core.FirstID {
		x, err := r.groundwork(ctx, ch, t)
		if err != nil {
			return nil, err
		}
		if x != nil {
			fixed, b.Baseline, r.floor = []*part{x}, x.base, x.code
		}
	}
	nothing, r.cmds = proof.Nothing, t.cmds
	next := plan.After(ch.Batch)
	where := "the first PR"
	if ch.ID != core.FirstID {
		where = "this catch-up PR"
	}
	// What has no later stage to go to is recorded as tried, for the
	// tracking issue, and comes back one change at a time.
	leave := func(c core.Change, why string) {
		s.logf("left %s out of %s: %s", name(&part{ch: &c}), where, why)
		if next == 0 && c.Kind != core.Security {
			s.st.Fail(c.ID, why, wt.Base, s.e.Now())
		}
		b.Next = append(b.Next, core.Later{Change: c, Why: why, Stage: next})
	}
	var parts []*part
	for i := range ch.Batch.Changes {
		c := &ch.Batch.Changes[i]
		x := &part{ch: c}
		if !patch.Bare(c) {
			var why string
			var err error
			if x, why, err = r.once(ctx, c, t); err != nil {
				return nil, err
			}
			if why != "" {
				leave(*c, why)
				continue
			}
		}
		x.n = len(parts)
		parts = append(parts, x)
	}
	for i := range ch.Batch.Unused {
		parts = append(parts, &part{n: len(parts), gone: &ch.Batch.Unused[i]})
	}

	kept := parts
	if !nothing {
		var err error
		if kept, err = t.keep(ctx, fixed, parts, nil); err != nil {
			return nil, err
		}
	}
	for _, d := range t.dropped {
		if d.x.gone != nil {
			s.logf("left %s out of %s: %s", name(d.x), where, d.why())
			b.Kept = append(b.Kept, core.Aside{Dep: *d.x.gone, Why: d.why()})
			continue
		}
		leave(*d.x.ch, d.why())
	}
	for _, x := range kept {
		if x.gone != nil {
			b.Unused = append(b.Unused, *x.gone)
		} else {
			b.Changes = append(b.Changes, *x.ch)
		}
	}
	out := *ch
	out.Batch = b
	plan.Refit(&out)

	pt, err := t.final(ctx, append(fixed, kept...), !s.configured(wt.Dir))
	if err != nil {
		return nil, err
	}
	if len(pt.Files) == 0 {
		if ch.ID == core.FirstID {
			s.st.Onboard = "clean"
		}
		return &outcome{Action: "nothing", line: "nothing to catch up on: no change or removal passed its proof, and tend.yml is there already"}, nil
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

// once makes a change on its own, on the base commit, the way its own PR
// would make it: the bump, the codemods, then the model. It learns whether
// the change can be made at all, and what it edits on top of the bump, to
// build it again next to the other parts without asking the model twice. A
// jump of more than one major is proven alone, and when it fails, or the
// change cannot be made, tend tries what it may do by itself: up a major at
// a time, then each option its rule allows, each proven. why says what
// stands in the way when none of it works.
func (r *runner) once(ctx context.Context, ch *core.Change, t *trial) (*part, string, error) {
	defer func(slug string) { r.slug = slug }(r.slug)
	r.slug = core.Slug(ch.ID)
	x, why, err := r.made(ctx, ch, t)
	if err != nil || x != nil && len(ch.Steps) == 0 {
		return x, why, err
	}
	if x != nil {
		if t.left <= 0 {
			return x, "", nil
		}
		run, err := r.prove(ctx, t.cmds, "patched")
		if err != nil {
			return nil, "", err
		}
		t.left--
		if plan.Proven(&core.Proof{Base: t.proof.Base, Patched: run}) {
			return x, "", nil
		}
		why = "on its own, " + broke(run)
	}
	if len(ch.Steps) > 0 {
		c, pt, _, stop, err := r.climb(ctx, ch, t.cmds, t.proof, &t.left)
		if err != nil {
			return nil, "", err
		}
		if c != nil {
			r.s.logf("climbed %s to %s%s", name(&part{ch: ch}), c.To, strings.TrimSuffix("; "+stop, "; "))
			y, err := r.piece(ctx, c, pt, t)
			return y, "", err
		}
		why = cmp.Or(stop, why)
	}
	c, pt, err := r.decide(ctx, ch, t.cmds, t.proof, &t.left)
	if err != nil || c == nil {
		return nil, why, err
	}
	y, err := r.piece(ctx, c, pt, t)
	return y, "", err
}

// made makes ch once, as once says, without proving it.
func (r *runner) made(ctx context.Context, ch *core.Change, t *trial) (*part, string, error) {
	if err := r.restart(ctx); err != nil {
		return nil, "", err
	}
	o := r.options(ch, t.cmds, t.proof)
	o.Strict = true
	bumped := ""
	o.Bumped = func(ctx context.Context) error {
		if bumped = r.tree(ctx); bumped == "" {
			return errors.New("cannot read the worktree after the bump")
		}
		return nil
	}
	r.s.logf("making %s on its own first", name(&part{ch: ch}))
	pt, err := patch.Apply(ctx, r.wt.Dir, ch, o)
	switch {
	case err != nil && ctx.Err() != nil:
		return nil, "", ctx.Err()
	case err != nil:
		why, _, _ := strings.Cut(err.Error(), "\n")
		return nil, why, nil
	case pt.Decision:
		return nil, pt.Note, nil
	case len(pt.Files) == 0:
		return nil, "making it changes no files", nil
	}
	code := ""
	// With nothing to bump, the edits are all there is, from the base on.
	bumped = cmp.Or(bumped, r.wt.Base)
	if after := r.tree(ctx); after != "" && after != bumped {
		if code, err = treeDiff(ctx, r.wt.Dir, bumped, after); err != nil {
			return nil, "", err
		}
	}
	return &part{ch: ch, pt: pt, code: code}, "", nil
}

// piece turns the change c the worktree holds staged, as a climb or a
// choice left it, into a part the trial can build again: the bump made
// anew on the base, and the rest as edits on top of it.
func (r *runner) piece(ctx context.Context, c *core.Change, pt *core.Patch, t *trial) (*part, error) {
	final := r.tree(ctx)
	if final == "" {
		return nil, errors.New("cannot read the worktree")
	}
	if err := r.restart(ctx); err != nil {
		return nil, err
	}
	if err := patch.Redo(ctx, r.wt.Dir, c, "", r.options(c, t.cmds, t.proof), &core.Patch{}); err != nil {
		return nil, err
	}
	bumped := r.tree(ctx)
	code := ""
	if bumped != final {
		var err error
		if code, err = treeDiff(ctx, r.wt.Dir, bumped, final); err != nil {
			return nil, err
		}
	}
	return &part{ch: c, pt: pt, code: code}, nil
}

// treeDiff is the diff between two trees, for git apply.
func treeDiff(ctx context.Context, dir, from, to string) (string, error) {
	return gitx.Raw(ctx, dir, "diff", "--binary", "--no-renames", "--no-color", "--no-ext-diff", "--no-textconv", "--src-prefix=a/", "--dst-prefix=b/", from, to)
}

// trial builds sets of parts on the base commit and proves them, keeping
// each passing run by the set it proved.
type trial struct {
	r       *runner
	cmds    core.Commands
	proof   *core.Proof
	runs    map[string]*core.Run
	dropped []drop
	left    int                                           // proofs it may still run
	budget  int                                           // proofs it could run at the start
	check   func(context.Context, []*part) (*drop, error) // try, or a test's stand-in
}

// drop is a part the first PR leaves out: the run that failed with it, why
// it could not be made, or that the proofs ran out before it was settled.
// x is set when the failure is the part's own.
type drop struct {
	x     *part
	run   *core.Run
	build string
	spent int // the proofs that ran out before it was settled, 0 when they did not
}

func (d drop) why() string {
	build := strings.TrimPrefix(d.build, "patch: ")
	switch {
	case d.spent > 0:
		return fmt.Sprintf("it was not proven: the pull request stops after %d runs of the project's commands", d.spent)
	case build != "" && d.x.gone != nil:
		if m := d.x.gone.Manifest.Path; m != "" {
			if rest, ok := strings.CutPrefix(build, m+": "); ok {
				return "removing it from " + m + " fails: " + rest
			}
		}
		return "removing it fails: " + build
	case build != "":
		return "making it next to the rest fails: " + build
	case d.x.gone != nil:
		return "with it removed, " + broke(d.run)
	}
	return "with it in the batch, " + broke(d.run)
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
// showed it, and one whose own making fails is dropped as soon as it does.
// failed, when set, is how kept and parts already failed together, which is
// not proven twice.
func (t *trial) keep(ctx context.Context, kept, parts []*part, failed *drop) ([]*part, error) {
	if len(parts) == 0 {
		return nil, nil
	}
	if failed == nil {
		f, err := t.settle(ctx, append(slices.Clone(kept), parts...))
		if err != nil || f == nil {
			return parts, err
		}
		if f.x != nil && slices.Contains(parts, f.x) {
			t.dropped = append(t.dropped, *f)
			return t.keep(ctx, kept, slices.DeleteFunc(slices.Clone(parts), func(x *part) bool { return x == f.x }), nil)
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

// settle checks set while proofs are left, and counts the proof it took. A
// set that could not be built took none.
func (t *trial) settle(ctx context.Context, set []*part) (*drop, error) {
	if t.left <= 0 {
		return &drop{spent: max(t.budget, proofMax)}, nil
	}
	f, err := t.check(ctx, set)
	if err == nil && (f == nil || f.build == "") {
		t.left--
	}
	return f, err
}

// try builds set and proves it. It returns how the set failed, or nil when
// it passes.
func (t *trial) try(ctx context.Context, set []*part) (*drop, error) {
	if _, x, err := t.build(ctx, set); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		first, _, _ := strings.Cut(err.Error(), "\n")
		return &drop{x: x, build: first}, nil
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

// build puts the worktree on the base commit plus set: each change's bump
// made again and its edits applied, then the removals, one native run for
// each manifest. Changes go first, while each manifest line is where it was
// read. When building fails, x is the part that failed when that can be
// told.
func (t *trial) build(ctx context.Context, set []*part) (pt *core.Patch, x *part, err error) {
	r := t.r
	if err := reset(ctx, r.wt); err != nil {
		return nil, nil, err
	}
	o := r.options(nil, t.cmds, t.proof)
	if t.runs[key(set)] != nil {
		// A set already proven is built again only to stage it; its steps
		// were logged the first time.
		o.Log = nil
	}
	p := &core.Patch{}
	var gone []*part
	var notes []string
	for _, x := range set {
		if x.base != nil {
			if err := patch.Put(ctx, r.wt.Dir, x.code); err != nil {
				return nil, x, err
			}
			p.Applied = append(p.Applied, x.pt.Applied...)
			p.Model = p.Model || x.pt.Model
			p.Edited = append(p.Edited, x.pt.Edited...)
			continue
		}
		if x.gone != nil {
			gone = append(gone, x)
			continue
		}
		q := &core.Patch{}
		if err := patch.Redo(ctx, r.wt.Dir, x.ch, x.code, o, q); err != nil {
			return nil, x, err
		}
		if x.pt != nil {
			q.Applied, q.Model, q.Edited, q.Note = x.pt.Applied, x.pt.Model, x.pt.Edited, x.pt.Note
		}
		p.Applied = append(p.Applied, q.Applied...)
		p.Model = p.Model || q.Model
		p.Edited = append(p.Edited, q.Edited...)
		if q.Note != "" {
			notes = append(notes, q.Note)
		}
		for k, v := range q.Landed {
			if p.Landed == nil {
				p.Landed = map[string]string{}
			}
			p.Landed[k] = v
		}
	}
	if len(notes) == 1 {
		p.Note = notes[0]
	}
	if len(gone) > 0 {
		var deps []core.Dep
		for _, x := range gone {
			deps = append(deps, *x.gone)
		}
		if err := patch.Remove(ctx, r.wt.Dir, deps, o, p); err != nil {
			if len(gone) == 1 {
				return nil, gone[0], err
			}
			return nil, nil, err
		}
	}
	return p, nil, nil
}

// final builds the first PR's patch from set, adds the starter tend.yml when
// nothing else changes and the repo asks for one, and stages and diffs it.
// The proof is the run that passed this very set when there was one.
func (t *trial) final(ctx context.Context, set []*part, starter bool) (*core.Patch, error) {
	r, wt := t.r, t.r.wt
	pt, _, err := t.build(ctx, set)
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
	if a := x.ch.API; a != nil {
		return strings.TrimSpace(a.Provider + " " + strings.ToUpper(a.Method) + " " + a.Path)
	}
	if len(x.ch.Deps) == 0 {
		return x.ch.ID
	}
	return x.ch.Deps[0].Name
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
