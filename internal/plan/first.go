package plan

import (
	"cmp"
	"slices"

	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/eco"
	"github.com/mohsensc/tend-cli/internal/scan"
)

// Spread is how many call sites a change may touch and still go into a
// catch-up stage before the last. Past it the edit is a migration, which
// waits until the libraries and runtimes it is written against are current.
const Spread = 10

// The stages of a catch-up, in the order tend takes them. Tier says which a
// change belongs to.
const (
	Fixes      = 1 // security fixes, API changes, moves off what is deprecated now
	Runtimes   = 2 // runtime pins, base images, CI Actions and build tools
	Majors     = 3 // releases that are only new, majors above all
	Migrations = 4 // wide migrations, and choices between replacements
	Last       = Migrations
)

// StageName says what a stage does, as a PR names it.
func StageName(k int) string {
	switch k {
	case Fixes:
		return "fixes"
	case Runtimes:
		return "runtimes and build tooling"
	case Majors:
		return "major upgrades"
	case Migrations:
		return "migrations and replacements"
	}
	return ""
}

// Tier is the catch-up stage a queued change belongs to. The first rule that
// matches wins: a migration past Spread call sites to move, or a choice between
// replacements, waits for the last stage; an API change and a security fix
// come first; what pins the project's runtime or build comes next, then the
// releases that are only new; any other move off something deprecated is a
// fix.
func Tier(ch *core.Change) int {
	switch {
	case MovesCode(ch) && core.Lines(core.Calls(ch)) > Spread:
		return Migrations
	case ch.API == nil && ch.To == "" && ch.ToName == "":
		return Migrations
	case ch.API != nil || ch.Kind == core.Security:
		return Fixes
	case tooling(ch):
		return Runtimes
	case ch.Kind == core.Breaking || ch.Kind == core.Update:
		return Majors
	}
	return Fixes
}

// MovesCode reports whether a change has call sites to move: an API change,
// a breaking release, a move off something deprecated, a major, or an option
// that is not a plain bump. A patch or minor release moves none of the sites
// that use the package.
func MovesCode(ch *core.Change) bool {
	return ch.API != nil || ch.Kind == core.Breaking || ch.Kind == core.Deprecation || ch.Level == "major" ||
		slices.ContainsFunc(ch.Options, func(o core.Option) bool { return !o.Safe })
}

// tooling reports whether every dep a change moves is something the project
// runs or builds on rather than code it calls: a runtime pin or minimum, a
// base image, an Action, or a dev dependency nothing outside the tests calls,
// such as a compiler, a bundler or a test runner.
func tooling(ch *core.Change) bool {
	if len(ch.Deps) == 0 {
		return false
	}
	code := slices.ContainsFunc(core.Calls(ch), func(s core.Site) bool { return !scan.IsTest(s.File) })
	for _, d := range ch.Deps {
		if !traits(d).Tooling && !d.Min && (!d.Dev || code) {
			return false
		}
	}
	return true
}

// First is tend's first PR on a repo as one change: the lowest stage of the
// catch-up with anything in it, the direct dependencies nothing uses, those
// that only look unused, and the rest of the queue, each in the stage it
// waits for. It is nil when it would change nothing: nothing to make or
// remove, and a tend.yml already there for a welcome PR to add.
func First(inv *core.Inventory, p *Plan, cfg *config.Config, configured bool) (*core.Change, error) {
	gone, kept, err := eco.Unused(inv, cfg.Keeps)
	if err != nil {
		return nil, err
	}
	b := stage(p, 0, gone)
	b.Unused, b.Kept = gone, kept
	if len(b.Changes)+len(b.Unused)+len(b.Next) == 0 && configured {
		return nil, nil
	}
	ch := &core.Change{ID: core.FirstID, Kind: core.First, Batch: b}
	Refit(ch)
	return ch, nil
}

// Next is the catch-up PR for the first stage past after with anything in
// it, carrying what is left of the stages before it too, or nil when no
// stage past after has anything left.
func Next(p *Plan, after int) *core.Change {
	if lowest(p.Queue, after) == 0 {
		return nil
	}
	b := stage(p, after, nil)
	ch := &core.Change{ID: core.StageID(b.Stage), Kind: core.First, Batch: b}
	Refit(ch)
	return ch
}

