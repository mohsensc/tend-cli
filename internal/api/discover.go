package api

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/fetch"
	"github.com/mohsensc/tend-cli/internal/scan"
	"github.com/mohsensc/tend-cli/internal/source"
	"github.com/mohsensc/tend-cli/internal/state"
)

// apisGuru is the public directory of OpenAPI specs hosts are looked up in.
const apisGuru = "https://api.apis.guru/v2/list.json"

// wellKnown are the paths an API often serves its own spec at.
var wellKnown = []string{"/openapi.json", "/openapi.yaml", "/swagger.json", "/v3/api-docs", "/.well-known/openapi.json"}

const (
	foundFor   = 7 * 24 * time.Hour // how long a finding, or the lack of one, holds
	probeAfter = 5 * time.Second    // one request
	hostAfter  = 20 * time.Second   // every request about one host
)

// notAPIs are hosts code names for other reasons: links, schemas, examples.
var notAPIs = []string{"github.com", "raw.githubusercontent.com", "www.w3.org", "w3.org", "schema.org", "json-schema.org", "example.com", "example.org", "localhost", "127.0.0.1"}

// Unknown lists the hosts the project's code calls that no provider covers.
// A host counts as an API when its name or one of the paths called on it
// says so: api.x.com, x.com/v2/..., x.com/api/...
func Unknown(inv *core.Inventory, cat []Provider) []string {
	if inv.Index == nil {
		return nil
	}
	covered := map[string]bool{}
	for _, p := range cat {
		for _, h := range p.Hosts {
			covered[strings.ToLower(h)] = true
		}
	}
	var res []string
	for _, u := range inv.Index.URLs() {
		h := strings.ToLower(u.Host)
		if h == "" || covered[h] || slices.Contains(res, h) || scan.IsTest(u.File) || slices.Contains(notAPIs, h) || strings.HasSuffix(h, ".example") || strings.HasSuffix(h, ".test") {
			continue
		}
		if apiLike(h, u.URL) {
			res = append(res, h)
		}
	}
	slices.Sort(res)
	return res
}

func apiLike(host, lit string) bool {
	if strings.HasPrefix(host, "api.") || strings.Contains(host, ".api.") {
		return true
	}
	p := lit
	if u, err := url.Parse(lit); err == nil && u.Host != "" {
		p = u.Path
	}
	seg, _, _ := strings.Cut(strings.TrimPrefix(p, "/"), "/")
	return seg == "api" || len(seg) > 1 && seg[0] == 'v' && strings.Trim(seg[1:], "0123456789") == ""
}

// Discover looks for an OpenAPI spec for each host: in the APIs.guru
// directory, then at the usual paths on the host and on its docs and
// developer siblings. Hosts are looked up a few at a time. It never sends
// credentials, and trusts what it found, or did not, for a week. It returns
// what is known about every host, the old findings included.
func Discover(ctx context.Context, f core.Fetcher, hosts []string, known map[string]state.Found, now time.Time) map[string]state.Found {
	res := map[string]state.Found{}
	var todo []string
	for _, h := range hosts {
		if k, ok := known[h]; ok && now.Sub(k.At) < foundFor {
			res[h] = k
			continue
		}
		todo = append(todo, h)
	}
	if len(todo) == 0 {
		return res
	}
	dir := directory(ctx, f)
	specs := make([]string, len(todo))
	source.Parallel(len(todo), func(i int) {
		if specs[i] = guru(dir, todo[i]); specs[i] == "" {
			specs[i] = probe(ctx, f, todo[i])
		}
	})
	for i, h := range todo {
		res[h] = state.Found{Spec: specs[i], At: now}
	}
	return res
}

type guruAPI struct {
	Preferred string `json:"preferred"`
	Versions  map[string]struct {
		SwaggerURL string `json:"swaggerUrl"`
		OpenAPIVer string `json:"openapiVer"`
		Info       struct {
			Provider string `json:"x-providerName"`
			Origin   []struct {
				URL string `json:"url"`
			} `json:"x-origin"`
		} `json:"info"`
	} `json:"versions"`
}

// directory is APIs.guru's list, empty when it cannot be had.
func directory(ctx context.Context, f core.Fetcher) map[string]guruAPI {
	res := map[string]guruAPI{}
	got, err := f.Get(ctx, apisGuru, fetch.TTL(foundFor))
	if err != nil || got.Status != 200 {
		return res
	}
	_ = json.Unmarshal(got.Body, &res)
	return res
}

// guru finds the directory's spec for a host: the entry named for the host
// or its domain, and among a domain's several entries the one named for the
// host's first label (googleapis.com:gmail for gmail.googleapis.com); failing
// that, the entry whose spec came from the host itself.
func guru(dir map[string]guruAPI, host string) string {
	labels := strings.Split(host, ".")
	var names []string
	for i := 0; i+2 <= len(labels); i++ {
		names = append(names, strings.Join(labels[i:], "."))
	}
	spec := func(id string) string {
		a := dir[id]
		v := a.Versions[a.Preferred]
		if !strings.HasPrefix(v.OpenAPIVer, "3") {
			return ""
		}
		return v.SwaggerURL
	}
	for _, n := range names {
		if _, ok := dir[n]; ok {
			return spec(n)
		}
		if _, ok := dir[n+":"+labels[0]]; ok {
			return spec(n + ":" + labels[0])
		}
		var ids []string
		for id := range dir {
			if strings.HasPrefix(id, n+":") {
				ids = append(ids, id)
			}
		}
		if len(ids) == 1 {
			return spec(ids[0])
		}
	}
	for _, id := range slices.Sorted(maps.Keys(dir)) {
		a := dir[id]
		for _, o := range a.Versions[a.Preferred].Info.Origin {
			if u, err := url.Parse(o.URL); err == nil && strings.EqualFold(u.Hostname(), host) {
				return spec(id)
			}
		}
	}
	return ""
}

// probe asks the host and its docs and developer siblings for a spec at the
// usual paths, with no credentials, and keeps the first that loads. Each of
// them is asked once per path, five times at most, and not again once it
// fails to answer; all of it within hostAfter.
func probe(ctx context.Context, f core.Fetcher, host string) string {
	ctx, cancel := context.WithTimeout(ctx, hostAfter)
	defer cancel()
	domain := host
	if i := strings.IndexByte(host, '.'); i > 0 && strings.Count(host, ".") > 1 {
		domain = host[i+1:]
	}
	for _, h := range []string{host, "docs." + domain, "developer." + domain} {
		for _, p := range wellKnown {
			u := "https://" + h + p
			c, cancel := context.WithTimeout(ctx, probeAfter)
			got, err := f.Get(c, u, fetch.TTL(foundFor))
			cancel()
			if err != nil {
				break
			}
			if got.Status != 200 {
				continue
			}
			if _, err := Load(got.Body); err == nil {
				return u
			}
		}
	}
	return ""
}

// Discovered makes a provider of each host a spec was found for, named after
// the host.
func Discovered(found map[string]state.Found) []Provider {
	var res []Provider
	for _, h := range slices.Sorted(maps.Keys(found)) {
		if s := found[h].Spec; s != "" {
			res = append(res, Provider{Name: h, Hosts: []string{h}, Spec: s})
		}
	}
	return res
}

// Suggest is the tend.yml entry that would let tend follow a host it found no
// spec for. A changelog: URL in place of the spec works too.
func Suggest(host string) string {
	return fmt.Sprintf("{name: %s, hosts: [%s], spec: <OpenAPI spec URL>}", strings.TrimPrefix(host, "api."), host)
}
