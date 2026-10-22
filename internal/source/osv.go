package source

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/eco"
	"github.com/mohsensc/tend-cli/internal/fetch"
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
		Ranges  []struct {
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
	var res []core.Change
	for _, d := range inv.Deps {
		u := upstream(s.Upstream, d)
		q, ok := osvFor(u)
		if !ok {
			continue
		}
		var advs []advisory
		// One issue can come back under several ids, a GitHub advisory and
		// its Go or PyPI twin among them; the first stands for the rest.
		taken := map[string]bool{}
		for _, id := range hits[q] {
			e := entries[id]
			if e == nil || e.Withdrawn != "" || taken[id] {
				continue
			}
			taken[id] = true
			for _, a := range e.Aliases {
				taken[a] = true
			}
			advs = append(advs, e.advisory(u, q))
		}
		res = append(res, securityChanges(d, advs)...)
	}
	return res, nil
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
	if !ok {
		return osvQuery{}, false
	}
	return osvQuery{Package: osvPkg{Name: d.Name, Ecosystem: e.Traits().OSV}, Version: v}, true
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
