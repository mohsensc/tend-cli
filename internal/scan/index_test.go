package scan

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
)

// build writes files under a temp dir and indexes exactly those.
func build(t *testing.T, files map[string]string) *Index {
	t.Helper()
	root := t.TempDir()
	var names []string
	for name, body := range files {
		write(t, root, name, body)
		names = append(names, name)
	}
	x, err := Build(Options{Root: root, Files: names})
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func write(t *testing.T, root, name, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sites(ss []core.Site) []string {
	var res []string
	for _, s := range ss {
		res = append(res, fmt.Sprintf("%s:%d:%d %s", s.File, s.Line, s.Col, strings.Join(s.Chain, ".")))
	}
	return res
}

var useFiles = map[string]string{
	"a.go": `package a

import (
	stripe "github.com/stripe/stripe-go/v79"
	"github.com/stripe/stripe-go/v79/charge"
	old "github.com/stripe/stripe-go"
)

func f() {
	stripe.Key = "k"
	charge.New(nil)
	old.Key = "k"
}
`,
	"b.ts": `import x from '@scope/pkg/sub';
import s from 'stripe';
import m from 'stripe-mock';
x.y();
s.charges.create();
m.start();
`,
	"c.py": `from google.cloud import storage
import google.cloud.bigquery

storage.Client()
google.cloud.bigquery.Client()
`,
	"d.rb": `Aws::S3::Client.new
Stripe::Charge.create(amount: 1)
`,
	"e.php": `<?php
use Stripe\StripeClient;
new StripeClient('k');
`,
	"f.rs": `fn main() { serde_json::to_string(&1); }
`,
	"g.cs": `using Stripe;
class A { void F() { new ChargeService(); } }
`,
}

func TestUses(t *testing.T) {
	x := build(t, useFiles)
	tests := []struct {
		name  string
		names []string
		want  []string
	}{
		{"go module covers its packages", []string{"github.com/stripe/stripe-go/v79"},
			[]string{"a.go:10:2 stripe.Key", "a.go:11:2 charge.New"}},
		{"go package alone", []string{"github.com/stripe/stripe-go/v79/charge"},
			[]string{"a.go:11:2 charge.New"}},
		{"go major versions are other modules", []string{"github.com/stripe/stripe-go"},
			[]string{"a.go:12:2 old.Key"}},
		{"npm subpath import", []string{"@scope/pkg"}, []string{"b.ts:4:1 x.y"}},
		{"npm scope alone is no package", []string{"@scope"}, nil},
		{"npm names are exact", []string{"stripe"}, []string{"b.ts:5:1 s.charges.create"}},
		{"python from-import", []string{"google.cloud.storage"}, []string{"c.py:4:1 storage.Client"}},
		{"python namespace prefix", []string{"google.cloud"},
			[]string{"c.py:4:1 storage.Client", "c.py:5:1 google.cloud.bigquery.Client"}},
		{"python sibling module", []string{"google.cloud.bigtable"}, nil},
		{"ruby constants ignore case", []string{"aws::s3"}, []string{"d.rb:1:1 Aws.S3.Client.new"}},
		{"camel case ignores case in php, ruby and c#", []string{"STRIPE"},
			[]string{"d.rb:2:1 Stripe.Charge.create", "e.php:3:5 StripeClient", "g.cs:2:26 ChargeService"}},
		{"namespaced php ignores case", []string{`stripe\stripeclient`}, []string{"e.php:3:5 StripeClient"}},
		{"rust crate with a dash", []string{"serde-json"}, []string{"f.rs:1:13 serde_json.to_string"}},
		{"several names, sorted and unique", []string{"serde_json", "@scope/pkg", "serde-json"},
			[]string{"b.ts:4:1 x.y", "f.rs:1:13 serde_json.to_string"}},
		{"nothing", []string{"left-pad", ""}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sites(x.Uses(tt.names)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Uses(%q)\n got %q\nwant %q", tt.names, got, tt.want)
			}
		})
	}
}

func TestImports(t *testing.T) {
	x := build(t, useFiles)
	tests := []struct {
		pkg  string
		want []string
	}{
		{"@scope/pkg", []string{"b.ts:1 @scope/pkg/sub"}},
		{"github.com/stripe/stripe-go/v79", []string{"a.go:4 github.com/stripe/stripe-go/v79", "a.go:5 github.com/stripe/stripe-go/v79/charge"}},
		{"google.cloud.storage", []string{"c.py:1 google.cloud"}},
		{"google.cloud", []string{"c.py:1 google.cloud", "c.py:2 google.cloud.bigquery"}},
		{"Stripe", []string{"e.php:2 Stripe\\StripeClient", "g.cs:1 Stripe"}},
		{"stripe", []string{"b.ts:2 stripe"}},
		{"stripe-mock", []string{"b.ts:3 stripe-mock"}},
	}
	for _, tt := range tests {
		var got []string
		for _, im := range x.Imports(tt.pkg) {
			got = append(got, fmt.Sprintf("%s:%d %s", im.File, im.Line, im.Package))
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Imports(%q)\n got %q\nwant %q", tt.pkg, got, tt.want)
		}
	}
}

func TestFields(t *testing.T) {
	x := build(t, map[string]string{
		"a.js": `const o = { total_cents: 5, other: 1 };
o.total_cents + o?.total_cents;
o['total_cents'];
send({"total_cents": 1});
const total_cents = 3;
x = a ? total_cents : b;
// o.total_cents in a comment
`,
		"b.py": `class Order(BaseModel):
    total_cents: int

create(amount=1, total_cents=5)
if total_cents:
    pass
order["total_cents"]
`,
		"c.php": `<?php
$o->total_cents;
Order::total_cents;
['total_cents' => 1];
$total_cents = 1;
`,
		"d.go": "package d\n\ntype Order struct {\n\tTotal int `json:\"total_cents,omitempty\"`\n}\n",
	})
	want := []string{
		"a.js:1:13", "a.js:2:3", "a.js:2:20", "a.js:3:4", "a.js:4:8",
		"b.py:2:5", "b.py:4:18", "b.py:7:8",
		"c.php:2:5", "c.php:3:8", "c.php:4:3",
		"d.go:4:19",
	}
	var got []string
	for _, s := range x.Fields("total_cents", nil) {
		got = append(got, fmt.Sprintf("%s:%d:%d", s.File, s.Line, s.Col))
		if !reflect.DeepEqual(s.Chain, []string{"total_cents"}) {
			t.Errorf("%s:%d chain %v", s.File, s.Line, s.Chain)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Fields\n got %q\nwant %q", got, want)
	}
	got = nil
	for _, s := range x.Fields("total_cents", []string{"c.php", "nope.js"}) {
		got = append(got, s.File)
	}
	if !reflect.DeepEqual(got, []string{"c.php", "c.php", "c.php"}) {
		t.Errorf("Fields restricted to c.php = %q", got)
	}
	if got := x.Fields("", nil); got != nil {
		t.Errorf("Fields of an empty name = %v", got)
	}
}

func TestEndpoints(t *testing.T) {
	x := build(t, map[string]string{
		"api.go": `package api

const base = "https://API.Stripe.com/v1"
var other = "https://example.org/v1/things"
`,
		"orders.py": `import requests
# talks to api.fixture.dev
def list_orders():
    return requests.get(BASE + "/v1/orders")
`,
		"web.js": `fetch("/v1/orders")
`,
		"mixed.ts": `const a = "https://api.fixture.dev/v1/a";
const b = "https://api.stripe.com/v1/b";
post("/v1/c");
`,
	})
	var got []string
	for _, u := range x.Endpoints([]string{"api.stripe.com", "API.FIXTURE.DEV"}) {
		got = append(got, fmt.Sprintf("%s:%d %s %s %s", u.File, u.Line, u.Method, u.URL, u.Host))
	}
	want := []string{
		"api.go:3  https://API.Stripe.com/v1 api.stripe.com",
		"mixed.ts:1  https://api.fixture.dev/v1/a api.fixture.dev",
		"mixed.ts:2  https://api.stripe.com/v1/b api.stripe.com",
		"mixed.ts:3  /v1/c api.fixture.dev",
		"orders.py:4 GET /v1/orders api.fixture.dev",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Endpoints\n got %q\nwant %q", got, want)
	}
	if got := x.Endpoints([]string{"api.unknown.dev"}); got != nil {
		t.Errorf("Endpoints for an unused host = %v", got)
	}
}

func TestMethod(t *testing.T) {
	tests := []struct {
		file, src, want string
	}{
		{"a.py", `requests.get("https://x.io/a")`, "GET"},
		{"a.go", `resp, err := http.Post("https://x.io/a", "", nil)`, "POST"},
		{"a.js", `client.delete("/v1/a")`, "DELETE"},
		{"a.js", `axios.put('/v1/a', body)`, "PUT"},
		{"a.js", `$http.get('/v1/a')`, "GET"},
		{"a.js", `fetch('/v1/a', {method: 'POST'})`, "POST"},
		{"a.js", "fetch('/v1/a', {\n  headers: h,\n  method: \"PATCH\",\n})", "PATCH"},
		{"a.js", `fetch('/v1/a')`, "GET"},
		{"a.js", "axios.post(\n  '/v1/a',\n  body)", "POST"},
		{"a.py", `requests.request(method="POST", url="/v1/a")`, "POST"},
		{"a.py", `urllib.request.Request("/v1/a", method='PUT')`, "PUT"},
		{"a.go", `req, _ := http.NewRequest(http.MethodPut, "/v1/a", nil)`, "PUT"},
		{"a.go", "u := \"/v1/a\"\nreq, _ := http.NewRequest(http.MethodDelete, u, nil)", "DELETE"},
		{"a.go", `req, _ := http.NewRequest("PATCH", "/v1/a", nil)`, "PATCH"},
		{"a.php", `<?php $client->post('/v1/a');`, "POST"},
		{"a.php", `<?php $c->request('GET', '/v1/a');`, "GET"},
		{"a.rb", `Net::HTTP::Post.new("/v1/a")`, "POST"},
		{"a.rb", `conn.run_request(:get, "/v1/a", method: :head)`, "HEAD"},
		{"a.cs", `await http.PostAsJsonAsync("/v1/a", body);`, "POST"},
		{"a.java", `HttpRequest.newBuilder().method("OPTIONS", body).uri("/v1/a")`, "OPTIONS"},
		{"a.js", `const u = '/v1/a';`, ""},
		{"a.js", "const u = '/v1/a';\nfetch('/v1/b', {method: 'POST'})", ""},
		{"a.js", "const u = '/v1/a';\n\n\n\nconst m = {method: 'POST'}", ""},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			f, _ := pick(tt.file).scan(tt.file, []byte(tt.src))
			if len(f.URLs) == 0 {
				t.Fatal("no URL found")
			}
			if got := f.URLs[0].Method; got != tt.want {
				t.Errorf("method = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEndpoint(t *testing.T) {
	tests := []struct {
		lit, url, host string
		ok             bool
	}{
		{"https://api.stripe.com/v1/charges", "https://api.stripe.com/v1/charges", "api.stripe.com", true},
		{"http://user@Localhost:8080/x", "http://user@Localhost:8080/x", "localhost", true},
		{"https://${region}.example.com/v1/x", "/v1/x", "", true},
		{"/v1/orders/{id}", "/v1/orders/{id}", "", true},
		{"${BASE}/v1/orders/${id}", "/v1/orders/${id}", "", true},
		{"%s/v2/items", "/v2/items", "", true},
		{"/", "", "", false},
		{"/123/x", "", "", false},
		{"see https://x.io/a for more", "", "", false},
		{"v1/orders", "", "", false},
		{"/a b", "", "", false},
	}
	for _, tt := range tests {
		u, host, ok := endpoint(tt.lit)
		if u != tt.url || host != tt.host || ok != tt.ok {
			t.Errorf("endpoint(%q) = %q, %q, %v; want %q, %q, %v", tt.lit, u, host, ok, tt.url, tt.host, tt.ok)
		}
	}
}

func TestBuildOrder(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"z/b.js": "import s from 'stripe';\ns.a();\ns.b();\n",
		"a.js":   "import s from 'stripe';\n\ns.c();\n",
		"m.py":   "import stripe\nstripe.d()\n",
	}
	var names []string
	for name, body := range files {
		write(t, root, name, body)
		names = append(names, name)
	}
	slices.Sort(names)
	first, err := Build(Options{Root: root, Files: names})
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(names)
	second, err := Build(Options{Root: root, Files: append(names, names[0])})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.js:3:1 s.c", "m.py:2:1 stripe.d", "z/b.js:2:1 s.a", "z/b.js:3:1 s.b"}
	for _, x := range []*Index{first, second} {
		if got := sites(x.Uses([]string{"stripe"})); !reflect.DeepEqual(got, want) {
			t.Errorf("Uses = %q, want %q", got, want)
		}
		if got := x.Files(); !reflect.DeepEqual(got, []string{"a.js", "m.py", "z/b.js"}) {
			t.Errorf("Files = %q", got)
		}
	}
}

func TestWith(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.js", "import s from 'stripe';\ns.a();\n")
	write(t, root, "b.js", "import s from 'stripe';\ns.b();\n")
	write(t, root, "gone.js", "import s from 'stripe';\ns.gone();\n")
	x, err := Build(Options{Root: root, Files: []string{"a.js", "b.js", "gone.js"}})
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "b.js", "import s from 'stripe';\n\ns.c();\n")
	write(t, root, "new.py", "import stripe\nstripe.d()\n")
	write(t, root, "node_modules/x.js", "import s from 'stripe';\ns.e();\n")
	if err := os.Remove(filepath.Join(root, "gone.js")); err != nil {
		t.Fatal(err)
	}
	if got := x.With(root, []string{"package.json"}); got != x {
		t.Error("With rebuilt the index for a file it does not read")
	}
	got := x.With(root, []string{"b.js", "gone.js", "new.py", "node_modules/x.js"})
	want, err := Build(Options{Root: root, Files: []string{"a.js", "b.js", "new.py"}})
	if err != nil {
		t.Fatal(err)
	}
	if g, w := sites(got.Uses([]string{"stripe"})), sites(want.Uses([]string{"stripe"})); !reflect.DeepEqual(g, w) {
		t.Errorf("Uses = %q, want %q", g, w)
	}
	if g, w := got.Files(), want.Files(); !reflect.DeepEqual(g, w) {
		t.Errorf("Files = %q, want %q", g, w)
	}
	if g := sites(x.Uses([]string{"stripe"})); len(g) != 3 {
		t.Errorf("With changed the index it came from: %q", g)
	}
}

// A file read again drops the notes it had, a Go parse error's among them.
func TestWithNotes(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package a\n\nfunc f() {\n\tif {\n}\n")
	write(t, root, "b.js", "const t = `unterminated\n")
	x, err := Build(Options{Root: root, Files: []string{"a.go", "b.js"}})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(x.Notes()); n != 2 {
		t.Fatalf("notes = %q", x.Notes())
	}
	write(t, root, "a.go", "package a\n")
	write(t, root, "b.js", "const t = 1\n")
	if got := x.With(root, []string{"a.go", "b.js"}).Notes(); len(got) != 0 {
		t.Errorf("notes = %q", got)
	}
}

func TestBuildSkips(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/a.js", "import s from 'stripe';\ns.a();\n")
	write(t, root, "src/big.js", "// "+strings.Repeat("x", maxSize)+"\n")
	write(t, root, "src/bin.js", "import s from 'stripe';\x00\ns.b();\n")
	write(t, root, "src/notes.txt", "s.c()\n")
	write(t, root, "node_modules/stripe/index.js", "import s from 'stripe';\ns.d();\n")
	write(t, root, "vendor/x/y.go", "package y\n")
	write(t, root, "build/out.js", "import s from 'stripe';\ns.e();\n")
	write(t, root, "gen/skip.js", "import s from 'stripe';\ns.f();\n")
	if err := os.Symlink(filepath.Join(root, "src", "a.js"), filepath.Join(root, "src", "link.js")); err != nil {
		t.Fatal(err)
	}

	x, err := Build(Options{Root: root, Skip: []string{"gen"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := x.Files(); !reflect.DeepEqual(got, []string{"src/a.js"}) {
		t.Errorf("Files = %q", got)
	}
	notes := strings.Join(x.Notes(), "\n")
	for _, want := range []string{"src/big.js: skipped, larger than 2 MB", "src/bin.js: skipped, binary"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes %q lack %q", notes, want)
		}
	}
}

func TestBuildGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	root := t.TempDir()
	write(t, root, ".gitignore", "ignored/\n")
	write(t, root, "a.js", "import s from 'stripe';\ns.a();\n")
	write(t, root, "untracked.py", "import stripe\nstripe.b()\n")
	write(t, root, "ignored/c.js", "import s from 'stripe';\ns.c();\n")
	write(t, root, "build/d.js", "import s from 'stripe';\ns.d();\n")
	write(t, root, "vendor/e.go", "package e\n")
	for _, args := range [][]string{{"init", "-q"}, {"add", "a.js", "build/d.js", "vendor/e.go", ".gitignore"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	x, err := Build(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if got := x.Files(); !reflect.DeepEqual(got, []string{"a.js", "build/d.js", "untracked.py"}) {
		t.Errorf("Files = %q", got)
	}
}

func TestEdges(t *testing.T) {
	x := build(t, map[string]string{
		"crlf.py":     "import stripe\r\n\r\nstripe.a()\r\n# c\r\nstripe.b()",
		"bom.go":      "\xef\xbb\xbfpackage b\n\nimport \"example.com/stripe\"\n\nvar _ = stripe.A()",
		"tabs.go":     "package t\n\nimport \"example.com/stripe\"\n\nfunc f() {\n\t\tstripe.B()\n}\n",
		"broken.go":   "package b\n\nimport \"example.com/stripe\"\n\nfunc f() {\n\tstripe.C()\n\tif {\n}\n",
		"broken.js":   "import s from 'stripe';\ns.d();\nconst t = `unterminated\ns.e();\n",
		"one-line.rb": "Stripe::Charge.create",
	})
	got := sites(x.Uses([]string{"example.com/stripe", "stripe", "Stripe"}))
	want := []string{
		"bom.go:5:9 stripe.A",
		"broken.go:6:2 stripe.C",
		"broken.js:2:1 s.d",
		"broken.js:4:1 s.e",
		"crlf.py:3:1 stripe.a",
		"crlf.py:5:1 stripe.b",
		"one-line.rb:1:1 Stripe.Charge.create",
		"tabs.go:6:3 stripe.B",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Uses\n got %q\nwant %q", got, want)
	}
	for _, s := range x.Uses([]string{"stripe"}) {
		if strings.ContainsAny(s.Text, "\r\n") || strings.HasPrefix(s.Text, string(bom)) {
			t.Errorf("%s:%d text %q", s.File, s.Line, s.Text)
		}
	}
	langs := map[string]string{}
	for _, f := range x.files {
		langs[f.File] = f.Lang
	}
	if langs["broken.js"] != "js?" || langs["broken.go"] != "go" {
		t.Errorf("langs = %v", langs)
	}
	notes := strings.Join(x.Notes(), "\n")
	if !strings.Contains(notes, "broken.go:7") || !strings.Contains(notes, "broken.js: unterminated") {
		t.Errorf("notes = %q", notes)
	}
}

func TestGoBuildTagsAndCgo(t *testing.T) {
	x := build(t, map[string]string{
		"c.go": "//go:build cgo && !windows\n// +build cgo,!windows\n\npackage c\n\n// #include <stdio.h>\nimport \"C\"\nimport \"unsafe\"\n\nfunc f() { C.puts((*C.char)(unsafe.Pointer(nil))) }\n",
	})
	if got := sites(x.Uses([]string{"C"})); !reflect.DeepEqual(got, []string{"c.go:10:12 C.puts", "c.go:10:21 C.char"}) {
		t.Errorf("Uses(C) = %q", got)
	}
	if notes := x.Notes(); len(notes) > 0 {
		t.Errorf("notes = %q", notes)
	}
}

func TestBuildFast(t *testing.T) {
	root := t.TempDir()
	srcs := map[string]string{
		".go": "package p\n\nimport \"github.com/stripe/stripe-go/v79/charge\"\n\nfunc F%d() { charge.New(nil) }\n",
		".ts": "import Stripe from 'stripe';\nconst s%d = new Stripe('k');\ns%[1]d.charges.create({ amount: 1, url: 'https://api.stripe.com/v1/x' });\n",
		".py": "import stripe\n\n\ndef f%d():\n    return stripe.Charge.create(amount=1)\n",
		".rb": "def f%d\n  Stripe::Charge.create(amount: 1)\nend\n",
	}
	var names []string
	for i := 0; i < 2000; i++ {
		ext := []string{".go", ".ts", ".py", ".rb"}[i%4]
		name := fmt.Sprintf("d%d/f%d%s", i%40, i, ext)
		write(t, root, name, fmt.Sprintf(srcs[ext], i))
		names = append(names, name)
	}
	limit := 2 * time.Second
	if race {
		limit *= 10
	}
	// CPU time where the OS gives it, so other packages' tests running
	// alongside do not fail a fast index.
	start, used := time.Now(), cpu()
	x, err := Build(Options{Root: root, Files: names})
	if err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	if used >= 0 {
		took = cpu() - used
	}
	if took > limit {
		t.Errorf("indexing 2000 files took %s", took)
	}
	if n := len(x.Uses([]string{"stripe", "Stripe", "github.com/stripe/stripe-go/v79"})); n != 2500 {
		t.Errorf("found %d sites, want 2500", n)
	}
}

func TestBinaryAfterFirstChunk(t *testing.T) {
	src := append([]byte("import s from 'stripe';\ns.a();\n"), bytes.Repeat([]byte(" "), 9000)...)
	src = append(src, 0)
	root := t.TempDir()
	write(t, root, "late.js", string(src))
	x, err := Build(Options{Root: root, Files: []string{"late.js"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(x.Uses([]string{"stripe"})) != 1 {
		t.Error("a NUL past the first 8000 bytes made the file binary")
	}
}

func TestGoScopes(t *testing.T) {
	x := build(t, map[string]string{
		"s.go": `package s

import "example.com/sdk"

type Hook func(h *sdk.Client)

type Svc struct{ api *sdk.Client }

func a(c *sdk.Client) {
	c.Ping()
	if v := sdk.New(); v != nil {
		v.Close()
	}
	v.Close()
}

func b(c int, h int) int {
	return c.Value + h.Value
}

func (s *Svc) d() {
	s.api.Ping()
	get().api.Ping()
	r, err := sdk.Get()
	_ = err.Error()
	r.Body.Close()
}
`,
	})
	want := []string{
		"s.go:5:19 sdk.Client",
		"s.go:7:23 sdk.Client",
		"s.go:9:11 sdk.Client",
		"s.go:10:2 c.Ping",
		"s.go:11:10 sdk.New",
		"s.go:12:3 v.Close",
		"s.go:22:4 api.Ping",
		"s.go:23:8 api.Ping",
		"s.go:24:12 sdk.Get",
		"s.go:26:2 r.Body.Close",
	}
	if got := sites(x.Uses([]string{"example.com/sdk"})); !reflect.DeepEqual(got, want) {
		t.Errorf("Uses\n got %q\nwant %q", got, want)
	}
}

func TestAliases(t *testing.T) {
	x := build(t, map[string]string{
		"a.js": `import Stripe from 'stripe';
early.charges.list();
const early = new Stripe('k');
const charge = await early.charges.create();
charge.refund();
early.charges.list();
`,
		"b.py": `import stripe
from openai import OpenAI
charge = stripe.Charge.create(amount=1)
charge.refund()
client: OpenAI = OpenAI()
client.models.list()
class A:
    def __init__(self):
        self.ai = OpenAI()
    def f(self):
        self.ai.files.list()
        other.ai.files.list()
`,
		"c.rb": `client = Stripe::StripeClient.new('k')
client.charges.create
@client = Stripe::StripeClient.new
@client.refunds.list
`,
		"d.rs": `fn main() {
    let mut c = reqwest::Client::new();
    c.get("u");
    let n = reqwest::get("u");
    n.status();
}
`,
	})
	want := []string{
		"a.js:3:19 Stripe",
		"a.js:4:22 early.charges.create",
		"a.js:6:1 early.charges.list",
		"b.py:3:10 stripe.Charge.create",
		"b.py:5:18 OpenAI",
		"b.py:6:1 client.models.list",
		"b.py:9:19 OpenAI",
		"b.py:11:14 ai.files.list",
		"c.rb:1:10 Stripe.StripeClient.new",
		"c.rb:2:1 client.charges.create",
		"c.rb:3:11 Stripe.StripeClient.new",
		"c.rb:4:2 client.refunds.list",
		"d.rs:2:17 reqwest.Client.new",
		"d.rs:3:5 c.get",
		"d.rs:4:13 reqwest.get",
	}
	if got := sites(x.Uses([]string{"stripe", "Stripe", "openai", "reqwest"})); !reflect.DeepEqual(got, want) {
		t.Errorf("Uses\n got %q\nwant %q", got, want)
	}
}

// A file counts as reached when a test names its path, imports its module or
// shares its Go package; one no test touches does not.
func TestReached(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"src/app.js":                           "module.exports = {}\n",
		"src/lib/index.js":                     "module.exports = {}\n",
		"src/alone.js":                         "module.exports = {}\n",
		"test/app.test.js":                     "const app = require('../src/app')\nconst lib = require('../src/lib')\n",
		"shop/billing.py":                      "def charge(): pass\n",
		"shop/orders.py":                       "def order(): pass\n",
		"tests/test_billing.py":                "from shop.billing import charge\n",
		"pkg/a/a.go":                           "package a\n",
		"pkg/a/a_test.go":                      "package a\n",
		"pkg/b/b.go":                           "package b\n",
		"src/main/java/shop/Cart.java":         "class Cart {}\n",
		"src/test/java/shop/CartTest.java":     "class CartTest {}\n",
		"src/main/java/shop/Db.java":           "class Db {}\n",
		"src/test/java/shop/FeedbackTest.java": "class FeedbackTest {}\n",
		"lib/api.rb":                           "class Api; end\n",
		"lib/cart.rb":                          "class Cart; end\n",
		"spec/capital_spec.rb":                 "describe 'x' do; end\n",
		"spec/cart_spec.rb":                    "describe Cart do; end\n",
		"lib/order.rb":                         "class Order; end\n",
		"test/test_order.rb":                   "class TestOrder; end\n",
		"src/Billing.cs":                       "class Billing {}\n",
		"Shop.Tests/BillingTests.cs":           "class BillingTests {}\n",
		"src/a.rs":                             "fn a() {}\n",
		"tests/data_test.rs":                   "fn t() {}\n",
		"crates/x/src/lib.rs":                  "fn f() {}\n\n#[cfg(test)]\nmod tests {\n    #[test]\n    fn t() {}\n}\n",
		"crates/x/src/other.rs":                "fn g() {}\n",
		"crates/x/src/cli.rs":                  "fn main() {}\n#[test]\nfn t() {}\n",
	}
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	x, err := Build(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]bool{
		"src/app.js": true, "src/lib/index.js": true, "src/alone.js": false,
		"shop/billing.py": true, "shop/orders.py": false,
		"pkg/a/a.go": true, "pkg/b/b.go": false,
		"src/main/java/shop/Cart.java": true, "src/main/java/shop/Db.java": false,
		"lib/api.rb": false, "lib/cart.rb": true, "lib/order.rb": true,
		"src/Billing.cs": true, "src/a.rs": true,
		"crates/x/src/lib.rs": true, "crates/x/src/cli.rs": true, "crates/x/src/other.rs": false,
	} {
		if got := x.Reached(p); got != want {
			t.Errorf("Reached(%s) = %v, want %v", p, got, want)
		}
	}
}

func TestRegexKeepsLaterCalls(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.js", "import Stripe from 'stripe';\n"+
		"const stripe = new Stripe(key);\n"+
		"const isTick = c => /`/.test(c);\n"+
		"function isUrl(u) {\n"+
		"  return /^https?:\\/\\//.test(u) && stripe.refunds.create({});\n"+
		"}\n"+
		"stripe.charges.create({});\n"+
		"const u = 'https://api.stripe.com/v1/charges';\n")
	x, err := Build(Options{Root: root, Files: []string{"a.js"}})
	if err != nil {
		t.Fatal(err)
	}
	got := sites(x.Uses([]string{"stripe"}))
	for _, want := range []string{"a.js:5:36 stripe.refunds.create", "a.js:7:1 stripe.charges.create"} {
		if !slices.Contains(got, want) {
			t.Errorf("Uses = %q, want %s among them", got, want)
		}
	}
	urls := x.URLs()
	if len(urls) != 1 || urls[0].URL != "https://api.stripe.com/v1/charges" {
		t.Errorf("URLs = %+v", urls)
	}
	if len(x.Notes()) != 0 {
		t.Errorf("notes = %q", x.Notes())
	}
}

func TestReaches(t *testing.T) {
	x := &Index{files: []*file{
		{FileScan: FileScan{File: "test/a.test.js", Lang: "js"}, lits: []lit{{Text: "../src/a"}}},
		{FileScan: FileScan{File: "test/b.test.js", Lang: "js"}, lits: []lit{{Text: "../src/b"}}},
	}}
	if !x.Reaches("test/a.test.js", "src/a.js") || x.Reaches("test/b.test.js", "src/a.js") || x.Reaches("test/a.test.js", "src/b.js") {
		t.Error("Reaches does not tell the tests apart")
	}
	if !x.Reached("src/a.js") || !x.Reached("src/b.js") || x.Reached("src/c.js") {
		t.Error("Reached")
	}
}
