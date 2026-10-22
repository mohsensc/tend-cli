package deliver

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mohsensc/tend-cli/internal/core"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden.md")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from golden:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func passRun(commit string, steps ...core.Step) *core.Run {
	return &core.Run{Commit: commit, Steps: steps, Pass: true}
}

func ok(cmd string, secs int, tail string) core.Step {
	return core.Step{Cmd: cmd, Took: time.Duration(secs) * time.Second, Tail: tail}
}

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 9, 0, 0, 0, time.UTC) }

const stripeDiff = `diff --git a/package.json b/package.json
--- a/package.json
+++ b/package.json
@@ -5,3 +5,3 @@
   "dependencies": {
-    "stripe": "^14.2.0"
+    "stripe": "^16.0.1"
   }
diff --git a/src/billing.js b/src/billing.js
--- a/src/billing.js
+++ b/src/billing.js
@@ -40,5 +40,5 @@ async function charge(order) {
   const stripe = require('./stripe')
 
-  const c = await stripe.charges.create({ amount: order.total })
+  const c = await stripe.paymentIntents.create({ amount: order.total })
   return c.id
 }
@@ -88,2 +88,2 @@ async function refund(id) {
-  return stripe.charges.retrieve(id)
+  return stripe.paymentIntents.retrieve(id)
 }
`

