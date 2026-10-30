package api

import (
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func shop(t *testing.T, name string) *Doc {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	d, err := load(b, "x-github.removalDate")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestLoad(t *testing.T) {
	for _, name := range []string{"shop.yaml", "shop.json"} {
		t.Run(name, func(t *testing.T) {
			d := shop(t, name)
			if d.Version != "2.4.0" || d.Base != "/v2" {
				t.Errorf("version %q base %q", d.Version, d.Base)
			}
			want := []string{
				"DELETE /v2/orders/{id}", "GET /v2/broken", "GET /v2/files/{name:[a-z]{2,}}", "GET /v2/items/{sku}.json",
				"GET /v2/orders", "GET /v2/orders/legacy", "GET /v2/orders/{id}",
				"POST /v2/orders", "POST /v2/orders/{id}/refund",
			}
			if got := slices.Sorted(maps.Keys(d.Ops)); !slices.Equal(got, want) {
				t.Errorf("ops = %q", got)
			}
			if !slices.Equal(d.Missing, []string{"#/components/schemas/Nope"}) {
				t.Errorf("missing = %q", d.Missing)
			}

			list := d.Ops["GET /v2/orders"]
			if want := []Param{{Name: "cursor", In: "query", Deprecated: true}, {Name: "limit", In: "query"}}; !slices.Equal(list.Params, want) {
				t.Errorf("list params = %+v", list.Params)
			}
			if list.Summary != "List orders" || list.Deprecated {
				t.Errorf("list = %+v", list)
			}

			get := d.Ops["GET /v2/orders/{id}"]
			if want := []Param{{Name: "id", In: "path", Required: true}, {Name: "expand", In: "query", Required: true}}; !slices.Equal(get.Params, want) {
				t.Errorf("operation params should win over path-level ones: %+v", get.Params)
			}
			if !get.Deprecated || !get.Sunset.Equal(time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)) {
				t.Errorf("get deprecated %v sunset %v", get.Deprecated, get.Sunset)
			}
			if refund := d.Ops["POST /v2/orders/{id}/refund"]; !refund.Deprecated || !refund.Sunset.IsZero() {
				t.Errorf("x-deprecated-at should deprecate without a sunset: %+v", refund)
			}
			legacy := d.Ops["GET /v2/orders/legacy"]
			if !legacy.Sunset.Equal(time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)) || legacy.Summary != "Old listing, kept for one more year." {
				t.Errorf("legacy = %+v", legacy)
			}

			order := get.Response
			if order == nil || order != d.Schemas["Order"] {
				t.Fatalf("GET response should be the shared Order schema, got %+v", order)
			}
			if got := slices.Sorted(maps.Keys(order.Props)); !slices.Equal(got, []string{"coupon", "id", "lines", "note", "parent", "payment", "tax", "total", "total_cents"}) {
				t.Errorf("Order props = %q", got)
			}
			if !slices.Equal(order.Required, []string{"id", "total"}) {
				t.Errorf("Order required = %q", order.Required)
			}
			if order.Props["parent"] != order {
				t.Error("a recursive ref should point back at the same schema")
			}
			if p := order.Props["note"]; p.Type != "string" {
				t.Errorf("3.1 nullable type = %q", p.Type)
			}
			if p := order.Props["coupon"]; p.Type != "string" {
				t.Errorf("3.0 nullable type = %q", p.Type)
			}
			if !order.Props["total_cents"].Deprecated {
				t.Error("total_cents should be deprecated")
			}
			if tax := order.Props["tax"]; !tax.Deprecated || d.Schemas["Money"].Deprecated || tax.Props["amount"] == nil {
				t.Error("deprecated beside a $ref should mark the field, not the shared schema")
			}
			if got := slices.Sorted(maps.Keys(order.Props["payment"].Props)); !slices.Equal(got, []string{"iban", "last4"}) {
				t.Errorf("oneOf props = %q", got)
			}
			if order.Props["payment"].Required != nil {
				t.Errorf("oneOf should not make fields required: %q", order.Props["payment"].Required)
			}
			if lines := order.Props["lines"]; lines.Type != "array" || lines.Items != d.Schemas["Line"] {
				t.Errorf("lines = %+v", lines)
			}
			if d.Schemas["Price"].Props["currency"] == nil {
				t.Error("an aliased component should resolve")
			}

			if items := list.Response.Props["data"].Items; items != order {
				t.Errorf("list data items = %+v", items)
			}
			post := d.Ops["POST /v2/orders"]
			if post.Request == nil || !slices.Equal(post.Request.Required, []string{"lines"}) || post.Response != order {
				t.Errorf("post = %+v", post)
			}
			if r := d.Ops["POST /v2/orders/{id}/refund"].Request; r == nil || r.Props["amount"] == nil {
				t.Error("a form request body should be read when there is no JSON one")
			}
			if r := d.Ops["GET /v2/items/{sku}.json"].Response; r != d.Schemas["Line"] {
				t.Error("a +json media type should be read")
			}
			if r := d.Ops["GET /v2/broken"].Response; r != nil {
				t.Errorf("missing ref should leave no schema, got %+v", r)
			}
		})
	}
}

