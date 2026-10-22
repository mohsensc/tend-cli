package plan

import (
	"cmp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/source"
	"github.com/mohsensc/tend-cli/internal/ver"
)

// depSet names the packages a change moves, independent of which manifests
// they appear in.
func depSet(ch *core.Change) string {
	var names []string
	for _, d := range ch.Deps {
		names = append(names, d.Ecosystem+"/"+d.Name)
	}
	slices.Sort(names)
	return strings.Join(slices.Compact(names), " ")
}

// mergeKey is equal for changes that are one piece of work. A field or
// parameter that changed on several operations is one change to the code
// that uses it.
func mergeKey(ch *core.Change) string {
	if a := ch.API; a != nil {
		if a.Field != "" {
			return "api/" + a.Provider + "#" + a.Field + " " + a.What
		}
		return core.ChangeID(ch)
	}
	return depSet(ch) + " ->" + ch.ToName + "@" + ch.To
}

// merge collapses changes that move the same packages to the same place.
// Security changes on the same packages first collapse onto the highest fixed
// version any advisory names, since only that one fixes them all.
func merge(chs []core.Change) []core.Change {
	chs = slices.Clone(chs)
	fix := map[string]string{}
	for _, ch := range chs {
		if ch.Kind != core.Security || ch.API != nil {
			continue
		}
		k := depSet(&ch) + " ->" + ch.ToName
		if to, ok := fix[k]; !ok || ver.Less(to, ch.To) {
			fix[k] = ch.To
		}
	}
	for i := range chs {
		if chs[i].Kind == core.Security && chs[i].API == nil {
			chs[i].To = fix[depSet(&chs[i])+" ->"+chs[i].ToName]
		}
	}

	var res []core.Change
	at := map[string]int{}
	for _, ch := range chs {
		k := mergeKey(&ch)
		if i, ok := at[k]; ok {
			res[i] = combine(res[i], ch)
			continue
		}
		at[k] = len(res)
		ch.Deps = unionDeps(nil, ch.Deps)
		res = append(res, ch)
	}
	return res
}

// combine folds b into a: the stronger kind and the higher severity win,
// everything else is a union.
func combine(a, b core.Change) core.Change {
	a.Kind = stronger(a, b)
	if sevRank(b.Severity) < sevRank(a.Severity) {
		a.Severity = b.Severity
	}
	if a.ID == "" {
		a.ID = b.ID
	}
	if a.Level == "" {
		a.Level = b.Level
	}
	if a.ToName == "" {
		a.ToName = b.ToName
	}
	if a.API == nil {
		a.API = b.API
	}
	a.Deps = unionDeps(a.Deps, b.Deps)
	a.Evidence = addEvidence(a.Evidence, b.Evidence...)
	a.Options = slices.Clone(a.Options)
	for _, o := range b.Options {
		if !slices.ContainsFunc(a.Options, func(x core.Option) bool { return x.Title == o.Title }) {
			a.Options = append(a.Options, o)
		}
	}
	a.Sunset = earliest(a.Sunset, b.Sunset)
	a.Published = earliest(a.Published, b.Published)
	a.Sites = sortSites(append(slices.Clone(a.Sites), b.Sites...))
	return a
}

// stronger is the better reason of two to make one change. A registry major
// bump is breaking only by its version number, so when the version in use is
// deprecated or end of life, that is the reason given.
func stronger(a, b core.Change) core.Kind {
	bump := func(ch core.Change) bool { return ch.Kind == core.Breaking && ch.API == nil }
	if a.Kind == core.Deprecation && bump(b) || b.Kind == core.Deprecation && bump(a) {
		return core.Deprecation
	}
	if kindRank(b.Kind) < kindRank(a.Kind) {
		return b.Kind
	}
	return a.Kind
}

func earliest(a, b time.Time) time.Time {
	if a.IsZero() || !b.IsZero() && b.Before(a) {
		return b
	}
	return a
}

