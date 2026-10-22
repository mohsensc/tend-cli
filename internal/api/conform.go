package api

import (
	"cmp"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/fetch"
	"github.com/mohsensc/tend-cli/internal/scan"
)

// Usage is one operation the project calls, with every place it calls it from.
type Usage struct {
	Method, Path string
	Sites        []core.Site
	URLs         []core.URLUse
	Files        []string
}

// Specs is the core.Source that checks the operations a project calls against
// the providers' published specs, and against the snapshot from the last run.
type Specs struct {
	Cat   []Provider
	Fetch core.Fetcher
	Dir   string // snapshots, one <provider>.json each
	Now   func() time.Time

	notes []string
	runs  map[string]*run
}

// run is what Changes learned about one provider, kept for Snapshot.
type run struct {
	cur, old *Doc
	used     []string
	pinned   map[string]bool // ops whose breaking changes must survive the next snapshot
}

var _ core.Source = (*Specs)(nil)

const specTTL = 24 * time.Hour

func (s *Specs) Name() string { return "openapi" }

// Notes lists what the last Changes could not check: specs that failed to load
// and endpoints found in neither the spec nor the snapshot.
func (s *Specs) Notes() []string { return slices.Clone(s.notes) }

// Current is the spec the last Changes fetched for a provider, nil if none.
func (s *Specs) Current(provider string) *Doc {
	if r := s.runs[provider]; r != nil {
		return r.cur
	}
	return nil
}

func (s *Specs) Changes(ctx context.Context, inv *core.Inventory) ([]core.Change, error) {
	s.notes, s.runs = nil, map[string]*run{}
	uses := inv.Providers
	if uses == nil {
		uses = Detect(inv, s.Cat)
	}
	byID := map[string]*core.Change{}
	for _, pu := range uses {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		i := slices.IndexFunc(s.Cat, func(p Provider) bool { return p.Name == pu.Provider })
		if i < 0 {
			continue
		}
		p := &s.Cat[i]
		if p.Spec == "" {
			continue
		}
		cur, err := s.spec(ctx, inv.Root, p)
		if err != nil {
			s.note("%s: %v", p.Name, err)
			continue
		}
		if n := len(cur.Missing); n > 0 {
			s.note("%s: %d unresolved $ref in the spec, first %s", p.Name, n, cur.Missing[0])
		}
		r := &run{cur: cur, old: s.snapshot(p.Name), pinned: map[string]bool{}}
		s.runs[p.Name] = r
		used := s.usages(p, pu, r)
		for _, u := range used {
			r.used = append(r.used, u.Method+" "+u.Path)
		}
		for _, c := range check(p, inv.Index, used, r) {
			if prev, ok := byID[c.ID]; ok {
				c = combine(*prev, c)
			}
			byID[c.ID] = &c
		}
	}
	res := make([]core.Change, 0, len(byID))
	for _, id := range slices.Sorted(maps.Keys(byID)) {
		res = append(res, *byID[id])
	}
	return res, nil
}

func (s *Specs) note(format string, args ...any) {
	s.notes = append(s.notes, fmt.Sprintf(format, args...))
}

func (s *Specs) spec(ctx context.Context, root string, p *Provider) (*Doc, error) {
	if p.Spec == "" {
		return nil, errors.New("no spec configured")
	}
	if !remote(p.Spec) {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p.Spec)))
		if err != nil {
			return nil, err
		}
		return load(b, p.Sunset)
	}
	if s.Fetch == nil {
		return nil, errors.New("no fetcher")
	}
	res, err := s.Fetch.Get(ctx, p.Spec, fetch.TTL(specTTL))
	if err != nil {
		return nil, err
	}
	if res.Status != 200 {
		return nil, fmt.Errorf("%s: status %d", p.Spec, res.Status)
	}
	return load(res.Body, p.Sunset)
}

func remote(spec string) bool {
	return strings.HasPrefix(spec, "https://") || strings.HasPrefix(spec, "http://")
}

// File is where the snapshot of a provider's spec is kept.
func (s *Specs) File(provider string) string {
	return filepath.Join(s.Dir, core.Slug(provider)+".json")
}

func (s *Specs) snapshot(provider string) *Doc {
	if s.Dir == "" {
		return nil
	}
	b, err := os.ReadFile(s.File(provider))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err == nil {
		var d *Doc
		if d, err = Load(b); err == nil {
			return d
		}
	}
	s.note("%s: snapshot: %v", provider, err)
	return nil
}

