package vet

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/scan"
)

// leftovers: is the deprecated package, member chain, endpoint or field still
// referenced anywhere in the worktree?
func leftovers(ctx context.Context, p *pass) ([]core.Finding, string) {
	ch := p.in.Change
	if p.in.Patch.Decision {
		return nil, "nothing was applied"
	}
	opt := p.in.applied()
	if opt != nil && (opt.Stay || opt.ToName == "" && opt.Codemod == nil && len(ch.Deps) > 0 && opt.To == ch.Deps[0].Version) {
		return nil, "the patch stays on the version in use"
	}
	if p.in.Index == nil {
		return nil, "no scan index"
	}
	renames, paths, flds := map[string]string{}, map[string]string{}, map[string]string{}
	for _, o := range ch.Options {
		if o.Codemod == nil {
			continue
		}
		merge(renames, o.Codemod.Renames)
		merge(paths, o.Codemod.Paths)
		merge(flds, o.Codemod.Fields)
	}
	if a := ch.API; a != nil {
		switch {
		case a.Field != "" && (strings.Contains(a.What, "removed") || strings.Contains(a.What, "deprecated")):
			if _, ok := flds[a.Field]; !ok {
				flds[a.Field] = ""
			}
		case strings.HasPrefix(a.What, "operation"):
			if _, ok := paths[a.Path]; !ok {
				paths[a.Path] = ""
			}
		}
	}
	if slices.ContainsFunc(ch.Evidence, func(e core.Evidence) bool { return e.Source == "runtime" }) {
		return warnedAgain(p)
	}
	swap := (ch.ToName != "" || opt != nil && opt.ToName != "") && len(ch.Deps) > 0
	if !swap && len(renames) == 0 && len(paths) == 0 && len(flds) == 0 {
		return nil, na
	}
	p.note = "no remaining references to " + olds(ch, swap, renames, paths, flds)

	var res []core.Finding
	if swap {
		for _, d := range ch.Deps {
			if ec := p.in.Ecos[d.Ecosystem]; ec != nil {
				for _, n := range ec.Imports(d) {
					for _, imp := range p.in.Index.Imports(n) {
						res = append(res, finding("leftovers", imp.File, imp.Line, "flagged", "still imports `%s`", imp.Package))
					}
				}
			}
		}
	}
	for _, d := range targets(ch) {
		ec := p.in.Ecos[d.Ecosystem]
		if ec == nil || len(renames) == 0 {
			continue
		}
		for _, s := range p.in.Index.Uses(ec.Imports(d)) {
			for old, now := range renames {
				if chainEnds(s.Chain, old) && !chainEnds(s.Chain, now) {
					res = append(res, finding("leftovers", s.File, s.Line, "flagged", "still calls `%s`", strings.Join(s.Chain, ".")))
				}
			}
		}
	}
	if len(paths) > 0 && len(p.in.Hosts) > 0 {
		for _, u := range p.in.Index.Endpoints(p.in.Hosts) {
			for old, now := range paths {
				if pathMatches(u.URL, old) && (now == "" || !pathMatches(u.URL, now)) {
					res = append(res, finding("leftovers", u.File, u.Line, "flagged", "still calls `%s`", u.URL))
				}
			}
		}
	}
	// A field name in a test is usually stub data shaped like the server's
	// reply; the proof, not this check, says whether the tests still hold.
	for old, now := range flds {
		if old == now {
			continue
		}
		for _, s := range p.in.Index.Fields(old, nil) {
			if !scan.IsTest(s.File) {
				res = append(res, finding("leftovers", s.File, s.Line, "flagged", "still uses field `%s`", old))
			}
		}
	}
	sortFindings(res)
	res = dedupe(res)
	if len(res) > 0 {
		p.note = ""
	}
	return res, ""
}

// olds names what leftovers looked for: the package swapped out, the member
// namespaces renamed (`charges.`), the endpoints and the fields.
func olds(ch *core.Change, swap bool, renames, paths, flds map[string]string) string {
	var res []string
	add := func(s string) {
		if s = "`" + s + "`"; !slices.Contains(res, s) {
			res = append(res, s)
		}
	}
	if swap {
		for _, d := range ch.Deps {
			add(d.Name)
		}
	}
	for _, old := range slices.Sorted(maps.Keys(renames)) {
		if head, _, ok := strings.Cut(old, "."); ok {
			add(head + ".")
		} else {
			add(old)
		}
	}
	for _, old := range slices.Sorted(maps.Keys(paths)) {
		add(old)
	}
	for _, old := range slices.Sorted(maps.Keys(flds)) {
		if flds[old] != old {
			add(old)
		}
	}
	return strings.Join(res, ", ")
}

// warnedAgain answers the question for a change made of runtime warnings
// with the patched run itself: does it still print them?
func warnedAgain(p *pass) ([]core.Finding, string) {
	pr := p.in.Proof
	if pr == nil || pr.Patched == nil {
		return nil, "the patched tree did not run"
	}
	var res []core.Finding
	for _, w := range pr.Patched.Warnings {
		if slices.ContainsFunc(p.in.Change.Evidence, func(e core.Evidence) bool { return e.Source == "runtime" && e.Text == w.Msg }) {
			res = append(res, finding("leftovers", w.File, w.Line, "flagged", "still warns: %s", w.Msg))
		}
	}
	if len(res) == 0 {
		p.note = "the patched run prints none of the warnings"
	}
	return res, ""
}

func merge(dst, src map[string]string) {
	for k, v := range src {
		dst[k] = v
	}
}

// chainEnds reports whether chain, past its root binding, ends with the dotted names in tail.
func chainEnds(chain []string, tail string) bool {
	want := strings.Split(tail, ".")
	if len(chain) < 2 || len(chain)-1 < len(want) {
		return false
	}
	got := chain[len(chain)-len(want):]
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// pathMatches compares a URL literal from code with a spec path template. A
// template parameter matches any segment, and a literal missing the client's
// base prefix still matches by its trailing segments when it has at least two.
func pathMatches(lit, tmpl string) bool {
	if u, err := url.Parse(lit); err == nil && u.Host != "" {
		lit = u.Path
	}
	if i := strings.IndexAny(lit, "?#"); i >= 0 {
		lit = lit[:i]
	}
	a := strings.Split(strings.Trim(lit, "/"), "/")
	b := strings.Split(strings.Trim(tmpl, "/"), "/")
	if len(a) > len(b) || len(a) < len(b) && len(a) < 2 {
		return false
	}
	b = b[len(b)-len(a):]
	for i := range a {
		if strings.HasPrefix(b[i], "{") {
			if a[i] == "" {
				return false
			}
			continue
		}
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func dedupe(res []core.Finding) []core.Finding {
	var out []core.Finding
	for i, f := range res {
		if i > 0 && f == res[i-1] {
			continue
		}
		out = append(out, f)
	}
	return out
}
