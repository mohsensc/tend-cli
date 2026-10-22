package plan

import (
	"slices"

	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/eco"
)

// Tidy is tend's first PR on a repo as one change: the direct dependencies
// nothing uses, those that only look unused, and the deprecated libraries in
// the queue, which it moves off as their own changes would. It is nil when
// it would change nothing: no dependency to remove, no library to move, and
// a tend.yml already there for a welcome PR to add.
func Tidy(inv *core.Inventory, p *Plan, cfg *config.Config, configured bool) (*core.Change, error) {
	gone, kept, err := eco.Unused(inv, cfg.Keeps)
	if err != nil {
		return nil, err
	}
	var moves []core.Change
	for _, ch := range p.Queue {
		if library(&ch) && !slices.ContainsFunc(gone, same(ch.Deps[0])) {
			moves = append(moves, ch)
		}
	}
	if len(gone) == 0 && len(moves) == 0 && configured {
		return nil, nil
	}
	ch := &core.Change{ID: core.TidyID, Kind: core.Tidy, Cleanup: &core.Cleanup{Unused: gone, Kept: kept, Moves: moves}}
	Refit(ch)
	return ch, nil
}

// same matches a dependency declared in the same manifest as d. A
// deprecated package the tidy-up removes needs no move off it.
func same(d core.Dep) func(core.Dep) bool {
	return func(x core.Dep) bool {
		return x.Ecosystem == d.Ecosystem && x.Name == d.Name && x.Manifest.Path == d.Manifest.Path
	}
}

// library reports a change that moves the code off a deprecated package it
// imports. Base images, Actions, runtimes, APIs and calls a runtime warned
// about keep PRs of their own.
func library(ch *core.Change) bool {
	if ch.Kind != core.Deprecation || ch.API != nil || len(ch.Deps) == 0 || !ch.Deps[0].Direct {
		return false
	}
	e := eco.ByName(ch.Deps[0].Ecosystem)
	if e == nil || len(e.Imports(ch.Deps[0])) == 0 {
		return false
	}
	return slices.ContainsFunc(ch.Evidence, func(ev core.Evidence) bool {
		return ev.Source == "registry" || ev.Source == "archived"
	})
}

// Refit sets a tidy change's deps and sites from what its cleanup carries:
// the dependencies it removes and moves, a swap's new package among them, and
// the call sites the moves touch. Vetting reads them like any change's.
func Refit(ch *core.Change) {
	c := ch.Cleanup
	deps := slices.Clone(c.Unused)
	var sites []core.Site
	for _, m := range c.Moves {
		deps = append(deps, m.Deps...)
		if m.ToName != "" {
			d := m.Deps[0]
			d.Name, d.Version, d.Req, d.Line = m.ToName, m.To, "", 0
			deps = append(deps, d)
		}
		sites = append(sites, m.Sites...)
	}
	ch.Deps, ch.Sites = deps, sortSites(sites)
}