func TestBodyGoldens(t *testing.T) {
	stripeSites := []core.Site{
		{File: "src/billing.js", Line: 42, Text: "const c = await stripe.charges.create({ amount: order.total })"},
		{File: "src/billing.js", Line: 88, Text: "return stripe.charges.retrieve(id)"},
	}
	npmProof := &core.Proof{
		Base: passRun("abc1234def",
			ok("npm ci", 12, "added 120 packages"),
			ok("npm test", 30, "ok 1 - charges")),
		Patched: passRun("fff9999aaa",
			ok("npm ci", 13, "added 123 packages"),
			ok("npm test", 31, "TAP version 13\n\x1b[32mok 1 - payment intents\x1b[0m\n1..1\n# pass 1")),
	}
	pass := func(lines ...string) *core.Vetting {
		return &core.Vetting{Checks: lines, Verdict: "pass"}
	}

	tests := []struct {
		name string
		in   BodyInput
	}{
		{
			name: "security_bump",
			in: BodyInput{
				Change: &core.Change{
					ID: "npm/stripe@16.0.1", Kind: core.Security, Severity: "high", Level: "major",
					Deps: []core.Dep{dep("npm", "stripe", "14.2.0")}, To: "16.0.1",
					Published: day(2026, 10, 12), Sites: stripeSites,
					Evidence: []core.Evidence{
						{Source: "osv", Title: "GHSA-xxxx-yyyy-zzzz Request smuggling in stripe", URL: "https://osv.dev/vulnerability/GHSA-xxxx-yyyy-zzzz",
							Text: "Versions before 16.0.1 accept a crafted header.\nUpgrade to 16.0.1 or later."},
						{Source: "osv", Title: "GHSA-aaaa-bbbb-cccc Prototype pollution", URL: "https://osv.dev/vulnerability/GHSA-aaaa-bbbb-cccc"},
					},
				},
				Patch:   &core.Patch{Files: []string{"package.json", "package-lock.json", "src/billing.js"}, Diff: stripeDiff},
				Proof:   npmProof,
				Vetting: pass("scope", "symbols", "fields: not run: this is not an API change", "lockfile"),
				Base:    "abc1234def", BaseRef: "main", Rev: 1,
			},
		},
		{
			name: "breaking_bump_model",
			in: BodyInput{
				Change: &core.Change{
					ID: "npm/stripe@16.0.0", Kind: core.Breaking, Level: "major",
					Deps: []core.Dep{dep("npm", "stripe", "14.2.0")}, To: "16.0.0",
					Published: day(2026, 9, 1), Sites: stripeSites,
					Evidence: []core.Evidence{
						{Source: "releases", Title: "16.0.0", URL: "https://github.com/stripe/stripe-node/releases/tag/v16.0.0",
							Text: "## Breaking changes\n\n* `charges.*` was removed, use `paymentIntents.*`\n* Node 16 is no longer supported\n\n## Changes\n* New `paymentIntents.confirm` helper\n* Fix retry on 409\n* Update types\n* Docs"},
						{Source: "releases", Title: "15.0.0", URL: "https://github.com/stripe/stripe-node/releases/tag/v15.0.0",
							Text: "Pin the API version to 2024-06-20."},
					},
				},
				Patch: &core.Patch{Files: []string{"package.json", "package-lock.json", "src/billing.js"}, Diff: stripeDiff, Model: true},
				Proof: npmProof,
				Vetting: &core.Vetting{
					Checks:   []string{"scope", "symbols", "question"},
					Findings: []core.Finding{{Check: "question", File: "src/billing.js", Line: 42, Msg: "the old call passed `capture: false`; the new one needs `capture_method: 'manual'`", Action: "flagged"}},
					Verdict:  "pass",
				},
				Base: "abc1234def", BaseRef: "main", Rev: 2,
				Notes: []string{"Auto-merge was not enabled: Pull request Auto merge is not allowed for this repository"},
			},
		},
		{
			name: "deprecation_swap",
			in: BodyInput{
				Change: &core.Change{
					ID: "npm/request->undici@6.19.8", Kind: core.Deprecation,
					Deps: []core.Dep{dep("npm", "request", "2.88.2")}, To: "6.19.8", ToName: "undici",
					Sites:    []core.Site{{File: "lib/http.js", Line: 3, Text: "const request = require('request')"}},
					Evidence: []core.Evidence{{Source: "table", Title: "request is deprecated (Feb 2020)", URL: "https://github.com/request/request/issues/3142"}},
				},
				Meta: &core.Meta{Name: "request", Deprecated: "request has been deprecated, see https://github.com/request/request/issues/3142"},
				Patch: &core.Patch{Files: []string{"package.json", "package-lock.json", "lib/http.js"}, Diff: `diff --git a/lib/http.js b/lib/http.js
--- a/lib/http.js
+++ b/lib/http.js
@@ -3,1 +3,1 @@
-const request = require('request')
+const { request } = require('undici')
`},
				Proof:   &core.Proof{Base: passRun("abc1234", ok("npm test", 4, "")), Patched: passRun("bbb2222", ok("npm test", 5, "# pass 4"))},
				Vetting: pass("symbols", "leftovers"),
				Base:    "abc1234", BaseRef: "main", Rev: 1,
			},
		},
		{
			name: "api_field_removed",
			in: BodyInput{
				Change: &core.Change{
					ID: "api/fixture/GET /v1/orders#total_cents", Kind: core.Breaking,
					API:   &core.APIChange{Provider: "fixture", Method: "GET", Path: "/v1/orders", Field: "total_cents", What: "response field removed"},
					Sites: []core.Site{{File: "app/orders.py", Line: 17, Text: `return order["total_cents"]`}},
					Evidence: []core.Evidence{{Source: "openapi", Title: "fixture spec 2.0.0", URL: "https://api.fixture.dev/openapi.json",
						Text: "GET /v1/orders\nresponse property total_cents removed\nresponse property total added: {amount, currency}"}},
					Options: []core.Option{{Title: "Read `total.amount`", Detail: "total.amount carries the same value in minor units.", Safe: true}},
				},
				Patch: &core.Patch{Files: []string{"app/orders.py"}, Diff: `diff --git a/app/orders.py b/app/orders.py
--- a/app/orders.py
+++ b/app/orders.py
@@ -17,1 +17,1 @@
-    return order["total_cents"]
+    return order["total"]["amount"]
`},
				Proof:   &core.Proof{Base: passRun("c0ffee1", ok("python3 -m unittest", 1, "")), Patched: passRun("c0ffee2", ok("python3 -m unittest", 1, "Ran 3 tests\n\nOK"))},
				Vetting: pass("fields", "leftovers"),
				Base:    "c0ffee1", BaseRef: "main", Rev: 1,
			},
		},
		{
			name: "docker_eol",
			in: BodyInput{
				Change: &core.Change{
					ID: "docker/library/node@22-alpine", Kind: core.Deprecation,
					Deps: []core.Dep{dep("docker", "library/node", "16-alpine")},
					To:   "22-alpine", Sunset: day(2023, 9, 11),
					Sites:    []core.Site{{File: "Dockerfile", Line: 1, Text: "FROM node:16-alpine"}},
					Evidence: []core.Evidence{{Source: "eol", Title: "node 16 reached end of life on 2023-09-11", URL: "https://endoflife.date/nodejs"}},
				},
				Patch: &core.Patch{Files: []string{"Dockerfile"}, Diff: `diff --git a/Dockerfile b/Dockerfile
--- a/Dockerfile
+++ b/Dockerfile
@@ -1,2 +1,2 @@
-FROM node:16-alpine
+FROM node:22-alpine
 WORKDIR /app
`},
				Proof:   &core.Proof{Nothing: true},
				Vetting: pass("scope"),
				Base:    "d00dfeed", BaseRef: "main", Rev: 1,
			},
		},
		{
			name: "actions_bump",
			in: BodyInput{
				Change: &core.Change{
					ID: "actions/actions/checkout@v4", Kind: core.Deprecation,
					Deps: []core.Dep{dep("actions", "actions/checkout", "v2")}, To: "v4",
					Sites: []core.Site{
						{File: ".github/workflows/ci.yml", Line: 9, Text: "- uses: actions/checkout@v2"},
						{File: ".github/workflows/release.yml", Line: 12, Text: "- uses: actions/checkout@v2"},
					},
					Evidence: []core.Evidence{{Source: "registry", Title: "actions/checkout v2 runs on node12", URL: "https://github.com/actions/checkout/releases/tag/v4.0.0"}},
				},
				Patch: &core.Patch{Files: []string{".github/workflows/ci.yml", ".github/workflows/release.yml"}, Diff: `diff --git a/.github/workflows/ci.yml b/.github/workflows/ci.yml
--- a/.github/workflows/ci.yml
+++ b/.github/workflows/ci.yml
@@ -9,1 +9,1 @@
-      - uses: actions/checkout@v2
+      - uses: actions/checkout@v4
diff --git a/.github/workflows/release.yml b/.github/workflows/release.yml
--- a/.github/workflows/release.yml
+++ b/.github/workflows/release.yml
@@ -12,1 +12,1 @@
-      - uses: actions/checkout@v2
+      - uses: actions/checkout@v4
`},
				Proof: &core.Proof{Nothing: true},
				Base:  "d00dfeed", BaseRef: "main", Rev: 1,
			},
		},
		{
			name: "decision",
			in: BodyInput{
				Change: &core.Change{
					ID: "npm/moment@", Kind: core.Deprecation,
					Deps: []core.Dep{dep("npm", "moment", "2.30.1")},
					Sites: []core.Site{
						{File: "src/dates.js", Line: 2, Text: "const moment = require('moment')"},
						{File: "src/report.js", Line: 5, Text: "moment().format('YYYY-MM-DD')"},
					},
					Evidence: []core.Evidence{{Source: "table", Title: "moment is in maintenance mode", URL: "https://momentjs.com/docs/#/-project-status/",
						Text: "Moment is a legacy project, now in maintenance mode."}},
					Options: []core.Option{
						{Title: "Migrate to `dayjs`", Detail: "Smallest API difference, 2 KB, immutable.", To: "1.11.13", ToName: "dayjs"},
						{Title: "Migrate to `luxon`", Detail: "Better time zone support, larger API change."},
						{Title: "Keep moment", Detail: "No change; it keeps working."},
					},
				},
				Meta:  &core.Meta{Name: "moment", Deprecated: "Moment is in maintenance mode"},
				Patch: &core.Patch{Decision: true},
				Base:  "abc1234", BaseRef: "main", Rev: 1,
			},
		},
		{
			name: "failing_baseline_draft",
			in: BodyInput{
				Change: &core.Change{
					ID: "npm/left-pad@1.3.0", Kind: core.Update, Level: "minor",
					Deps: []core.Dep{dep("npm", "left-pad", "1.1.0")}, To: "1.3.0",
					Sites: []core.Site{{File: "src/pad.js", Line: 1, Text: "const pad = require('left-pad')"}},
				},
				Patch: &core.Patch{Files: []string{"package.json", "package-lock.json"}, Diff: ""},
				Proof: &core.Proof{
					Base: &core.Run{Commit: "abc1234", Pass: false, Steps: []core.Step{
						ok("npm ci", 9, ""),
						{Cmd: "npm test", Exit: 1, Took: 1500 * time.Millisecond, Tail: "not ok 1 - flaky clock"},
					}},
					Patched: &core.Run{Commit: "eee5555", Pass: false, Steps: []core.Step{
						ok("npm ci", 9, ""),
						{Cmd: "npm test", Exit: 1, Took: 75 * time.Second, Tail: "not ok 1 - flaky clock\n# fail 1"},
						{Cmd: "npm run build", Skipped: "after failure"},
					}},
				},
				Vetting: &core.Vetting{Checks: []string{"scope", "lockfile"}, Verdict: "draft", Findings: []core.Finding{{Check: "lockfile", Msg: "unrelated lockfile churn: ms, debug", Action: "flagged"}}},
				Base:    "abc1234", BaseRef: "main", Rev: 1,
			},
		},
		{
			name: "nothing_to_run",
			in: BodyInput{
				Change: &core.Change{
					ID: "npm/left-pad@1.1.1", Kind: core.Update, Level: "patch",
					Deps: []core.Dep{dep("npm", "left-pad", "1.1.0")}, To: "1.1.1",
				},
				Patch: &core.Patch{Files: []string{"package.json"}},
				Proof: &core.Proof{Nothing: true},
				Base:  "abc1234", Rev: 1,
			},
		},
		{
			name: "tidy",
			in: BodyInput{
				Change: &core.Change{
					ID: core.TidyID, Kind: core.Tidy,
					Cleanup: &core.Cleanup{
						Unused: []core.Dep{
							{Ecosystem: "npm", Name: "@acme/colors", Version: "1.2.0", Direct: true, Manifest: core.Manifest{Path: "package.json"}},
							{Ecosystem: "npm", Name: "left-pad", Version: "1.3.0", Direct: true, Manifest: core.Manifest{Path: "package.json"}},
						},
						Moves: []core.Change{{
							ID: "npm/stripe@16.0.0", Kind: core.Deprecation,
							Deps: []core.Dep{dep("npm", "stripe", "14.2.0")}, To: "16.0.0", Sites: stripeSites,
							Evidence: []core.Evidence{{Source: "registry", Title: "stripe 14.2.0 is deprecated", Text: "14.x is deprecated: charges.* was replaced by paymentIntents.*"}},
						}},
						Kept: []core.Aside{
							{Dep: core.Dep{Ecosystem: "npm", Name: "eslint-plugin-acme", Version: "2.0.0", Manifest: core.Manifest{Path: "package.json"}},
								Why: "a plugin eslint loads by a name its config gives"},
							{Dep: core.Dep{Ecosystem: "npm", Name: "chalk", Version: "5.3.0", Manifest: core.Manifest{Path: "package.json"}}, Why: "with it removed, `npm test` fails (exit 1)"},
						},
						Left: []core.Aside{{Dep: dep("npm", "moment", "2.30.1"), Why: "No model is configured to move the 3 call sites.",
							Options: []core.Option{{Title: "Migrate to `dayjs`"}, {Title: "Pin to 2.30.1 and ignore"}}}},
					},
				},
				Patch: &core.Patch{Files: []string{"package-lock.json", "package.json", "src/billing.js"}, Diff: stripeDiff, Model: true,
					Note: "4 steps: 1 read, 2 edits, 1 run; the commands passed the one time they ran"},
				Proof:   npmProof,
				Vetting: pass("scope: 3 files, all explained by this change", "symbols: 2/2 changed calls resolve", "lockfile", "debug", "comments"),
				Base:    "abc1234def", BaseRef: "main", Rev: 1,
			},
		},
		{
			name: "tidy_welcome",
			in: BodyInput{
				Change:  &core.Change{ID: core.TidyID, Kind: core.Tidy, Cleanup: &core.Cleanup{}},
				Patch:   &core.Patch{Files: []string{"tend.yml"}},
				Proof:   &core.Proof{Base: passRun("abc1234", ok("go test ./...", 4, "")), Patched: passRun("bbb2222", ok("go test ./...", 4, "ok  example.com/app"))},
				Vetting: pass("scope: 1 file, explained by this change"),
				Base:    "abc1234", BaseRef: "main", Rev: 1,
			},
		},
		{
			name: "baseline_missing",
			in: BodyInput{
				Change: &core.Change{
					ID: "npm/left-pad@1.1.1", Kind: core.Update, Level: "patch",
					Deps: []core.Dep{dep("npm", "left-pad", "1.1.0")}, To: "1.1.1",
				},
				Patch: &core.Patch{Files: []string{"package.json"}},
				Proof: &core.Proof{Container: "node:22", Patched: passRun("eee5555", ok("npm test", 3, "pass"))},
				Base:  "abc1234", BaseRef: "main", Rev: 1,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Body(tc.in)
			golden(t, tc.name, got)
			id, base, rev, ok := ParseMarker(got)
			if !ok || id != tc.in.Change.ID || base != tc.in.Base || rev != tc.in.Rev {
				t.Errorf("marker = %q %q %d %v", id, base, rev, ok)
			}
		})
	}
}

