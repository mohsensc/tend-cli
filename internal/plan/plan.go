// Package plan turns an inventory and upstream changes into one ranked queue.
package plan

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/api"
	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/eco"
	"github.com/mohsensc/tend-cli/internal/scan"
	"github.com/mohsensc/tend-cli/internal/source"
	"github.com/mohsensc/tend-cli/internal/state"
	"github.com/mohsensc/tend-cli/internal/ver"
)

type Plan struct {
	Queue     []core.Change // ranked, deliverable
	Dropped   []Drop
	Notes     []string              // unmapped endpoints, registry errors, skipped deps
	Untracked []Untracked           // what tend saw and cannot follow
	Open      *core.PR              // tend's open PR, if any
	Metas     map[string]*core.Meta `json:"-"` // registry metadata by "<eco>/<name>"
}

// Untracked is something the project uses that tend cannot follow, why, and
// the tend.yml entry that would change that, when there is one.
type Untracked struct {
	What, Why string
	Key, Fix  string // Fix is one entry of tend.yml's Key list
}

type Drop struct {
	Change core.Change
	Reason string // noise|ignored|cooldown|parked|unsupported
	Detail string
}

// Input is what Build plans from. Ecos defaults to every ecosystem tend knows.
type Input struct {
	Inv     *core.Inventory
	Cfg     *config.Config
	State   *state.State
	Fetch   core.Fetcher
	Ecos    map[string]core.Ecosystem
	Sources []core.Source
	Specs   *api.Specs // among Sources, for the notes it keeps
	Now     time.Time
	Base    string // the base commit, for verdicts that hold only on it
}

// Inventory reads every manifest of the enabled ecosystems under root and scans
// the code. A manifest that cannot be read is untracked, not a failure.
func Inventory(root string, cfg *config.Config) (*core.Inventory, []Untracked, error) {
	inv := &core.Inventory{Root: root}
	done, err := eco.Keep(root)
	if err != nil {
		return nil, nil, err
	}
	defer done()
	var notes []Untracked
	for _, e := range eco.For(cfg) {
		ms, err := e.Detect(root)
		if err != nil {
			notes = append(notes, Untracked{What: e.Name() + " manifests", Why: err.Error()})
			continue
		}
		for _, m := range ms {
			deps, err := e.Deps(root, m)
			if err != nil {
				notes = append(notes, Untracked{What: m.Path, Why: "cannot be read: " + err.Error()})
				continue
			}
			inv.Manifests = append(inv.Manifests, m)
			inv.Deps = append(inv.Deps, deps...)
		}
	}
	for _, p := range eco.Submodules(root) {
		notes = append(notes, Untracked{What: p, Why: "a git submodule, pinned to a commit; tend does not move submodules"})
	}
	files, err := eco.Files(root)
	if err != nil {
		return nil, nil, err
	}
	idx, err := scan.Build(scan.Options{Root: root, Files: files})
	if err != nil {
		return nil, nil, fmt.Errorf("scan: %w", err)
	}
	inv.Index = idx
	inv.Providers = api.Detect(inv, Providers(cfg))
	return inv, notes, nil
}

// Providers is the built-in API catalog with tend.yml's apis applied.
func Providers(cfg *config.Config) []api.Provider { return api.Merge(api.Catalog(), cfg.APIs) }

// Discover looks for specs for the hosts the code calls that no provider
// covers, that are not registries tend reads anyway, and that tend.yml does
// not ignore, keeps what it learns in st, and
// returns the providers it found. Inventory's providers gain them; a host
// with no spec is untracked.
func Discover(ctx context.Context, inv *core.Inventory, cfg *config.Config, st *state.State, f core.Fetcher, now time.Time) ([]api.Provider, []Untracked) {
	hosts := slices.DeleteFunc(api.Unknown(inv, Providers(cfg)), func(h string) bool {
		if slices.Contains(eco.Registries, h) {
			return true
		}
		_, ok := cfg.Ignored(core.Dep{Ecosystem: "api", Name: h}, "")
		return ok
	})
	if len(hosts) == 0 {
		return nil, nil
	}
	found := api.Discover(ctx, f, hosts, st.Found, now)
	for h, x := range found {
		st.Found[h] = x
	}
	extra := api.Discovered(found)
	inv.Providers = append(inv.Providers, api.Detect(inv, extra)...)
	var res []Untracked
	for _, h := range hosts {
		if found[h].Spec == "" {
			res = append(res, Untracked{What: h, Why: "called by the code, with no OpenAPI spec tend could find; a spec or changelog URL in tend.yml fixes that", Key: "apis", Fix: api.Suggest(h)})
		}
	}
	return extra, res
}

