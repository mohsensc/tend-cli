package vet

import (
	"context"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

func TestLeftovers(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"src/a.js":       "const request = require('request')\nrequest('x')\n",
		"src/b.js":       "const undici = require('undici')\n",
		"src/billing.js": "const pay = require('@fixture/pay')\npay.charges.create({})\npay.paymentIntents.create({})\n",
		"app/orders.py":  "x = order[\"total\"]\ny = order[\"total_cents\"]\n",
	})
	idx := &index{dir: dir,
		imports: []core.Import{
			{File: "src/a.js", Line: 1, Package: "request", Binding: "request"},
			{File: "src/b.js", Line: 1, Package: "undici", Binding: "undici"},
			{File: "src/billing.js", Line: 1, Package: "@fixture/pay", Binding: "pay"},
		},
		endpoints: []core.URLUse{
			{File: "app/client.py", Line: 3, URL: "https://api.fixture.dev/v1/orders/42/legacy"},
			{File: "app/client.py", Line: 4, URL: "/v1/orders/42"},
			{File: "app/client.py", Line: 5, URL: "/orders/{id}/legacy?x=1"},
		},
	}
	pay := core.Dep{Ecosystem: core.Npm, Name: "@fixture/pay"}
	tests := []struct {
		name  string
		ch    *core.Change
		hosts []string
		want  []string
	}{
		{
			"swapped package still imported",
			&core.Change{Deps: []core.Dep{{Ecosystem: core.Npm, Name: "request"}}, ToName: "undici", To: "6.0.0"},
			nil,
			[]string{"flagged src/a.js:1 still imports `request`"},
		},
		{
			"renamed member still called",
			&core.Change{Deps: []core.Dep{pay}, To: "2.0.0", Options: []core.Option{
				{Codemod: &core.Codemod{Renames: map[string]string{"charges.create": "paymentIntents.create"}}},
			}},
			nil,
			[]string{"flagged src/billing.js:2 still calls `pay.charges.create`"},
		},
		{
			"deprecated endpoint still called",
			&core.Change{API: &core.APIChange{Provider: "fixture", Method: "GET", Path: "/v1/orders/{id}/legacy", What: "operation deprecated"}},
			[]string{"api.fixture.dev"},
			[]string{
				"flagged app/client.py:3 still calls `https://api.fixture.dev/v1/orders/42/legacy`",
				"flagged app/client.py:5 still calls `/orders/{id}/legacy?x=1`",
			},
		},
		{
			"removed field still read",
			&core.Change{API: &core.APIChange{Provider: "fixture", Method: "GET", Path: "/v1/orders", Field: "total_cents", What: "response field removed"}},
			nil,
			[]string{"flagged app/orders.py:2 still uses field `total_cents`"},
		},
		{
			"renamed field gone everywhere",
			&core.Change{Deps: []core.Dep{pay}, Options: []core.Option{{Codemod: &core.Codemod{Fields: map[string]string{"amount_cents": "amount"}}}}},
			nil,
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := Input{Dir: dir, Change: tt.ch, Patch: &core.Patch{}, Index: idx, Ecos: ecos(core.Npm), Hosts: tt.hosts}
			got, skip := leftovers(context.Background(), newPass(in))
			if skip != "" {
				t.Fatalf("skipped: %s", skip)
			}
			eq(t, got, tt.want...)
		})
	}

	in := Input{Dir: dir, Change: &core.Change{Deps: []core.Dep{pay}, To: "2.0.0"}, Patch: &core.Patch{}, Index: idx, Ecos: ecos(core.Npm)}
	if _, skip := leftovers(context.Background(), newPass(in)); skip != na {
		t.Errorf("plain bump: skip = %q", skip)
	}
	in.Patch.Decision = true
	if _, skip := leftovers(context.Background(), newPass(in)); skip != "nothing was applied" {
		t.Errorf("decision: skip = %q", skip)
	}
}

func TestPathMatches(t *testing.T) {
	tests := []struct {
		lit, tmpl string
		want      bool
	}{
		{"/v1/orders/123", "/v1/orders/{id}", true},
		{"https://api.x.dev/v1/orders/123?expand=a", "/v1/orders/{id}", true},
		{"/v1/orders/${id}", "/v1/orders/{id}", true},
		{"/orders/{id}", "/v1/orders/{id}", true},
		{"/v1/orders", "/v1/orders/{id}", false},
		{"/v1/orders/123/items", "/v1/orders/{id}", false},
		{"/{id}", "/v1/orders/{id}", false},
		{"/v2/orders/1", "/v1/orders/{id}", false},
		{"/v1/orders//", "/v1/orders/{id}", false},
	}
	for _, tt := range tests {
		if got := pathMatches(tt.lit, tt.tmpl); got != tt.want {
			t.Errorf("pathMatches(%q, %q) = %v", tt.lit, tt.tmpl, got)
		}
	}
}

func TestLeftoversWarnings(t *testing.T) {
	ev := []core.Evidence{{Source: "runtime", Text: "fetch_all() is deprecated"}}
	ch := &core.Change{Kind: core.Deprecation, Deps: []core.Dep{{Ecosystem: core.Pip, Name: "fixturelib", Version: "1.0.0"}}, To: "1.0.0", Evidence: ev}
	run := func(ws ...core.Warning) *core.Proof { return &core.Proof{Patched: &core.Run{Pass: true, Warnings: ws}} }
	p := newPass(Input{Change: ch, Patch: &core.Patch{}, Index: &index{}, Proof: run(core.Warning{File: "app/x.py", Line: 2, Msg: "something else"})})
	got, skip := leftovers(context.Background(), p)
	if skip != "" || len(got) != 0 || p.note != "the patched run prints none of the warnings" {
		t.Errorf("clean: %v, %q, %q", got, skip, p.note)
	}
	p = newPass(Input{Change: ch, Patch: &core.Patch{}, Index: &index{}, Proof: run(core.Warning{File: "app/report.py", Line: 9, Msg: "fetch_all() is deprecated"})})
	got, _ = leftovers(context.Background(), p)
	eq(t, got, "flagged app/report.py:9 still warns: fetch_all() is deprecated")
	if _, skip := leftovers(context.Background(), newPass(Input{Change: ch, Patch: &core.Patch{}, Index: &index{}})); skip == "" {
		t.Error("checked warnings without a patched run")
	}
}