// The welcome is plain: a few sentences, no exclamation marks, no emoji, and
// every way to steer tend named.
func TestWelcome(t *testing.T) {
	for _, c := range []*core.Cleanup{
		{},
		{Unused: []core.Dep{{Name: "a"}}},
		{Unused: []core.Dep{{Name: "a"}, {Name: "b"}}, Moves: []core.Change{{}}, Kept: []core.Aside{{}}},
		{Left: []core.Aside{{}}},
	} {
		w := welcome(c)
		if n := strings.Count(w, ". ") + 1; n < 3 || n > 5 {
			t.Errorf("%d sentences:\n%s", n, w)
		}
		if strings.ContainsAny(w, "!\n") {
			t.Errorf("welcome:\n%s", w)
		}
		for _, r := range w {
			if r > unicode.MaxASCII {
				t.Errorf("welcome has %q:\n%s", r, w)
			}
		}
		for _, cmd := range []string{"`tend.yml`", "`tend skip`", "`tend retry`", "`tend apply N`", "one pull request at a time", "touch your code", "prove"} {
			if !strings.Contains(w, cmd) {
				t.Errorf("welcome does not say %s:\n%s", cmd, w)
			}
		}
		// It never says it found nothing and then lists what it found.
		if len(c.Kept)+len(c.Left) > 0 && strings.Contains(w, "found nothing to tidy up") {
			t.Errorf("welcome contradicts itself:\n%s", w)
		}
	}
}

