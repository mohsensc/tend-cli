package api

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/state"
)

func TestUnknown(t *testing.T) {
	idx := &index{urls: []core.URLUse{
		{File: "src/a.js", URL: "https://api.example-unknown.com/v2/items", Host: "api.example-unknown.com"},
		{File: "src/a.js", URL: "/v2/items/%s", Host: "api.example-unknown.com"},
		{File: "src/b.js", URL: "https://hooks.slack.test/x", Host: "hooks.slack.test"},
		{File: "src/c.py", URL: "https://status.vendor.io/api/v1/summary", Host: "status.vendor.io"},
		{File: "src/c.py", URL: "https://vendor.io/pricing", Host: "vendor.io"},
		{File: "src/d.go", URL: "https://github.com/acme/app/issues", Host: "github.com"},
		{File: "src/e.go", URL: "https://api.stripe.com/v1/charges", Host: "api.stripe.com"},
		{File: "src/e_test.go", URL: "https://api.mock.dev/v1/x", Host: "api.mock.dev"},
		{File: "src/f.js", URL: "http://localhost:8080/v1/x", Host: "localhost"},
	}}
	got := Unknown(&core.Inventory{Index: idx}, Catalog())
	if want := []string{"api.example-unknown.com", "status.vendor.io"}; !slices.Equal(got, want) {
		t.Errorf("Unknown = %q, want %q", got, want)
	}
}

const guruList = `{
  "example-unknown.com": {"preferred": "2.0.0", "versions": {"2.0.0": {"swaggerUrl": "https://api.apis.guru/v2/specs/example-unknown.com/2.0.0/openapi.json", "openapiVer": "3.0.3", "info": {"x-providerName": "example-unknown.com"}}}},
  "googleapis.com:gmail": {"preferred": "v1", "versions": {"v1": {"swaggerUrl": "https://api.apis.guru/v2/specs/googleapis.com/gmail/v1/openapi.json", "openapiVer": "3.0.0"}}},
  "googleapis.com:drive": {"preferred": "v3", "versions": {"v3": {"swaggerUrl": "https://api.apis.guru/v2/specs/googleapis.com/drive/v3/openapi.json", "openapiVer": "3.0.0"}}},
  "old.com": {"preferred": "1", "versions": {"1": {"swaggerUrl": "https://api.apis.guru/v2/specs/old.com/1/swagger.json", "openapiVer": "2.0"}}},
  "acme.dev:billing": {"preferred": "2", "versions": {"2": {"swaggerUrl": "https://api.apis.guru/v2/specs/acme.dev/billing/2/openapi.json", "openapiVer": "3.1.0", "info": {"x-origin": [{"format": "openapi", "url": "https://billing.acme.io/openapi.json"}]}}}}
}`

func TestGuru(t *testing.T) {
	dir := directory(context.Background(), &pages{body: map[string]string{apisGuru: guruList}})
	tests := []struct{ host, want string }{
		{"api.example-unknown.com", "https://api.apis.guru/v2/specs/example-unknown.com/2.0.0/openapi.json"},
		{"gmail.googleapis.com", "https://api.apis.guru/v2/specs/googleapis.com/gmail/v1/openapi.json"},
		{"storage.googleapis.com", ""},
		{"api.old.com", ""},
		{"api.nowhere-documented.com", ""},
		{"billing.acme.io", "https://api.apis.guru/v2/specs/acme.dev/billing/2/openapi.json"},
	}
	for _, tt := range tests {
		if got := guru(dir, tt.host); got != tt.want {
			t.Errorf("guru(%s) = %q, want %q", tt.host, got, tt.want)
		}
	}
}

