package api

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func obj(props map[string]*Schema, required ...string) *Schema {
	return &Schema{Type: "object", Props: props, Required: required}
}

func typ(t string) *Schema { return &Schema{Type: t} }

func TestDiff(t *testing.T) {
	order := func() *Schema {
		return obj(map[string]*Schema{"id": typ("string"), "total_cents": typ("integer"), "status": typ("string")})
	}
	march := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		old, cur *Op // nil cur means the operation is gone
		want     []string
	}{
		{
			name: "same",
			old:  &Op{Response: order()},
			cur:  &Op{Response: order()},
		},
		{
			name: "operation removed",
			old:  &Op{},
			want: []string{"GET /o: operation removed"},
		},
		{
			name: "deprecated and sunset added",
			old:  &Op{},
			cur:  &Op{Deprecated: true, Sunset: march},
			want: []string{"GET /o: operation deprecated", "GET /o: sunset added (2027-03-01)"},
		},
		{
			name: "sunset moved",
			old:  &Op{Sunset: march},
			cur:  &Op{Sunset: march.AddDate(0, 1, 0).Add(90 * time.Minute)},
			want: []string{"GET /o: sunset changed (2027-03-01 -> 2027-04-01T01:30:00Z)"},
		},
		{
			name: "sunset kept",
			old:  &Op{Deprecated: true, Sunset: march},
			cur:  &Op{Deprecated: true, Sunset: march},
		},
		{
			name: "parameters",
			old:  &Op{Params: []Param{{Name: "id", In: "path", Required: true}, {Name: "limit", In: "query"}, {Name: "expand", In: "query"}, {Name: "page", In: "query"}}},
			cur:  &Op{Params: []Param{{Name: "key", In: "path", Required: true}, {Name: "limit", In: "query", Required: true}, {Name: "expand", In: "query", Deprecated: true}, {Name: "region", In: "header", Required: true}}},
			want: []string{
				"GET /o: parameter deprecated expand (query)",
				"GET /o: parameter removed page (query)",
				"GET /o: required parameter added limit (query)",
				"GET /o: required parameter added region (header)",
			},
		},
		{
			name: "request",
			old:  &Op{Request: obj(map[string]*Schema{"amount": typ("integer"), "source": typ("string"), "note": typ("string")}, "amount")},
			cur:  &Op{Request: obj(map[string]*Schema{"amount": typ("integer"), "payment_method": typ("string"), "note": typ("string")}, "amount", "payment_method", "note")},
			want: []string{
				"GET /o: request field removed source",
				"GET /o: required parameter added note (body)",
				"GET /o: required parameter added payment_method (body)",
			},
		},
		{
			name: "request appears",
			old:  &Op{},
			cur:  &Op{Request: obj(map[string]*Schema{"q": typ("string")}, "q")},
			want: []string{"GET /o: required parameter added q (body)"},
		},
		{
			name: "response",
			old:  &Op{Response: order()},
			cur: &Op{Response: obj(map[string]*Schema{
				"id":     typ("integer"),
				"status": {Type: "string", Deprecated: true},
				"total":  obj(map[string]*Schema{"amount": typ("integer")}),
			})},
			want: []string{
				"GET /o: response field deprecated status",
				"GET /o: response field removed total_cents",
				"GET /o: response field type changed id (string -> integer)",
			},
		},
		{
			// Fields are matched by name, the way code is searched for them.
			name: "field moves deeper",
			old:  &Op{Response: obj(map[string]*Schema{"amount": typ("integer")})},
			cur:  &Op{Response: obj(map[string]*Schema{"total": obj(map[string]*Schema{"amount": typ("integer")})})},
		},
		{
			name: "list response",
			old:  &Op{Response: &Schema{Type: "array", Items: order()}},
			cur:  &Op{Response: &Schema{Type: "array", Items: obj(map[string]*Schema{"id": typ("string"), "status": typ("string")})}},
			want: []string{"GET /o: response field removed total_cents"},
		},
		{
			name: "type appears",
			old:  &Op{Response: obj(map[string]*Schema{"x": {}})},
			cur:  &Op{Response: obj(map[string]*Schema{"x": typ("string")})},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := &Doc{Ops: map[string]*Op{"GET /o": tt.old}}
			cur := &Doc{Ops: map[string]*Op{}}
			if tt.cur != nil {
				cur.Ops["GET /o"] = tt.cur
			}
			var got []string
			for _, l := range Diff(old, cur, []Usage{{Method: "GET", Path: "/o"}}) {
				got = append(got, l.String())
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Diff =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestDiffOnlyUsed(t *testing.T) {
	old := &Doc{Ops: map[string]*Op{"GET /a": {}, "GET /b": {}}}
	cur := &Doc{Ops: map[string]*Op{}}
	got := Diff(old, cur, []Usage{{Method: "GET", Path: "/b"}, {Method: "GET", Path: "/new"}})
	if want := []Line{{Op: "GET /b", What: "operation removed"}}; !slices.Equal(got, want) {
		t.Errorf("Diff = %+v", got)
	}
	if got := Diff(nil, cur, []Usage{{Method: "GET", Path: "/b"}}); got != nil {
		t.Errorf("no snapshot should mean no lines, got %+v", got)
	}
}

func TestDiffRecursive(t *testing.T) {
	node := obj(map[string]*Schema{"name": typ("string")})
	node.Props["child"] = node
	old := &Doc{Ops: map[string]*Op{"GET /t": {Response: node}}}
	next := obj(map[string]*Schema{"title": typ("string")})
	next.Props["child"] = next
	cur := &Doc{Ops: map[string]*Op{"GET /t": {Response: next}}}
	got := Diff(old, cur, []Usage{{Method: "GET", Path: "/t"}})
	if want := []Line{{Op: "GET /t", What: "response field removed", Field: "name"}}; !slices.Equal(got, want) {
		t.Errorf("Diff = %+v", got)
	}
}