func TestParseMarker(t *testing.T) {
	tests := []struct {
		name string
		body string
		id   string
		base string
		rev  int
		ok   bool
	}{
		{"plain", `text\n<!-- tend id="npm/stripe@16.0.1" base="abc123" rev="1" -->`, "npm/stripe@16.0.1", "abc123", 1, true},
		{"api id", `<!-- tend id="api/fixture/GET /v1/orders#total_cents" base="" rev="12" -->`, "api/fixture/GET /v1/orders#total_cents", "", 12, true},
		{"escaped", `<!-- tend id="npm/a--&gt;b&amp;c@1" base="x" rev="3" -->`, "npm/a-->b&c@1", "x", 3, true},
		{"swap id", `<!-- tend id="npm/request->undici@6.19.8" base="x" rev="1" -->`, "npm/request->undici@6.19.8", "x", 1, true},
		{"last wins", `<!-- tend id="old" base="a" rev="1" -->
quoted
<!-- tend id="new" base="b" rev="2" -->`, "new", "b", 2, true},
		{"none", "no marker here", "", "", 0, false},
		{"empty", "", "", "", 0, false},
		{"bad rev", `<!-- tend id="x" base="y" rev="one" -->`, "", "", 0, false},
		{"tidy-up owed", `<!-- tend id="npm/a@1.0.1" base="y" rev="2" tidy="due" -->`, "npm/a@1.0.1", "y", 2, true},
		{"other comment", `<!-- something else -->`, "", "", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, base, rev, ok := ParseMarker(tc.body)
			if id != tc.id || base != tc.base || rev != tc.rev || ok != tc.ok {
				t.Errorf("got %q %q %d %v", id, base, rev, ok)
			}
		})
	}
}

