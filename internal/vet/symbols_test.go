package vet

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
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

// A package with no typings whose exports a scan cannot read, as lodash's
// module.exports = require('./lodash'), says nothing is missing from it.
func TestSymbolsUntyped(t *testing.T) {
	base := map[string]string{".gitignore": "node_modules/\n", "src/a.js": "const _ = require('lodash')\n_.chunk([])\n"}
	patched := map[string]string{
		"src/a.js":                         "const _ = require('lodash')\n_.chunk([1])\n",
		"node_modules/lodash/package.json": `{"name": "lodash", "version": "4.18.1", "main": "lodash.js"}`,
		"node_modules/lodash/lodash.js":    "lodash.chunk = chunk;\nmodule.exports = lodash;\n",
	}
	dir, sha, diff := worktree(t, base, patched)
	p := newPass(Input{
		Dir: dir, Base: sha,
		Change: &core.Change{Deps: []core.Dep{{Ecosystem: core.Npm, Name: "lodash"}}, To: "4.18.1"},
		Patch:  &core.Patch{Diff: diff},
		Ecos:   ecos(core.Npm),
		Index:  &index{dir: dir, imports: []core.Import{{File: "src/a.js", Line: 1, Package: "lodash", Binding: "_"}}},
	})
	got, _ := symbols(context.Background(), p)
	for _, f := range got {
		if strings.Contains(f.Msg, "is not exported") {
			t.Errorf("flagged %+v", f)
		}
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
	if st, why := goDoc(context.Background(), dir, "example.com/m/lib", "Hello", ""); st != resolved {
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

// A require or default import binds what the module itself is, so a member
// of it resolves through the default export's type, what it extends among it.
func TestSymbolsDefaultExport(t *testing.T) {
	pkgs := map[string]map[string]string{
		"axios": {
			"package.json": `{"name": "axios", "version": "1.20.0", "main": "index.js", "types": "index.d.ts"}`,
			"index.d.ts": `export class Axios {
  constructor(config?: AxiosRequestConfig);
  get<T = any, R = AxiosResponse<T>, D = any>(url: string, config?: AxiosRequestConfig<D>): Promise<R>;
}
export interface AxiosInstance extends Axios {
  <T = any>(config: AxiosRequestConfig): Promise<T>;
  defaults: object;
}
export interface AxiosStatic extends AxiosInstance {
  create(config?: CreateAxiosDefaults): AxiosInstance;
}
declare const axios: AxiosStatic;
export default axios;
`,
			"index.js": "module.exports = require('./lib/axios')\n",
		},
		"chalk": {
			"package.json": `{"name": "chalk", "version": "4.1.2", "main": "source", "types": "index.d.ts"}`,
			"index.d.ts": `declare namespace chalk {
	interface ChalkFunction {
		(text: TemplateStringsArray, ...placeholders: unknown[]): string;
	}
	interface Chalk extends ChalkFunction {
		readonly green: Chalk;
	}
}
declare const chalk: chalk.Chalk & chalk.ChalkFunction & {
	supportsColor: chalk.ColorSupport | false;
};
export = chalk;
`,
		},
		"kleur": {
			"package.json": `{"name": "kleur", "version": "5.0.0", "types": "index.d.ts"}`,
			"index.d.ts": `export interface ChalkInstance {
	red: ChalkInstance;
}
declare const kleur: ChalkInstance;
export default kleur;
`,
		},
		"passport": {
			"package.json": `{"name": "passport", "version": "0.6.0", "main": "./lib"}`,
			"lib/index.js": `var Passport = require('./authenticator')
  , SessionStrategy = require('./strategies/session');

exports = module.exports = new Passport();
exports.Passport =
exports.Authenticator = Passport;
exports.Strategy = require('passport-strategy');
`,
		},
	}
	base := map[string]string{".gitignore": "node_modules/\n", "src/a.js": "const axios = require('axios')\n"}
	src := `const axios = require('axios')
const chalk = require('chalk')
import kleur from 'kleur'
const passport = require('passport')
axios.get(url)
chalk.green('ok')
kleur.red('x')
kleur.blue('x')
passport.initialize()
`
	patched := map[string]string{"src/a.js": src}
	var imports []core.Import
	for i, name := range []string{"axios", "chalk", "kleur", "passport"} {
		imports = append(imports, core.Import{File: "src/a.js", Line: i + 1, Package: name, Binding: name})
		for f, body := range pkgs[name] {
			patched["node_modules/"+name+"/"+f] = body
		}
	}
	dir, sha, diff := worktree(t, base, patched)
	var deps []core.Dep
	for _, name := range []string{"axios", "chalk", "kleur", "passport"} {
		deps = append(deps, core.Dep{Ecosystem: core.Npm, Name: name})
	}
	p := newPass(Input{
		Dir: dir, Base: sha,
		Change: &core.Change{Deps: deps, To: "1.0.0"},
		Patch:  &core.Patch{Diff: diff},
		Ecos:   ecos(core.Npm),
		Index:  &index{dir: dir, imports: imports},
	})
	got, _ := symbols(context.Background(), p)
	eq(t, got,
		"flagged src/a.js:8 `kleur.blue`: `blue` is neither exported by kleur@5.0.0 nor a member of what it exports",
		"flagged src/a.js:9 `passport.initialize` not verified: passport@0.6.0's module.exports is a value whose members cannot be read",
	)
}

// Typings from @types count for a package that ships none.
func TestSymbolsAtTypes(t *testing.T) {
	base := map[string]string{".gitignore": "node_modules/\n", "src/a.js": "const passport = require('passport')\n"}
	patched := map[string]string{
		"src/a.js":                                  "const passport = require('passport')\npassport.use(s)\npassport.authenticate('x')\npassport.gone()\n",
		"node_modules/passport/package.json":        `{"name": "passport", "version": "0.6.0", "main": "./lib"}`,
		"node_modules/passport/lib/index.js":        "exports = module.exports = new Passport();\nexports.Passport = Passport;\n",
		"node_modules/@types/passport/package.json": `{"name": "@types/passport", "version": "1.0.16"}`,
		"node_modules/@types/passport/index.d.ts": `declare namespace passport {
    interface Authenticator {
        use(strategy: Strategy): this;
        authenticate(strategy: string | string[]): any;
    }
    interface PassportStatic extends Authenticator {
        Authenticator: { new (): Authenticator };
    }
}
declare const passport: passport.PassportStatic;
export = passport;
`,
	}
	dir, sha, diff := worktree(t, base, patched)
	p := newPass(Input{
		Dir: dir, Base: sha,
		Change: &core.Change{Deps: []core.Dep{{Ecosystem: core.Npm, Name: "passport"}}, To: "0.6.0"},
		Patch:  &core.Patch{Diff: diff},
		Ecos:   ecos(core.Npm),
		Index:  &index{dir: dir, imports: []core.Import{{File: "src/a.js", Line: 1, Package: "passport", Binding: "passport"}}},
	})
	got, _ := symbols(context.Background(), p)
	eq(t, got, "flagged src/a.js:4 `passport.gone`: `gone` is neither exported by passport@0.6.0 nor a member of what it exports")
	if typesName("@scope/pkg") != "scope__pkg" || typesName("passport") != "passport" {
		t.Error("typesName")
	}
}

// A file behind build constraints is looked up for a platform it builds on.
func TestGoTarget(t *testing.T) {
	dir := t.TempDir()
	other := "linux"
	if runtime.GOOS == "linux" {
		other = "darwin"
	}
	files := map[string]string{
		"plain.go":                       "package x\n",
		"only.go":                        "//go:build " + other + "\n\npackage x\n",
		"term_solaris.go":                "package x\n",
		"tagged.go":                      "//go:build integration\n\npackage x\n",
		"terminal_check_notappengine.go": "//go:build !appengine && !js && !windows && !nacl && !plan9\n\npackage x\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, want := range map[string]string{"plain.go": "", "only.go": other + "/", "term_solaris.go": "solaris/amd64"} {
		got, ok := goTarget(filepath.Join(dir, name))
		if !ok || !strings.HasPrefix(got, want) || want == "" && got != "" {
			t.Errorf("goTarget(%s) = %q, %v; want %q", name, got, ok, want)
		}
	}
	if got, ok := goTarget(filepath.Join(dir, "tagged.go")); ok {
		t.Errorf("a custom tag builds for %q", got)
	}
}

// A call the patch left alone that tend cannot look into is said on a major
// bump, so the PR does not read as fully checked.
func TestSymbolsMajorUnverified(t *testing.T) {
	base := map[string]string{".gitignore": "node_modules/\n", "src/a.js": "const _ = require('lodash')\n_.chunk([])\n", "package.json": "{}\n"}
	patched := map[string]string{
		"package.json":                     "{\"x\": 1}\n",
		"node_modules/lodash/package.json": `{"name": "lodash", "version": "5.0.0", "main": "lodash.js"}`,
		"node_modules/lodash/lodash.js":    "module.exports = lodash;\n",
	}
	dir, sha, diff := worktree(t, base, patched)
	in := func(level string) *pass {
		return newPass(Input{
			Dir: dir, Base: sha,
			Change: &core.Change{Level: level, Deps: []core.Dep{{Ecosystem: core.Npm, Name: "lodash"}}, To: "5.0.0"},
			Patch:  &core.Patch{Diff: diff},
			Ecos:   ecos(core.Npm),
			Index:  &index{dir: dir, imports: []core.Import{{File: "src/a.js", Line: 1, Package: "lodash", Binding: "_"}}},
		})
	}
	got, _ := symbols(context.Background(), in("major"))
	if len(got) != 1 || !strings.Contains(got[0].Msg, "not verified across the major release") {
		t.Errorf("major: %+v", got)
	}
	if got, skip := symbols(context.Background(), in("minor")); len(got) != 0 || skip != na {
		t.Errorf("minor: %+v, %q", got, skip)
	}
}