func TestLoadSame(t *testing.T) {
	y, j := shop(t, "shop.yaml"), shop(t, "shop.json")
	if string(trimSpec(y, nil)) != string(trimSpec(j, nil)) {
		t.Error("YAML and JSON load to different models")
	}
}

func TestLoadSunsetKey(t *testing.T) {
	b, _ := os.ReadFile("testdata/shop.yaml")
	d, err := Load(b)
	if err != nil {
		t.Fatal(err)
	}
	if s := d.Ops["GET /v2/orders/legacy"].Sunset; !s.IsZero() {
		t.Errorf("without a sunset key the removal date is not a sunset: %v", s)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct{ name, src string }{
		{"swagger", `{"swagger": "2.0", "paths": {}}`},
		{"no version", `{"info": {"version": "1"}}`},
		{"openapi 2", `openapi: "2.0"`},
		{"bad json", `{"openapi": "3.0.0",`},
		{"bad yaml", "openapi: [3\n"},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if d, err := Load([]byte(tt.src)); err == nil {
				t.Errorf("Load = %+v, want an error", d)
			}
		})
	}
}

func TestLoadTolerant(t *testing.T) {
	src := `{"openapi": "3.0.0", "paths": {"/a": {"get": {"deprecated": "yes", "summary": 7,
		"responses": {"200": {"content": {"application/json": {"schema": {"required": true, "properties": {"x": {"type": "string"}}}}}}}}}}}`
	d, err := Load([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	op := d.Ops["GET /a"]
	if op == nil || op.Response == nil || op.Response.Props["x"] == nil {
		t.Errorf("fields of the wrong type should not lose the rest: %+v", op)
	}

	d, err = Load([]byte("openapi: 3.0.0\ninfo: {version: 2.1}\nservers: {url: https://x.test/v1}\npaths: {/a: {get: {}}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Version != "2.1" || d.Base != "" || d.Ops["GET /a"] == nil {
		t.Errorf("version %q base %q ops %q", d.Version, d.Base, slices.Sorted(maps.Keys(d.Ops)))
	}
}

func TestTrimSpec(t *testing.T) {
	d := shop(t, "shop.yaml")
	d.At = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	keep := []string{"GET /v2/orders/{id}", "GET /v2/orders/legacy"}
	b := trimSpec(d, keep)
	got, err := Load(b)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(slices.Sorted(maps.Keys(got.Ops)), []string{"GET /v2/orders/legacy", "GET /v2/orders/{id}"}) {
		t.Errorf("ops = %q", slices.Sorted(maps.Keys(got.Ops)))
	}
	if got.Base != "/v2" || got.Version != "2.4.0" || !got.At.Equal(d.At) {
		t.Errorf("base %q version %q at %v", got.Base, got.Version, got.At)
	}
	if string(trimSpec(got, nil)) != string(b) {
		t.Error("a trimmed spec should load back to the same model")
	}
	if s := got.Ops["GET /v2/orders/legacy"].Sunset; !s.Equal(d.Ops["GET /v2/orders/legacy"].Sunset) {
		t.Errorf("a sunset from the catalog key should survive as x-sunset: %v", s)
	}
	curr := got.Ops["GET /v2/orders/{id}"].Response
	for range depth {
		if curr = curr.Props["parent"]; curr == nil {
			t.Fatal("recursion should be kept down to the depth bound")
		}
	}
	if curr.Props != nil || curr.Type != "object" {
		t.Errorf("recursion should stop at depth %d with the type only: %+v", depth, curr)
	}
	full := shop(t, "shop.yaml")
	again, _ := Load(trimSpec(full, nil))
	if string(trimSpec(again, nil)) != string(trimSpec(full, nil)) {
		t.Error("a trimmed spec should load back to the spec it came from")
	}
}

func TestFind(t *testing.T) {
	d := shop(t, "shop.yaml")
	tests := []struct {
		method, path, want string
	}{
		{"GET", "/v2/orders", "GET /v2/orders"},
		{"get", "/v2/orders/", "GET /v2/orders"},
		{"GET", "/v2/orders?limit=10&cursor=x", "GET /v2/orders"},
		{"GET", "https://api.shop.test/v2/orders#top", "GET /v2/orders"},
		{"GET", "http://localhost:8080/v2/orders", "GET /v2/orders"},
		{"GET", "/orders", "GET /v2/orders"},
		{"GET", "orders", "GET /v2/orders"},
		{"", "/v2/orders", "GET /v2/orders"},
		{"POST", "/v2/orders", "POST /v2/orders"},
		{"GET", "/v2/orders/123", "GET /v2/orders/{id}"},
		{"GET", "/v2/orders/{order_id}", "GET /v2/orders/{id}"},
		{"GET", "/v2/orders/${id}", "GET /v2/orders/{id}"},
		{"GET", "/v2/orders/legacy", "GET /v2/orders/legacy"},
		{"", "/v2/orders/42/refund", "POST /v2/orders/{id}/refund"},
		{"GET", "/v2/items/ab-12.json", "GET /v2/items/{sku}.json"},
		{"GET", "/v2/items/.json", ""},
		{"GET", "/v2/items/ab-12", ""},
		{"GET", "/v2/files/readme", "GET /v2/files/{name:[a-z]{2,}}"},
		{"", "/v2/orders/7", "GET /v2/orders/{id}"},
		{"DELETE", "/v2/orders/7", "DELETE /v2/orders/{id}"},
		{"PUT", "/v2/orders/1", ""},
		{"GET", "/v2/orders/1/refund/now", ""},
		{"GET", "/v3/orders", ""},
		{"GET", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			got := ""
			if op := d.Find(tt.method, tt.path); op != nil {
				got = op.Method + " " + op.Path
			}
			if got != tt.want {
				t.Errorf("Find = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFindLoose(t *testing.T) {
	d := &Doc{Ops: map[string]*Op{"POST /v1/payment_intents": {Method: "POST", Path: "/v1/payment_intents"}}}
	if d.Find("POST", "/v1/paymentintents") != nil {
		t.Error("Find should be exact")
	}
	if d.find("POST", "/v1/paymentintents", true) == nil {
		t.Error("loose find should ignore underscores")
	}
	var nilDoc *Doc
	if nilDoc.Find("GET", "/") != nil {
		t.Error("a nil doc finds nothing")
	}
}

func TestSplit(t *testing.T) {
	tests := []struct {
		seg  string
		want []part
	}{
		{"orders", []part{{lit: "orders"}}},
		{"{id}", []part{{param: true}}},
		{"{sku}.json", []part{{param: true}, {lit: ".json"}}},
		{"v{major}.{minor}", []part{{lit: "v"}, {param: true}, {lit: "."}, {param: true}}},
		{"{name:[a-z]{2,}}", []part{{param: true}}},
		{"{open", []part{{lit: "{open"}}},
		{"a{b", []part{{lit: "a"}, {lit: "{b"}}},
		{"close}", []part{{lit: "close}"}}},
	}
	for _, tt := range tests {
		if got := split(tt.seg); !slices.Equal(got, tt.want) {
			t.Errorf("split(%q) = %+v, want %+v", tt.seg, got, tt.want)
		}
	}
}

func load3(t *testing.T, spec string) *Doc {
	t.Helper()
	d, err := Load([]byte(spec))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func customerSpec(customer string) string {
	return `openapi: 3.1.0
info: {version: "1"}
paths:
  /v1/o:
    get:
      responses:
        "200":
          content:
            application/json:
              schema:
                type: object
                properties:
                  customer: ` + customer + `
components:
  schemas:
    C:
      type: object
      properties:
        id: {type: string}
    Untyped:
      properties:
        id: {type: string}
`
}

func TestUnionType(t *testing.T) {
	tests := []struct {
		name, customer, want string
	}{
		{"ref", `{$ref: '#/components/schemas/C'}`, "object"},
		{"nullable ref", `{anyOf: [{type: "null"}, {$ref: '#/components/schemas/C'}]}`, "object"},
		{"ref then null", `{anyOf: [{$ref: '#/components/schemas/C'}, {type: "null"}]}`, "object"},
		{"untyped ref and null", `{anyOf: [{type: "null"}, {$ref: '#/components/schemas/Untyped'}]}`, "object"},
		{"only null", `{oneOf: [{type: "null"}]}`, ""},
		{"string or object", `{anyOf: [{type: string}, {$ref: '#/components/schemas/C'}]}`, ""},
		{"object or string", `{anyOf: [{$ref: '#/components/schemas/C'}, {type: string}]}`, ""},
		{"allOf", `{allOf: [{type: integer}]}`, "integer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := load3(t, customerSpec(tt.customer))
			got := d.Ops["GET /v1/o"].Response.Props["customer"]
			if got == nil || got.Type != tt.want {
				t.Fatalf("customer = %+v, want type %q", got, tt.want)
			}
		})
	}
}

func TestUnionTypeDiff(t *testing.T) {
	ref := `{$ref: '#/components/schemas/C'}`
	tests := []struct{ old, cur string }{
		{ref, `{anyOf: [{type: "null"}, {$ref: '#/components/schemas/C'}]}`},
		{ref, `{anyOf: [{$ref: '#/components/schemas/C'}, {type: "null"}]}`},
		{`{anyOf: [{type: string}, {$ref: '#/components/schemas/C'}]}`, `{anyOf: [{$ref: '#/components/schemas/C'}, {type: string}]}`},
		{`{anyOf: [{type: "null"}, {$ref: '#/components/schemas/Untyped'}]}`, ref},
		{`{type: "null"}`, ref},
	}
	used := []Usage{{Method: "GET", Path: "/v1/o"}}
	for _, tt := range tests {
		old, cur := load3(t, customerSpec(tt.old)), load3(t, customerSpec(tt.cur))
		if got := Diff(old, cur, used); len(got) != 0 {
			t.Errorf("%s -> %s: %v", tt.old, tt.cur, got)
		}
		snap := load3(t, string(trimSpec(old, nil)))
		if got := Diff(snap, cur, used); len(got) != 0 {
			t.Errorf("snapshot of %s -> %s: %v", tt.old, tt.cur, got)
		}
	}
}

const wildcardSpec = `openapi: 3.0.1
info: {version: "1"}
paths:
  /a:
    get:
      responses:
        "200":
          content:
            "*/*":
              schema:
                type: object
                properties:
                  x: {type: string}
                  y: {type: integer}
  /b:
    get:
      responses:
        "200":
          content:
            "*/*":
              schema: {properties: {wild: {type: string}}}
            "Application/JSON ; charset=utf-8":
              schema: {properties: {json: {type: string}}}
`

func TestLoadWildcardBody(t *testing.T) {
	d := load3(t, wildcardSpec)
	a := d.Find("GET", "/a")
	if a == nil || a.Response == nil || a.Response.Props["x"] == nil {
		t.Fatalf("a */* response should have a schema: %+v", a)
	}
	if b := d.Find("GET", "/b").Response; b == nil || b.Props["json"] == nil {
		t.Errorf("JSON should win over */*, whatever its case: %+v", b)
	}
	cur := load3(t, strings.Replace(wildcardSpec, "                  x: {type: string}\n", "", 1))
	got := Diff(d, cur, []Usage{{Method: "GET", Path: "/a"}})
	if len(got) != 1 || got[0].What != "response field removed" || got[0].Field != "x" {
		t.Errorf("diff = %v", got)
	}
}

func TestLoadUnquotedVersion(t *testing.T) {
	d := load3(t, "openapi: 3.0.0\ninfo: {title: x, version: 2.10}\npaths: {}\n")
	if d.Version != "2.10" {
		t.Errorf("version = %q", d.Version)
	}
	d = load3(t, "openapi: 3.0.0\ninfo:\n  version: 2026-09-30\npaths: {}\n")
	if d.Version != "2026-09-30" {
		t.Errorf("version = %q", d.Version)
	}
}

func TestFindTrailingSlash(t *testing.T) {
	d := load3(t, `openapi: 3.0.0
info: {version: "1"}
servers: [{url: "https://x.test/v1"}]
paths:
  /:
    get: {responses: {}}
  /users/:
    get: {responses: {}}
`)
	tests := []struct{ path, want string }{
		{"https://x.test/v1/", "GET /v1/"},
		{"/v1", "GET /v1/"},
		{"/v1/users/", "GET /v1/users/"},
		{"https://x.test/v1/users/?a=1", "GET /v1/users/"},
		{"/users", "GET /v1/users/"},
	}
	for _, tt := range tests {
		got := ""
		if op := d.Find("GET", tt.path); op != nil {
			got = op.Method + " " + op.Path
		}
		if got != tt.want {
			t.Errorf("Find(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
	plain := load3(t, "openapi: 3.0.0\ninfo: {version: \"1\"}\npaths:\n  /users/:\n    get: {responses: {}}\n")
	if plain.Find("GET", "https://api.x/users/?a=1") == nil || plain.Find("GET", "/users/") == nil {
		t.Error("a /users/ template should be found")
	}
}
