package plan

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/eco"
	"github.com/mohsensc/tend-cli/internal/source"
	"github.com/mohsensc/tend-cli/internal/ver"
)

type pkg struct{ eco, name string }

func (p pkg) String() string { return p.eco + "/" + p.name }

// metas fetches registry metadata for every package through a bounded pool.
// A failed lookup becomes a note; the plan goes on without that package.
func (b *builder) metas(ctx context.Context, pkgs []pkg) {
	var todo []pkg
	for _, p := range pkgs {
		if _, ok := b.meta[p]; !ok && !b.asked[p] && b.ecos[p.eco] != nil {
			b.asked[p] = true
			todo = append(todo, p)
		}
	}
	got := make([]*core.Meta, len(todo))
	errs := make([]error, len(todo))
	source.Parallel(len(todo), func(i int) {
		got[i], errs[i] = b.ecos[todo[i].eco].Meta(ctx, b.in.Fetch, todo[i].name)
	})
	for i, p := range todo {
		switch err := errs[i]; {
		case errors.Is(err, core.ErrNotSupported):
		case err != nil:
			b.note("%s: %s: %v", p.eco, p.name, err)
		case got[i] != nil:
			b.meta[p] = got[i]
		}
	}
}

// Current is the version a dep is at: the locked one, else the lowest the
// requirement allows.
func Current(d core.Dep) string {
	if d.Version != "" {
		return d.Version
	}
	s := strings.TrimLeft(strings.TrimSpace(d.Req), "^~>=<! ")
	if i := strings.IndexAny(s, " ,|;"); i >= 0 {
		s = s[:i]
	}
	return s
}

// shape is how a tag is written: the count of numeric parts and what follows
// them. Docker tags and Action refs keep their shape across a bump, so
// node:16-alpine moves to another -alpine tag and actions/checkout@v2 to a
// major-only ref.
func shape(v string) (int, string) {
	s := strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	n := 0
	for {
		i := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == 0 {
			break
		}
		n++
		s = s[i:]
		if len(s) < 2 || s[0] != '.' || s[1] < '0' || s[1] > '9' {
			break
		}
		s = s[1:]
	}
	return n, s
}

// traits are the facts of d's ecosystem, none for one tend has no adapter for.
func traits(d core.Dep) core.Traits {
	if e := eco.ByName(d.Ecosystem); e != nil {
		return e.Traits()
	}
	return core.Traits{}
}

// candidate picks the release to move d to: the newest stable release that is
// not yanked or deprecated, not past the registry's latest, and, when one
// exists, older than the cooldown. When every newer release is still cooling
// down the newest of those comes back so the caller can record it as such.
// maxMajor caps the major version; -1 means no cap.
func candidate(d core.Dep, m *core.Meta, now time.Time, cooldown time.Duration, maxMajor int) (core.Release, bool) {
	cur, ok := ver.Parse(Current(d))
	if !ok {
		return core.Release{}, false
	}
	n, suffix := shape(Current(d))
	keep := traits(d).KeepShape
	latest, hasLatest := ver.Parse(m.Latest)
	var best, young core.Release
	var bestV, youngV ver.V
	found, foundYoung := false, false
	for _, r := range m.Releases {
		v, ok := ver.Parse(r.Version)
		if !ok || ver.Compare(v, cur) <= 0 || r.Yanked || r.Deprecated != "" {
			continue
		}
		pre := r.Pre || v.Pre != ""
		if pre && (cur.Pre == "" || v.Major() != cur.Major()) {
			continue
		}
		if hasLatest && !pre && ver.Compare(v, latest) > 0 {
			continue
		}
		if maxMajor >= 0 && v.Major() > maxMajor {
			continue
		}
		if keep {
			if rn, rs := shape(r.Version); rn != n || rs != suffix {
				continue
			}
		}
		if !r.Time.IsZero() && now.Sub(r.Time) < cooldown {
			if !foundYoung || ver.Compare(v, youngV) > 0 {
				young, youngV, foundYoung = r, v, true
			}
			continue
		}
		if !found || ver.Compare(v, bestV) > 0 {
			best, bestV, found = r, v, true
		}
	}
	if found {
		return best, true
	}
	return young, foundYoung
}

// fixFor is the smallest release at or above to that can be installed: not
// yanked, deprecated or a pre-release. It is to itself when the registry knows
// nothing better.
func fixFor(m *core.Meta, to string) (core.Release, bool) {
	want, ok := ver.Parse(to)
	if !ok || m == nil {
		return core.Release{Version: to}, false
	}
	for _, r := range m.Releases {
		v, ok := ver.Parse(r.Version)
		if !ok || ver.Compare(v, want) < 0 || r.Yanked || r.Pre || v.Pre != "" || r.Deprecated != "" {
			continue
		}
		return r, true
	}
	return core.Release{Version: to}, false
}

func release(m *core.Meta, version string) (core.Release, bool) {
	for _, r := range m.Releases {
		if r.Version == version {
			return r, true
		}
	}
	return core.Release{}, false
}