func TestTidyDueMarker(t *testing.T) {
	ch := &core.Change{ID: "npm/a@1.0.1", Kind: core.Security, Deps: []core.Dep{{Ecosystem: "npm", Name: "a", Version: "1.0.0"}}, To: "1.0.1"}
	body := Body(BodyInput{Change: ch, Base: "abc", TidyDue: true})
	if !strings.Contains(body, `<!-- tend id="npm/a@1.0.1" base="abc" rev="1" tidy="due" -->`) || !TidyDue(body) {
		t.Errorf("marker of a PR ahead of the tidy-up:\n%s", body)
	}
	if id, _, _, ok := ParseMarker(body); !ok || id != "npm/a@1.0.1" {
		t.Errorf("ParseMarker = %q, %v", id, ok)
	}
	if body := Body(BodyInput{Change: ch, Base: "abc"}); TidyDue(body) || !strings.Contains(body, `rev="1" -->`) {
		t.Errorf("marker of any other PR:\n%s", body)
	}
}

func TestMarkerEscapes(t *testing.T) {
	ch := &core.Change{ID: `npm/a"b-->c&d@1`, Kind: core.Update, Deps: []core.Dep{dep("npm", "a", "1")}, To: "1"}
	body := Body(BodyInput{Change: ch, Base: "x", Rev: 4})
	id, _, rev, ok := ParseMarker(body)
	if !ok || id != ch.ID || rev != 4 {
		t.Fatalf("round trip lost the id: %q %d %v", id, rev, ok)
	}
	if strings.Count(body, "-->") != 1 {
		t.Errorf("an id can end the comment early:\n%s", body)
	}
}

