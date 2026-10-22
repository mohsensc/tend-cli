package deliver

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/state"
)

const branchPrefix = "tend/"

var now = time.Now

// Reconcile finds tend's open PR, closes extras, and brings st.Open up to date
// with what happened on the forge since the last run. It returns the open PR,
// nil when there is none. The caller saves st.
func Reconcile(ctx context.Context, forge core.Forge, st *state.State) (*core.PR, error) {
	open, err := forge.OpenPRs(ctx, branchPrefix)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(open, func(a, b core.PR) int { return cmp.Compare(a.Number, b.Number) })
	var keep *core.PR
	closed := map[int]bool{}
	if n := len(open); n > 0 {
		keep = &open[n-1]
		for _, pr := range open[:n-1] {
			msg := fmt.Sprintf("Closing: tend keeps one pull request open at a time, and #%d is the current one.", keep.Number)
			if err := Close(ctx, forge, pr.Number, msg); err != nil {
				return nil, err
			}
			closed[pr.Number] = true
		}
	}
	if st.Open != nil && st.Open.PR == 0 {
		return keep, nil
	}
	if keep == nil && st.Open != nil {
		pr, err := forge.FindPR(ctx, st.Open.Branch)
		if err != nil {
			return nil, err
		}
		if pr == nil || pr.State != "open" {
			return nil, finish(ctx, forge, st, pr)
		}
		keep = pr
	}
	if keep == nil {
		return nil, nil
	}
	if st.Open != nil && st.Open.PR != keep.Number {
		if closed[st.Open.PR] {
			st.Open = nil
		} else if err := settle(ctx, forge, st); err != nil {
			return nil, err
		}
	}
	adopt(st, keep)
	return keep, nil
}

func settle(ctx context.Context, forge core.Forge, st *state.State) error {
	pr, err := forge.FindPR(ctx, st.Open.Branch)
	if err != nil {
		return err
	}
	return finish(ctx, forge, st, pr)
}

// finish records the end of st.Open: merged, declined, or closed some other
// way, by a bot say, which leaves the change free to come back. A missing PR
// counts as declined, since its branch is gone.
func finish(ctx context.Context, forge core.Forge, st *state.State, pr *core.PR) error {
	o := st.Open
	if o.ID == "" {
		st.Open = nil
		return nil
	}
	if pr != nil && pr.State == "merged" {
		if st.Seen == nil {
			st.Seen = map[string]state.Seen{}
		}
		s := st.Seen[o.ID]
		s.Verdict = "delivered"
		st.Seen[o.ID] = s
		st.Open = nil
		return nil
	}
	if p, ok := st.Parked[o.ID]; ok && p.PR == o.PR {
		st.Open = nil
		return nil
	}
	if pr != nil {
		no, err := Declined(ctx, forge, pr)
		if err != nil {
			return err
		}
		if !no {
			st.Open = nil
			return nil
		}
	}
	if st.Parked == nil {
		st.Parked = map[string]state.Parked{}
	}
	st.Parked[o.ID] = state.Parked{PR: o.PR, Reason: "rejected", To: core.TargetOf(o.ID), At: now()}
	st.Open = nil
	return nil
}

func adopt(st *state.State, pr *core.PR) {
	if st.Open == nil || st.Open.PR != pr.Number {
		id, _, _, _ := ParseMarker(pr.Body)
		st.Open = &state.OpenPR{ID: id, PR: pr.Number, At: now()}
	}
	st.Open.Branch, st.Open.Base = pr.Branch, pr.Base
}
