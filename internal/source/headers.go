package source

import (
	"context"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
)

// Headers probes the endpoints of providers that opt in and reads their
// Deprecation, Sunset and Link headers. Hosts are probed a few at a time,
// each host's endpoints one after another and within hostAfter; a host that
// does not answer is not asked about its other endpoints.
type Headers struct {
	HTTP      *http.Client // nil means a plain client; redirects off-host are refused either way
	Providers []string     // names of the providers with probe: true
}

var _ core.Source = (*Headers)(nil)

func (h *Headers) Name() string { return "headers" }

var paramRe = regexp.MustCompile(`\{[^}/]*\}`)

const hostAfter = 30 * time.Second

// probe is what an endpoint said about itself.
type probe struct {
	deprecation string // raw header value
	sunset      string
	deprecated  time.Time // zero for "true"
	sunsetAt    time.Time
	link        string
}

func (p *probe) announces() bool { return p.deprecation != "" || !p.sunsetAt.IsZero() }

func (h *Headers) Changes(ctx context.Context, inv *core.Inventory) ([]core.Change, error) {
	probes := h.probeAll(ctx, h.targets(inv))
	var res []core.Change
	at := map[string]int{}
	for _, pv := range inv.Providers {
		if !slices.Contains(h.Providers, pv.Provider) {
			continue
		}
		for _, u := range pv.URLs {
			target, path, ok := endpoint(u)
			if !ok {
				continue
			}
			p := probes[target]
			if p == nil || !p.announces() {
				continue
			}
			method := strings.ToUpper(u.Method)
			if method == "" {
				method = "GET"
			}
			key := pv.Provider + " " + method + " " + path
			site := core.Site{File: u.File, Line: u.Line}
			if i, ok := at[key]; ok {
				res[i].Sites = append(res[i].Sites, site)
				continue
			}
			at[key] = len(res)
			res = append(res, headerChange(pv.Provider, method, path, target, p, site))
		}
	}
	return res, nil
}

// targets are the URLs to probe, by host, each once and in the order the
// code names them.
func (h *Headers) targets(inv *core.Inventory) map[string][]string {
	res := map[string][]string{}
	seen := map[string]bool{}
	for _, pv := range inv.Providers {
		if !slices.Contains(h.Providers, pv.Provider) {
			continue
		}
		for _, u := range pv.URLs {
			target, _, ok := endpoint(u)
			if !ok || seen[target] {
				continue
			}
			seen[target] = true
			host := strings.TrimPrefix(target, "https://")
			host, _, _ = strings.Cut(host, "/")
			res[host] = append(res[host], target)
		}
	}
	return res
}

// probeAll probes several hosts at once. An endpoint that said nothing has no
// entry.
func (h *Headers) probeAll(ctx context.Context, byHost map[string][]string) map[string]*probe {
	hosts := slices.Sorted(maps.Keys(byHost))
	found := make([]map[string]*probe, len(hosts))
	c := h.client()
	Parallel(len(hosts), func(i int) {
		found[i] = probeHost(ctx, c, byHost[hosts[i]])
	})
	res := map[string]*probe{}
	for _, m := range found {
		maps.Copy(res, m)
	}
	return res
}

// probeHost probes one host's endpoints in turn, and stops at the first that
// cannot be reached or when hostAfter runs out.
func probeHost(ctx context.Context, c *http.Client, targets []string) map[string]*probe {
	ctx, cancel := context.WithTimeout(ctx, hostAfter)
	defer cancel()
	res := map[string]*probe{}
	for _, t := range targets {
		p := probeOne(ctx, c, t)
		if p == nil {
			break
		}
		res[t] = p
	}
	return res
}

// endpoint turns a URL literal into the https URL to probe and the path template.
func endpoint(u core.URLUse) (target, path string, ok bool) {
	raw := u.URL
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		if u.Host == "" {
			return "", "", false
		}
		raw = "https://" + u.Host + raw
	}
	pu, err := url.Parse(raw)
	if err != nil || pu.Host == "" {
		return "", "", false
	}
	path = pu.Path
	if path == "" {
		path = "/"
	}
	return "https://" + pu.Host + paramRe.ReplaceAllString(path, "1"), path, true
}