// Snapshot records the operations the last Changes saw in use, as doc has them,
// for the next run to diff against. An operation with an unresolved breaking
// change keeps its old version, so the change stays visible until the code no
// longer depends on it. Call it only after a run completed, so a crash never
// loses the old snapshot.
func (s *Specs) Snapshot(provider string, doc *Doc) error {
	r := s.runs[provider]
	if doc == nil || r == nil {
		return fmt.Errorf("api: %s: nothing checked to snapshot", provider)
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	snap := &Doc{Version: doc.Version, Base: doc.Base, Ops: map[string]*Op{}, At: now()}
	for _, k := range r.used {
		o := doc.Ops[k]
		if old := op(r.old, k); old != nil && (o == nil || r.pinned[k]) {
			o = old
		}
		if o != nil {
			snap.Ops[k] = o
		}
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("api: %w", err)
	}
	dst := s.File(provider)
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, trimSpec(snap, nil), 0o644); err != nil {
		return fmt.Errorf("api: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("api: %w", err)
	}
	return nil
}

// usages maps every URL literal and SDK call of a provider to an operation in
// the current spec, else the snapshot. SDK chains are guesses and drop out
// quietly, and so do path literals, whose host is only a guess from the file
// they sit in, and base URLs; a full URL that maps to nothing becomes a note.
func (s *Specs) usages(p *Provider, pu core.ProviderUse, r *run) []Usage {
	byKey := map[string]*Usage{}
	at := func(o *Op) *Usage {
		key := o.Method + " " + o.Path
		if byKey[key] == nil {
			byKey[key] = &Usage{Method: o.Method, Path: o.Path}
		}
		return byKey[key]
	}
	for _, u := range pu.URLs {
		o := lookup(r, u.Method, u.URL, false)
		if o == nil {
			if endpoint(u.URL) && !r.base(u.URL) {
				s.note("%s: unmapped endpoint %s at %s:%d", p.Name, strings.TrimSpace(u.Method+" "+u.URL), u.File, u.Line)
			}
			continue
		}
		us := at(o)
		us.URLs = append(us.URLs, u)
		us.Files = append(us.Files, u.File)
	}
	for _, site := range pu.Sites {
		if !site.Call {
			continue
		}
		method, path, ok := p.Operation(site.Chain, scan.Lang(site.File))
		if !ok {
			continue
		}
		if o := lookup(r, method, path, true); o != nil {
			us := at(o)
			us.Sites = append(us.Sites, site)
			us.Files = append(us.Files, site.File)
		}
	}
	res := make([]Usage, 0, len(byKey))
	for _, k := range slices.Sorted(maps.Keys(byKey)) {
		u := byKey[k]
		slices.Sort(u.Files)
		u.Files = slices.Compact(u.Files)
		res = append(res, *u)
	}
	return res
}

// endpoint reports whether a literal names a whole endpoint: a full URL with a
// path that is not the start of one being put together, like ".../repos/".
func endpoint(lit string) bool {
	u, err := url.Parse(lit)
	if err != nil || u.Host == "" {
		return false
	}
	p := strings.Trim(u.Path, "/")
	return p != "" && !strings.HasSuffix(u.Path, "/")
}

// base reports whether lit is where the code's paths start, like
// "https://api.example.com/v1": the server's own path or the start of an
// operation's.
func (r *run) base(lit string) bool {
	p := clean(lit)
	for _, d := range []*Doc{r.cur, r.old} {
		if d == nil {
			continue
		}
		if p == d.Base {
			return true
		}
		for _, op := range d.Ops {
			if strings.HasPrefix(op.Path, p+"/") {
				return true
			}
		}
	}
	return false
}

func lookup(r *run, method, path string, loose bool) *Op {
	if o := r.cur.find(method, path, loose); o != nil {
		return o
	}
	return r.old.find(method, path, loose)
}

// check turns the used operations of one provider into changes.
func check(p *Provider, idx core.ScanIndex, used []Usage, r *run) []core.Change {
	reads := func(name string, files []string) []core.Site {
		if idx == nil {
			return nil
		}
		return idx.Fields(name, files)
	}
	lines := Diff(r.old, r.cur, used)
	var res []core.Change
	for _, u := range used {
		key := u.Method + " " + u.Path
		var mine []Line
		for _, l := range lines {
			if l.Op == key {
				mine = append(mine, l)
			}
		}
		add := func(o *Op, kind core.Kind, what, field string, sites []core.Site) {
			c := change(p, r, u, o, kind, what, field, mine, sites)
			if kind == core.Breaking {
				r.pinned[key] = true
			}
			res = append(res, c)
		}

		cur := op(r.cur, key)
		if cur == nil {
			add(op(r.old, key), core.Breaking, "operation removed", "", nil)
			continue
		}
		if cur.Deprecated || !cur.Sunset.IsZero() {
			add(cur, core.Deprecation, "operation deprecated", "", nil)
		}
		for _, prm := range cur.Params {
			if prm.Deprecated && prm.In != "path" {
				if hits := reads(prm.Name, u.Files); len(hits) > 0 {
					add(cur, core.Deprecation, "parameter deprecated", prm.Name, hits)
				}
			}
		}
		walk(cur.Response, func(name string, sch *Schema) {
			if sch.Deprecated {
				if hits := reads(name, u.Files); len(hits) > 0 {
					add(cur, core.Deprecation, "response field deprecated", name, hits)
				}
			}
		})
		for _, l := range mine {
			hits := reads(l.Field, u.Files)
			named := len(hits) > 0 || query(u, l.Field)
			switch l.What {
			case "parameter removed", "request field removed":
				if named {
					add(cur, core.Breaking, l.What, l.Field, hits)
				}
			case "response field removed", "response field type changed":
				if len(hits) > 0 {
					add(cur, core.Breaking, "response field removed", l.Field, hits)
				}
			case "required parameter added":
				if !named {
					add(cur, core.Breaking, l.What, l.Field, nil)
				}
			}
		}
	}
	return res
}

// query reports whether a URL literal of u sets the parameter in its query string.
func query(u Usage, name string) bool {
	for _, x := range u.URLs {
		if _, q, ok := strings.Cut(x.URL, "?"); ok {
			if v, err := url.ParseQuery(q); err == nil && v.Has(name) {
				return true
			}
		}
	}
	return false
}

func change(p *Provider, r *run, u Usage, o *Op, kind core.Kind, what, field string, lines []Line, extra []core.Site) core.Change {
	text := o.Summary
	var why []Line
	for _, l := range lines {
		if l.Field == field {
			why = append(why, l)
			text += "\n" + l.String()
		}
	}
	ev := core.Evidence{
		Source: "openapi",
		Title:  strings.TrimSpace(p.Name + " spec " + r.cur.Version),
		Text:   core.Clip(strings.TrimSpace(text), 1200),
	}
	if remote(p.Spec) {
		ev.URL = p.Spec
	}
	c := core.Change{
		Kind:     kind,
		API:      &core.APIChange{Provider: p.Name, Method: o.Method, Path: o.Path, Field: field, What: what},
		Evidence: []core.Evidence{ev},
		Options:  options(p.Name, o.Method, o.Path, field),
		Sites:    sites(u, extra),
	}
	if kind == core.Deprecation {
		c.Sunset = o.Sunset
	}
	if len(why) > 0 && r.old != nil {
		c.Published = r.old.At
	}
	c.ID = core.ChangeID(&c)
	return c
}

// sites are the calls of the operation plus, for a field, where the code names it.
func sites(u Usage, extra []core.Site) []core.Site {
	res := slices.Clone(u.Sites)
	for _, x := range u.URLs {
		res = append(res, core.Site{File: x.File, Line: x.Line, Text: strings.TrimSpace(x.Method + " " + x.URL)})
	}
	res = append(res, extra...)
	slices.SortFunc(res, func(a, b core.Site) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), cmp.Compare(a.Col, b.Col))
	})
	return slices.CompactFunc(res, func(a, b core.Site) bool { return a.File == b.File && a.Line == b.Line && a.Col == b.Col })
}

// combine merges two changes that share an ID, such as a field that is both a
// deprecated parameter and a deprecated response field. Breaking wins.
func combine(a, b core.Change) core.Change {
	if b.Kind == core.Breaking && a.Kind != core.Breaking {
		a, b = b, a
	}
	a.Sites = sites(Usage{Sites: a.Sites}, b.Sites)
	return a
}

//go:embed migrations.yml
var migrationsYML []byte

type migration struct {
	Provider string        `yaml:"provider"`
	Method   string        `yaml:"method"`
	Path     string        `yaml:"path"`
	Field    string        `yaml:"field"`
	Options  []core.Option `yaml:"options"`
}

// options returns the known migrations off an operation or field. The table is
// read on each call so every change owns its options.
func options(provider, method, path, field string) []core.Option {
	var all []migration
	if err := yaml.Unmarshal(migrationsYML, &all); err != nil {
		panic("api: migrations.yml: " + err.Error())
	}
	for _, m := range all {
		if m.Provider == provider && m.Method == method && m.Path == path && m.Field == field {
			return m.Options
		}
	}
	return nil
}
