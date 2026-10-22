package vet

import (
	"context"
	"os/exec"
	"slices"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

func TestSymbolsNpm(t *testing.T) {
	base := map[string]string{
		".gitignore": "node_modules/\n",
		"src/billing.js": `const pay = require('@fixture/pay')
const { paymentIntents } = require('@fixture/pay')

function run() {
  return pay.charges.create({})
}
pay.legacy.thing()
`,
	}
	patched := map[string]string{
		"src/billing.js": `const pay = require('@fixture/pay')
const { paymentIntents } = require('@fixture/pay')

function run() {
  pay.paymentIntents.create({})
  pay.paymentIntents.capture('x')
  pay.refunds.create({})
  pay.charges.retrieve('ch_1')
  pay.paymentIntents.create.withRaw({})
  paymentIntents.create({})
  return pay.Client
}
pay.legacy.thing()
`,
		"node_modules/@fixture/pay/package.json": `{"name": "@fixture/pay", "version": "2.0.0", "main": "lib/index.js", "types": "types/index.d.ts"}`,
		"node_modules/@fixture/pay/types/index.d.ts": `export declare namespace paymentIntents {
  function create(params: object): Promise<object>;
  function confirm(id: string): Promise<object>;
}
export declare const charges: ChargesResource;
interface ChargesResource {
  retrieve(id: string): Promise<object>;
}
export declare class Client {}
`,
		"node_modules/@fixture/pay/lib/index.js": "module.exports = { paymentIntents: {}, charges: {} }\n",
	}
	dir, sha, diff := worktree(t, base, patched)
	in := Input{
		Dir: dir, Base: sha,
		Change: &core.Change{Deps: []core.Dep{{Ecosystem: core.Npm, Name: "@fixture/pay"}}, To: "2.0.0"},
		Patch:  &core.Patch{Diff: diff},
		Ecos:   ecos(core.Npm),
		Index: &index{dir: dir, imports: []core.Import{
			{File: "src/billing.js", Line: 1, Package: "@fixture/pay", Binding: "pay"},
			{File: "src/billing.js", Line: 2, Package: "@fixture/pay", Names: []string{"paymentIntents"}},
		}},
	}
	p := newPass(in)
	got, skip := symbols(context.Background(), p)
	if skip != "" {
		t.Fatalf("skipped: %s", skip)
	}
	// Line 13 is not part of the patch, but 2.0.0 no longer has what it calls.
	eq(t, got,
		"flagged src/billing.js:6 `pay.paymentIntents.capture`: `paymentIntents` has no member `capture` in @fixture/pay@2.0.0",
		"flagged src/billing.js:7 `pay.refunds.create`: `refunds` is not exported by @fixture/pay@2.0.0",
		"flagged src/billing.js:9 `pay.paymentIntents.create.withRaw` not verified: checked as far as `paymentIntents.create`",
		"flagged src/billing.js:13 `pay.legacy.thing`: `legacy` is not exported by @fixture/pay@2.0.0",
	)
	if want := "4/8 calls resolve in @fixture/pay@2.0.0 typings"; p.note != want {
		t.Errorf("note = %q, want %q", p.note, want)
	}
	// The calls the patch wrote that resolve, as the question check shows them.
	want := []string{
		"`paymentIntents.create` in @fixture/pay@2.0.0: function create(params: object): Promise<object>;",
		"`charges.retrieve` in @fixture/pay@2.0.0: retrieve(id: string): Promise<object>;",
	}
	if got := p.decls["src/billing.js"]; !slices.Equal(got, want) {
		t.Errorf("decls = %q, want %q", got, want)
	}
}

func TestSymbolsCommonJS(t *testing.T) {
	js := &jsPkg{text: `exports.e = 1;
module.exports = {
  a: 1,
  'b': function () {},
  async c() { return { nested: 1 } },
  d: { inner() {}, other: 2 },
  ...spread,
}`, exports: map[string]bool{}}
	for _, m := range cjsProp.FindAllStringSubmatch(js.text, -1) {
		js.exports[m[1]] = true
	}
	for _, loc := range cjsObj.FindAllStringIndex(js.text, -1) {
		for _, k := range topKeys(braceBlock(js.text, loc[1]-1)) {
			js.exports[k] = true
		}
	}
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		if !js.exports[name] {
			t.Errorf("%s not exported; have %v", name, js.exports)
		}
	}
	if js.exports["nested"] || js.exports["inner"] || js.exports["spread"] {
		t.Errorf("inner keys taken for exports: %v", js.exports)
	}
	if st := js.member("d", "inner"); st != resolved {
		t.Errorf("d.inner = %v", st)
	}
	if st := js.member("d", "gone"); st != missing {
		t.Errorf("d.gone = %v", st)
	}
	if st := js.member("a", "x"); st != unknown {
		t.Errorf("a.x = %v, want unknown", st)
	}
}