// registry turns one dep and its metadata into at most one change: a
// deprecation when the version or the whole package is deprecated, else a
// breaking change for a major bump, else an update.
func (b *builder) registry(ctx context.Context, d core.Dep, m *core.Meta) *core.Change {
	if m.Deprecated != "" {
		return b.swap(ctx, d, m, deprecated(d, "", m.Deprecated))
	}
	rel, ok := candidate(d, m, b.in.Now, b.cooldown(), b.typesCap(d))
	cur, _ := release(m, Current(d))
	if cur.Deprecated != "" || cur.Yanked {
		ev := deprecated(d, Current(d), cur.Deprecated)
		if cur.Deprecated == "" {
			ev.Title = d.Name + " " + Current(d) + " was yanked"
			ev.Text = "The registry pulled this release; installs that do not already have it locked cannot get it."
		}
		ch := &core.Change{
			Kind:     core.Deprecation,
			Deps:     []core.Dep{d},
			Evidence: []core.Evidence{ev},
		}
		// When the version was deprecated is not on record, so Published stays
		// zero: the target's release date is not the deprecation's.
		if ok {
			ch.To, ch.Level = rel.Version, ver.Step(Current(d), rel.Version)
		}
		return ch
	}
	if !ok {
		return nil
	}
	ch := &core.Change{
		Kind:      core.Update,
		Level:     ver.Step(Current(d), rel.Version),
		Deps:      []core.Dep{d},
		To:        rel.Version,
		Published: rel.Time,
	}
	if ch.Level == "major" {
		ch.Kind = core.Breaking
	}
	return ch
}

// swap handles a package deprecated or abandoned as a whole, as ev says:
// move to the registry's named replacement, else to the one in the bundled
// table, else list the table's options, else leave the choice to the
// maintainer.
func (b *builder) swap(ctx context.Context, d core.Dep, m *core.Meta, ev core.Evidence) *core.Change {
	ch := &core.Change{
		Kind:     core.Deprecation,
		Deps:     []core.Dep{d},
		Evidence: []core.Evidence{ev},
	}
	name, to := m.Replacement, ""
	if name == "" {
		one, opts, ok := source.Replacement(d.Ecosystem, d.Name)
		switch {
		case ok && one != nil:
			name, to = one.ToName, one.To
			ch.Options = []core.Option{*one}
		case ok:
			ch.Options = opts
			for i := range ch.Options {
				ch.Options[i].To = b.latest(ctx, d.Ecosystem, ch.Options[i].ToName)
			}
		}
	}
	if name == "" {
		return ch
	}
	ch.ToName = name
	if to == "" {
		to = b.latest(ctx, d.Ecosystem, name)
	}
	ch.To = to
	return ch
}

// latest is the newest stable release of a package the project may not use
// yet, such as the replacement for a deprecated one.
func (b *builder) latest(ctx context.Context, eco, name string) string {
	p := pkg{eco, name}
	b.metas(ctx, []pkg{p})
	m := b.meta[p]
	if m == nil {
		return ""
	}
	rel, ok := candidate(core.Dep{Ecosystem: eco, Name: name, Version: "0"}, m, b.in.Now, 0, -1)
	if !ok {
		return ""
	}
	return rel.Version
}

// steps are the rungs a major bump of more than one major can climb when
// the jump to its target fails: the release candidate would pick within
// each major in between, ascending. A runtime or an image is not climbed:
// the lines in between are past their end of life too.
func (b *builder) steps(ch *core.Change) []string {
	if ch.API != nil || ch.ToName != "" || ch.To == "" || len(ch.Deps) == 0 || traits(ch.Deps[0]).Tooling {
		return nil
	}
	d := ch.Deps[0]
	cur, ok1 := ver.Parse(Current(d))
	to, ok2 := ver.Parse(ch.To)
	m := b.meta[pkg{d.Ecosystem, d.Name}]
	if !ok1 || !ok2 || m == nil || to.Major()-cur.Major() < 2 {
		return nil
	}
	var res []string
	for major := cur.Major() + 1; major < to.Major(); major++ {
		rel, ok := candidate(d, m, b.in.Now, b.cooldown(), major)
		if v, _ := ver.Parse(rel.Version); ok && v.Major() == major {
			res = append(res, rel.Version)
		}
	}
	return res
}

// typesCap keeps @types/x on the major of x: the typings follow the package,
// never lead it.
func (b *builder) typesCap(d core.Dep) int {
	if d.Ecosystem != core.Npm {
		return -1
	}
	rest, ok := strings.CutPrefix(d.Name, "@types/")
	if !ok {
		return -1
	}
	name := rest
	if scope, n, ok := strings.Cut(rest, "__"); ok {
		name = "@" + scope + "/" + n
	}
	for _, x := range b.in.Inv.Deps {
		if x.Ecosystem == core.Npm && x.Name == name {
			if v, ok := ver.Parse(Current(x)); ok {
				return v.Major()
			}
		}
	}
	return -1
}

func deprecated(d core.Dep, version, msg string) core.Evidence {
	what := d.Name
	if version != "" {
		what += " " + version
	}
	return core.Evidence{
		Source: "registry",
		Title:  what + " is deprecated",
		Text:   core.Clip(msg, 1200),
		URL:    page(d),
	}
}

// page is where a person reads about the package d names.
func page(d core.Dep) string {
	if p := traits(d).Page; p != nil {
		return p(d.Name)
	}
	return ""
}