// Sources are the upstream sources a plan consults, in the order they run,
// and the API specs among them. dir is the repo's state dir, where spec
// snapshots live; m, when set, reads the changelog entries plain matching
// cannot; extra are the providers discovery found.
func Sources(dir string, cfg *config.Config, f core.Fetcher, m core.Model, now func() time.Time, extra ...api.Provider) ([]core.Source, *api.Specs) {
	up := eco.Upstream(cfg.Deps)
	cat := append(Providers(cfg), extra...)
	specs := &api.Specs{Cat: cat, Fetch: f, Dir: filepath.Join(dir, "specs"), Now: now}
	res := []core.Source{
		&source.OSV{Fetch: f, Upstream: up},
		specs,
		&api.Changelogs{Cat: cat, Fetch: f, Model: m, Dir: dir},
		&source.EOL{Fetch: f, Now: now},
	}
	var probe []string
	for _, a := range cfg.APIs {
		if a.Probe {
			probe = append(probe, a.Name)
		}
	}
	if len(probe) > 0 {
		res = append(res, &source.Headers{Providers: probe})
	}
	return res, specs
}

// Snapshot keeps the specs this plan read as the baseline for the next one,
// and notes each in st. Call it once the run that used the plan has finished.
func Snapshot(sp *api.Specs, inv *core.Inventory, st *state.State, now time.Time) error {
	if sp == nil {
		return nil
	}
	var errs []error
	for _, pu := range inv.Providers {
		doc := sp.Current(pu.Provider)
		if doc == nil {
			continue
		}
		if err := sp.Snapshot(pu.Provider, doc); err != nil {
			errs = append(errs, err)
			continue
		}
		st.Specs[pu.Provider] = state.Snap{At: now, File: sp.File(pu.Provider)}
	}
	return errors.Join(errs...)
}

type builder struct {
	in    Input
	ecos  map[string]core.Ecosystem
	meta  map[pkg]*core.Meta
	asked map[pkg]bool // looked up, found or not
	notes []string
	ev    map[string][]core.Evidence // release notes already fetched, by change id
}

func (b *builder) note(format string, args ...any) {
	b.notes = append(b.notes, fmt.Sprintf(format, args...))
}

func (b *builder) cooldown() time.Duration { return time.Duration(b.in.Cfg.Cooldown) }

// Build asks the registries and every source what changed, keeps what matters
// to this project and ranks it.
func Build(ctx context.Context, in Input) (*Plan, error) {
	b := &builder{in: in, ecos: in.Ecos, meta: map[pkg]*core.Meta{}, asked: map[pkg]bool{}, ev: map[string][]core.Evidence{}}
	if b.ecos == nil {
		b.ecos = map[string]core.Ecosystem{}
		for _, e := range eco.For(in.Cfg) {
			b.ecos[e.Name()] = e
		}
	}
	b.metas(ctx, b.direct())
	found := b.sources(ctx)
	all := append(b.registryPass(ctx, found), found...)
	all = b.fixes(merge(all))
	all = b.companions(b.group(all))
	for i := range all {
		all[i].ID = core.ChangeID(&all[i])
		all[i].Sites = b.sites(&all[i])
		// A major bump nothing in the code uses is a plain bump.
		if all[i].Kind == core.Breaking && all[i].API == nil && len(all[i].Sites) == 0 {
			all[i].Kind = core.Update
		}
		b.workflowCommands(&all[i])
		deprecationOptions(&all[i])
		all[i].Steps = b.steps(&all[i])
	}
	b.mentions(ctx, all)

	p := &Plan{Metas: map[string]*core.Meta{}}
	for k, m := range b.meta {
		p.Metas[k.String()] = m
	}
	for _, ch := range all {
		reason, detail := b.drop(&ch)
		if reason != "" {
			p.Dropped = append(p.Dropped, Drop{Change: ch, Reason: reason, Detail: detail})
			continue
		}
		p.Queue = append(p.Queue, ch)
	}
	Rank(p.Queue, in.Now)
	slices.SortFunc(p.Dropped, func(x, y Drop) int {
		if c := strings.Compare(x.Reason, y.Reason); c != 0 {
			return c
		}
		return compare(&x.Change, &y.Change, in.Now)
	})
	for i := range min(5, len(p.Queue)) {
		b.attachNotes(ctx, &p.Queue[i])
	}
	b.record(p)
	slices.Sort(b.notes)
	p.Notes = slices.Compact(b.notes)
	return p, nil
}

// forget is how long a change nobody has seen stays in state.
const forget = 180 * 24 * time.Hour

