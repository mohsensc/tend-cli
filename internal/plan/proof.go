package plan

import "github.com/mohsensc/tend-cli/internal/core"

// Proven says whether the patched tree did its part: it passes, or the base
// already fails and the patch stops at the very step the base stops at. The
// PR for the second goes out as a draft.
func Proven(pr *core.Proof) bool {
	if pr.Patched != nil && pr.Patched.Pass {
		return true
	}
	if pr.Base == nil || pr.Base.Pass || pr.Patched == nil {
		return false
	}
	fp, fb := pr.Patched.Last(), pr.Base.Last()
	return fp != nil && fb != nil && fp.Cmd == fb.Cmd && reached(pr.Patched) == reached(pr.Base)
}

// reached is how many steps of a run passed before the first that failed.
func reached(run *core.Run) int {
	for i, st := range run.Steps {
		if st.Skipped != "" || st.Exit != 0 {
			return i
		}
	}
	return len(run.Steps)
}

// Decides says whether a change that could not be proven still deserves a PR
// that lays out the choice: a security hole, options, or code that must move
// off something. A plain major bump that breaks the tests has nothing to
// choose between, so it gets no PR.
func Decides(ch *core.Change) bool {
	switch {
	case ch.Kind == core.Security, len(ch.Options) > 0, ch.API != nil:
		return true
	case len(ch.Sites) == 0:
		return false
	}
	return ch.Kind == core.Deprecation || ch.Kind == core.Breaking && ch.Level != "major"
}
