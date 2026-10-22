package patch

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

const billingJS = `const pay = require('@fixture/pay');

async function charge(amount) {
  const res = await pay.charges.create({ amount, currency: 'usd' });
  return pay?.charges?.retrieve(res.id);
}

function legacy(id) {
  return pay['charges']["retrieve"](id);
}

const later = pay
  .charges
  .create;

// pay.charges.create in a comment is not a site
const nested = pay.v1.charges.create;
pay.refunds.create({ charge: 'ch_1' });
pay.charges.capture('ch_1');
pay['charges'].capture('ch_2');
`

// site builds a site the way a scanner reports it: Col is the 1-based column of the root.
func site(src, file string, line int, chain ...string) core.Site {
	ln := strings.Split(src, "\n")[line-1]
	return core.Site{
		File:  file,
		Line:  line,
		Col:   strings.Index(ln, chain[0]) + 1,
		Chain: chain,
		Text:  strings.TrimSpace(ln),
	}
}

func TestCodemod(t *testing.T) {
	ordersPy := `import json

def total(order):
    return order["total_cents"]

def label(o):
    return o.total_cents / 100

# total_cents stays in this comment
defaults = {"total_cents": 0, "currency": "usd"}
`
	mainGo := `package main

import (
	"fmt"

	"github.com/x/y"
	ysub "github.com/x/y/sub"
	"github.com/x/yz"
)

func main() { fmt.Println(y.Do(), ysub.Do(), yz.Do()) }
`
	utilGo := `package main

import "github.com/x/y"

func util() string { return y.Name }
`
	workflow := `name: ci
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - id: v
        run: echo "::set-output name=version::${VERSION}"
      - run: echo "::set-output name=version::${VERSION}" && echo "::set-output name=version::${VERSION}"
`
	clientJS := "const base = 'https://api.fixture.dev';\n" +
		"export const one = (id) => fetch(`https://api.fixture.dev/v1/orders/${id}?expand=items`);\n" +
		"export const all = () => fetch(base + '/v1/orders');\n" +
		"export const items = (id) => fetch(`/v1/orders/${id}/items`);\n" +
		"export const bad = (id) => fetch('/v1/orders/' + id);\n" +
		"export const raw = \"/v1/orders/ord_42\";\n"

	tests := []struct {
		name    string
		files   map[string]string
		cm      core.Codemod
		sites   []core.Site
		idx     *fakeIndex
		applied []string
	}{
		{
			name:  "js-renames",
			files: map[string]string{"src/billing.js": billingJS},
			cm: core.Codemod{Renames: map[string]string{
				"charges.create":   "paymentIntents.create",
				"charges.retrieve": "paymentIntents.retrieve",
				"create":           "make",
				"charges.capture":  "paymentIntents.capture.now",
			}},
			sites: []core.Site{
				site(billingJS, "src/billing.js", 4, "pay", "charges", "create"),
				site(billingJS, "src/billing.js", 5, "pay", "charges", "retrieve"),
				site(billingJS, "src/billing.js", 9, "pay", "charges", "retrieve"),
				site(billingJS, "src/billing.js", 12, "pay", "charges", "create"),
				site(billingJS, "src/billing.js", 17, "pay", "v1", "charges", "create"),
				site(billingJS, "src/billing.js", 18, "pay", "refunds", "create"),
				site(billingJS, "src/billing.js", 19, "pay", "charges", "capture"),
				site(billingJS, "src/billing.js", 20, "pay", "charges", "capture"),
			},
			applied: []string{
				"renamed charges.create -> paymentIntents.create at src/billing.js:4, :12, :17",
				"renamed charges.retrieve -> paymentIntents.retrieve at src/billing.js:5, :9",
				"renamed create -> make at src/billing.js:18",
				"renamed charges.capture -> paymentIntents.capture.now at src/billing.js:19",
			},
		},
		{
			name:  "py-fields",
			files: map[string]string{"app/orders.py": ordersPy},
			cm:    core.Codemod{Fields: map[string]string{"total_cents": "amount_total", "currency": ""}},
			sites: []core.Site{site(ordersPy, "app/orders.py", 4, "order")},
			idx: &fakeIndex{fields: []core.Site{
				{File: "app/orders.py", Line: 4, Col: 18, Chain: []string{"total_cents"}},
				{File: "app/orders.py", Line: 7, Col: 13, Chain: []string{"total_cents"}},
				{File: "app/orders.py", Line: 10, Col: 13, Chain: []string{"total_cents"}},
				{File: "app/other.py", Line: 1, Col: 1, Chain: []string{"total_cents"}},
			}},
			applied: []string{"renamed field total_cents -> amount_total at app/orders.py:4, :7, :10"},
		},
		{
			name:  "go-imports",
			files: map[string]string{"main.go": mainGo, "util.go": utilGo},
			cm:    core.Codemod{Imports: map[string]string{"github.com/x/y": "github.com/x/y/v2"}},
			sites: []core.Site{
				site(mainGo, "main.go", 11, "y", "Do"),
				site(utilGo, "util.go", 5, "y", "Name"),
			},
			idx: &fakeIndex{imports: []core.Import{
				{File: "main.go", Line: 6, Package: "github.com/x/y", Binding: "y"},
				{File: "main.go", Line: 7, Package: "github.com/x/y/sub", Binding: "ysub"},
				{File: "main.go", Line: 8, Package: "github.com/x/yz", Binding: "yz"},
				{File: "util.go", Line: 3, Package: "github.com/x/y", Binding: "y"},
			}},
			applied: []string{"rewrote import github.com/x/y -> github.com/x/y/v2 at main.go:6, :7, util.go:3"},
		},
		{
			name:  "yaml-text",
			files: map[string]string{".github/workflows/ci.yml": workflow},
			cm: core.Codemod{Text: map[string]string{
				`echo "::set-output name=version::${VERSION}"`: `echo "version=${VERSION}" >> "$GITHUB_OUTPUT"`,
				"two\nlines": "never",
			}},
			sites: []core.Site{{File: ".github/workflows/ci.yml", Line: 8}, {File: ".github/workflows/ci.yml", Line: 9}},
			applied: []string{
				`replaced echo "::set-output name=version::${VERSION}" -> echo "version=${VERSION}" >> "$GITHUB_OUTPUT" at .github/workflows/ci.yml:8, :9`,
			},
		},
		{
			name:  "url-paths",
			files: map[string]string{"src/client.js": clientJS},
			cm: core.Codemod{Paths: map[string]string{
				"/v1/orders/{id}":       "/v2/orders/{id}",
				"/v1/orders":            "/v2/orders",
				"/v1/orders/{id}/items": "/v2/orders/{id}/lines",
			}},
			sites: []core.Site{{File: "src/client.js", Line: 2}},
			applied: []string{
				"rewrote path /v1/orders/{id} -> /v2/orders/{id} at src/client.js:2, :6",
				"rewrote path /v1/orders -> /v2/orders at src/client.js:3",
				"rewrote path /v1/orders/{id}/items -> /v2/orders/{id}/lines at src/client.js:4",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := repo(t, tt.files)
			idx := tt.idx
			if idx == nil {
				idx = &fakeIndex{}
			}
			builds := useIndex(t, idx)
			applied, err := codemod(&tree{wt: dir}, &tt.cm, tt.sites)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(applied, tt.applied) {
				t.Errorf("applied:\n%s\nwant:\n%s", strings.Join(applied, "\n"), strings.Join(tt.applied, "\n"))
			}
			if tt.idx == nil && *builds != 0 {
				t.Errorf("scanned the worktree %d times for rules that do not need it", *builds)
			}
			golden(t, "codemod/"+tt.name+".diff", staged(t, dir))

			again, err := codemod(&tree{wt: dir}, &tt.cm, tt.sites)
			if err != nil {
				t.Fatal(err)
			}
			if len(again) != 0 {
				t.Errorf("second run changed more: %q", again)
			}
		})
	}
}

