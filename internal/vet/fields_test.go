package vet

import (
	"context"
	"testing"

	"github.com/mohsensc/tend-cli/internal/api"
	"github.com/mohsensc/tend-cli/internal/core"
)

func ordersSpec() *api.Doc {
	order := &api.Schema{Type: "object", Props: map[string]*api.Schema{
		"id": {Type: "string"},
		"total": {Type: "object", Props: map[string]*api.Schema{
			"amount":   {Type: "integer"},
			"currency": {Type: "string"},
		}},
	}}
	order.Props["parent"] = order
	return &api.Doc{Version: "2.0.0", Ops: map[string]*api.Op{
		"GET /v1/orders/{id}": {
			Method: "GET", Path: "/v1/orders/{id}", Response: order,
			Params: []api.Param{{Name: "id", In: "path", Required: true}, {Name: "expand", In: "query"}},
		},
	}}
}

const ordersBase = `import urllib.request

def total(order):
    return order["total_cents"]

def label(order):
    return order["total_cents"]
`

func TestFields(t *testing.T) {
	removed := &core.APIChange{Provider: "fixture", Method: "get", Path: "/v1/orders/{id}", Field: "total_cents", What: "response field removed"}
	tests := []struct {
		name    string
		patched string
		want    []string
	}{
		{
			"typo and a leftover",
			`import urllib.request

def total(order):
    amount = order["total"]["amount"]
    cur = order["currncy"]
    return amount

def label(order):
    return order["total_cents"]
`,
			[]string{
				"flagged app/orders.py:5 `currncy` is not a field of GET /v1/orders/{id} in spec 2.0.0",
				"flagged app/orders.py:9 `total_cents` is still used here (response field removed)",
			},
		},
		{
			"clean migration",
			`import urllib.request

def total(order):
    # cents are gone in v2
    return order["total"]["amount"], order.get("expand"), order.parent.id

def label(order):
    return order["total"]["currency"]
`,
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, sha, diff := worktree(t, map[string]string{"app/orders.py": ordersBase}, map[string]string{"app/orders.py": tt.patched})
			in := Input{
				Dir: dir, Base: sha,
				Change: &core.Change{API: removed},
				Patch:  &core.Patch{Diff: diff},
				Index:  &index{dir: dir},
				Spec:   ordersSpec(),
			}
			got, skip := fields(context.Background(), newPass(in))
			if skip != "" {
				t.Fatalf("skipped: %s", skip)
			}
			eq(t, got, tt.want...)
		})
	}
}

func TestFieldsSkips(t *testing.T) {
	in := Input{Patch: &core.Patch{}, Index: &index{dir: t.TempDir()}}
	for _, tt := range []struct {
		ch   *core.Change
		want string
	}{
		{&core.Change{Deps: []core.Dep{{Name: "x"}}}, na},
		{&core.Change{API: &core.APIChange{Method: "GET", Path: "/v1/x", What: "operation deprecated"}}, na},
	} {
		in.Change = tt.ch
		if _, skip := fields(context.Background(), newPass(in)); skip != tt.want {
			t.Errorf("skip = %q, want %q", skip, tt.want)
		}
	}
}

func TestSchemaNames(t *testing.T) {
	got := schemaNames(ordersSpec().Ops["GET /v1/orders/{id}"])
	for _, n := range []string{"id", "total", "amount", "currency", "parent", "expand"} {
		if !got[n] {
			t.Errorf("missing %s", n)
		}
	}
	if len(got) != 6 {
		t.Errorf("names = %v", got)
	}
}

func TestFieldNames(t *testing.T) {
	tests := []struct {
		line string
		want []string
	}{
		{`x = order["total"]["amount"]`, []string{"total", "amount"}},
		{`order.total.amount.toFixed(2)`, []string{"total", "amount"}},
		{`cur = order.get("currency")`, []string{"currency"}},
		{`return {amount: o.total, "currency": c}`, []string{"total", "amount", "currency"}},
	}
	for _, tt := range tests {
		got := fieldNames(tt.line)
		if len(got) != len(tt.want) {
			t.Errorf("fieldNames(%q) = %q, want %q", tt.line, got, tt.want)
			continue
		}
		for _, w := range tt.want {
			found := false
			for _, g := range got {
				found = found || g == w
			}
			if !found {
				t.Errorf("fieldNames(%q) = %q, missing %q", tt.line, got, w)
			}
		}
	}
}
