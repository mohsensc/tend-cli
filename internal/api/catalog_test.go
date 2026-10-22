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
	"sendgrid":     {"POST /v3/mail/send"},
	"plaid":        {"POST /transactions/get"},
	"digitalocean": {"GET /v2/droplets"},
	"cloudflare":   {"GET /client/v4/zones"},
	"datadog":      {"GET /api/v1/monitor"},
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
		res, err := c.Get(context.Background(), p.Spec)
		if err != nil || res.Status != 200 {
			t.Errorf("%s: %s: %v %+v", p.Name, p.Spec, err, res)
			continue
		}
		d, err := load(res.Body, p.Sunset)
		if err != nil {
			t.Errorf("%s: %v", p.Name, err)
			continue
		}
		for _, k := range ops {
			if d.Ops[k] == nil {
				t.Errorf("%s: no %s", p.Name, k)
			}
		}
		rec, _ := json.MarshalIndent(map[string]any{"url": p.Spec, "status": 200, "body": string(trimSpec(d, ops))}, "", "  ")
		path := filepath.Join(recordDir, fetch.RecordName("GET", p.Spec, nil))
		if err := os.WriteFile(path, append(rec, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRecorded(t *testing.T) {
	f := fetch.Replay(recordDir)
	docs := map[string]*Doc{}
	for _, p := range Catalog() {
		ops, ok := recorded[p.Name]
		if !ok {
			t.Errorf("%s: not recorded", p.Name)
			continue
		}
		fi, err := os.Stat(filepath.Join(recordDir, fetch.RecordName("GET", p.Spec, nil)))
		if err != nil {
			t.Error(err)
			continue
		}
		if fi.Size() > 200<<10 {
			t.Errorf("%s: recording is %d KB", p.Name, fi.Size()>>10)
		}
		res, err := f.Get(context.Background(), p.Spec)
		if err != nil {
			t.Fatal(err)
		}
		d, err := load(res.Body, p.Sunset)
		if err != nil {
			t.Fatalf("%s: %v", p.Name, err)
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
