package source

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/eco"
	"github.com/mohsensc/tend-cli/internal/fetch"
	"github.com/mohsensc/tend-cli/internal/ver"
)

const (
	osvBatch = "https://api.osv.dev/v1/querybatch"
	osvVuln  = "https://api.osv.dev/v1/vulns/"
	osvMax   = 500
)

// OSV asks osv.dev which known advisories hit the versions in use.
type OSV struct {
	Fetch core.Fetcher
	// Upstream names a dep as the registry it comes from does, for deps
	// tend.yml points at another ecosystem's package; nil for none.
	Upstream func(core.Dep) (core.Dep, bool)
}

var _ core.Source = (*OSV)(nil)

func (s *OSV) Name() string { return "osv" }

type osvPkg struct {
	Name      string `json:"name"`
	Ecosystem string `json:"ecosystem"`
}

type osvQuery struct {
	Package   osvPkg `json:"package"`
	Version   string `json:"version"`
	PageToken string `json:"page_token,omitempty"`
}

type osvResult struct {
	Vulns []struct {
		ID string `json:"id"`
	} `json:"vulns"`
	Next string `json:"next_page_token"`
}

type osvEntry struct {
	ID        string         `json:"id"`
	Aliases   []string       `json:"aliases"`
	Summary   string         `json:"summary"`
	Details   string         `json:"details"`
	Published time.Time      `json:"published"`
	Withdrawn string         `json:"withdrawn"`
	Extra     map[string]any `json:"database_specific"`
	Severity  []struct {
		Score string `json:"score"`
	} `json:"severity"`
	Affected []struct {
		Package osvPkg `json:"package"`
		DB      struct {
			Informational string `json:"informational"`
		} `json:"database_specific"`
		Ranges []struct {
			Type   string              `json:"type"`
			Events []map[string]string `json:"events"`
		} `json:"ranges"`
	} `json:"affected"`
}

func (s *OSV) Changes(ctx context.Context, inv *core.Inventory) ([]core.Change, error) {
	var qs []osvQuery
	seen := map[osvQuery]bool{}
	for _, d := range inv.Deps {
		q, ok := osvFor(upstream(s.Upstream, d))
		if ok && !seen[q] {
			seen[q] = true
			qs = append(qs, q)
		}
	}
	hits, err := s.query(ctx, qs)
	if err != nil {
		return nil, err
	}
	entries, err := s.entries(ctx, hits)
	if err != nil {
		return nil, err
	}
	var hit []*hitDep
	for _, d := range inv.Deps {
		u := upstream(s.Upstream, d)
		q, ok := osvFor(u)
		if !ok {
			continue
		}
		h := &hitDep{d: d, u: u, q: q, taken: map[string]bool{}}
		h.take(hits[q], entries, q)
		hit = append(hit, h)
	}
	if err := s.clear(ctx, hit, entries); err != nil {
		return nil, err
	}
	var res []core.Change
	for _, h := range hit {
		res = append(res, securityChanges(h.d, h.advs)...)
		if len(h.notices) > 0 {
			res = append(res, notice(h.d, h.notices))
		}
	}
	return res, nil
}

// hitDep is what OSV says of one dep: the advisories that hit it, the
// notices about it, and the ids already counted.
type hitDep struct {
	d, u    core.Dep
	q       osvQuery
	advs    []advisory
	notices []core.Evidence
	taken   map[string]bool
}

// take counts the advisories ids name against the version q asks about. One
// issue can come back under several ids, a GitHub advisory and its Go or
// PyPI twin among them; the first stands for the rest.
func (h *hitDep) take(ids []string, entries map[string]*osvEntry, q osvQuery) []advisory {
	var res []advisory
	for _, id := range ids {
		e := entries[id]
		if e == nil || e.Withdrawn != "" || h.taken[id] {
			continue
		}
		h.taken[id] = true
		for _, a := range e.Aliases {
			h.taken[a] = true
		}
		a := e.advisory(h.u, q)
		switch e.informational(h.u, q) {
		case "unmaintained", "notice":
			h.notices = append(h.notices, a.ev)
			continue
		case "unsound":
			a.severity = "low"
		}
		h.advs = append(h.advs, a)
		res = append(res, a)
	}
	return res
}

// fix is the release that fixes every advisory h has a fix for, "" for none.
func (h *hitDep) fix() string {
	res := ""
	for _, a := range h.advs {
		if a.fixed != "" && (res == "" || ver.Less(res, a.fixed)) {
			res = a.fixed
		}
	}
	return res
}

// clearMax caps how many times a fix is checked against OSV for advisories
// of its own.
const clearMax = 5

