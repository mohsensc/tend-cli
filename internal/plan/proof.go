package plan

import "github.com/mohsensc/tend-cli/internal/core"

// Proven says whether the patched tree did its part; see core.Proof.Proven.
func Proven(pr *core.Proof) bool { return pr.Proven() }

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