func TestRemaining(t *testing.T) {
	dir := repo(t, map[string]string{"src/billing.js": billingJS})
	useIndex(t, &fakeIndex{})
	sites := []core.Site{
		site(billingJS, "src/billing.js", 4, "pay", "charges", "create"),
		site(billingJS, "src/billing.js", 18, "pay", "refunds", "create"),
		site(billingJS, "src/billing.js", 20, "pay", "charges", "capture"),
		{File: "src/gone.js", Line: 1},
	}
	cm := &core.Codemod{Renames: map[string]string{
		"charges.create":  "paymentIntents.create",
		"charges.capture": "paymentIntents.capture.now",
	}}
	tr := &tree{wt: dir}
	if _, err := codemod(tr, cm, sites); err != nil {
		t.Fatal(err)
	}
	var got []int
	for _, s := range remaining(context.Background(), tr, sites) {
		got = append(got, s.Line)
	}
	// 18 matches no rule; 20 needs a different length across a bracket, which is not mechanical.
	if want := []int{18, 20}; !slices.Equal(got, want) {
		t.Errorf("remaining lines = %v, want %v", got, want)
	}
}

func TestFill(t *testing.T) {
	tests := []struct {
		old, repl, path, want string
		ok                    bool
	}{
		{"/v1/orders/{id}", "/v2/orders/{id}", "/v1/orders/42", "/v2/orders/42", true},
		{"/v1/orders/{id}", "/v2/orders/{id}", "/v1/orders/${id}", "/v2/orders/${id}", true},
		{"/v1/orders/{id}", "/v2/orders/{id}", "/v1/orders/", "", false},
		{"/v1/orders/{id}", "/v2/orders/{id}", "/v1/orders/42/items", "", false},
		{"/v1/a/{x}/b/{y}", "/v2/b/{y}/a/{x}", "/v1/a/1/b/2", "/v2/b/2/a/1", true},
		{"/v1/orders/{id}", "/v2/orders/{order}", "/v1/orders/42", "", false},
		{"/v1/orders", "/v1/orders", "/v1/orders", "/v1/orders", false},
	}
	for _, tt := range tests {
		got, ok := fill(tt.old, tt.repl, tt.path)
		if got != tt.want || ok != tt.ok {
			t.Errorf("fill(%q, %q, %q) = %q, %v; want %q, %v", tt.old, tt.repl, tt.path, got, ok, tt.want, tt.ok)
		}
	}
}