// clear checks the release each dep's fix moves it to against OSV, since a
// release can fix the advisories on the version in use and be hit by
// others that never touched it: axios 0.21.1 moves to 0.33.0, which two
// advisories introduced since 0.27.2 still hit. Each fix rises to one
// those miss too, a round at a time, and the advisories that hit it are
// counted with the rest; merge then moves them all to the highest fix.
func (s *OSV) clear(ctx context.Context, hit []*hitDep, entries map[string]*osvEntry) error {
	want := map[*hitDep]string{}
	for _, h := range hit {
		want[h] = h.fix()
	}
	for range clearMax {
		at := map[osvQuery][]*hitDep{}
		var qs []osvQuery
		for _, h := range hit {
			if want[h] == "" {
				continue
			}
			q := h.q
			q.Version = want[h]
			if at[q] == nil {
				qs = append(qs, q)
			}
			at[q] = append(at[q], h)
		}
		if len(qs) == 0 {
			break
		}
		hits, err := s.query(ctx, qs)
		if err != nil {
			return err
		}
		fresh := map[osvQuery][]string{}
		for q, ids := range hits {
			for _, id := range ids {
				if _, ok := entries[id]; !ok {
					fresh[q] = append(fresh[q], id)
				}
			}
		}
		more, err := s.entries(ctx, fresh)
		if err != nil {
			return err
		}
		maps.Copy(entries, more)
		moved := false
		for _, q := range qs {
			for _, h := range at[q] {
				next := want[h]
				for _, a := range h.take(hits[q], entries, q) {
					if a.fixed != "" && ver.Less(next, a.fixed) {
						next = a.fixed
					}
				}
				if next != want[h] {
					want[h], moved = next, true
				} else {
					want[h] = ""
				}
			}
		}
		if !moved {
			break
		}
	}
	return nil
}

// notice is what an informational advisory, such as RustSec's
// unmaintained, says of d: a deprecation with no fix, with the ways off it
// the catalog or the notice itself names.
func notice(d core.Dep, evs []core.Evidence) core.Change {
	ch := core.Change{Kind: core.Deprecation, Deps: []core.Dep{d}, Evidence: evs}
	if one, opts, ok := Replacement(d.Ecosystem, d.Name); ok {
		if one != nil {
			opts = []core.Option{*one}
		}
		ch.Options = opts
	} else {
		for _, ev := range evs {
			if m := useInstead.FindStringSubmatch(ev.Title + "\n" + ev.Text); m != nil {
				ch.Options = []core.Option{{Title: "Switch to " + m[1], Detail: ev.URL, ToName: m[1]}}
				break
			}
		}
	}
	ch.ID = core.ChangeID(&ch)
	return ch
}

// useInstead finds the replacement a notice names: "use `terminal_size`
// instead", "Consider using [`nu-ansi-term`]".
var useInstead = regexp.MustCompile(`(?i)(?:use|consider using|switch to|migrate to)\s+\[?` + "`" + `([A-Za-z0-9_.:/-]+)` + "`")

// informational is what kind of notice e is for d, as RustSec marks one in
// the affected entry: unmaintained, unsound or notice; "" for a
// vulnerability.
func (e *osvEntry) informational(d core.Dep, q osvQuery) string {
	for _, a := range e.Affected {
		if a.Package.Ecosystem == q.Package.Ecosystem && sameName(d.Ecosystem, a.Package.Name, d.Name) {
			return a.DB.Informational
		}
	}
	return ""
}

// upstream is d as its registry names it, d itself when there is no mapping.
func upstream(up func(core.Dep) (core.Dep, bool), d core.Dep) core.Dep {
	if up == nil {
		return d
	}
	if u, ok := up(d); ok {
		return u
	}
	return d
}

func osvFor(d core.Dep) (osvQuery, bool) {
	e := eco.ByName(d.Ecosystem)
	if e == nil || e.Traits().OSV == "" {
		return osvQuery{}, false
	}
	v, ok := plainVersion(d)
	if !ok && d.Version == "" {
		v, ok = reqFloor(d.Req)
	}
	if !ok {
		return osvQuery{}, false
	}
	return osvQuery{Package: osvPkg{Name: d.Name, Ecosystem: e.Traits().OSV}, Version: v}, true
}

