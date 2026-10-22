package api

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name string, v any) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs (go test -update to rewrite):\n%s", path, got)
	}
}

const fixtureSpec = "https://api.fixture.dev/openapi.json"

var clock = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// pyAPI is the inventory of a small Python service that lists orders, reads
// total_cents from each, and still calls the legacy endpoint.
func pyAPI(cat []Provider, reads ...string) *core.Inventory {
	idx := &index{urls: []core.URLUse{
		{File: "app/orders.py", Line: 8, Method: "GET", URL: "https://api.fixture.dev/v1/orders"},
		{File: "app/orders.py", Line: 14, URL: "https://api.fixture.dev/v1/orders/{order_id}/legacy"},
		{File: "app/refunds.py", Line: 3, Method: "POST", URL: "https://api.fixture.dev/v1/refunds"},
		{File: "app/client.py", Line: 2, URL: "https://api.fixture.dev/v1"},
	}}
	for i, name := range reads {
		idx.fields = append(idx.fields, field("app/orders.py", 10+i, name))
	}
	inv := &core.Inventory{Root: "/nonexistent", Index: idx}
	inv.Providers = Detect(inv, cat)
	return inv
}

func fixtureCat() []Provider {
	return Merge(Catalog(), []config.API{{Name: "fixture", Hosts: []string{"api.fixture.dev"}, Spec: fixtureSpec}})
}

func specs(t *testing.T, version string) *Specs {
	return &Specs{
		Cat:   fixtureCat(),
		Fetch: fetcher{fixtureSpec: read(t, "testdata/fixture/"+version+".yaml")},
		Dir:   t.TempDir(),
		Now:   func() time.Time { return clock },
	}
}