func TestDiscover(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	spec := `{"openapi": "3.0.0", "paths": {}}`
	f := &pages{body: map[string]string{
		apisGuru: guruList,
		"https://developer.vendor.io/v3/api-docs": spec,
	}}
	known := map[string]state.Found{
		"api.cached.com": {Spec: "https://cached.test/spec.json", At: now.Add(-24 * time.Hour)},
		"api.stale.com":  {Spec: "https://stale.test/spec.json", At: now.Add(-8 * 24 * time.Hour)},
	}
	got := Discover(context.Background(), f, []string{"api.cached.com", "api.example-unknown.com", "api.nowhere-documented.com", "api.stale.com", "status.vendor.io"}, known, now)
	want := map[string]string{
		"api.cached.com":             "https://cached.test/spec.json",
		"api.example-unknown.com":    "https://api.apis.guru/v2/specs/example-unknown.com/2.0.0/openapi.json",
		"api.nowhere-documented.com": "",
		"api.stale.com":              "",
		"status.vendor.io":           "https://developer.vendor.io/v3/api-docs",
	}
	for h, spec := range want {
		if got[h].Spec != spec {
			t.Errorf("%s: spec %q, want %q", h, got[h].Spec, spec)
		}
	}
	if got["api.cached.com"].At != known["api.cached.com"].At || !got["api.stale.com"].At.Equal(now) {
		t.Error("a fresh finding was asked again or a stale one was kept")
	}
	nowhere := 0
	for _, u := range f.asked {
		if strings.Contains(u, "nowhere-documented.com") {
			nowhere++
		}
		if strings.Contains(u, "cached") {
			t.Errorf("asked about a host it knew: %s", u)
		}
	}
	if want := 3 * len(wellKnown); nowhere != want {
		t.Errorf("asked about a host and its siblings %d times, want %d", nowhere, want)
	}
	if n := slices.Index(f.asked, apisGuru); n < 0 || slices.Index(f.asked[n+1:], apisGuru) >= 0 {
		t.Errorf("the directory was read %d times", strings.Count(strings.Join(f.asked, " "), apisGuru))
	}
	ps := Discovered(got)
	if len(ps) != 3 || ps[1].Name != "api.example-unknown.com" || ps[1].Hosts[0] != "api.example-unknown.com" {
		t.Errorf("providers = %+v", ps)
	}
}

// dead is a fetcher for hosts that do not answer, a few of them slowly.
type dead struct {
	mu       sync.Mutex
	asked    map[string]int
	now, max int
	limit    bool // every probe had a deadline no later than probeAfter
}

func (d *dead) Get(ctx context.Context, u string, opts ...core.FetchOpt) (*core.Response, error) {
	d.mu.Lock()
	d.asked[strings.SplitN(u, "/", 4)[2]]++
	d.now++
	d.max = max(d.max, d.now)
	if at, ok := ctx.Deadline(); u != apisGuru && (!ok || time.Until(at) > probeAfter) {
		d.limit = false
	}
	d.mu.Unlock()
	if u != apisGuru {
		time.Sleep(10 * time.Millisecond)
	}
	d.mu.Lock()
	d.now--
	d.mu.Unlock()
	if u == apisGuru {
		return &core.Response{Status: 200, Body: []byte(`{}`)}, nil
	}
	return nil, errors.New("connection refused")
}

func (d *dead) Post(context.Context, string, []byte, ...core.FetchOpt) (*core.Response, error) {
	panic("discovery never posts")
}

func TestDiscoverDeadHosts(t *testing.T) {
	var hosts []string
	for i := range 20 {
		hosts = append(hosts, fmt.Sprintf("api.gone%d.test", i))
	}
	f := &dead{asked: map[string]int{}, limit: true}
	got := Discover(context.Background(), f, hosts, nil, time.Now())
	if len(got) != len(hosts) {
		t.Fatalf("found %d hosts, want %d", len(got), len(hosts))
	}
	for _, h := range hosts {
		domain := strings.TrimPrefix(h, "api.")
		for _, sib := range []string{h, "docs." + domain, "developer." + domain} {
			if n := f.asked[sib]; n != 1 {
				t.Errorf("asked %s %d times; a host that does not answer is asked once", sib, n)
			}
		}
	}
	if f.max < 2 || f.max > 8 {
		t.Errorf("%d lookups at once, want between 2 and 8", f.max)
	}
	if !f.limit {
		t.Error("a probe went out with no deadline of its own")
	}
}
