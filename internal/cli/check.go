package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/deliver"
	"github.com/mohsensc/tend-cli/internal/gitx"
	"github.com/mohsensc/tend-cli/internal/plan"
	"github.com/mohsensc/tend-cli/internal/verify"
)

func check(ctx context.Context, e Env, o opts) int {
	s, err := open(ctx, e, o, true)
	if err != nil {
		return fail(e, err)
	}
	defer s.close()
	reached := s.reach(ctx)
	base, berr := s.baseName(ctx)
	if berr == nil {
		berr = reached
	}
	rev, rerr := s.baseRev(ctx, base, false)
	if rerr != nil {
		rev = "HEAD"
	}
	sha, _ := gitx.Run(ctx, s.root, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	p, inv, err := s.plan(ctx, s.root, sha, nil)
	if err != nil {
		return fail(e, err)
	}
	p.Notes = append(p.Notes, s.away(ctx, base, rev, rerr, sha)...)
	if berr != nil {
		p.Notes = append(p.Notes, fmt.Sprintf("cannot read the default branch: %v", berr))
	}
	if s.forge == nil {
		s.openWhy = s.why
	} else if p.Open, err = newestOpen(ctx, s.forge); err != nil {
		s.openWhy = "cannot list them"
		p.Notes = append(p.Notes, fmt.Sprintf("cannot list open pull requests: %v", err))
	}
	// Like run, check owes no first PR where there is nothing to watch.
	empty := len(inv.Manifests) == 0 && len(inv.Providers) == 0 && len(p.Untracked) == 0
	// check reads the checkout, local edits and all, not the base commit, so
	// what it finds shows the catch-up but does not settle it.
	was, stage := s.st.Onboard, s.st.Stage
	s.mergedStage(ctx)
	if !empty {
		if err := s.onboard(ctx, inv, p, sha); err != nil {
			p.Notes = append(p.Notes, fmt.Sprintf("cannot tell whether the first PR is due: %v", err))
		}
	}
	s.st.Onboard, s.st.Stage = was, stage
	if ctx.Err() != nil {
		return interrupted(e)
	}
	if !s.readOnly {
		s.snapshot(inv)
	}
	s.save()
	if o.json {
		err = writeJSON(e.Stdout, checkJSON(s.repo, base, p))
	} else {
		err = writeCheck(e.Stdout, s, p, empty)
	}
	if err != nil {
		return fail(e, err)
	}
	if o.failOn != "" && slices.ContainsFunc(p.Queue, func(ch core.Change) bool { return fails(ch, o.failOn, s.e.Now()) }) {
		return exitFound
	}
	return exitOK
}

// mergedStage counts a merged catch-up PR's stage as done, as run's
// reconcile does, for this check only: without it, check lists a stage PR
// that the next run, seeing the merge, never opens.
func (s *session) mergedStage(ctx context.Context) {
	o := s.st.Open
	if s.forge == nil || o == nil || !core.CatchUp(o.ID) || o.Branch == "" {
		return
	}
	pr, err := s.forge.FindPR(ctx, o.Branch)
	if err != nil || pr == nil || pr.State != "merged" || pr.Number != o.PR {
		return
	}
	s.st.Stage = max(s.st.Stage, o.Stage, deliver.StageOf(pr.Body))
}

// away says when the checkout check reads is not what run works on: the
// base branch is not here yet and run fetches it, or the checkout is behind
// the base or apart from it. A branch built on the base says nothing.
func (s *session) away(ctx context.Context, base, rev string, rerr error, sha string) []string {
	head, err := gitx.Run(ctx, s.root, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	switch {
	case err != nil || head == "":
		return nil
	case rerr != nil && s.remote != "":
		return []string{fmt.Sprintf("base branch %q is not in this clone; showing the checkout at %s, but run fetches %s/%s first and works on that", base, core.Short(head), s.remote, base)}
	case rerr != nil:
		return []string{fmt.Sprintf("%v; showing the checkout at %s", rerr, core.Short(head))}
	case sha == "" || sha == head:
		return nil
	}
	if _, err := gitx.Run(ctx, s.root, "merge-base", "--is-ancestor", sha, "HEAD"); err == nil {
		return nil
	}
	where := strings.TrimPrefix(strings.TrimPrefix(rev, "refs/remotes/"), "refs/heads/")
	note := fmt.Sprintf("showing the checkout at %s; run works on %s at %s", core.Short(head), where, core.Short(sha))
	if strings.HasPrefix(rev, "refs/remotes/") {
		note += ", fetched fresh first"
	}
	return []string{note}
}

// fails says whether ch trips --fail-on kind; breaking takes in deprecations
// whose sunset is near or past. The first PR's own changes are
// in the queue on their own; what it adds is removals, and a tend.yml that
// changes nothing, which is no work upstream.
func fails(ch core.Change, kind string, now time.Time) bool {
	if ch.Kind == core.First {
		return kind == "any" && ch.Batch != nil && len(ch.Batch.Unused) > 0
	}
	return kind == "any" || string(ch.Kind) == kind || kind == "breaking" && plan.Urgent(&ch, now)
}

// plan inventories root, asks upstream and builds the queue against the base
// commit sha. base, when set, runs the base commit once the inventory is
// known and returns the deprecation warnings it printed; without it, the last
// base run's are used.
func (s *session) plan(ctx context.Context, root, sha string, base func(*core.Inventory) []core.Warning) (*plan.Plan, *core.Inventory, error) {
	inv, untracked, err := plan.Inventory(root, s.cfg)
	if err != nil {
		return nil, nil, err
	}
	var warned []core.Warning
	if base != nil {
		warned = base(inv)
	} else if b := s.st.Baseline; b != nil {
		warned = b.Run.Warnings
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	extra, unknown := plan.Discover(ctx, inv, s.cfg, s.st, s.fetch, s.e.Now())
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	srcs, specs := plan.Sources(s.dir, s.cfg, s.fetch, s.llm(), s.e.Now, extra...)
	specs.Done = s.done
	if len(warned) > 0 {
		srcs = append(srcs, &verify.Runtime{Warnings: warned})
	}
	p, err := plan.Build(ctx, plan.Input{Inv: inv, Cfg: s.cfg, State: s.st, Fetch: s.fetch, Sources: srcs, Specs: specs, Now: s.e.Now(), Base: sha})
	if err != nil {
		return nil, nil, err
	}
	p.Untracked = append(untracked, unknown...)
	s.specs = specs
	return p, inv, nil
}

// snapshot keeps the API specs the plan read as the baseline for the next
// one. Only a pass that finished calls it, so a crash keeps the old baseline.
func (s *session) snapshot(inv *core.Inventory) {
	if err := plan.Snapshot(s.specs, inv, s.st, s.e.Now()); err != nil {
		s.logf("cannot keep the API spec snapshot: %v", err)
	}
}

// done reports whether the change id was dealt with: delivered, ignored, or
// declined by a maintainer.
func (s *session) done(id string) bool {
	v := s.st.Seen[id].Verdict
	return v == "delivered" || v == "ignored" || s.st.Parked[id].Reason == "rejected"
}

func newestOpen(ctx context.Context, forge core.Forge) (*core.PR, error) {
	prs, err := forge.OpenPRs(ctx, "tend/")
	if err != nil || len(prs) == 0 {
		return nil, err
	}
	res := prs[0]
	for _, pr := range prs[1:] {
		if pr.Number > res.Number {
			res = pr
		}
	}
	return &res, nil
}

func fail(e Env, err error) int {
	if errors.Is(err, context.Canceled) {
		return interrupted(e)
	}
	fmt.Fprintf(e.Stderr, "tend: %v\n", err)
	return exitErr
}

// interrupted ends a command a signal cut short, with the code a shell
// gives a process an interrupt stopped.
func interrupted(e Env) int {
	fmt.Fprintln(e.Stderr, "tend: interrupted")
	return exitInterrupted
}
