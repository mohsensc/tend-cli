package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/deliver"
	"github.com/mohsensc/tend-cli/internal/gitx"
	"github.com/mohsensc/tend-cli/internal/plan"
	"github.com/mohsensc/tend-cli/internal/state"
	"github.com/mohsensc/tend-cli/internal/verify"
)

// ciWait is how long tend waits for the repository's CI on a commit it
// pushed before it takes no answer as no CI.
const ciWait = 6 * time.Hour

// settleCommit closes the books on a change delivered as a commit: it is on
// the branch and the repository's CI passed it or has none, or someone took
// it off again. While that CI still runs, the run waits on it. When it
// failed on the commit and not on the one before, tend reverts its commit.
// It returns the run's outcome when there is nothing more for it to do.
func (s *session) settleCommit(ctx context.Context) (*outcome, error) {
	o := s.st.Open
	if o == nil || o.PR != 0 || s.o.dryRun {
		return nil, nil
	}
	if s.remote == "" || o.Commit == "" {
		s.st.Open = nil
		return nil, nil
	}
	if err := s.push.Fetch(ctx, o.Branch); err != nil {
		s.logf("cannot fetch %s to look for %s: %v", o.Branch, core.Short(o.Commit), err)
		return nil, nil
	}
	ref := "refs/remotes/" + s.remote + "/" + o.Branch
	if _, err := gitx.Run(ctx, s.root, "merge-base", "--is-ancestor", o.Commit, ref); err != nil {
		s.st.Open = nil
		s.st.Parked[o.ID] = state.Parked{Reason: "rejected", To: core.TargetOf(o.ID), At: s.e.Now()}
		if core.CatchUp(o.ID) {
			s.st.Stage = plan.Last
		}
		return nil, nil
	}
	ci, err := s.checks(ctx, o.Commit)
	waited := s.e.Now().Sub(o.At)
	switch {
	case err != nil && waited < ciWait:
		s.logf("cannot read CI on %s: %v", core.Short(o.Commit), err)
		return &outcome{Action: "waiting", line: fmt.Sprintf("waiting to read CI on %s, which tend pushed to %s", core.Short(o.Commit), o.Branch)}, nil
	case err != nil:
		s.logf("cannot read CI on %s after %s, so tend takes it as passed: %v", core.Short(o.Commit), ciWait, err)
	case ci.State == "pending" && waited < ciWait:
		return &outcome{Action: "waiting", line: fmt.Sprintf("waiting on CI for %s, which tend pushed to %s", core.Short(o.Commit), o.Branch)}, nil
	case ci.State == "pending":
		s.logf("CI on %s has not finished after %s, so tend takes it as passed", core.Short(o.Commit), ciWait)
	case ci.State == "failure":
		before, err := s.checks(ctx, o.Commit+"^")
		if err == nil && before.State == "failure" {
			s.logf("CI fails on %s, as it did on the commit before it, so tend's change is not what broke it", core.Short(o.Commit))
			break
		}
		return s.revert(ctx, o, ci)
	}
	s.st.Open = nil
	s.st.See(o.ID, "delivered", "", s.e.Now())
	s.landed(o)
	return nil, nil
}

// landed moves the catch-up on once a catch-up commit is in.
func (s *session) landed(o *state.OpenPR) {
	if !core.CatchUp(o.ID) {
		return
	}
	if o.ID == core.FirstID {
		s.st.Onboard = "merged"
	}
	s.st.Stage = max(s.st.Stage, o.Stage)
}

// checks is what the repository's CI made of a commit, none when the forge
// cannot say.
func (s *session) checks(ctx context.Context, rev string) (core.CI, error) {
	c, ok := s.forge.(core.Checks)
	if !ok {
		return core.CI{State: "none"}, nil
	}
	sha, err := gitx.Run(ctx, s.root, "rev-parse", "--verify", rev+"^{commit}")
	if err != nil {
		return core.CI{}, err
	}
	return c.Checks(ctx, sha)
}

