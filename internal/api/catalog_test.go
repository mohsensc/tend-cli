package api

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/fetch"
)

func TestCatalog(t *testing.T) {
	cat := Catalog()
	if len(cat) == 0 {
		t.Fatal("empty catalog")
	}
	seen := map[string]bool{}
	for _, p := range cat {
		if p.Name == "" || len(p.Hosts) == 0 || !strings.HasPrefix(p.Spec, "https://") {
			t.Errorf("incomplete provider %+v", p)
		}
		if seen[p.Name] {
			t.Errorf("duplicate provider %s", p.Name)
		}
		seen[p.Name] = true
		for verb, spec := range p.Verbs {
			method, _, _ := strings.Cut(spec, "/")
			if !slices.Contains(methods, strings.ToLower(method)) {
				t.Errorf("%s: verb %s maps to %q", p.Name, verb, spec)
			}
		}
	}
	stripe := cat[0]
	if stripe.Name != "stripe" || stripe.Base != "/v1" || stripe.Verbs["retrieve"] != "GET/{id}" || len(stripe.SDKs) != 7 {
		t.Errorf("stripe = %+v", stripe)
	}
}

func TestMerge(t *testing.T) {
	cat := []Provider{
		{Name: "stripe", Hosts: []string{"api.stripe.com"}, Spec: "https://stripe.test/spec.json", Base: "/v1", SDKs: []SDK{{Eco: core.Npm, Name: "stripe"}}},
		{Name: "github", Hosts: []string{"api.github.com"}, Spec: "https://github.test/spec.json"},
	}
	tests := []struct {
		name string
		cfg  []config.API
		want []Provider
	}{
		{"none", nil, cat},
		{
			"append",
			[]config.API{{Name: "billing", Hosts: []string{"billing.internal:8443"}, Spec: "api/openapi.yaml"}},
			append(slices.Clone(cat), Provider{Name: "billing", Hosts: []string{"billing.internal:8443"}, Spec: "api/openapi.yaml"}),
		},
		{
			"override spec, keep the rest",
			[]config.API{{Name: "stripe", Spec: "specs/stripe.json"}},
			[]Provider{{Name: "stripe", Hosts: []string{"api.stripe.com"}, Spec: "specs/stripe.json", Base: "/v1", SDKs: cat[0].SDKs}, cat[1]},
		},
		{
			"override hosts",
			[]config.API{{Name: "github", Hosts: []string{"github.example.com"}}},
			[]Provider{cat[0], {Name: "github", Hosts: []string{"github.example.com"}, Spec: "https://github.test/spec.json"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Merge(cat, tt.cfg)
			a, _ := json.Marshal(got)
			b, _ := json.Marshal(tt.want)
			if string(a) != string(b) {
				t.Errorf("Merge =\n%s\nwant\n%s", a, b)
			}
		})
	}
	if cat[0].Spec != "https://stripe.test/spec.json" {
		t.Error("Merge changed the catalog it was given")
	}
}

// recorded lists the operations kept from each catalog spec for tests.
var recorded = map[string][]string{
	"stripe":       {"POST /v1/charges", "GET /v1/customers/{customer}/cards"},
	"github":       {"GET /repos/{owner}/{repo}", "GET /assignments/{assignment_id}"},
	"openai":       {"POST /v1/chat/completions"},
	"twilio":       {"POST /2010-04-01/Accounts/{AccountSid}/Messages.json"},
	"sendgrid":     {"POST /v3/mail/send", "GET /v3/marketing/contacts", "GET /v3/templates"},
	"plaid":        {"POST /transactions/get"},
	"digitalocean": {"GET /v2/droplets"},
	"cloudflare":   {"GET /client/v4/zones", "GET /client/v4/zones/{zone_id}/cache/origin_cloud_regions"},
	"datadog":      {"GET /api/v1/monitor", "GET /api/v2/logs/events"},
}

const recordDir = "testdata/recorded"

// TestRecord fetches every catalog spec and keeps a trimmed copy for the
// offline tests: TEND_RECORD=1 go test ./internal/api -run Record
func TestRecord(t *testing.T) {
	if os.Getenv("TEND_RECORD") == "" {
		t.Skip("set TEND_RECORD=1 to fetch the catalog specs")
	}
	c := fetch.New("", "test")
	for _, p := range Catalog() {
		ops := recorded[p.Name]
		found := map[string]bool{}
		for _, spec := range p.Specs() {
			res, err := c.Get(context.Background(), spec)
			if err != nil || res.Status != 200 {
				t.Errorf("%s: %s: %v %+v", p.Name, spec, err, res)
				continue
			}
			d, err := load(res.Body, p.Sunset)
			if err != nil {
				t.Errorf("%s: %v", p.Name, err)
				continue
			}
			mine := []string{}
			for _, k := range ops {
				if d.Ops[k] != nil {
					found[k] = true
					mine = append(mine, k)
				}
			}
			rec, _ := json.MarshalIndent(map[string]any{"url": spec, "status": 200, "body": string(trimSpec(d, mine))}, "", "  ")
			path := filepath.Join(recordDir, fetch.RecordName("GET", spec, nil))
			if err := os.WriteFile(path, append(rec, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for _, k := range ops {
			if !found[k] {
				t.Errorf("%s: no %s", p.Name, k)
			}
		}
	}
}

func TestRecorded(t *testing.T) {
	s := &Specs{Fetch: fetch.Replay(recordDir)}
	docs := map[string]*Doc{}
	for _, p := range Catalog() {
		ops, ok := recorded[p.Name]
		if !ok {
			t.Errorf("%s: not recorded", p.Name)
			continue
		}
		for _, spec := range p.Specs() {
			fi, err := os.Stat(filepath.Join(recordDir, fetch.RecordName("GET", spec, nil)))
			if err != nil {
				t.Error(err)
				continue
			}
			if fi.Size() > 200<<10 {
				t.Errorf("%s: recording is %d KB", p.Name, fi.Size()>>10)
			}
		}
		d, err := s.spec(context.Background(), "", &p)
		if err != nil || len(s.Notes()) > 0 {
			t.Fatalf("%s: %v %q", p.Name, err, s.Notes())
		}
		if got := slices.Sorted(maps.Keys(d.Ops)); !slices.Equal(got, slices.Sorted(maps.Keys(set(ops)))) {
			t.Errorf("%s: ops = %q", p.Name, got)
		}
		docs[p.Name] = d
	}

	tests := []struct {
		provider, method, url, want string
		reads                       []string // fields the response must have
	}{
		{"stripe", "POST", "https://api.stripe.com/v1/charges", "POST /v1/charges", []string{"amount", "paid", "payment_intent"}},
		{"stripe", "", "/v1/customers/cus_123/cards?limit=3", "GET /v1/customers/{customer}/cards", []string{"data", "has_more"}},
		{"github", "GET", "https://api.github.com/repos/golang/go", "GET /repos/{owner}/{repo}", []string{"full_name", "owner", "login"}},
		{"openai", "POST", "https://api.openai.com/v1/chat/completions", "POST /v1/chat/completions", []string{"choices", "message"}},
		{"openai", "POST", "/chat/completions", "POST /v1/chat/completions", nil},
		{"twilio", "POST", "https://api.twilio.com/2010-04-01/Accounts/AC123/Messages.json", "POST /2010-04-01/Accounts/{AccountSid}/Messages.json", []string{"sid", "status"}},
		{"sendgrid", "POST", "https://api.sendgrid.com/v3/mail/send", "POST /v3/mail/send", nil},
		{"plaid", "POST", "https://production.plaid.com/transactions/get", "POST /transactions/get", []string{"transactions", "accounts"}},
		{"digitalocean", "GET", "https://api.digitalocean.com/v2/droplets?per_page=10", "GET /v2/droplets", []string{"droplets"}},
		{"cloudflare", "GET", "https://api.cloudflare.com/client/v4/zones", "GET /client/v4/zones", []string{"result"}},
		{"datadog", "GET", "https://api.datadoghq.com/api/v1/monitor", "GET /api/v1/monitor", nil},
		{"datadog", "GET", "https://api.datadoghq.com/api/v2/logs/events?filter[query]=x", "GET /api/v2/logs/events", nil},
		{"sendgrid", "GET", "https://api.sendgrid.com/v3/templates?generations=dynamic", "GET /v3/templates", nil},
		{"sendgrid", "GET", "https://api.sendgrid.com/v3/marketing/contacts", "GET /v3/marketing/contacts", nil},
	}
	for _, tt := range tests {
		d := docs[tt.provider]
		if d == nil {
			continue
		}
		op := d.Find(tt.method, tt.url)
		if op == nil || op.Method+" "+op.Path != tt.want {
			t.Errorf("%s: Find(%s %s) = %+v", tt.provider, tt.method, tt.url, op)
			continue
		}
		have := fields(op.Response)
		for _, name := range tt.reads {
			if have[name] == nil {
				t.Errorf("%s %s: response has no %s", tt.provider, tt.want, name)
			}
		}
	}

	if r := docs["stripe"].Ops["POST /v1/charges"].Request; r == nil || r.Props["amount"] == nil || r.Props["currency"] == nil {
		t.Error("stripe: the form-encoded charge request should be read")
	}
	if op := docs["stripe"].Ops["GET /v1/customers/{customer}/cards"]; !op.Deprecated {
		t.Error("stripe: customer cards are deprecated")
	}
	cf := docs["cloudflare"].Ops["GET /client/v4/zones/{zone_id}/cache/origin_cloud_regions"]
	if !cf.Deprecated || !cf.Sunset.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("cloudflare: origin cloud regions deprecated %v sunset %v", cf.Deprecated, cf.Sunset)
	}
	gh := docs["github"].Ops["GET /assignments/{assignment_id}"]
	if !gh.Deprecated || !gh.Sunset.Equal(time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("github: assignment deprecated %v sunset %v", gh.Deprecated, gh.Sunset)
	}
}

func set(keys []string) map[string]bool {
	res := map[string]bool{}
	for _, k := range keys {
		res[k] = true
	}
	return res
}

func TestChangesRecorded(t *testing.T) {
	cat := Catalog()
	inv := &core.Inventory{
		Deps: []core.Dep{{Ecosystem: core.Npm, Name: "openai"}, {Ecosystem: core.Npm, Name: "stripe"}},
		Index: &index{
			uses: map[string][]core.Site{
				"openai": {call("src/ask.js", 7, "openai.chat.completions.create")},
				"stripe": {call("src/pay.js", 3, "stripe.paymentIntents.create")},
			},
			urls: []core.URLUse{
				{File: "src/cards.js", Line: 12, Method: "GET", URL: "https://api.stripe.com/v1/customers/cus_1/cards"},
				{File: "tools/class.py", Line: 30, URL: "https://api.github.com/assignments/42"},
			},
		},
	}
	inv.Providers = Detect(inv, cat)
	s := &Specs{Cat: cat, Fetch: fetch.Replay(recordDir)}
	got := changes(t, s, inv)
	want := []string{
		"deprecation api/github/GET /assignments/{assignment_id}",
		"deprecation api/stripe/GET /v1/customers/{customer}/cards",
	}
	if !slices.Equal(ids(got), want) {
		t.Fatalf("changes = %q", ids(got))
	}
	if gh := got[0]; !gh.Sunset.Equal(time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)) || !strings.HasPrefix(gh.Evidence[0].Title, "github spec ") {
		t.Errorf("github change = %+v", gh)
	}
	if len(s.Notes()) != 0 {
		t.Errorf("notes = %q", s.Notes())
	}
	if s.Current("openai") == nil || s.Current("slack") != nil {
		t.Error("Current should hold the specs this run fetched")
	}
	if used := s.runs["openai"].used; !slices.Equal(used, []string{"POST /v1/chat/completions"}) {
		t.Errorf("openai used = %q", used)
	}
	if used := s.runs["stripe"].used; !slices.Equal(used, []string{"GET /v1/customers/{customer}/cards"}) {
		t.Errorf("stripe used = %q; payment_intents is not in the recording, so the guess drops out", used)
	}
}

func TestCloudflareSunsetKey(t *testing.T) {
	p := provider(t, "cloudflare")
	spec := `{"openapi": "3.0.3", "servers": [{"url": "https://api.cloudflare.com/client/v4"}], "paths": {
"/accounts/{account_id}/access/bookmarks": {"get": {"deprecated": true, "x-cfDeprecation": {"id": "b", "eol": "2023-03-19T00:00:00Z"}}},
"/zones": {"get": {"x-cfDeprecation": {"id": "z", "description": "no date"}}}}}`
	d, err := load([]byte(spec), p.Sunset)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Ops["GET /client/v4/accounts/{account_id}/access/bookmarks"].Sunset; !got.Equal(time.Date(2023, 3, 19, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("eol = %v", got)
	}
	if got := d.Ops["GET /client/v4/zones"].Sunset; !got.IsZero() {
		t.Errorf("no eol = %v", got)
	}
}

func TestSpecList(t *testing.T) {
	var ps []Provider
	err := yaml.Unmarshal([]byte("- {name: a, hosts: [a.test], spec: [https://a.test/1.json, https://a.test/2.json], base: /v1}\n- {name: b, hosts: [b.test], spec: https://b.test/s.json}\n"), &ps)
	if err != nil {
		t.Fatal(err)
	}
	if ps[0].Spec != "https://a.test/1.json" || !slices.Equal(ps[0].More, []string{"https://a.test/2.json"}) || ps[0].Base != "/v1" || ps[0].Hosts[0] != "a.test" {
		t.Errorf("a = %+v", ps[0])
	}
	if ps[1].Spec != "https://b.test/s.json" || ps[1].More != nil || len(ps[1].Specs()) != 1 {
		t.Errorf("b = %+v", ps[1])
	}
	if got := Merge(ps, []config.API{{Name: "a", Spec: "local.yaml"}}); got[0].Spec != "local.yaml" || got[0].More != nil {
		t.Errorf("a spec from tend.yml replaces them all: %+v", got[0])
	}

	one := `{"openapi": "3.0.0", "servers": [{"url": "https://a.test"}], "paths": {"/v1/x": {"get": {}}}}`
	two := `{"openapi": "3.0.0", "servers": [{"url": "https://a.test"}], "paths": {"/v2/y": {"get": {}}}}`
	s := &Specs{Cat: ps, Fetch: fetcher{"https://a.test/1.json": one, "https://a.test/2.json": two}}
	inv := &core.Inventory{Index: &index{urls: []core.URLUse{
		{File: "a.go", Line: 1, Method: "GET", URL: "https://a.test/v1/x"},
		{File: "a.go", Line: 2, Method: "GET", URL: "https://a.test/v2/y"},
	}}}
	changes(t, s, inv)
	if used := s.runs["a"].used; !slices.Equal(used, []string{"GET /v1/x", "GET /v2/y"}) || len(s.Notes()) != 0 {
		t.Errorf("used = %q notes %q", used, s.Notes())
	}
	s.Fetch = fetcher{"https://a.test/1.json": one}
	changes(t, s, inv)
	if n := s.Notes(); len(n) != 2 || !strings.HasPrefix(n[0], "a: https://a.test/2.json: status 404") {
		t.Errorf("a missing file is a note, the rest still checked: %q", n)
	}
}