func TestBodyFencesUpstreamText(t *testing.T) {
	ch := &core.Change{
		ID: "npm/x@2", Kind: core.Update, Deps: []core.Dep{dep("npm", "x", "1")}, To: "2",
		Evidence: []core.Evidence{{Source: "releases", Title: "2.0.0", Text: "thanks @someone for #123\n```js\nfoo()\n```\n"}},
	}
	got := Body(BodyInput{Change: ch, Rev: 1})
	if !strings.Contains(got, "  ````\n  thanks @someone for #123\n  ```js\n  foo()\n  ```\n  ````") {
		t.Errorf("upstream text is not safely fenced:\n%s", got)
	}
}

func TestReplacements(t *testing.T) {
	diff := `diff --git a/a.js b/a.js
--- a/a.js
+++ b/a.js
@@ -3,6 +3,7 @@
 keep
-one
-two
+uno
+dos
 keep
-solo
+a
+b
+c
 tail
@@ -20,2 +21,1 @@
-gone
 end
diff --git a/b.js b/b.js
--- a/b.js
+++ b/b.js
@@ -1,1 +1,2 @@
 keep
+only added
`
	got := replacements(diff)
	want := map[string]map[int][]string{"a.js": {4: {"uno"}, 5: {"dos"}, 7: {"a", "b", "c"}}}
	if len(got) != 1 || len(got["a.js"]) != 3 {
		t.Fatalf("got %v", got)
	}
	for line, lines := range want["a.js"] {
		if strings.Join(got["a.js"][line], "|") != strings.Join(lines, "|") {
			t.Errorf("line %d = %v, want %v", line, got["a.js"][line], lines)
		}
	}
}

func TestReplacementsKeepsDashedContent(t *testing.T) {
	diff := `diff --git a/q.sql b/q.sql
--- a/q.sql
+++ b/q.sql
@@ -1,3 +1,3 @@
 select 1;
--- old comment
+++ new comment
 select 2;
`
	got := replacements(diff)
	if len(got) != 1 || strings.Join(got["q.sql"][2], "|") != "++ new comment" {
		t.Errorf("got %v", got)
	}
}

