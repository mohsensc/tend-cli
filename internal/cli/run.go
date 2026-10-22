package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/api"
	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/deliver"
	"github.com/mohsensc/tend-cli/internal/eco"
	"github.com/mohsensc/tend-cli/internal/gitx"
	"github.com/mohsensc/tend-cli/internal/model"
	"github.com/mohsensc/tend-cli/internal/patch"
	"github.com/mohsensc/tend-cli/internal/plan"
	"github.com/mohsensc/tend-cli/internal/scan"
	"github.com/mohsensc/tend-cli/internal/selfupdate"
	"github.com/mohsensc/tend-cli/internal/state"
	"github.com/mohsensc/tend-cli/internal/ver"
	"github.com/mohsensc/tend-cli/internal/verify"
	"github.com/mohsensc/tend-cli/internal/vet"
)

// outcome is what one run did, for the final line and for --json.
type outcome struct {
	Action   string        `json:"action"` // opened|updated|committed|branch|dry-run|waiting|nothing|up-to-date|unproven|moved
	Change   *core.Change  `json:"change"`
	PR       *core.PR      `json:"pr"`
	Branch   string        `json:"branch"`
	Commit   string        `json:"commit"`
	Worktree string        `json:"worktree"`
	Title    string        `json:"title"`
	Body     string        `json:"body"`
	Proof    *core.Proof   `json:"proof"`
	Vetting  *core.Vetting `json:"vetting"`
	Notes    []string      `json:"notes"`

	line string
	code int
}

func run(ctx context.Context, e Env, o opts) int {
	s, err := open(ctx, e, o)
	if err != nil {
		return fail(e, err)
	}
	defer s.close()
	res, err := s.pass(ctx)
	if err != nil {
		return fail(e, err)
	}
	return s.report(res)
}

func (s *session) report(res *outcome) int {
	if s.o.json {
		if err := writeJSON(s.e.Stdout, res); err != nil {
			return fail(s.e, err)
		}
		return res.code
	}
	if !s.o.quiet && res.Body != "" && (res.Action == "dry-run" || res.Action == "branch") {
		fmt.Fprintf(s.e.Stdout, "%s\n\n%s\n", res.Title, strings.TrimRight(res.Body, "\n"))
	}
	fmt.Fprintf(s.e.Stdout, "tend: %s\n", res.line)
	return res.code
}