// unionDeps keeps one dep per place it is written: a base image named on two
// FROM lines of one Dockerfile is two deps, each bumped on its own line.
func unionDeps(a, b []core.Dep) []core.Dep {
	res := slices.Clone(a)
	for _, d := range b {
		if !slices.ContainsFunc(res, func(x core.Dep) bool {
			return x.Ecosystem == d.Ecosystem && x.Name == d.Name && x.Manifest.Path == d.Manifest.Path && x.Line == d.Line
		}) {
			res = append(res, d)
		}
	}
	slices.SortStableFunc(res, func(x, y core.Dep) int {
		if c := strings.Compare(x.Name, y.Name); c != 0 {
			return c
		}
		if c := strings.Compare(x.Manifest.Path, y.Manifest.Path); c != 0 {
			return c
		}
		return x.Line - y.Line
	})
	return res
}

// addEvidence appends what is new: an item is old when it links the same URL.
func addEvidence(have []core.Evidence, more ...core.Evidence) []core.Evidence {
	res := slices.Clone(have)
	for _, ev := range more {
		if ev.URL == "" || !slices.ContainsFunc(res, func(x core.Evidence) bool { return x.URL == ev.URL }) {
			res = append(res, ev)
		}
	}
	return res
}

// group joins bumps of different packages that move to the same version
// together: those a tend.yml group matches, and those released from the same
// source repo.
func (b *builder) group(chs []core.Change) []core.Change {
	type key struct{ eco, group, repo, to string }
	members := map[key][]int{}
	var keys []key
	for i := range chs {
		ch := &chs[i]
		if !groupable(ch) {
			continue
		}
		d := ch.Deps[0]
		k := key{eco: d.Ecosystem, to: ch.To}
		if g := configGroup(b.in.Cfg.Groups, d.Name); g != "" {
			k.group = g
		} else if m := b.meta[pkg{d.Ecosystem, d.Name}]; m != nil && m.Repo != "" {
			k.repo = repoKey(m.Repo)
		} else {
			continue
		}
		if members[k] == nil {
			keys = append(keys, k)
		}
		members[k] = append(members[k], i)
	}

	gone := map[int]bool{}
	for _, k := range keys {
		idx := members[k]
		if len(idx) < 2 {
			continue
		}
		g := chs[idx[0]]
		g.ID = ""
		if k.group != "" {
			g.ID = k.eco + "/group:" + k.group + "@" + k.to
		}
		for _, i := range idx[1:] {
			g = combine(g, chs[i])
			gone[i] = true
		}
		chs[idx[0]] = g
	}
	var res []core.Change
	for i, ch := range chs {
		if !gone[i] {
			res = append(res, ch)
		}
	}
	return res
}

// companions adds to a plain bump of one npm package the direct deps of the
// same manifests that only resolve at its version: a package whose release in
// use pins it as a peer at exactly the version in use, or one of its scope
// family (vitest and @vitest/coverage-v8) at that same version. Each moves to
// the same target, when it has that release, in the same change; a change of
// its own to that target folds in.
func (b *builder) companions(chs []core.Change) []core.Change {
	gone := map[int]bool{}
	// a security fix takes its companions first, and names the change
	order := make([]int, len(chs))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(x, y int) int { return kindRank(chs[x].Kind) - kindRank(chs[y].Kind) })
	for _, i := range order {
		ch := &chs[i]
		if gone[i] || !groupable(ch) || ch.Deps[0].Ecosystem != core.Npm {
			continue
		}
		d := ch.Deps[0]
		cur := Current(d)
		paths := map[string]bool{}
		for _, x := range ch.Deps {
			paths[x.Manifest.Path] = true
		}
		var add []core.Dep
		for _, c := range b.in.Inv.Deps {
			if !c.Direct || c.Ecosystem != d.Ecosystem || c.Name == d.Name || !paths[c.Manifest.Path] {
				continue
			}
			m := b.meta[pkg{c.Ecosystem, c.Name}]
			if m == nil {
				continue
			}
			at, ok := release(m, Current(c))
			if !ok {
				continue
			}
			pinned := at.Peers[d.Name] == cur
			kin := family(c.Name) == family(d.Name) && Current(c) == cur
			if to, ok := release(m, ch.To); (pinned || kin) && ok && !to.Yanked {
				add = append(add, c)
			}
		}
		if len(add) == 0 {
			continue
		}
		ch.ID = d.Ecosystem + "/group:" + d.Name + "@" + ch.To
		ch.Deps = append(ch.Deps, add...)
		for j := range chs {
			if j != i && !gone[j] && groupable(&chs[j]) && chs[j].To == ch.To && slices.ContainsFunc(add, func(c core.Dep) bool { return c.Name == chs[j].Deps[0].Name }) {
				*ch = combine(*ch, chs[j])
				gone[j] = true
			}
		}
		// the package the change is about stays first, for its notes and title
		slices.SortStableFunc(ch.Deps, func(x, y core.Dep) int {
			return cmp.Compare(boolInt(x.Name != d.Name), boolInt(y.Name != d.Name))
		})
	}
	var res []core.Change
	for i, ch := range chs {
		if !gone[i] {
			res = append(res, ch)
		}
	}
	return res
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// family is an npm package's name without its scope's punctuation: vitest
// for vitest and for @vitest/coverage-v8.
func family(name string) string {
	if scope, _, ok := strings.Cut(strings.TrimPrefix(name, "@"), "/"); ok && strings.HasPrefix(name, "@") {
		return scope
	}
	return name
}