// stage is the batch of the first stage past after with anything in it:
// every queued change of that stage or an earlier one, and the rest of the
// queue, each under the stage it waits for. A change on a package another
// change in the batch moves waits for the next stage.
func stage(p *Plan, after int, gone []core.Dep) *core.Batch {
	var queue []core.Change
	for _, ch := range p.Queue {
		if ch.Kind != core.First && !removed(&ch, gone) {
			queue = append(queue, ch)
		}
	}
	b := &core.Batch{Stage: cmp.Or(lowest(queue, after), max(after+1, Fixes))}
	var held []core.Dep
	for _, ch := range queue {
		t := Tier(&ch)
		switch {
		case t > b.Stage:
			b.Next = append(b.Next, core.Later{Change: ch, Stage: t})
		case slices.ContainsFunc(ch.Deps, func(d core.Dep) bool { return slices.ContainsFunc(held, sameCopy(d)) }):
			b.Next = append(b.Next, core.Later{Change: ch, Why: "another change here moves the same package, so this one comes after it"})
		default:
			held = append(held, ch.Deps...)
			b.Changes = append(b.Changes, ch)
		}
	}
	for i := range b.Next {
		if b.Next[i].Stage == 0 {
			b.Next[i].Stage = After(b)
		}
	}
	return b
}

// lowest is the lowest stage past after that a queued change belongs to,
// 0 when there is none.
func lowest(queue []core.Change, after int) int {
	res := 0
	for _, ch := range queue {
		if t := Tier(&ch); ch.Kind != core.First && t > after && (res == 0 || t < res) {
			res = t
		}
	}
	return res
}

// After is the stage that comes after a batch's own: the lowest one its
// queue has changes waiting in, 0 when none, which ends the catch-up.
func After(b *core.Batch) int {
	res := 0
	for _, l := range b.Next {
		if l.Stage > b.Stage && (res == 0 || l.Stage < res) {
			res = l.Stage
		}
	}
	return res
}

// removed reports whether everything ch moves is a dependency the first PR
// removes, which needs no fix or move of its own.
func removed(ch *core.Change, gone []core.Dep) bool {
	return len(ch.Deps) > 0 && !slices.ContainsFunc(ch.Deps, func(d core.Dep) bool {
		return !slices.ContainsFunc(gone, same(d))
	})
}

// sameCopy matches what same does, except that two copies of a package only
// the lockfile names are apart when they sit at different versions.
func sameCopy(d core.Dep) func(core.Dep) bool {
	return func(x core.Dep) bool {
		return same(d)(x) && (x.Direct || d.Direct || x.Version == d.Version)
	}
}

// same matches a dependency declared in the same manifest as d.
func same(d core.Dep) func(core.Dep) bool {
	return func(x core.Dep) bool {
		return x.Ecosystem == d.Ecosystem && x.Name == d.Name && x.Manifest.Path == d.Manifest.Path
	}
}

// Refit sets a catch-up PR's deps and sites from what its batch carries: the
// dependencies it removes and moves, a swap's new package among them, and the
// call sites its changes touch. Vetting reads them like any change's.
func Refit(ch *core.Change) {
	b := ch.Batch
	deps := slices.Clone(b.Unused)
	var sites []core.Site
	for _, c := range b.Changes {
		deps = append(deps, c.Deps...)
		if name, to := Target(&c); len(c.Deps) > 0 && name != c.Deps[0].Name {
			d := c.Deps[0]
			d.Name, d.Version, d.Req, d.Line = name, to, "", 0
			deps = append(deps, d)
		}
		sites = append(sites, c.Sites...)
	}
	ch.Deps, ch.Sites = deps, sortSites(sites)
}

// Target is the package a change moves its first dep to and the release,
// its own or that of the option a run took.
func Target(ch *core.Change) (string, string) {
	name, to := "", ch.To
	if len(ch.Deps) > 0 {
		name = ch.Deps[0].Name
	}
	if ch.ToName != "" {
		name = ch.ToName
	}
	if c := ch.Choice; c != nil && c.N <= len(ch.Options) {
		o := ch.Options[c.N-1]
		if o.ToName != "" {
			name = o.ToName
		}
		if o.To != "" {
			to = o.To
		}
	}
	return name, to
}