// pass is one run: plan, pick, patch, prove, vet, deliver.
func (s *session) pass(ctx context.Context) (*outcome, error) {
	if !s.git {
		return nil, fmt.Errorf("%s is not a git repository", s.root)
	}
	s.notice(ctx)
	if s.forge == nil && s.cfg.Deliver.Mode == "pr" && !s.o.dryRun {
		s.logf("%s, so this run ends at a local branch", s.why)
	}
	if s.cfg.Deliver.AutoMerge != "none" && s.e.Getenv("TEND_DEFAULT_TOKEN") == "true" {
		s.logf("deliver.automerge is on, but a PR opened with the workflow's own token starts no checks, so it never merges; pass a fine-grained or GitHub App token as the token input")
	}
	pr, err := s.reconcile(ctx)
	if err != nil {
		return nil, err
	}
	s.settleCommit(ctx)
	baseRef, err := s.baseName(ctx)
	if err != nil {
		return nil, fmt.Errorf("forge: %w", err)
	}
	rev := s.baseRev(ctx, baseRef, true)
	// Commit mode builds on the branch it pushes to, so what lands is a
	// fast-forward to the very tree that was proven.
	if c := s.cfg.Deliver; c.Mode == "commit" && c.Branch != "" && c.Branch != baseRef {
		if r := s.baseRev(ctx, c.Branch, true); r != "HEAD" {
			rev = r
		}
	}
	sha, err := gitx.Run(ctx, s.root, "rev-parse", "--verify", rev+"^{commit}")
	if err != nil {
		if _, herr := gitx.Run(ctx, s.root, "rev-parse", "--verify", "--quiet", "HEAD"); herr != nil {
			return &outcome{Action: "nothing", line: "nothing to watch: the repository has no commits"}, nil
		}
		return nil, err
	}
	wt, err := verify.Checkout(ctx, s.root, filepath.Join(s.dir, "wt", dirName(sha)), sha)
	if err != nil {
		return nil, fmt.Errorf("cannot create the worktree: %w", err)
	}
	if !s.o.keep && !s.o.dryRun {
		defer func() {
			if err := wt.Remove(context.WithoutCancel(ctx)); err != nil {
				s.logf("cannot remove the worktree %s: %v", wt.Dir, err)
			}
		}()
	}

	r := &runner{s: s, wt: wt, baseRef: baseRef, ecos: map[string]core.Ecosystem{}}
	for _, e := range eco.For(s.cfg) {
		r.ecos[e.Name()] = e
	}
	// The base commit's run comes first: what the runtimes say is deprecated
	// while the project's own commands run is one more thing to plan from.
	p, inv, err := s.plan(ctx, wt.Dir, wt.Base, func(inv *core.Inventory) []core.Warning {
		cmds := verify.Detect(wt.Dir, inv.Manifests, r.ecos, s.cfg.Verify)
		if len(cmds.Install)+len(cmds.Build)+len(cmds.Test) == 0 {
			return nil
		}
		r.listen = verify.Listeners(inv.Manifests)
		if run := r.baseline(ctx, cmds); run != nil {
			return run.Warnings
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	inv.Base = wt.Base
	p.Open = pr
	for _, n := range p.Notes {
		s.logf("%s", n)
	}
	if len(inv.Manifests) == 0 && len(inv.Providers) == 0 && len(p.Untracked) == 0 {
		s.save()
		return &outcome{Action: "nothing", line: "nothing to watch: no manifests or API calls found in " + s.root}, nil
	}
	if err := s.onboard(ctx, inv, p, wt.Base); err != nil {
		return nil, err
	}
	r.inv, r.p = inv, p
	res, err := r.work(ctx)
	if err != nil {
		return nil, err
	}
	res.Notes = append(res.Notes, p.Notes...)
	// A dry run does not move the API baseline forward.
	if !s.o.dryRun {
		s.snapshot(inv)
	}
	s.save()
	return res, nil
}

// runner carries one pass from the pick to the delivery.
type runner struct {
	s        *session
	wt       *verify.Worktree
	inv      *core.Inventory
	p        *plan.Plan
	baseRef  string
	ecos     map[string]core.Ecosystem
	listen   []verify.Listener // read for deprecations after each proof
	model    core.Model
	pick     int       // the option the maintainer picked, 0 for none
	cmdAt    time.Time // when the maintainer's command this run honors was made
	comments []core.Comment
	slug     string // the change being worked on, for its logs
}

// option takes up `tend apply N`. An option the PR does not have gets one
// reply, and the command is not honored.
func (r *runner) option(ctx context.Context, ch *core.Change, cmd plan.Command) bool {
	if cmd.N <= len(ch.Options) {
		r.pick = cmd.N
		return true
	}
	s, open := r.s, r.p.Open
	msg := fmt.Sprintf("Option %d does not exist; this PR has none to pick from.", cmd.N)
	if n := len(ch.Options); n > 0 {
		msg = fmt.Sprintf("Option %d does not exist; pick 1 to %d.", cmd.N, n)
	}
	if s.o.dryRun || slices.ContainsFunc(r.comments, func(c core.Comment) bool { return c.Body == msg && c.At.After(cmd.At) }) {
		return false
	}
	if err := s.forge.Comment(ctx, open.Number, msg); err != nil {
		s.logf("cannot reply on #%d: %v", open.Number, err)
	}
	return false
}

func (r *runner) work(ctx context.Context) (*outcome, error) {
	s, p := r.s, r.p
	_, preempt, wait := plan.Pick(p, s.st)
	var cmd plan.Command
	hasCmd := false
	if p.Open != nil && preempt == nil {
		cmd, hasCmd = r.command(ctx, p.Open)
	}
	id := plan.OpenID(p.Open, s.st)
	switch {
	case hasCmd && cmd.Verb == "skip" && !s.o.dryRun:
		if err := s.skip(ctx, p.Open, id, cmd); err != nil {
			return nil, err
		}
		p.Queue = slices.DeleteFunc(p.Queue, func(ch core.Change) bool { return ch.ID == id })
		p.Open, hasCmd = nil, false
	case hasCmd && cmd.Verb == "retry" && wait != nil && r.fresh(cmd):
		r.requeue(id)
	case wait != nil && !s.o.dryRun:
		if why := cmp.Or(plan.Stale(p, r.inv, id), s.untidy(p, id)); why != "" {
			if err := deliver.Close(ctx, s.forge, wait.Number, "Closing: "+why+"."); err != nil {
				return nil, err
			}
			s.logf("closed #%d: %s", wait.Number, why)
			s.st.Open, p.Open = nil, nil
		}
	}
	pick, preempt, wait := plan.Pick(p, s.st)
	pick, preempt, wait, err := r.declined(ctx, pick, preempt, wait)
	if err != nil {
		return nil, err
	}
	if wait != nil {
		line := fmt.Sprintf("waiting on PR #%d (%s) to be merged or closed", wait.Number, wait.Title)
		if n := len(p.Queue); n > 0 {
			line += fmt.Sprintf("; %d queued behind it", n)
		}
		return &outcome{Action: "waiting", PR: wait, line: line}, nil
	}
	if pick == nil {
		return &outcome{Action: "nothing", line: "up to date"}, nil
	}
	if p.Open != nil && preempt == nil && deliver.Foreign(r.comments) {
		line := fmt.Sprintf("waiting on PR #%d (%s), whose branch has commits tend did not push", p.Open.Number, p.Open.Title)
		return &outcome{Action: "waiting", PR: p.Open, line: line}, nil
	}
	if hasCmd && cmd.Verb == "apply" && !r.option(ctx, pick, cmd) {
		hasCmd = false
	}
	if hasCmd {
		r.cmdAt = cmd.At
	}
	if r.upToDate(pick, preempt, cmd, hasCmd) {
		return &outcome{Action: "up-to-date", Change: pick, PR: p.Open, line: fmt.Sprintf("waiting on PR #%d (%s), which is up to date", p.Open.Number, p.Open.Title)}, nil
	}
	s.logf("working on %s (%s)", strings.TrimSpace(subject(pick)+" "+target(pick)), pick.Kind)

	r.model = s.llm()
	if r.model == nil {
		s.logf("no model is configured, so this run is mechanical only: version bumps and codemods")
	}
	res, err := r.change(ctx, pick, preempt)
	if err == nil {
		r.consume()
	}
	return res, err
}

// fresh is true for a command no earlier run took up.
func (r *runner) fresh(cmd plan.Command) bool {
	o, open := r.s.st.Open, r.p.Open
	return o == nil || open == nil || o.PR != open.Number || cmd.At.After(o.At)
}

// consume marks the command this run honored as done, whatever came of it:
// a retry that fails again is not tried on every run after, each time with
// the patch, the proof and every model call.
func (r *runner) consume() {
	o, open := r.s.st.Open, r.p.Open
	if r.cmdAt.IsZero() || r.s.o.dryRun || o == nil || open == nil || o.PR != open.Number {
		return
	}
	if r.cmdAt.After(o.At) {
		o.At = r.cmdAt
	}
}

// declined passes over every change whose PR a maintainer already closed
// unmerged. The forge remembers that when state does not.
func (r *runner) declined(ctx context.Context, pick *core.Change, preempt, wait *core.PR) (*core.Change, *core.PR, *core.PR, error) {
	s, p := r.s, r.p
	for pick != nil && (p.Open == nil || preempt != nil) && s.forge != nil && s.cfg.Deliver.Mode == "pr" {
		pr, err := s.forge.FindPR(ctx, deliver.Branch(pick))
		if err != nil {
			return nil, nil, nil, fmt.Errorf("forge: %w", err)
		}
		no, err := deliver.Declined(ctx, s.forge, pr)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("forge: %w", err)
		}
		if !no {
			break
		}
		s.logf("#%d for %s was closed without merging, so tend leaves it", pr.Number, pick.ID)
		s.st.Parked[pick.ID] = state.Parked{PR: pr.Number, Reason: "rejected", To: pick.To, At: s.e.Now()}
		s.st.See(pick.ID, "parked", fmt.Sprintf("#%d was closed without merging", pr.Number), s.e.Now())
		gone := pick.ID
		p.Queue = slices.DeleteFunc(p.Queue, func(ch core.Change) bool { return ch.ID == gone })
		pick, preempt, wait = plan.Pick(p, s.st)
	}
	return pick, preempt, wait, nil
}

// requeue takes back a change the plan put aside after a failed attempt, on
// the maintainer's `tend retry`.
func (r *runner) requeue(id string) {
	p := r.p
	i := slices.IndexFunc(p.Dropped, func(d plan.Drop) bool { return d.Change.ID == id && d.Reason == "unsupported" })
	if i < 0 {
		return
	}
	p.Queue = append(p.Queue, p.Dropped[i].Change)
	p.Dropped = slices.Delete(p.Dropped, i, i+1)
	plan.Rank(p.Queue, r.s.e.Now())
	r.s.st.See(id, "queued", "", r.s.e.Now())
}

// upToDate is true when the open PR already carries this change on this base
// and nobody asked for it to be redone since.
func (r *runner) upToDate(pick *core.Change, preempt *core.PR, cmd plan.Command, hasCmd bool) bool {
	s, open := r.s, r.p.Open
	if open == nil || preempt != nil || s.o.dryRun {
		return false
	}
	id, base, _, ok := deliver.ParseMarker(open.Body)
	if !ok || id != pick.ID || base != r.wt.Base {
		return false
	}
	return !hasCmd || s.st.Open == nil || !cmd.At.After(s.st.Open.At)
}

func (r *runner) change(ctx context.Context, ch *core.Change, preempt *core.PR) (*outcome, error) {
	if ch.Kind == core.Tidy {
		return r.tidy(ctx, ch, preempt)
	}
	s, wt := r.s, r.wt
	r.slug = core.Slug(ch.ID)
	cmds := verify.Detect(wt.Dir, r.inv.Manifests, r.ecos, s.cfg.Verify)
	nothing := len(cmds.Install)+len(cmds.Build)+len(cmds.Test) == 0
	proof := &core.Proof{Container: s.cfg.Verify.Container, Nothing: nothing}
	if !nothing {
		proof.Base = r.baseline(ctx, cmds)
	}

	pt, err := r.attempt(ctx, ch, cmds, proof)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return r.unproven(ch, "patch: "+err.Error()), nil
	}
	if !pt.Decision && !nothing && !plan.Proven(proof) {
		if !plan.Decides(ch) {
			return r.unproven(ch, failure(proof.Patched)), nil
		}
		if err := reset(ctx, wt); err != nil {
			return nil, err
		}
		s.logf("the fix did not pass the tests; asking the maintainer to decide")
		note := "The bump alone breaks this project's tests, and there is no model to move the call sites."
		switch {
		case pt.Model:
			note = "The model's migration does not pass this project's tests."
		case len(ch.Options) > 0:
			note = "The bump alone breaks this project's tests; pick a way forward below."
		case ch.Kind == core.Security:
			note = "The release that fixes the advisory breaks this project's tests, so the hole stays open until someone moves the code."
		}
		pt = &core.Patch{Decision: true, Applied: []string{"no mechanical fix; options listed"}, Note: note}
	}
	if !pt.Decision && len(pt.Files) == 0 {
		return r.unproven(ch, "the patch changed no files"), nil
	}
	if pt.Decision && r.model == nil && r.pick == 0 && (len(ch.Deps) == 0 || ch.To != "" || ch.ToName != "") {
		s.logf("%s needs code edits a model would make, so it goes out as a decision PR", ch.ID)
	}

	var vt *core.Vetting
	if !pt.Decision {
		if vt, err = r.vet(ctx, ch, pt, proof); err != nil {
			return nil, err
		}
		if vt.Verdict == "reject" {
			return r.unproven(ch, "vetting rejected the patch: "+findings(vt)), nil
		}
		if !nothing && !plan.Proven(proof) {
			return r.unproven(ch, "the patch failed its tests after vetting: "+failure(proof.Patched)), nil
		}
	}
	return r.deliver(ctx, ch, pt, proof, vt, preempt)
}

// options are how this run makes a patch: its ecosystems, index and model,
// and for a model that calls tools, the project's commands to run as it works.
func (r *runner) options(cmds core.Commands, proof *core.Proof) patch.Options {
	s := r.s
	o := patch.Options{Ecos: r.ecos, Index: r.inv.Index, Model: r.model, Attempts: s.cfg.Model.Attempts, Option: r.pick, Proven: !proof.Nothing, Log: s.logf}
	if _, ok := r.model.(model.Agent); ok && !proof.Nothing {
		o.Commands, o.Turns, o.Tokens = cmds, s.cfg.Model.Turns, s.cfg.Model.Tokens
		o.Transcript = filepath.Join(s.dir, "logs", r.slug, "agent.jsonl")
		o.Prove = func(ctx context.Context, c core.Commands) (*core.Run, error) { return r.prove(ctx, c, "patched") }
	}
	return o
}

// attempt applies the change and proves it, once.
func (r *runner) attempt(ctx context.Context, ch *core.Change, cmds core.Commands, proof *core.Proof) (*core.Patch, error) {
	o := r.options(cmds, proof)
	var last *core.Run
	lastTree := ""
	if o.Prove != nil {
		// The agent runs the project's commands as it works; the last full
		// run on the tree it leaves is the proof, not a reason to run again.
		o.Prove = func(ctx context.Context, c core.Commands) (*core.Run, error) {
			run, err := r.prove(ctx, c, "patched")
			if err == nil && slices.Equal(c.Test, cmds.Test) && slices.Equal(c.Build, cmds.Build) {
				last, lastTree = run, r.tree(ctx)
			}
			return run, err
		}
	}
	pt, err := patch.Apply(ctx, r.wt.Dir, ch, o)
	if err != nil || pt.Decision || proof.Nothing {
		return pt, err
	}
	if last != nil && lastTree != "" && lastTree == r.tree(ctx) {
		proof.Patched = last
		return pt, nil
	}
	proof.Patched, err = r.prove(ctx, cmds, "patched")
	return pt, err
}

// tree names the worktree's staged files, to tell whether two proofs saw the same thing.
func (r *runner) tree(ctx context.Context) string {
	if _, err := gitx.Run(ctx, r.wt.Dir, "add", "-A"); err != nil {
		return ""
	}
	t, _ := gitx.Run(ctx, r.wt.Dir, "write-tree")
	return t
}

// logs is where the steps of one proof write, cleared first: the last run's
// logs of each change, and of the base, are all tend keeps.
func (r *runner) logs(name string) string {
	dir := filepath.Join(r.s.dir, "logs", cmp.Or(r.slug, "base"), name)
	if err := os.RemoveAll(dir); err != nil {
		r.s.logf("cannot clear %s: %v", dir, err)
	}
	return dir
}

func (r *runner) prove(ctx context.Context, cmds core.Commands, name string) (*core.Run, error) {
	s := r.s
	o := verify.Options{
		Commands:  cmds,
		Timeout:   time.Duration(s.cfg.Verify.Timeout),
		Container: s.cfg.Verify.Container,
		Env:       s.cfg.Verify.Env,
		LogDir:    r.logs(name),
		Warnings:  true,
		Listen:    r.listen,
	}
	if s.o.verbose {
		o.Stream = s.e.Stderr
	}
	s.logf("proving %s: %s", name, strings.Join(slices.Concat(cmds.Install, cmds.Build, cmds.Test), " && "))
	run, err := verify.Run(ctx, r.wt, o)
	if err != nil {
		return nil, err
	}
	return run, discard(ctx, r.wt)
}

// discard puts the worktree back to what is staged: whatever the build and
// tests wrote (a binary, a regenerated file) is not part of the patch.
// Ignored files such as installed dependencies stay.
func discard(ctx context.Context, wt *verify.Worktree) error {
	if _, err := gitx.Run(ctx, wt.Dir, "checkout", "--quiet", "--", "."); err != nil {
		return err
	}
	_, err := gitx.Run(ctx, wt.Dir, "clean", "--quiet", "--force", "-d")
	return err
}

// baseline runs the commands on the base commit in a worktree of its own,
// once per base: a base that already fails is shown next to the patch. A
// failing base runs twice before it is kept, so one flaky install does not
// stand for the whole commit.
func (r *runner) baseline(ctx context.Context, cmds core.Commands) *core.Run {
	s, sha := r.s, r.wt.Base
	if b := s.st.Baseline; b != nil && b.Sha == sha {
		return b.Run
	}
	bwt, err := verify.Checkout(ctx, s.root, filepath.Join(s.dir, "baseline", dirName(sha)), sha)
	if err != nil {
		s.logf("baseline: %v", err)
		return nil
	}
	defer func() {
		if err := bwt.Remove(context.WithoutCancel(ctx)); err != nil {
			s.logf("cannot remove the worktree %s: %v", bwt.Dir, err)
		}
	}()
	br := &runner{s: s, wt: bwt, listen: r.listen}
	run, err := br.prove(ctx, cmds, "base")
	if err == nil && !run.Pass {
		s.logf("the base fails at %s; running it once more", run.Last().Cmd)
		run, err = br.prove(ctx, cmds, "base")
	}
	if err != nil {
		s.logf("baseline: %v", err)
		return nil
	}
	s.st.Baseline = &state.BaseRun{Sha: sha, Run: run}
	s.save()
	return run
}

func (r *runner) vet(ctx context.Context, ch *core.Change, pt *core.Patch, proof *core.Proof) (*core.Vetting, error) {
	s, wt := r.s, r.wt
	// Only the files the patch touched have changed since the plan read the tree.
	var idx core.ScanIndex
	if x, ok := r.inv.Index.(*scan.Index); ok {
		idx = x.With(wt.Dir, pt.Files)
	} else {
		var err error
		if idx, err = scan.Build(scan.Options{Root: wt.Dir}); err != nil {
			return nil, err
		}
	}
	in := vet.Input{Dir: wt.Dir, Base: wt.Base, Change: ch, Patch: pt, Proof: proof, Index: idx, Ecos: r.ecos, Model: r.model, Fetch: s.fetch}
	if ch.API != nil {
		in.Spec, in.Hosts = r.spec(ch.API.Provider)
	}
	vt, err := vet.Check(ctx, in)
	if err != nil {
		return nil, err
	}
	if !vt.Changed {
		return vt, nil
	}
	if _, err := gitx.Run(ctx, wt.Dir, "add", "-A"); err != nil {
		return nil, err
	}
	if pt.Diff, pt.Files, err = patch.Diff(ctx, wt.Dir, wt.Base); err != nil {
		return nil, err
	}
	if !proof.Nothing {
		s.logf("vetting changed the patch; proving it again")
		cmds := verify.Detect(wt.Dir, r.inv.Manifests, r.ecos, s.cfg.Verify)
		if proof.Patched, err = r.prove(ctx, cmds, "patched"); err != nil {
			return nil, err
		}
		vt.Verdict = vet.Verdict(vt, proof)
	}
	return vt, nil
}

func (r *runner) spec(provider string) (*api.Doc, []string) {
	var doc *api.Doc
	if sp := r.s.specs; sp != nil {
		doc = sp.Current(provider)
	}
	for _, p := range plan.Providers(r.s.cfg) {
		if p.Name == provider {
			return doc, p.Hosts
		}
	}
	return doc, nil
}

func (r *runner) deliver(ctx context.Context, ch *core.Change, pt *core.Patch, proof *core.Proof, vt *core.Vetting, preempt *core.PR) (*outcome, error) {
	s, wt, cfg := r.s, r.wt, r.s.cfg.Deliver
	in := deliver.BodyInput{
		Change: ch, Patch: pt, Proof: proof, Vetting: vt, Base: wt.Base, BaseRef: r.baseRef,
		Rev: 1, Option: r.pick, Meta: r.meta(ch),
		TidyDue: ch.Kind != core.Tidy && s.st.Onboard == due,
	}
	if s.forge == nil && cfg.Mode == "pr" {
		in.Notes = append(in.Notes, "Prepared without a pull request: "+s.why+".")
	}
	res := &outcome{Change: ch, Proof: proof, Vetting: vt, Title: deliver.Title(in, cfg.TitlePrefix), Worktree: wt.Dir, Branch: deliver.Branch(ch)}
	res.Body = deliver.Body(in)
	if s.o.dryRun {
		res.Action, res.line = "dry-run", "dry run; the worktree is kept at "+wt.Dir
		return res, nil
	}

	author := verify.AuthorFrom(s.root, s.e.Getenv)
	sha, err := wt.Commit(ctx, deliver.Message(in, cfg.TitlePrefix), author, pt.Decision)
	if err != nil {
		return nil, err
	}
	res.Commit = sha
	// The open PR reflects every command up to the one this run honored, even
	// one stamped later than this machine's clock.
	now := s.e.Now()
	if r.cmdAt.After(now) {
		now = r.cmdAt
	}
	switch {
	case cfg.Mode == "commit" && s.remote != "":
		branch := cmp.Or(cfg.Branch, r.baseRef)
		if branch == "" {
			return nil, errors.New("commit mode needs deliver.branch or a base branch")
		}
		if res.Commit, err = deliver.Commit(ctx, wt, s.push, branch); err != nil {
			return r.undelivered(ch, res, err)
		}
		s.st.Open = &state.OpenPR{ID: ch.ID, Branch: branch, Base: r.baseRef, Commit: res.Commit, At: now}
		res.Action, res.Branch = "committed", branch
		res.line = fmt.Sprintf("committed %s to %s: %s", core.Short(res.Commit), branch, res.Title)
	case s.forge == nil || s.remote == "":
		if _, err := gitx.Run(ctx, wt.Dir, "branch", "--force", res.Branch, "HEAD"); err != nil {
			return nil, err
		}
		why := cmp.Or(s.why, "no remote to push to")
		res.Action = "branch"
		res.line = fmt.Sprintf("branch %s has the change; %s, so no pull request was opened", res.Branch, why)
	default:
		last := ""
		if o := s.st.Open; o != nil && o.ID == ch.ID && o.Branch == res.Branch {
			last = o.Commit
		}
		oldID := plan.OpenID(preempt, s.st)
		if preempt != nil {
			if err := s.displace(ctx, preempt, oldID, ch); err != nil {
				return nil, err
			}
		}
		pr, err := deliver.Send(ctx, s.forge, wt, deliver.SendInput{BodyInput: in, Remote: s.push, Cfg: cfg, Last: last})
		if err != nil {
			return r.undelivered(ch, res, err)
		}
		res.Action, res.PR = "opened", pr
		if r.p.Open != nil && r.p.Open.Number == pr.Number {
			res.Action = "updated"
		}
		s.st.Open = &state.OpenPR{ID: ch.ID, PR: pr.Number, Branch: pr.Branch, Base: pr.Base, Commit: cmp.Or(pr.Head, sha), At: now}
		if p, ok := s.st.Parked[ch.ID]; ok && p.Reason == "preempted" {
			delete(s.st.Parked, ch.ID)
		}
		res.Body = pr.Body
		res.line = fmt.Sprintf("%s #%d %s %s", res.Action, pr.Number, pr.Title, pr.URL)
		if preempt != nil {
			if err := deliver.Replaced(ctx, s.forge, preempt.Number, pr.Number); err != nil {
				s.logf("cannot point #%d at #%d: %v", preempt.Number, pr.Number, err)
			}
		}
	}
	return res, nil
}

// undelivered turns what went wrong on the way out into the run's outcome.
// What a retry cannot fix is recorded, so the next run moves on.
func (r *runner) undelivered(ch *core.Change, res *outcome, err error) (*outcome, error) {
	s := r.s
	switch {
	case errors.Is(err, deliver.ErrMoved):
		res.Action, res.line = "moved", err.Error()+"; the next run does the change again on the new tip"
		return res, nil
	case errors.Is(err, deliver.ErrForeign):
		res.Action, res.PR = "waiting", r.p.Open
		res.line = err.Error() + "; tend leaves that PR alone"
		return res, nil
	case errors.Is(err, deliver.ErrRefused):
		s.st.Fail(ch.ID, err.Error(), r.wt.Base, s.e.Now())
	}
	return nil, err
}

func (r *runner) meta(ch *core.Change) *core.Meta {
	if len(ch.Deps) == 0 {
		return nil
	}
	return r.p.Metas[ch.Deps[0].Ecosystem+"/"+ch.Deps[0].Name]
}

// unproven records that tend could not prove a change, so the queue moves past
// it until the base moves. A security fix is never put aside: it comes up
// again on the next run.
func (r *runner) unproven(ch *core.Change, detail string) *outcome {
	s := r.s
	if ch.Kind != core.Security {
		s.st.Fail(ch.ID, detail, r.wt.Base, s.e.Now())
	}
	first, _, _ := strings.Cut(detail, "\n")
	if !s.o.quiet && strings.Contains(detail, "\n") {
		fmt.Fprintf(s.e.Stderr, "%s\n", detail)
	}
	return &outcome{Action: "unproven", Change: ch, line: fmt.Sprintf("could not prove %s: %s", ch.ID, first), code: exitUnproven}
}

func failure(run *core.Run) string {
	st := run.Last()
	if st == nil {
		return "the proof did not run"
	}
	if st.Exit == 0 {
		return fmt.Sprintf("%s did not finish\n%s", st.Cmd, st.Tail)
	}
	return fmt.Sprintf("%s failed (exit %d)\n%s", st.Cmd, st.Exit, st.Tail)
}

func findings(vt *core.Vetting) string {
	var res []string
	for _, f := range vt.Findings {
		if f.Action == "flagged" {
			res = append(res, fmt.Sprintf("%s: %s", f.Check, f.Msg))
		}
	}
	return strings.Join(res, "; ")
}

// reset puts the worktree back on its base, for another attempt.
func reset(ctx context.Context, wt *verify.Worktree) error {
	if _, err := gitx.Run(ctx, wt.Dir, "reset", "--quiet", "--hard", wt.Base); err != nil {
		return err
	}
	_, err := gitx.Run(ctx, wt.Dir, "clean", "--quiet", "--force", "-d")
	return err
}

func (s *session) reconcile(ctx context.Context) (*core.PR, error) {
	if s.forge == nil {
		return nil, nil
	}
	if s.o.dryRun {
		return newestOpen(ctx, s.forge)
	}
	pr, err := deliver.Reconcile(ctx, s.forge, s.st)
	if err != nil {
		return nil, fmt.Errorf("forge: %w", err)
	}
	s.save()
	return pr, nil
}

// settleCommit closes the books on a change delivered as a commit: it is on
// the branch, or someone took it off again.
func (s *session) settleCommit(ctx context.Context) {
	o := s.st.Open
	if o == nil || o.PR != 0 || s.o.dryRun {
		return
	}
	if s.remote == "" || o.Commit == "" {
		s.st.Open = nil
		return
	}
	if err := s.push.Fetch(ctx, o.Branch); err != nil {
		s.logf("cannot fetch %s to look for %s: %v", o.Branch, core.Short(o.Commit), err)
		return
	}
	s.st.Open = nil
	ref := "refs/remotes/" + s.remote + "/" + o.Branch
	if _, err := gitx.Run(ctx, s.root, "merge-base", "--is-ancestor", o.Commit, ref); err != nil {
		s.st.Parked[o.ID] = state.Parked{Reason: "rejected", To: core.TargetOf(o.ID), At: s.e.Now()}
		return
	}
	s.st.See(o.ID, "delivered", "", s.e.Now())
}

func (r *runner) command(ctx context.Context, pr *core.PR) (plan.Command, bool) {
	cs, err := r.s.forge.Comments(ctx, pr.Number)
	if err != nil {
		r.s.logf("cannot read the comments on #%d: %v", pr.Number, err)
		return plan.Command{}, false
	}
	r.comments = cs
	return plan.ParseCommand(cs)
}

// skip honors `tend skip`: the change is parked for good at this target. The
// PR is closed as the maintainer's no, not as tend's own closing.
func (s *session) skip(ctx context.Context, pr *core.PR, id string, c plan.Command) error {
	msg := fmt.Sprintf("Skipped as @%s asked. tend will not propose %s again; a newer release starts over.", c.Author, id)
	if err := s.forge.Comment(ctx, pr.Number, msg); err != nil {
		return err
	}
	if err := s.forge.ClosePR(ctx, pr.Number); err != nil {
		return err
	}
	s.st.Parked[id] = state.Parked{PR: pr.Number, Reason: "rejected", To: core.TargetOf(id), At: s.e.Now()}
	s.st.See(id, "parked", fmt.Sprintf("#%d skipped by @%s", pr.Number, c.Author), s.e.Now())
	s.st.Open = nil
	s.logf("skipped %s as @%s asked", id, c.Author)
	return nil
}

// displace closes the PR a new one is about to replace: the same change at an
// older target is superseded; anything else gives way to security work and
// comes back. It goes first, so tend never has two PRs open, and the state is
// saved at once, so the displaced change returns even if the new PR never
// opens.
func (s *session) displace(ctx context.Context, old *core.PR, oldID string, ch *core.Change) error {
	msg := "Closing to make room for a security fix. tend will reopen this after that merges."
	same := core.Subject(oldID) == core.Subject(ch.ID) && !ver.Less(ch.To, core.TargetOf(oldID))
	if same {
		msg = fmt.Sprintf("Superseded by a newer change that moves to %s.", ch.To)
	}
	if err := deliver.Close(ctx, s.forge, old.Number, msg); err != nil {
		return err
	}
	if !same && oldID != "" {
		s.st.Parked[oldID] = state.Parked{PR: old.Number, Reason: "preempted", To: core.TargetOf(oldID), At: s.e.Now()}
	}
	if o := s.st.Open; o != nil && o.PR == old.Number {
		s.st.Open = nil
	}
	s.save()
	return nil
}

// notice prints one line a day at most when a newer release of tend exists.
func (s *session) notice(ctx context.Context) {
	rel := &s.st.Release
	if s.e.Now().Sub(rel.Checked) >= 24*time.Hour {
		info, err := selfupdate.Latest(ctx, s.fetch, s.e.Version)
		rel.Checked, rel.Tag = s.e.Now(), ""
		if err == nil && info.Newer {
			rel.Tag = info.Tag
		}
	}
	if rel.Tag != "" && !s.o.quiet && newer(rel.Tag, s.e.Version) {
		fmt.Fprintf(s.e.Stderr, "tend: %s is available; run tend update\n", rel.Tag)
	}
}

func newer(tag, current string) bool {
	if _, ok := ver.Parse(current); !ok {
		return false
	}
	return ver.Less(current, tag)
}

// dirName keeps worktree paths short while staying unique per commit.
func dirName(sha string) string { return sha[:min(12, len(sha))] }