func changes(t *testing.T, s *Specs, inv *core.Inventory) []core.Change {
	t.Helper()
	res, err := s.Changes(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func ids(cs []core.Change) []string {
	var res []string
	for _, c := range cs {
		res = append(res, string(c.Kind)+" "+c.ID)
	}
	return res
}

func TestChangesWithoutSnapshot(t *testing.T) {
	s := specs(t, "v2")
	got := changes(t, s, pyAPI(s.Cat, "total_cents", "amount"))
	golden(t, "fresh.json", got)
	want := []string{"fixture: unmapped endpoint POST https://api.fixture.dev/v1/refunds at app/refunds.py:3"}
	if !slices.Equal(s.Notes(), want) {
		t.Errorf("notes = %q", s.Notes())
	}
}

func TestEndpoint(t *testing.T) {
	tests := []struct {
		lit  string
		want bool
	}{
		{"https://api.fixture.dev/v1/refunds", true},
		{"https://api.fixture.dev/v1/orders?limit=3", true},
		{"https://api.fixture.dev", false},
		{"https://api.fixture.dev/", false},
		{"https://api.github.com/repos/", false},
		{"/v1/orders", false},
	}
	for _, tt := range tests {
		if got := endpoint(tt.lit); got != tt.want {
			t.Errorf("endpoint(%q) = %v, want %v", tt.lit, got, tt.want)
		}
	}
}

func TestChangesWithSnapshot(t *testing.T) {
	s := specs(t, "v1")
	inv := pyAPI(s.Cat, "total_cents", "amount")
	if got := changes(t, s, inv); len(got) != 0 {
		t.Fatalf("v1 has nothing to report, got %q", ids(got))
	}
	if err := s.Snapshot("fixture", s.Current("fixture")); err != nil {
		t.Fatal(err)
	}

	s.Fetch = fetcher{fixtureSpec: read(t, "testdata/fixture/v2.yaml")}
	golden(t, "snapshot.json", changes(t, s, inv))

	// The run went through, so the brain snapshots v2. The removed field stays
	// a change while the code still reads it.
	if err := s.Snapshot("fixture", s.Current("fixture")); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"breaking api/fixture/GET /v1/orders#total_cents",
		"deprecation api/fixture/GET /v1/orders/{id}/legacy",
	}
	if got := ids(changes(t, s, inv)); !slices.Equal(got, want) {
		t.Errorf("after a snapshot of v2 = %q", got)
	}

	// Once the code reads total instead, the snapshot moves on and the old
	// field is forgotten.
	fixed := pyAPI(s.Cat, "total", "amount")
	if got := ids(changes(t, s, fixed)); !slices.Equal(got, want[1:]) {
		t.Errorf("after the fix = %q", got)
	}
	if err := s.Snapshot("fixture", s.Current("fixture")); err != nil {
		t.Fatal(err)
	}
	if got := ids(changes(t, s, inv)); !slices.Equal(got, want[1:]) {
		t.Errorf("a field gone before the snapshot is not a change = %q", got)
	}
	snap := read(t, filepath.Join(s.Dir, "fixture.json"))
	if strings.Contains(snap, "total_cents") || !strings.Contains(snap, `"version": "2.0.0"`) || !strings.Contains(snap, "x-tend-snapshot") {
		t.Errorf("snapshot:\n%s", snap)
	}
}

func TestChangesOperationRemoved(t *testing.T) {
	s := specs(t, "v1")
	inv := pyAPI(s.Cat, "amount")
	changes(t, s, inv)
	if err := s.Snapshot("fixture", s.Current("fixture")); err != nil {
		t.Fatal(err)
	}
	v3 := strings.Replace(read(t, "testdata/fixture/v2.yaml"), "/v1/orders/{id}/legacy:", "/v1/orders/{id}/old:", 1)
	s.Fetch = fetcher{fixtureSpec: v3}
	got := changes(t, s, inv)
	if want := []string{"breaking api/fixture/GET /v1/orders/{id}/legacy"}; !slices.Equal(ids(got), want) {
		t.Fatalf("changes = %q", ids(got))
	}
	c := got[0]
	if c.API.What != "operation removed" || !c.Published.Equal(clock) || len(c.Sites) != 1 || c.Sites[0].Line != 14 {
		t.Errorf("change = %+v", c)
	}
	if len(s.Notes()) != 1 {
		t.Errorf("only the refunds call is unmapped: %q", s.Notes())
	}
}

func TestChangesSDK(t *testing.T) {
	root := t.TempDir()
	spec := `openapi: 3.0.0
info: {version: "2026-09-30"}
servers: [{url: "https://api.stripe.com/"}]
paths:
  /v1/charges:
    post:
      summary: Create a charge
      deprecated: true
      requestBody:
        content:
          application/x-www-form-urlencoded:
            schema:
              properties:
                amount: {type: integer}
                source: {type: string, deprecated: true}
      responses:
        "200":
          content:
            application/json:
              schema: {$ref: '#/components/schemas/Charge'}
  /v1/charges/{charge}:
    get:
      parameters:
        - {name: expand, in: query, deprecated: true}
      responses:
        "200":
          content:
            application/json:
              schema: {$ref: '#/components/schemas/Gone'}
components:
  schemas:
    Charge:
      properties:
        id: {type: string}
        paid: {type: boolean, deprecated: true}
`
	if err := os.MkdirAll(filepath.Join(root, "specs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "specs", "stripe.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	cat := Merge(Catalog(), []config.API{{Name: "stripe", Spec: "specs/stripe.yaml"}})
	inv := &core.Inventory{
		Root: root,
		Deps: []core.Dep{{Ecosystem: core.Npm, Name: "stripe"}, {Ecosystem: core.Go, Name: "github.com/stripe/stripe-go/v79"}},
		Index: &index{
			uses: map[string][]core.Site{
				"stripe": {
					call("src/pay.js", 4, "stripe.charges.create"),
					call("src/pay.js", 9, "stripe.charges.retrieve"),
					{File: "src/pay.js", Line: 12, Chain: []string{"stripe", "charges", "create"}},
					call("src/pay.js", 15, "stripe.refunds.create"),
				},
				"github.com/stripe/stripe-go/v79": {call("pay/charge.go", 20, "charge.New")},
			},
			fields: []core.Site{
				field("src/pay.js", 5, "paid"),
				field("src/pay.js", 9, "expand"),
				field("pay/charge.go", 21, "paid"),
				field("src/other.js", 1, "source"),
			},
		},
	}
	inv.Providers = Detect(inv, cat)
	s := &Specs{Cat: cat, Dir: t.TempDir()}
	got := changes(t, s, inv)
	golden(t, "sdk.json", got)

	want := []string{"stripe: 1 unresolved $ref in the spec, first #/components/schemas/Gone"}
	if !slices.Equal(s.Notes(), want) {
		t.Errorf("notes = %q", s.Notes())
	}
}

func TestChangesSkipsBadSpecs(t *testing.T) {
	cat := []Provider{
		{Name: "swagger", Hosts: []string{"a.test"}, Spec: "https://a.test/spec.json"},
		{Name: "gone", Hosts: []string{"b.test"}, Spec: "https://b.test/spec.json"},
		{Name: "none", Hosts: []string{"c.test"}},
		{Name: "local", Hosts: []string{"d.test"}, Spec: "missing.yaml"},
		{Name: "fine", Hosts: []string{"e.test"}, Spec: "https://e.test/spec.json"},
	}
	idx := &index{}
	for _, h := range []string{"a", "b", "c", "d", "e"} {
		idx.urls = append(idx.urls, core.URLUse{File: "main.go", Line: 1, Method: "GET", URL: "https://" + h + ".test/x"})
	}
	inv := &core.Inventory{Root: t.TempDir(), Index: idx}
	s := &Specs{Cat: cat, Fetch: fetcher{
		"https://a.test/spec.json": `{"swagger": "2.0"}`,
		"https://e.test/spec.json": `{"openapi": "3.0.0", "paths": {"/x": {"get": {"deprecated": true}}}}`,
	}}
	got := changes(t, s, inv)
	if want := []string{"deprecation api/fine/GET /x"}; !slices.Equal(ids(got), want) {
		t.Errorf("changes = %q", ids(got))
	}
	// A provider without a spec is the changelog source's, not a broken one.
	notes := s.Notes()
	if len(notes) != 3 {
		t.Fatalf("want one note per broken provider, got %q", notes)
	}
	for i, prefix := range []string{"swagger: openapi: swagger 2.0", "gone: https://b.test/spec.json: status 404", "local: open "} {
		if !strings.HasPrefix(notes[i], prefix) {
			t.Errorf("note %d = %q, want prefix %q", i, notes[i], prefix)
		}
	}
}

func TestChangesCanceled(t *testing.T) {
	s := specs(t, "v2")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Changes(ctx, pyAPI(s.Cat)); err == nil {
		t.Error("a canceled run should stop")
	}
}

func TestSnapshotBadFile(t *testing.T) {
	s := specs(t, "v2")
	if err := os.WriteFile(filepath.Join(s.Dir, "fixture.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := changes(t, s, pyAPI(s.Cat, "total_cents"))
	if len(got) != 1 || len(s.Notes()) != 2 || !strings.HasPrefix(s.Notes()[0], "fixture: snapshot: ") {
		t.Errorf("changes %q notes %q", ids(got), s.Notes())
	}
	if err := s.Snapshot("fixture", nil); err == nil {
		t.Error("snapshot of nothing should fail")
	}
	if err := s.Snapshot("stripe", s.Current("fixture")); err == nil {
		t.Error("snapshot of a provider the run did not check should fail")
	}
}

func TestChangesParams(t *testing.T) {
	v1 := read(t, "testdata/fixture/v1.yaml")
	tests := []struct {
		name, url string
		params    string // replaces the status parameter of GET /v1/orders
		reads     []string
		want      []string
	}{
		{
			name:   "required added, set in the query",
			url:    "https://api.fixture.dev/v1/orders?status=open",
			params: "- {name: status, in: query, required: true}",
		},
		{
			name:   "required added, set by name",
			url:    "https://api.fixture.dev/v1/orders",
			params: "- {name: status, in: query, required: true}",
			reads:  []string{"status"},
		},
		{
			name:   "required added, never set",
			url:    "https://api.fixture.dev/v1/orders?state=open",
			params: "- {name: status, in: query, required: true}\n        - {name: page, in: header}",
			want:   []string{"breaking api/fixture/GET /v1/orders#status"},
		},
		{
			name:   "removed, set in the query",
			url:    "https://api.fixture.dev/v1/orders?status=open",
			params: "- {name: state, in: query}",
			want:   []string{"breaking api/fixture/GET /v1/orders#status"},
		},
		{
			name:   "removed, never set",
			url:    "https://api.fixture.dev/v1/orders",
			params: "- {name: state, in: query}",
		},
		{
			name:   "deprecated, set by name",
			url:    "https://api.fixture.dev/v1/orders",
			params: "- {name: status, in: query, deprecated: true}",
			reads:  []string{"status"},
			want:   []string{"deprecation api/fixture/GET /v1/orders#status"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx := &index{urls: []core.URLUse{{File: "app/orders.py", Line: 8, Method: "GET", URL: tt.url}}}
			for _, name := range tt.reads {
				idx.fields = append(idx.fields, field("app/orders.py", 9, name))
			}
			inv := &core.Inventory{Index: idx}
			s := specs(t, "v1")
			if got := changes(t, s, inv); len(got) != 0 {
				t.Fatalf("v1 = %q", ids(got))
			}
			if err := s.Snapshot("fixture", s.Current("fixture")); err != nil {
				t.Fatal(err)
			}
			s.Fetch = fetcher{fixtureSpec: strings.Replace(v1, "- {name: status, in: query}", tt.params, 1)}
			got := changes(t, s, inv)
			if !slices.Equal(ids(got), tt.want) {
				t.Errorf("changes = %q, want %q", ids(got), tt.want)
			}
			for _, c := range got {
				if c.Kind == core.Breaking && !c.Published.Equal(clock) {
					t.Errorf("a change from the diff should date from the snapshot: %v", c.Published)
				}
			}
		})
	}
}

func TestOptions(t *testing.T) {
	got := options("stripe", "POST", "/v1/charges", "")
	if len(got) != 2 || got[0].Safe || got[0].Codemod == nil || got[0].Codemod.Renames["charges.create"] != "paymentIntents.create" {
		t.Fatalf("options = %+v", got)
	}
	got[0].Title = "changed"
	if options("stripe", "POST", "/v1/charges", "")[0].Title == "changed" {
		t.Error("options should be a fresh copy each time")
	}
	if options("stripe", "POST", "/v1/charges", "amount") != nil || options("stripe", "GET", "/v1/charges", "") != nil {
		t.Error("options should match field and method exactly")
	}
}

func TestCombine(t *testing.T) {
	dep := core.Change{Kind: core.Deprecation, Sites: []core.Site{{File: "a.js", Line: 3}, {File: "b.js", Line: 1}}}
	brk := core.Change{Kind: core.Breaking, Sites: []core.Site{{File: "a.js", Line: 3}, {File: "a.js", Line: 1}}}
	for _, got := range []core.Change{combine(dep, brk), combine(brk, dep)} {
		var at []string
		for _, s := range got.Sites {
			at = append(at, fmt.Sprintf("%s:%d", s.File, s.Line))
		}
		if got.Kind != core.Breaking || !slices.Equal(at, []string{"a.js:1", "a.js:3", "b.js:1"}) {
			t.Errorf("combine = %s %q", got.Kind, at)
		}
	}
}