// revert takes back the commit tend pushed for o, which CI failed: a revert
// on the branch's tip, proven first unless it puts back the very tree the
// commit started from, and pushed with a message that says why. The change
// is parked at its target, so a newer release starts over; a catch-up
// commit ends the catch-up, and its changes come back one at a time.
func (s *session) revert(ctx context.Context, o *state.OpenPR, ci core.CI) (*outcome, error) {
	short := core.Short(o.Commit)
	failed := strings.Join(ci.Failed, ", ")
	tip, err := gitx.Run(ctx, s.root, "rev-parse", "--verify", "refs/remotes/"+s.remote+"/"+o.Branch+"^{commit}")
	if err != nil {
		return nil, err
	}
	wt, err := verify.Checkout(ctx, s.root, filepath.Join(s.dir, "wt", "revert-"+dirName(tip)), tip)
	if err != nil {
		return nil, fmt.Errorf("cannot create the worktree: %w", err)
	}
	defer func() {
		if err := wt.Remove(context.WithoutCancel(ctx)); err != nil {
			s.logf("cannot remove the worktree %s: %v", wt.Dir, err)
		}
	}()
	park := func(detail string) {
		s.st.Parked[o.ID] = state.Parked{Reason: "reverted", To: core.TargetOf(o.ID), At: s.e.Now(), Detail: detail}
		s.st.See(o.ID, "parked", detail, s.e.Now())
		if core.CatchUp(o.ID) {
			s.landed(o)
			s.st.Stage = plan.Last
		}
		s.st.Open = nil
	}
	if _, err := gitx.Run(ctx, wt.Dir, "revert", "--no-commit", o.Commit); err != nil {
		park(fmt.Sprintf("CI failed on %s (%s), and reverting it does not apply cleanly; revert it by hand", short, failed))
		return &outcome{Action: "unproven", line: fmt.Sprintf("CI fails on %s and tend cannot revert it cleanly", short), code: exitUnproven}, nil
	}
	if _, err := gitx.Run(ctx, wt.Dir, "add", "-A"); err != nil {
		return nil, err
	}
	tree, err := gitx.Run(ctx, wt.Dir, "write-tree")
	if err != nil {
		return nil, err
	}
	before, err := gitx.Run(ctx, wt.Dir, "rev-parse", o.Commit+"^^{tree}")
	if err != nil {
		return nil, err
	}
	// Commits landed on top of tend's since: the revert is a new tree, and
	// is proven like any other.
	if tree != before {
		inv, _, err := plan.Inventory(wt.Dir, s.cfg)
		if err != nil {
			return nil, err
		}
		r := &runner{s: s, wt: wt, inv: inv, ecos: ecosFor(s.cfg), slug: "revert"}
		run, err := r.prove(ctx, r.detect(), "revert")
		if err != nil {
			return nil, err
		}
		if !run.Pass {
			park(fmt.Sprintf("CI failed on %s (%s), and the revert does not pass the project's commands either: %s; revert it by hand", short, failed, broke(run)))
			return &outcome{Action: "unproven", line: fmt.Sprintf("CI fails on %s and its revert fails too", short), code: exitUnproven}, nil
		}
	}
	subject, err := gitx.Run(ctx, s.root, "log", "-1", "--format=%s", o.Commit)
	if err != nil {
		return nil, err
	}
	msg := fmt.Sprintf("Revert %q\n\nCI failed on %s: %s. tend proved that commit with the project's own\ncommands, but the repository's CI did not pass it, so tend takes it back\nand will not try %s again; a newer release starts over.\n\nThis reverts commit %s.",
		subject, short, failed, o.ID, o.Commit)
	if _, err := wt.Commit(ctx, msg, verify.AuthorFrom(s.root, s.e.Getenv), false); err != nil {
		return nil, err
	}
	sha, err := deliver.Commit(ctx, wt, s.push, o.Branch)
	switch {
	case errors.Is(err, deliver.ErrMoved):
		return &outcome{Action: "moved", line: err.Error() + "; the next run reverts on the new tip"}, nil
	case err != nil:
		return nil, err
	}
	park(fmt.Sprintf("reverted in %s: CI failed on %s (%s)", core.Short(sha), short, failed))
	s.logf("CI failed on %s (%s); reverted it in %s", short, failed, core.Short(sha))
	return &outcome{Action: "reverted", Commit: sha, Branch: o.Branch, line: fmt.Sprintf("reverted %s on %s: CI failed (%s)", short, o.Branch, failed)}, nil
}