// groupable is a plain bump of one package, in one or more manifests.
func groupable(ch *core.Change) bool {
	if ch.API != nil || ch.ToName != "" || ch.To == "" || len(ch.Deps) == 0 {
		return false
	}
	return !strings.Contains(depSet(ch), " ")
}

func configGroup(groups []config.Group, name string) string {
	for _, g := range groups {
		for _, m := range g.Match {
			if config.Glob(m, name) {
				return g.Name
			}
		}
	}
	return ""
}

func repoKey(s string) string {
	if owner, name, ok := source.ParseRepo(s); ok {
		return strings.ToLower(owner + "/" + name)
	}
	return strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(strings.TrimPrefix(s, "git+")), "/"), ".git")
}

// kindRank puts tend's first PR ahead of everything: the security fixes it
// can make are part of it.
func kindRank(k core.Kind) int {
	switch k {
	case core.First:
		return 0
	case core.Security:
		return 1
	case core.Breaking:
		return 2
	case core.Deprecation:
		return 3
	}
	return 4
}

func sevRank(s string) int {
	switch strings.ToLower(s) {
	case "critical":
		return 0
	case "high":
		return 1
	case "moderate", "medium":
		return 2
	case "low":
		return 3
	}
	return 4
}

// soon is how close a sunset has to be for a deprecation to rank as breaking.
const soon = 30 * 24 * time.Hour

// Rank orders a queue: kind (the first PR, security, a removed API, a
// deprecation, a major bump, an update), severity, nearest sunset, most
// sites, oldest upstream change, then id so the order never depends on map
// iteration.
func Rank(chs []core.Change, now time.Time) {
	sort.SliceStable(chs, func(i, j int) bool { return compare(&chs[i], &chs[j], now) < 0 })
}

func compare(a, b *core.Change, now time.Time) int {
	if c := effectiveRank(a, now) - effectiveRank(b, now); c != 0 {
		return c
	}
	if c := sevRank(a.Severity) - sevRank(b.Severity); c != 0 {
		return c
	}
	if c := zeroLast(a.Sunset, b.Sunset); c != 0 {
		return c
	}
	if c := len(b.Sites) - len(a.Sites); c != 0 {
		return c
	}
	if c := zeroLast(a.Published, b.Published); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

// effectiveRank is the kind's place in the queue with two refinements: a
// deprecation whose sunset is near or past is as urgent as a break, and a
// major bump that only its version number calls breaking is less urgent than
// moving off something deprecated, since nothing is broken until it lands.
func effectiveRank(ch *core.Change, now time.Time) int {
	switch {
	case ch.Kind == core.Deprecation && !ch.Sunset.IsZero() && ch.Sunset.Sub(now) <= soon:
		return 2 * kindRank(core.Breaking)
	case ch.Kind == core.Breaking && ch.API == nil:
		return 2*kindRank(core.Deprecation) + 1
	}
	return 2 * kindRank(ch.Kind)
}

func zeroLast(a, b time.Time) int {
	switch {
	case a.Equal(b):
		return 0
	case a.IsZero():
		return 1
	case b.IsZero():
		return -1
	}
	return a.Compare(b)
}
