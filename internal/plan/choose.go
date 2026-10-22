package plan

import (
	"slices"

	"github.com/mohsensc/tend-cli/internal/core"
)

// tries is how many options tend makes and proves on its own before it
// stays put.
const tries = 3

// Try is an option tend tries on its own, and the rule that put it where
// it is in the order.
type Try struct {
	N   int // 1-based, into the change's Options
	Why string
}

// Ranked lists the options tend tries on its own for a change, in order:
// only those that move somewhere, a replacement only when it has a release
// to move to, one the project already depends on first, then in the order
// the options come, which for a replacement is the catalog's: recommended
// first, then by how widely each is used. Staying put is never tried; it is
// what happens when none of these is proven.
func Ranked(ch *core.Change, deps []core.Dep) []Try {
	var res []Try
	had := func(o core.Option) bool {
		return o.ToName != "" && slices.ContainsFunc(deps, func(d core.Dep) bool { return d.Direct && d.Name == o.ToName })
	}
	rec := slices.IndexFunc(ch.Options, func(o core.Option) bool { return o.ToName != "" })
	for i, o := range ch.Options {
		var why string
		switch {
		case o.Stay, o.ToName != "" && o.To == "":
			continue
		case had(o):
			why = "the project already depends on " + o.ToName
		case i == rec:
			why = "the catalog's recommended replacement"
		case o.ToName != "":
			why = "the next replacement in the catalog's order"
		case o.Codemod != nil:
			why = "the rename the deprecation notice names"
		case o.To != "" && len(ch.Deps) > 0 && o.To != Current(ch.Deps[0]):
			why = "a release past the deprecated one"
		default:
			continue
		}
		res = append(res, Try{N: i + 1, Why: why})
	}
	slices.SortStableFunc(res, func(a, b Try) int {
		return boolInt(!had(ch.Options[a.N-1])) - boolInt(!had(ch.Options[b.N-1]))
	})
	return res[:min(len(res), tries)]
}