// reqFloor is the least version a requirement admits, for a dep no
// lockfile pins: "0.2.11" for cargo's 0.2.11 or ^0.2.11, 2.1 for >=2.1. A
// requirement with no floor, * or <3, or one that leaves the floor out, as
// >1.0 does, has none. The manifest allows that version, so an advisory on
// it is a real exposure, and raising the floor fixes it.
func reqFloor(req string) (string, bool) {
	first := strings.TrimSpace(req)
	if i := strings.IndexAny(first, ",|;"); i >= 0 {
		first = strings.TrimSpace(first[:i])
	}
	if f := strings.Fields(first); len(f) > 1 && !strings.ContainsAny(f[0], "0123456789") {
		first = f[0] + f[1]
	} else if len(f) > 0 {
		first = f[0]
	}
	v := strings.TrimLeft(first, "^~=v ")
	if strings.HasPrefix(first, ">=") {
		v = strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(first, ">=")), "v")
	}
	if v == "" || strings.ContainsAny(v, "<>*xX!") {
		return "", false
	}
	if _, ok := ver.Parse(v); !ok {
		return "", false
	}
	return v, true
}

// query returns the advisory ids per query, following page tokens until each
// query is exhausted.
func (s *OSV) query(ctx context.Context, qs []osvQuery) (map[osvQuery][]string, error) {
	res := map[osvQuery][]string{}
	pending := slices.Clone(qs)
	for len(pending) > 0 {
		batch := pending[:min(len(pending), osvMax)]
		pending = pending[len(batch):]
		body, err := json.Marshal(map[string]any{"queries": batch})
		if err != nil {
			return nil, err
		}
		resp, err := s.Fetch.Post(ctx, osvBatch, body, fetch.Idempotent())
		if err != nil {
			return nil, err
		}
		if resp.Status != 200 {
			return nil, fmt.Errorf("osv: querybatch: status %d", resp.Status)
		}
		var out struct {
			Results []osvResult `json:"results"`
		}
		if err := json.Unmarshal(resp.Body, &out); err != nil {
			return nil, fmt.Errorf("osv: querybatch: %w", err)
		}
		if len(out.Results) != len(batch) {
			return nil, fmt.Errorf("osv: querybatch: %d results for %d queries", len(out.Results), len(batch))
		}
		for i, r := range out.Results {
			key := batch[i]
			key.PageToken = ""
			for _, v := range r.Vulns {
				res[key] = append(res[key], v.ID)
			}
			if r.Next != "" {
				next := key
				next.PageToken = r.Next
				pending = append(pending, next)
			}
		}
	}
	return res, nil
}

// entries fetches the details of every distinct id. A missing advisory is skipped.
func (s *OSV) entries(ctx context.Context, hits map[osvQuery][]string) (map[string]*osvEntry, error) {
	var ids []string
	for _, v := range hits {
		ids = append(ids, v...)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	got := make([]*osvEntry, len(ids))
	errs := make([]error, len(ids))
	Parallel(len(ids), func(i int) {
		resp, err := s.Fetch.Get(ctx, osvVuln+url.PathEscape(ids[i]), fetch.TTL(day))
		switch {
		case err != nil:
			errs[i] = err
		case resp.Status == 404:
		case resp.Status != 200:
			errs[i] = fmt.Errorf("osv: %s: status %d", ids[i], resp.Status)
		default:
			e := &osvEntry{}
			if err := json.Unmarshal(resp.Body, e); err != nil {
				errs[i] = fmt.Errorf("osv: %s: %w", ids[i], err)
				return
			}
			got[i] = e
		}
	})
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	res := map[string]*osvEntry{}
	for i, e := range got {
		res[ids[i]] = e
	}
	return res, nil
}

func (e *osvEntry) advisory(d core.Dep, q osvQuery) advisory {
	return advisory{
		severity:  e.band(),
		fixed:     smallestFixed(q.Version, e.fixes(d, q)),
		published: e.Published,
		ev: core.Evidence{
			Source: "osv",
			Title:  strings.TrimSpace(e.ID + " " + e.Summary),
			Text:   core.Clip(e.Details, 1200),
			URL:    "https://osv.dev/vulnerability/" + e.ID,
		},
	}
}

// fixes lists the fixed events of the ranges that talk about this package.
func (e *osvEntry) fixes(d core.Dep, q osvQuery) []string {
	var res []string
	for _, a := range e.Affected {
		if a.Package.Ecosystem != q.Package.Ecosystem || !sameName(d.Ecosystem, a.Package.Name, d.Name) {
			continue
		}
		for _, r := range a.Ranges {
			if r.Type != "ECOSYSTEM" && r.Type != "SEMVER" {
				continue
			}
			for _, ev := range r.Events {
				if f := ev["fixed"]; f != "" {
					res = append(res, strings.TrimPrefix(f, "v"))
				}
			}
		}
	}
	return res
}

func (e *osvEntry) band() string {
	if s, ok := e.Extra["severity"].(string); ok {
		if res := normSeverity(s); res != "" {
			return res
		}
	}
	for _, s := range e.Severity {
		if res := cvssBand(s.Score); res != "" {
			return res
		}
	}
	return ""
}