func TestLineSpan(t *testing.T) {
	src := []byte("a\nbb\n\nc")
	tests := []struct {
		n    int
		want string
		ok   bool
	}{
		{0, "", false}, {1, "a", true}, {2, "bb", true}, {3, "", true}, {4, "c", true}, {5, "", false},
	}
	for _, tt := range tests {
		start, end, ok := lineSpan(src, tt.n)
		if ok != tt.ok || ok && string(src[start:end]) != tt.want {
			t.Errorf("line %d = %q, %v", tt.n, src[start:end], ok)
		}
	}
	if _, _, ok := lineSpan([]byte("a\n"), 2); ok {
		t.Error("line past the final newline exists")
	}
}

func TestRenameKeepsLines(t *testing.T) {
	dir := repo(t, map[string]string{"src/billing.js": billingJS})
	useIndex(t, &fakeIndex{})
	sites := []core.Site{
		site(billingJS, "src/billing.js", 4, "pay", "charges", "create"),
		site(billingJS, "src/billing.js", 12, "pay", "charges", "create"),
		site(billingJS, "src/billing.js", 18, "pay", "refunds", "create"),
	}
	cm := &core.Codemod{Renames: map[string]string{"charges.create": "createCharge"}}
	tr := &tree{wt: dir}
	if _, err := codemod(tr, cm, sites); err != nil {
		t.Fatal(err)
	}
	var got []int
	for _, s := range remaining(context.Background(), tr, sites) {
		got = append(got, s.Line)
	}
	// Joining the chain on 12-14 into one line would shift 18 and hide it.
	if want := []int{12, 18}; !slices.Equal(got, want) {
		t.Errorf("remaining lines = %v, want %v", got, want)
	}
}
