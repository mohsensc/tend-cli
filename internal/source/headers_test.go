package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
)

type hit struct{ Method, Path, Auth string }

type probed struct {
	*httptest.Server
	mu   sync.Mutex
	hits []hit
}

// probeServer plays an API that announces deprecations in all three header forms.
func probeServer(t *testing.T) *probed {
	t.Helper()
	p := &probed{}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/unix", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Deprecation", "@1688169599")
		w.Header().Set("Sunset", "Thu, 31 Dec 2026 23:59:59 GMT")
		w.Header().Add("Link", `<https://docs.example.com/deprecation>; rel="deprecation"; type="text/html", <https://example.com/other>; rel="alternate"`)
	})
	mux.HandleFunc("/v1/true", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Deprecation", "true")
		}
	})
	mux.HandleFunc("/v1/date", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Deprecation", "Sun, 11 Jun 2023 12:00:00 GMT")
	})
	mux.HandleFunc("/v1/sunset", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Sunset", "Wed, 11 Nov 2026 23:59:59 GMT")
		w.Header().Set("Link", `</docs/sunset>; rel="sunset"`)
	})
	mux.HandleFunc("/v1/false", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Deprecation", "false")
	})
	mux.HandleFunc("/v1/junk", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Deprecation", "soonish")
		w.Header().Set("Sunset", "whenever")
	})
	mux.HandleFunc("/v1/orders/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Deprecation", "true")
	})
	mux.HandleFunc("/v1/jump", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/v1/true", http.StatusFound)
	})
	mux.HandleFunc("/v1/clean", func(w http.ResponseWriter, r *http.Request) {})
	p.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.hits = append(p.hits, hit{r.Method, r.URL.Path, r.Header.Get("Authorization")})
		p.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *probed) host() string { return p.Listener.Addr().String() }

func (p *probed) inv(provider string, uses ...core.URLUse) *core.Inventory {
	for i := range uses {
		if uses[i].Host == "" {
			uses[i].Host = p.host()
		}
	}
	return &core.Inventory{Providers: []core.ProviderUse{{Provider: provider, URLs: uses}}}
}

func TestHeaders(t *testing.T) {
	p := probeServer(t)
	h := &Headers{HTTP: p.Client(), Providers: []string{"fixture"}}
	inv := p.inv("fixture",
		core.URLUse{File: "a.go", Line: 10, Method: "get", URL: "/v1/unix"},
		core.URLUse{File: "a.go", Line: 20, Method: "POST", URL: "/v1/true"},
		core.URLUse{File: "a.go", Line: 30, URL: "/v1/date"},
		core.URLUse{File: "b.go", Line: 5, Method: "GET", URL: "/v1/sunset"},
		core.URLUse{File: "b.go", Line: 6, Method: "GET", URL: "/v1/false"},
		core.URLUse{File: "b.go", Line: 7, Method: "GET", URL: "/v1/junk"},
		core.URLUse{File: "b.go", Line: 8, Method: "GET", URL: "/v1/clean"},
		core.URLUse{File: "b.go", Line: 9, Method: "GET", URL: "/v1/orders/{id}"},
		core.URLUse{File: "b.go", Line: 10, Method: "GET", URL: "/v1/jump"},
	)
	got, err := h.Changes(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range got {
		ids = append(ids, c.ID)
		if c.Kind != core.Deprecation || c.API.What != "operation deprecated" || c.API.Provider != "fixture" {
			t.Errorf("change = %+v", c)
		}
	}
	want := []string{
		"api/fixture/GET /v1/unix",
		"api/fixture/POST /v1/true",
		"api/fixture/GET /v1/date",
		"api/fixture/GET /v1/sunset",
		"api/fixture/GET /v1/orders/{id}",
		"api/fixture/GET /v1/jump",
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("changes:\n%v\nwant:\n%v", ids, want)
	}

	unix := got[0]
	if want := time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC); !unix.Sunset.Equal(want) {
		t.Errorf("sunset = %v, want %v", unix.Sunset, want)
	}
	if want := time.Unix(1688169599, 0); !unix.Published.Equal(want) {
		t.Errorf("published = %v, want %v", unix.Published, want)
	}
	ev := unix.Evidence[0]
	if ev.Source != "headers" || ev.URL != "https://docs.example.com/deprecation" {
		t.Errorf("evidence = %+v", ev)
	}
	if ev.Text != "Deprecation: @1688169599\nSunset: Thu, 31 Dec 2026 23:59:59 GMT" {
		t.Errorf("text = %q", ev.Text)
	}
	if want := []core.Site{{File: "a.go", Line: 10}}; !reflect.DeepEqual(unix.Sites, want) {
		t.Errorf("sites = %+v", unix.Sites)
	}

	if !got[1].Sunset.IsZero() || !got[1].Published.IsZero() || got[1].Evidence[0].URL != "https://"+p.host()+"/v1/true" {
		t.Errorf("deprecation: true over HEAD only = %+v", got[1])
	}
	if want := time.Date(2023, 6, 11, 12, 0, 0, 0, time.UTC); !got[2].Published.Equal(want) {
		t.Errorf("http-date deprecation published = %v", got[2].Published)
	}
	if got[2].API.Method != "GET" {
		t.Errorf("method = %q, want GET when the code does not say", got[2].API.Method)
	}
	sunset := got[3]
	if sunset.Evidence[0].URL != "https://"+p.host()+"/docs/sunset" || sunset.Sunset.IsZero() || !sunset.Published.IsZero() {
		t.Errorf("sunset-only = %+v", sunset)
	}
	if got[4].API.Path != "/v1/orders/{id}" {
		t.Errorf("path = %q, want the template", got[4].API.Path)
	}
}