// probeOne sends OPTIONS, then HEAD, and keeps the first answer for each
// header. An endpoint that cannot be reached says nothing.
func probeOne(ctx context.Context, c *http.Client, target string) *probe {
	p := &probe{}
	reached := false
	for _, method := range []string{http.MethodOptions, http.MethodHead} {
		req, err := http.NewRequestWithContext(ctx, method, target, nil)
		if err != nil {
			return nil
		}
		req.Header.Set("User-Agent", "tend")
		resp, err := c.Do(req)
		if err != nil {
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		reached = true
		p.read(resp.Header, target)
	}
	if !reached {
		return nil
	}
	return p
}

func (h *Headers) client() *http.Client {
	c := http.Client{}
	if h.HTTP != nil {
		c = *h.HTTP
	}
	if c.Timeout == 0 {
		c.Timeout = 10 * time.Second
	}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Host != via[0].URL.Host || req.URL.Scheme != "https" || len(via) > 5 {
			return http.ErrUseLastResponse
		}
		return nil
	}
	return &c
}

func (p *probe) read(h http.Header, base string) {
	if v := h.Get("Deprecation"); v != "" && p.deprecation == "" {
		if t, ok := parseDeprecation(v); ok {
			p.deprecation, p.deprecated = v, t
		}
	}
	if v := h.Get("Sunset"); v != "" && p.sunset == "" {
		if t, err := http.ParseTime(v); err == nil {
			p.sunset, p.sunsetAt = v, t
		}
	}
	for _, l := range links(h) {
		if (l.Rel == "deprecation" || l.Rel == "sunset") && p.link == "" {
			p.link = absolute(base, l.URL)
		}
	}
}

// parseDeprecation reads RFC 9745 ("@1688169599") and the older spellings,
// "true" and an HTTP date. The date is zero for "true".
func parseDeprecation(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if n, ok := strings.CutPrefix(v, "@"); ok {
		sec, err := strconv.ParseInt(n, 10, 64)
		return time.Unix(sec, 0).UTC(), err == nil
	}
	if v == "true" {
		return time.Time{}, true
	}
	t, err := http.ParseTime(v)
	return t, err == nil
}

func absolute(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

func headerChange(provider, method, path, target string, p *probe, site core.Site) core.Change {
	title := target + " announces its deprecation"
	if p.deprecation == "" {
		title = target + " announces its sunset"
	}
	var text []string
	if p.deprecation != "" {
		text = append(text, "Deprecation: "+p.deprecation)
	}
	if p.sunset != "" {
		text = append(text, "Sunset: "+p.sunset)
	}
	link := p.link
	if link == "" {
		link = target
	}
	c := core.Change{
		Kind:      core.Deprecation,
		API:       &core.APIChange{Provider: provider, Method: method, Path: path, What: "operation deprecated"},
		Evidence:  []core.Evidence{{Source: "headers", Title: title, Text: strings.Join(text, "\n"), URL: link}},
		Sunset:    p.sunsetAt,
		Published: p.deprecated,
		Sites:     []core.Site{site},
	}
	c.ID = core.ChangeID(&c)
	return c
}

type link struct{ URL, Rel string }

// links reads every Link header value. A rel may carry several words.
func links(h map[string][]string) []link {
	var res []link
	for _, v := range http.Header(h).Values("Link") {
		for _, m := range linkRe.FindAllStringSubmatch(v, -1) {
			rel := relRe.FindStringSubmatch(m[2])
			if rel == nil {
				continue
			}
			for _, w := range strings.Fields(strings.ToLower(rel[1] + rel[2])) {
				res = append(res, link{URL: m[1], Rel: w})
			}
		}
	}
	return res
}

var (
	linkRe = regexp.MustCompile(`<([^>]*)>([^<]*)`)
	relRe  = regexp.MustCompile(`(?i)rel\s*=\s*(?:"([^"]*)"|([^\s;,]*))`)
)