func TestSymbolsGo(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not found")
	}
	base := map[string]string{
		"go.mod":     "module example.com/m\n\ngo 1.21\n",
		"lib/lib.go": "package lib\n\nfunc Sign() {}\n\ntype Key struct{}\n\nfunc (Key) Bytes() []byte { return nil }\n",
		"main.go":    "package main\n\nimport \"example.com/m/lib\"\n\nfunc main() {\n}\n",
	}
	patched := map[string]string{
		"main.go": "package main\n\nimport \"example.com/m/lib\"\n\nfunc main() {\n\tlib.Sign()\n\tlib.Missing()\n\tlib.Key.Bytes(lib.Key{})\n\tlib.Key.Nope()\n}\n",
	}
	dir, sha, diff := worktree(t, base, patched)
	in := Input{
		Dir: dir, Base: sha,
		Change: &core.Change{Deps: []core.Dep{{Ecosystem: core.Go, Name: "example.com/m"}}},
		Patch:  &core.Patch{Diff: diff},
		Ecos:   map[string]core.Ecosystem{core.Go: fakeEco{core.Go, map[string][]string{"example.com/m": {"example.com/m/lib"}}}},
		Index:  &index{dir: dir, imports: []core.Import{{File: "main.go", Line: 3, Package: "example.com/m/lib", Binding: "lib"}}},
	}
	got, skip := symbols(context.Background(), newPass(in))
	if skip != "" {
		t.Fatalf("skipped: %s", skip)
	}
	eq(t, got,
		"flagged main.go:7 `lib.Missing`: `Missing` is not in example.com/m/lib",
		"flagged main.go:9 `lib.Key.Nope`: `Key.Nope` is not in example.com/m/lib",
	)
}

func TestGoDocLeavesGoModAlone(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not found")
	}
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOWORK", "off")
	// No go directive: a go command allowed to write go.mod adds one.
	mod := "module example.com/m\n"
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"go.mod":     mod,
		"lib/lib.go": "package lib\n\nfunc Hello() {}\n",
	})
	if st, why := goDoc(context.Background(), dir, "example.com/m/lib", "Hello"); st != resolved {
		t.Errorf("goDoc = %v %q, want resolved", st, why)
	}
	if got := read(t, dir, "go.mod"); got != mod {
		t.Errorf("go.mod changed:\n%s", got)
	}
}

func TestGoImports(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"a.go": `package a

import (
	"fmt"
	yml "gopkg.in/yaml.v3"
	"github.com/google/go-github/v66/github"
	"github.com/stripe/stripe-go/v79"
	"gopkg.in/ini.v1"
)
`})
	got := goImports(dir + "/a.go")
	want := map[string]string{
		"fmt": "fmt", "yml": "gopkg.in/yaml.v3", "github": "github.com/google/go-github/v66/github",
		"stripe": "github.com/stripe/stripe-go/v79", "ini": "gopkg.in/ini.v1",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s -> %q, want %q", k, got[k], v)
		}
	}
}

func TestSymbolsPip(t *testing.T) {
	base := map[string]string{
		".gitignore": ".venv/\n",
		"app.py":     "import pay\n\n",
	}
	sp := ".venv/lib/python3.12/site-packages/"
	patched := map[string]string{
		"app.py": `import pay

pay.PaymentIntent.create()
pay.PaymentIntent.capture()
pay.Refund.create()
pay.Client()
pay.VERSION
pay.Charge.capture()
pay.checkout.Session.create()
pay.PaymentIntent.retrieve()
`,
		sp + "pay/__init__.py": `from .client import Client
VERSION = "1"


class PaymentIntent(object):
    def create(cls, **params):
        pass

    retrieve = classmethod(lambda cls: None)


class Charge(APIResource):
    pass
`,
		sp + "pay/checkout/__init__.py": "class Session:\n    pass\n",
	}
	dir, sha, diff := worktree(t, base, patched)
	in := Input{
		Dir: dir, Base: sha,
		Change: &core.Change{Deps: []core.Dep{{Ecosystem: core.Pip, Name: "pay"}}},
		Patch:  &core.Patch{Diff: diff},
		Ecos:   ecos(core.Pip),
		Index:  &index{dir: dir, imports: []core.Import{{File: "app.py", Line: 1, Package: "pay", Binding: "pay"}}},
	}
	got, skip := symbols(context.Background(), newPass(in))
	if skip != "" {
		t.Fatalf("skipped: %s", skip)
	}
	eq(t, got,
		"flagged app.py:4 `pay.PaymentIntent.capture`: `PaymentIntent` has no member `capture` in pay",
		"flagged app.py:5 `pay.Refund.create`: `Refund` is not defined in pay",
		"flagged app.py:8 `pay.Charge.capture` not verified: `Charge.capture` may be inherited or imported",
		"flagged app.py:9 `pay.checkout.Session.create` not verified: checked as far as module `pay.checkout`",
	)
}

func TestSymbolsSkips(t *testing.T) {
	dir, sha, diff := worktree(t, map[string]string{"app.rb": "require 'stripe'\n"}, map[string]string{"app.rb": "require 'stripe'\nStripe.api_key = 'x'\nStripe\n"})
	in := Input{
		Dir: dir, Base: sha,
		Change: &core.Change{Deps: []core.Dep{{Ecosystem: core.Gems, Name: "stripe"}}},
		Patch:  &core.Patch{Diff: diff},
		Ecos:   ecos(core.Gems),
		Index:  &index{dir: dir, imports: []core.Import{{File: "app.rb", Line: 1, Package: "stripe", Binding: "Stripe"}}},
	}
	got, skip := symbols(context.Background(), newPass(in))
	eq(t, got, "flagged app.rb:2 `Stripe.api_key` not verified: symbols are not checked for rubygems")
	if skip != "" {
		t.Errorf("skip = %q", skip)
	}

	in.Change = &core.Change{API: &core.APIChange{Provider: "x"}}
	if _, skip := symbols(context.Background(), newPass(in)); skip != na {
		t.Errorf("skip = %q", skip)
	}
	in.Change = &core.Change{Deps: []core.Dep{{Ecosystem: core.Gems, Name: "rack"}}}
	if _, skip := symbols(context.Background(), newPass(in)); skip != na {
		t.Errorf("skip = %q", skip)
	}
}