func TestGoMoved(t *testing.T) {
	diff := `diff --git a/go.mod b/go.mod
--- a/go.mod
+++ b/go.mod
@@ -1,9 +1,9 @@
 module example.com/app
 
-go 1.24.0
+go 1.25.0
 
 require (
-	golang.org/x/mod v0.33.0
+	golang.org/x/mod v0.40.0
 )
`
	ch := &core.Change{Deps: []core.Dep{dep("go", "golang.org/x/mod", "v0.33.0")}, To: "v0.40.0"}
	want := "The go line in `go.mod` moved from 1.24.0 to 1.25.0, as golang.org/x/mod v0.40.0 requires, so building this module now takes Go 1.25.0 or later."
	if got := goMoved(ch, diff); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if got := goMoved(ch, strings.Replace(diff, "+go 1.25.0", "+go 1.24.0", 1)); got != "" {
		t.Errorf("an unchanged go line is news: %q", got)
	}
}

func TestDur(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{250 * time.Millisecond, "250ms"},
		{12 * time.Second, "12s"},
		{12600 * time.Millisecond, "13s"},
		{75 * time.Second, "1m15s"},
		{3605 * time.Second, "60m05s"},
	}
	for _, tc := range tests {
		if got := dur(tc.in); got != tc.want {
			t.Errorf("dur(%v) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// A failing build that prints long lines, with a pile of advisories, still
// fits in a PR body, keeps the end of the output and keeps the marker.
func TestBodyFits(t *testing.T) {
	var out strings.Builder
	for i := range 40 {
		fmt.Fprintf(&out, "%d %s\n", i, strings.Repeat("x", 5000))
	}
	out.WriteString("the error is here")
	ch := &core.Change{ID: "npm/a@2.0.0", Kind: core.Security, Level: "patch", To: "2.0.0", Deps: []core.Dep{{Ecosystem: "npm", Name: "a", Version: "1.0.0"}}}
	for i := range 200 {
		ch.Evidence = append(ch.Evidence, core.Evidence{Source: "osv", Title: fmt.Sprintf("GHSA-%04d", i), Text: strings.Repeat("y", 1200), URL: "https://osv.dev/x"})
	}
	run := &core.Run{Steps: []core.Step{{Cmd: "npm test", Exit: 1, Tail: out.String()}}}
	body := Body(BodyInput{Change: ch, Patch: &core.Patch{Files: []string{"package.json"}}, Proof: &core.Proof{Base: run, Patched: run}, Base: "abc1234", Rev: 1})
	if len(body) > bodyMax {
		t.Errorf("body is %d bytes", len(body))
	}
	if id, _, _, ok := ParseMarker(body); !ok || id != "npm/a@2.0.0" {
		t.Error("the marker was cut")
	}
	if !strings.Contains(body, "the error is here") {
		t.Error("the end of the failing output was cut")
	}
}

func TestUpstreamCapsBackgroundNotes(t *testing.T) {
	var notes []core.Evidence
	for i := 7; i >= 1; i-- {
		text := "Fixes."
		if i == 2 {
			text = "BREAKING: drops node 16"
		}
		notes = append(notes, core.Evidence{Source: "releases", Title: fmt.Sprintf("v%d.0.0", i), Text: text})
	}
	ch := &core.Change{Evidence: append([]core.Evidence{{Source: "osv", Title: "GHSA-1", Text: "a hole"}}, notes...)}
	got := upstream(ch)
	for _, want := range []string{"**v7.0.0**", "**v5.0.0**", "**v2.0.0**", "- 3 more releases, not quoted"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	for _, gone := range []string{"**v4.0.0**", "**v3.0.0**", "**v1.0.0**"} {
		if strings.Contains(got, gone) {
			t.Errorf("quotes %q:\n%s", gone, got)
		}
	}
	if got := upstream(&core.Change{Evidence: notes}); strings.Count(got, "- **") != 7 || strings.Contains(got, "not quoted") {
		t.Errorf("notes that are the evidence were cut:\n%s", got)
	}
}

func TestOneLineKeepsRunesWhole(t *testing.T) {
	got := oneLine("ab"+strings.Repeat("é", 10), 5)
	if !utf8.ValidString(got) || got != "abé…" {
		t.Errorf("oneLine = %q", got)
	}
}