func (b *builder) record(p *Plan) {
	st := b.in.State
	if st == nil {
		return
	}
	for id, s := range st.Seen {
		if b.in.Now.Sub(s.Last) > forget {
			delete(st.Seen, id)
		}
	}
	for _, ch := range p.Queue {
		st.See(ch.ID, "queued", "", b.in.Now)
	}
	for _, d := range p.Dropped {
		st.See(d.Change.ID, d.Reason, d.Detail, b.in.Now)
	}
}

// sources runs every source; one failing entirely is a note and the plan goes
// on without it.
func (b *builder) sources(ctx context.Context) []core.Change {
	var res []core.Change
	for _, s := range b.in.Sources {
		chs, err := s.Changes(ctx, b.in.Inv)
		if err != nil {
			b.note("%s: unavailable, planned without it: %v", s.Name(), err)
			continue
		}
		res = append(res, chs...)
		if c, ok := s.(*api.Changelogs); ok {
			b.notes = append(b.notes, c.Notes()...)
		}
	}
	if b.in.Specs != nil {
		b.notes = append(b.notes, b.in.Specs.Notes()...)
	}
	return res
}

// direct lists the packages the project depends on by name.
func (b *builder) direct() []pkg {
	var res []pkg
	seen := map[pkg]bool{}
	for _, d := range b.in.Inv.Deps {
		p := pkg{d.Ecosystem, d.Name}
		if d.Direct && !seen[p] {
			seen[p] = true
			res = append(res, p)
		}
	}
	return res
}

// registryPass looks up every transitive dep a security source named, on top
// of the direct ones already known, and proposes a bump for each direct dep.
func (b *builder) registryPass(ctx context.Context, found []core.Change) []core.Change {
	var pkgs []pkg
	for _, ch := range found {
		if ch.Kind == core.Security {
			for _, d := range ch.Deps {
				pkgs = append(pkgs, pkg{d.Ecosystem, d.Name})
			}
		}
	}
	b.metas(ctx, pkgs)

	gone := b.archived(ctx)
	var res []core.Change
	for _, d := range b.in.Inv.Deps {
		m := b.meta[pkg{d.Ecosystem, d.Name}]
		if !d.Direct || m == nil {
			continue
		}
		if ev, ok := gone[m.Repo]; ok && m.Deprecated == "" {
			res = append(res, *b.swap(ctx, d, m, ev))
			continue
		}
		if ch := b.registry(ctx, d, m); ch != nil {
			res = append(res, *ch)
		}
	}
	return res
}

// archived finds the direct deps whose source repository is archived: a
// package nobody maintains, whatever its registry says.
func (b *builder) archived(ctx context.Context) map[string]core.Evidence {
	var repos []string
	for _, d := range b.in.Inv.Deps {
		if m := b.meta[pkg{d.Ecosystem, d.Name}]; d.Direct && m != nil && m.Repo != "" {
			repos = append(repos, m.Repo)
		}
	}
	res, err := source.Archived(ctx, b.in.Fetch, repos)
	if err != nil {
		b.note("archived: unavailable, planned without it: %v", err)
	}
	return res
}

// fixes moves each security change to the smallest release that can actually
// be installed and sets its level, which may be major.
func (b *builder) fixes(chs []core.Change) []core.Change {
	for i := range chs {
		ch := &chs[i]
		if ch.Kind != core.Security || len(ch.Deps) == 0 || ch.ToName != "" {
			continue
		}
		d := ch.Deps[0]
		if rel, ok := fixFor(b.meta[pkg{d.Ecosystem, d.Name}], ch.To); ok {
			ch.To = rel.Version
		}
		ch.Level = ver.Step(Lowest(ch.Deps), ch.To)
	}
	return merge(chs)
}

// Lowest is the version the least advanced of deps is at, as Current reads it.
func Lowest(deps []core.Dep) string {
	res := ""
	for _, d := range deps {
		if v := Current(d); res == "" || ver.Less(v, res) {
			res = v
		}
	}
	return res
}

// sites are the places in this repo a change touches: call sites of the
// package, or, for what code cannot import (a base image, an Action, a
// runtime pin), the line that names it.
func (b *builder) sites(ch *core.Change) []core.Site {
	res := slices.Clone(ch.Sites)
	for _, d := range ch.Deps {
		e := b.ecos[d.Ecosystem]
		if e == nil {
			continue
		}
		imports := e.Imports(d)
		switch {
		case len(imports) == 0:
			res = append(res, manifestSite(b.in.Inv.Root, d))
		case b.in.Inv.Index != nil:
			res = append(res, b.in.Inv.Index.Uses(imports)...)
		}
	}
	return sortSites(res)
}