func TestHeadersRequests(t *testing.T) {
	p := probeServer(t)
	h := &Headers{HTTP: p.Client(), Providers: []string{"fixture"}}
	inv := p.inv("fixture",
		core.URLUse{File: "a.go", Line: 1, Method: "GET", URL: "/v1/true"},
		core.URLUse{File: "a.go", Line: 2, Method: "GET", URL: "https://" + p.host() + "/v1/true?page=2"},
		core.URLUse{File: "b.go", Line: 3, Method: "DELETE", URL: "http://" + p.host() + "/v1/true"},
		core.URLUse{File: "b.go", Line: 4, URL: "/v1/orders/{order_id}"},
		core.URLUse{File: "b.go", Line: 5, URL: "/v1/orders/{id}"},
	)
	got, err := h.Changes(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	want := []hit{
		{"OPTIONS", "/v1/true", ""}, {"HEAD", "/v1/true", ""},
		{"OPTIONS", "/v1/orders/1", ""}, {"HEAD", "/v1/orders/1", ""},
	}
	if !reflect.DeepEqual(p.hits, want) {
		t.Errorf("requests = %v\nwant one OPTIONS and one HEAD per endpoint: %v", p.hits, want)
	}
	if len(got) != 4 {
		var ids []string
		for _, c := range got {
			ids = append(ids, c.ID)
		}
		t.Fatalf("changes = %v", ids)
	}
	get := got[0]
	if get.ID != "api/fixture/GET /v1/true" || len(get.Sites) != 2 {
		t.Errorf("GET change = %+v; uses on two lines are one change", get)
	}
	if got[1].ID != "api/fixture/DELETE /v1/true" || len(got[1].Sites) != 1 {
		t.Errorf("DELETE change = %+v", got[1])
	}
}

func TestHeadersProviders(t *testing.T) {
	p := probeServer(t)
	inv := p.inv("quiet", core.URLUse{File: "a.go", Line: 1, URL: "/v1/true"})
	got, err := (&Headers{HTTP: p.Client(), Providers: []string{"other"}}).Changes(context.Background(), inv)
	if err != nil || len(got) != 0 || len(p.hits) != 0 {
		t.Fatalf("got %v, %v; hits %v; a provider that did not opt in must not be probed", got, err, p.hits)
	}
	got, err = (&Headers{HTTP: p.Client()}).Changes(context.Background(), inv)
	if err != nil || len(got) != 0 || len(p.hits) != 0 {
		t.Fatalf("got %v, %v; hits %v; nobody opted in", got, err, p.hits)
	}
}

func TestHeadersNoRedirectOffHost(t *testing.T) {
	var other []string
	elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		other = append(other, r.Method+" "+r.URL.Path)
		w.Header().Set("Deprecation", "true")
	}))
	defer elsewhere.Close()
	home := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/landing", http.StatusTemporaryRedirect)
	}))
	defer home.Close()

	h := &Headers{HTTP: home.Client(), Providers: []string{"fixture"}}
	inv := &core.Inventory{Providers: []core.ProviderUse{{Provider: "fixture", URLs: []core.URLUse{
		{File: "a.go", Line: 1, Method: "GET", URL: "/v1/x", Host: home.Listener.Addr().String()},
	}}}}
	got, err := h.Changes(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("followed a redirect to another host: %v", other)
	}
	if len(got) != 0 {
		t.Fatalf("read headers from another host: %+v", got)
	}
}

func TestHeadersSendsNothingPrivate(t *testing.T) {
	var seen http.Header
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = r.Header.Clone() }))
	defer srv.Close()
	c := srv.Client()
	h := &Headers{HTTP: c, Providers: []string{"p"}}
	inv := &core.Inventory{Providers: []core.ProviderUse{{Provider: "p", URLs: []core.URLUse{{URL: "/v1/x", Host: srv.Listener.Addr().String()}}}}}
	if _, err := h.Changes(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"Authorization", "Cookie", "X-Api-Key"} {
		if seen.Get(k) != "" {
			t.Errorf("sent %s", k)
		}
	}
	if seen.Get("User-Agent") != "tend" {
		t.Errorf("user agent = %q", seen.Get("User-Agent"))
	}
	if c.CheckRedirect != nil || c.Timeout != 0 {
		t.Error("the caller's client was modified")
	}
}

