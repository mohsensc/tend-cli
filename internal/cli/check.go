package cli

import (
	"context"
	"fmt"
	"slices"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/gitx"
	"github.com/mohsensc/tend-cli/internal/plan"
	"github.com/mohsensc/tend-cli/internal/verify"
)

func check(ctx context.Context, e Env, o opts) int {
	s, err := open(ctx, e, o)
	if err != nil {
		return fail(e, err)
	}
	defer s.close()
	base, berr := s.baseName(ctx)
	sha, _ := gitx.Run(ctx, s.root, "rev-parse", "--verify", "--quiet", s.baseRev(ctx, base, false)+"^{commit}")
	p, inv, err := s.plan(ctx, s.root, sha, nil)
	if err != nil {
		return fail(e, err)
	}
	if berr != nil {
		p.Notes = append(p.Notes, fmt.Sprintf("cannot read the default branch: %v", berr))
	}
	if s.forge == nil {
		p.Notes = append(p.Notes, "open pull requests unknown: "+s.why)
	} else if p.Open, err = newestOpen(ctx, s.forge); err != nil {
		p.Notes = append(p.Notes, fmt.Sprintf("cannot list open pull requests: %v", err))
	}
	// check reads the checkout, local edits and all, not the base commit, so
	// what it finds shows the first PR but does not settle it.
	was := s.st.Onboard
	if err := s.onboard(ctx, inv, p, sha); err != nil {
		p.Notes = append(p.Notes, fmt.Sprintf("cannot tell whether the first PR is due: %v", err))
	}
	s.st.Onboard = was
	s.snapshot(inv)
	s.save()
	if o.json {
		err = writeJSON(e.Stdout, checkJSON(s.repo, base, p))
	} else {
		err = writeCheck(e.Stdout, s, p, len(inv.Manifests) == 0 && len(inv.Providers) == 0 && len(p.Untracked) == 0)
	}
	if err != nil {
		return fail(e, err)
	}
	if o.failOn != "" && slices.ContainsFunc(p.Queue, func(ch core.Change) bool {
		return o.failOn == "any" || string(ch.Kind) == o.failOn
	}) {
		return exitFound
	}
	return exitOK
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
	extra, unknown := plan.Discover(ctx, inv, s.cfg, s.st, s.fetch, s.e.Now())
	srcs, specs := plan.Sources(s.dir, s.cfg, s.fetch, s.llm(), s.e.Now, extra...)
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
	fmt.Fprintf(e.Stderr, "tend: %v\n", err)
	return exitErr
}