func manifestSite(root string, d core.Dep) core.Site {
	s := core.Site{File: d.Manifest.Path, Line: d.Line}
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(d.Manifest.Path)))
	if err != nil || d.Line < 1 {
		return s
	}
	lines := strings.Split(string(b), "\n")
	if d.Line <= len(lines) {
		s.Text = core.Clip(strings.TrimSpace(lines[d.Line-1]), 200)
	}
	return s
}

func sortSites(sites []core.Site) []core.Site {
	slices.SortFunc(sites, func(a, b core.Site) int {
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return a.Col - b.Col
	})
	return slices.CompactFunc(sites, func(a, b core.Site) bool {
		return a.File == b.File && a.Line == b.Line && a.Col == b.Col
	})
}

// workflowCommands moves a workflow off the commands GitHub retired in the
// same PR that bumps its Actions, with the adapter's codemod as a safe option.
func (b *builder) workflowCommands(ch *core.Change) {
	if ch.To == "" || len(ch.Deps) == 0 || ch.Deps[0].Ecosystem != core.Actions {
		return
	}
	text := map[string]string{}
	var found []core.Site
	seen := map[string]bool{}
	for _, d := range ch.Deps {
		f := d.Manifest.Path
		if seen[f] {
			continue
		}
		seen[f] = true
		src, err := os.ReadFile(filepath.Join(b.in.Inv.Root, filepath.FromSlash(f)))
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(src), "\n") {
			cm := eco.RetiredCommands([]string{line})
			if cm == nil {
				continue
			}
			maps.Copy(text, cm.Text)
			found = append(found, core.Site{File: f, Line: i + 1, Text: core.Clip(strings.TrimSpace(line), 200)})
		}
	}
	if len(found) == 0 {
		return
	}
	ch.Sites = sortSites(append(ch.Sites, found...))
	ch.Evidence = addEvidence(ch.Evidence, core.Evidence{
		Source: "table",
		Title:  "GitHub Actions retired the set-output and save-state commands",
		URL:    "https://github.blog/changelog/2022-10-11-github-actions-deprecating-save-state-and-set-output-commands/",
	})
	ch.Options = append(ch.Options, core.Option{
		Title:   "Write step outputs and state to environment files",
		Detail:  "The same values go to $GITHUB_OUTPUT and $GITHUB_STATE, which every current runner supports.",
		Safe:    true,
		To:      ch.To,
		Codemod: &core.Codemod{Text: text},
	})
}

// mentions reads the release notes of the top updates that touch code and
// promotes those whose notes deprecate, break or remove a symbol the code uses.
func (b *builder) mentions(ctx context.Context, chs []core.Change) {
	var idx []int
	for i := range chs {
		if chs[i].Kind == core.Update && len(chs[i].Sites) > 0 {
			idx = append(idx, i)
		}
	}
	slices.SortStableFunc(idx, func(x, y int) int { return compare(&chs[x], &chs[y], b.in.Now) })
	for _, i := range idx[:min(5, len(idx))] {
		ch := &chs[i]
		hits := source.Mentions(b.notesFor(ctx, ch), ch.Sites)
		if len(hits) == 0 {
			continue
		}
		ch.Kind = core.Breaking
		for _, ev := range hits {
			if strings.Contains(strings.ToLower(ev.Title+" "+ev.Text), "deprecat") {
				ch.Kind = core.Deprecation
				break
			}
		}
		ch.Evidence = addEvidence(ch.Evidence, hits...)
	}
}

func (b *builder) attachNotes(ctx context.Context, ch *core.Change) {
	ch.Evidence = addEvidence(ch.Evidence, b.notesFor(ctx, ch)...)
}

// notesFor fetches the release notes between the current version and the
// target, once per change.
func (b *builder) notesFor(ctx context.Context, ch *core.Change) []core.Evidence {
	if ev, ok := b.ev[ch.ID]; ok {
		return ev
	}
	b.ev[ch.ID] = nil
	if ch.API != nil || len(ch.Deps) == 0 || ch.To == "" || ch.ToName != "" {
		return nil
	}
	d := ch.Deps[0]
	m := b.meta[pkg{d.Ecosystem, d.Name}]
	if m == nil || m.Repo == "" {
		return nil
	}
	ev, err := source.Notes(ctx, b.in.Fetch, m, Lowest(ch.Deps), ch.To)
	if errors.Is(err, core.ErrNotSupported) {
		return nil
	}
	if err != nil {
		b.note("release notes: %s: %v", d.Name, err)
		return nil
	}
	b.ev[ch.ID] = ev
	return ev
}