func TestHeadersUnreachable(t *testing.T) {
	p := probeServer(t)
	host := p.host()
	p.Close()
	h := &Headers{HTTP: &http.Client{Timeout: time.Second}, Providers: []string{"p"}}
	inv := &core.Inventory{Providers: []core.ProviderUse{{Provider: "p", URLs: []core.URLUse{{URL: "/v1/x", Host: host}}}}}
	got, err := h.Changes(context.Background(), inv)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; an unreachable endpoint says nothing", got, err)
	}
}

func TestHeadersDeadHost(t *testing.T) {
	var mu sync.Mutex
	var dead []string
	hang := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		dead = append(dead, r.Method+" "+r.URL.Path)
		mu.Unlock()
		<-r.Context().Done()
	}))
	defer hang.Close()
	p := probeServer(t)
	c := *p.Client()
	c.Timeout = 100 * time.Millisecond
	h := &Headers{HTTP: &c, Providers: []string{"p"}}
	host := hang.Listener.Addr().String()
	inv := &core.Inventory{Providers: []core.ProviderUse{{Provider: "p", URLs: []core.URLUse{
		{URL: "/v1/a", Host: host},
		{URL: "/v1/b", Host: host},
		{URL: "/v1/c", Host: host},
		{URL: "/v1/true", Host: p.host()},
	}}}}
	got, err := h.Changes(context.Background(), inv)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v, %v; the live host should still be read", got, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"OPTIONS /v1/a", "HEAD /v1/a"}; !reflect.DeepEqual(dead, want) {
		t.Errorf("asked a host that did not answer %v, want %v", dead, want)
	}
}

func TestHeadersHostsAtOnce(t *testing.T) {
	var mu sync.Mutex
	var now, most int
	var inv core.Inventory
	pv := core.ProviderUse{Provider: "p"}
	var c *http.Client
	for range 3 * workers {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			now++
			most = max(most, now)
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			now--
			mu.Unlock()
		}))
		defer srv.Close()
		c = srv.Client()
		pv.URLs = append(pv.URLs, core.URLUse{URL: "/v1/x", Host: srv.Listener.Addr().String()})
	}
	inv.Providers = []core.ProviderUse{pv}
	if _, err := (&Headers{HTTP: c, Providers: []string{"p"}}).Changes(context.Background(), &inv); err != nil {
		t.Fatal(err)
	}
	if most < 2 || most > workers {
		t.Errorf("%d requests at once, want between 2 and %d", most, workers)
	}
}

func TestHeadersUnusableURLs(t *testing.T) {
	p := probeServer(t)
	h := &Headers{HTTP: p.Client(), Providers: []string{"p"}}
	inv := &core.Inventory{Providers: []core.ProviderUse{{Provider: "p", URLs: []core.URLUse{
		{URL: "/v1/true"}, // path with no host
		{URL: "://bad", Host: "x"},
	}}}}
	got, err := h.Changes(context.Background(), inv)
	if err != nil || len(got) != 0 || len(p.hits) != 0 {
		t.Fatalf("got %v, %v, hits %v", got, err, p.hits)
	}
}

func TestParseDeprecation(t *testing.T) {
	tests := []struct {
		in   string
		want time.Time
		ok   bool
	}{
		{"@1688169599", time.Unix(1688169599, 0).UTC(), true},
		{" @0 ", time.Unix(0, 0).UTC(), true},
		{"true", time.Time{}, true},
		{"Sun, 11 Jun 2023 12:00:00 GMT", time.Date(2023, 6, 11, 12, 0, 0, 0, time.UTC), true},
		{"false", time.Time{}, false},
		{"@abc", time.Time{}, false},
		{"@", time.Time{}, false},
		{"", time.Time{}, false},
		{"2023-06-11", time.Time{}, false},
	}
	for _, tc := range tests {
		got, ok := parseDeprecation(tc.in)
		if ok != tc.ok || ok && !got.Equal(tc.want) {
			t.Errorf("parseDeprecation(%q) = %v, %v; want %v, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestEndpoint(t *testing.T) {
	tests := []struct {
		use          core.URLUse
		target, path string
		ok           bool
	}{
		{core.URLUse{URL: "/v1/orders", Host: "api.example.com"}, "https://api.example.com/v1/orders", "/v1/orders", true},
		{core.URLUse{URL: "/v1/orders/{id}/items/{item}", Host: "api.example.com"}, "https://api.example.com/v1/orders/1/items/1", "/v1/orders/{id}/items/{item}", true},
		{core.URLUse{URL: "https://api.example.com:8443/v1/x?a=b#c"}, "https://api.example.com:8443/v1/x", "/v1/x", true},
		{core.URLUse{URL: "http://api.example.com/v1/x"}, "https://api.example.com/v1/x", "/v1/x", true},
		{core.URLUse{URL: "https://api.example.com"}, "https://api.example.com/", "/", true},
		{core.URLUse{URL: "/v1/x"}, "", "", false},
		{core.URLUse{URL: "https:///v1/x"}, "", "", false},
	}
	for _, tc := range tests {
		target, path, ok := endpoint(tc.use)
		if target != tc.target || path != tc.path || ok != tc.ok {
			t.Errorf("endpoint(%+v) = %q, %q, %v; want %q, %q, %v", tc.use, target, path, ok, tc.target, tc.path, tc.ok)
		}
	}
}
