package api

import (
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/eco"
	"github.com/mohsensc/tend-cli/internal/scan"
)

// Detect returns the providers the inventory uses, by SDK dependency or by host
// literal. URL literals in test code are fixtures and assertions, not calls, so
// they count for nothing.
func Detect(inv *core.Inventory, cat []Provider) []core.ProviderUse {
	var res []core.ProviderUse
	for _, p := range cat {
		use := core.ProviderUse{Provider: p.Name, Hosts: p.Hosts}
		var names []string
		for _, d := range inv.Deps {
			if !p.ships(d) {
				continue
			}
			use.SDK = append(use.SDK, d)
			names = append(names, imports(d)...)
		}
		if inv.Index != nil {
			for _, u := range inv.Index.Endpoints(p.Hosts) {
				if !scan.IsTest(u.File) {
					use.URLs = append(use.URLs, u)
				}
			}
			if len(names) > 0 {
				slices.Sort(names)
				use.Sites = inv.Index.Uses(slices.Compact(names))
			}
		}
		if len(use.SDK) > 0 || len(use.URLs) > 0 {
			res = append(res, use)
		}
	}
	return res
}

// ships reports whether d is one of the provider's SDKs. Go modules match by
// path prefix, so github.com/stripe/stripe-go/v82 is stripe-go.
func (p *Provider) ships(d core.Dep) bool {
	name := canon(d.Ecosystem, d.Name)
	for _, s := range p.SDKs {
		if s.Eco != d.Ecosystem {
			continue
		}
		want := canon(s.Eco, s.Name)
		if name == want || d.Ecosystem == core.Go && strings.HasPrefix(name, want+"/") {
			return true
		}
	}
	return false
}

func canon(ecosystem, name string) string {
	switch ecosystem {
	case core.Pip:
		return eco.PyNorm(name)
	case core.Nuget, core.Composer:
		return strings.ToLower(name)
	}
	return name
}

func imports(d core.Dep) []string {
	if e := eco.ByName(d.Ecosystem); e != nil {
		if res := e.Imports(d); len(res) > 0 {
			return res
		}
	}
	return []string{d.Name}
}
