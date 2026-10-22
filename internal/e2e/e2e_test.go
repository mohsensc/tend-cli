package e2e

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/deliver"
	"github.com/mohsensc/tend-cli/internal/model"
)

// script is a model that writes the migration in replies/<name>.diff and finds
// nothing to say when vetting asks about it.
func script(t *testing.T, name string) *model.Script {
	t.Helper()
	diff := string(must(os.ReadFile(filepath.Join("testdata", "replies", name+".diff"))))
	s := &model.Script{Replies: []string{diff}}
	for range 12 {
		s.Replies = append(s.Replies, "NONE")
	}
	return s
}

func onlyPR(t *testing.T, w *world) core.PR {
	t.Helper()
	all := w.forge.All()
	if len(all) != 1 {
		t.Fatalf("%d PRs, want 1: %+v", len(all), all)
	}
	return all[0]
}

func (w *world) goldenPR(pr core.PR) {
	w.t.Helper()
	w.golden("title.txt", pr.Title+"\n")
	w.golden("body.md", pr.Body)
	w.golden("diff.patch", w.diff(pr.Branch)+"\n")
}

type commit struct {
	author, subject, body, trailers string
	files                           []string
}

// commits lists what a branch on origin adds to main, oldest first.
func (w *world) commits(branch string) []commit {
	w.t.Helper()
	out := w.git(w.origin, "log", "--reverse", "--format=%H%x1f%an <%ae>%x1f%s%x1f%b%x1f%(trailers:only)%x1e", "main.."+branch)
	var res []commit
	for _, rec := range strings.Split(out, "\x1e") {
		f := strings.Split(strings.TrimSpace(rec), "\x1f")
		if len(f) < 5 {
			continue
		}
		files := w.git(w.origin, "show", "--format=", "--name-only", f[0])
		c := commit{author: f[1], subject: f[2], body: strings.TrimSpace(f[3]), trailers: strings.TrimSpace(f[4])}
		if files != "" {
			c.files = strings.Split(files, "\n")
		}
		res = append(res, c)
	}
	return res
}

func rev(t *testing.T, pr core.PR) int {
	t.Helper()
	_, _, n, ok := deliver.ParseMarker(pr.Body)
	if !ok {
		t.Fatalf("no marker in the body of #%d", pr.Number)
	}
	return n
}

// S1. A deprecated SDK version with call sites: the registry names the
// replacement major, the model moves the calls, the tests prove it.
func TestNodeDeprecation(t *testing.T) {
	need(t, "node", "npm")
	w := setup(t, "node-sdk")
	w.model = script(t, "node-sdk")

	w.golden("check.json", w.tend("check", "--json").want(t, 0).stdout)
	w.tend("check", "--fail-on", "deprecation").want(t, 3)
	w.tend("check", "--fail-on", "security").want(t, 0)

	res := w.tend("run").want(t, 0)
	if !strings.HasPrefix(res.stdout, "tend: opened #1 Migrate off deprecated @fixture/pay 1.x ") {
		t.Fatalf("run said %q", res.stdout)
	}
	pr := onlyPR(t, w)
	w.goldenPR(pr)
	if pr.Draft || pr.State != "open" || pr.Branch != "tend/npm-fixture-pay-2.0.0" {
		t.Errorf("pr = %+v", pr)
	}
	if got := w.forge.Labels[1]; !slices.Equal(got, []string{"tend"}) {
		t.Errorf("labels = %v", got)
	}
	cs := w.commits(pr.Branch)
	if len(cs) != 1 {
		t.Fatalf("%d commits on the branch, want 1", len(cs))
	}
	c := cs[0]
	if c.subject != "Migrate off deprecated @fixture/pay 1.x" || c.author != maintainer.Name+" <"+maintainer.Email+">" || c.trailers != "" {
		t.Errorf("commit = %+v", c)
	}
	if !slices.Equal(c.files, []string{"package-lock.json", "package.json", "src/billing.js"}) {
		t.Errorf("commit touches %q", c.files)
	}
	st := w.state()
	if st.Open == nil || st.Open.PR != 1 || st.Open.ID != "npm/@fixture/pay@2.0.0" {
		t.Errorf("state open = %+v", st.Open)
	}
	if s := st.Seen["npm/@fixture/util@1.0.1"]; s.Verdict != "noise" || s.Detail != "patch, 0 sites" {
		t.Errorf("the patch bump was not recorded as noise: %+v", s)
	}

	tip := w.git(w.origin, "rev-parse", pr.Branch)
	res = w.tend("run").want(t, 0)
	if !strings.Contains(res.stdout, "waiting on PR #1") || len(w.forge.All()) != 1 {
		t.Errorf("second run: %q, %d PRs", res.stdout, len(w.forge.All()))
	}
	if now := w.git(w.origin, "rev-parse", pr.Branch); now != tip {
		t.Error("a run with nothing new pushed a commit")
	}

	w.merge(1)
	res = w.tend("run").want(t, 0)
	if res.stdout != "tend: up to date\n" {
		t.Errorf("after merge: %q", res.stdout)
	}
	st = w.state()
	if st.Open != nil || st.Seen["npm/@fixture/pay@2.0.0"].Verdict != "delivered" {
		t.Errorf("after merge: open %+v, seen %+v", st.Open, st.Seen["npm/@fixture/pay@2.0.0"])
	}
}

// S1 with a model that calls tools: it reads the code, edits the two calls,
// runs the tests and says it is done; the proof is the run it left behind.
func TestNodeAgent(t *testing.T) {
	need(t, "node", "npm")
	w := setup(t, "node-sdk")
	call := func(id, name string, args map[string]any) model.ToolCall {
		b := must(json.Marshal(args))
		return model.ToolCall{ID: id, Name: name, Args: b}
	}
	m := &model.Steps{Replies: []model.Reply{
		{Calls: []model.ToolCall{call("1", "read", map[string]any{"path": "src/billing.js"})}},
		{Text: "Both calls move to paymentIntents.", Calls: []model.ToolCall{
			call("2", "edit", map[string]any{"path": "src/billing.js", "old": "pay.charges.create(", "new": "pay.paymentIntents.create("}),
			call("3", "edit", map[string]any{"path": "src/billing.js", "old": "pay.charges.retrieve(", "new": "pay.paymentIntents.retrieve("}),
		}},
		{Calls: []model.ToolCall{call("4", "run", map[string]any{"name": "test"})}},
		{Calls: []model.ToolCall{call("5", "done", map[string]any{"summary": "moved charges to paymentIntents"})}},
	}}
	for range 12 {
		m.Script.Replies = append(m.Script.Replies, "NONE")
	}
	w.model = m
	res := w.tend("run").want(t, 0)
	if !strings.HasPrefix(res.stdout, "tend: opened #1 Migrate off deprecated @fixture/pay 1.x ") {
		t.Fatalf("run said %q\n%s", res.stdout, res.stderr)
	}
	pr := onlyPR(t, w)
	if want := "Part of this change was written by a model, in 5 steps: 1 read, 2 edits, 1 run; the commands passed both times they ran."; !strings.Contains(pr.Body, want) {
		t.Errorf("body does not say what the model did:\n%s", pr.Body)
	}
	d := w.diff(pr.Branch)
	if !strings.Contains(d, "+  return pay.paymentIntents.create(") || !strings.Contains(d, "+  return pay.paymentIntents.retrieve(id)") {
		t.Errorf("diff:\n%s", d)
	}
	if strings.Count(res.stderr, "proving patched") != 2 {
		t.Errorf("the proof ran again after the agent's last run:\n%s", res.stderr)
	}
	log := string(must(os.ReadFile(filepath.Join(filepath.Dir(w.statePath()), "logs", "npm-fixture-pay-2.0.0", "agent.jsonl"))))
	if strings.Count(log, "\n") != 5 || !strings.Contains(log, `"tool":"done"`) {
		t.Errorf("transcript:\n%s", log)
	}
}

// S1 without a model: the bare bump breaks the tests, so tend takes the way
// off it its rule allows, the rename the deprecation names, proves it, and
// says why it chose it. The other option stays one comment away, and only a
// maintainer's comment takes it.
func TestNodeChoice(t *testing.T) {
	need(t, "node", "npm")
	w := setup(t, "node-sdk")
	res := w.tend("run").want(t, 0)
	if !strings.HasPrefix(res.stdout, "tend: opened #1 Migrate off deprecated @fixture/pay 1.x ") {
		t.Fatalf("run said %q\n%s", res.stdout, res.stderr)
	}
	pr := onlyPR(t, w)
	w.goldenPR(pr)
	if want := "tend took option 1 on its own: the rename the deprecation notice names. Comment `tend apply N` to have another instead."; !strings.Contains(pr.Body, want) {
		t.Errorf("the body does not say why tend chose:\n%s", pr.Body)
	}
	if !strings.Contains(res.stderr, "trying option 1 of @fixture/pay on its own") {
		t.Errorf("the choice is not logged:\n%s", res.stderr)
	}

	w.forge.Now = func() time.Time { return now.Add(time.Hour) }
	w.say(1, "drive-by", "NONE", "tend apply 2")
	if res := w.tend("run").want(t, 0); !strings.Contains(res.stdout, "waiting on PR #1") || rev(t, *w.forge.PR(1)) != 1 {
		t.Fatalf("a comment from outside the repo was honored: %q", res.stdout)
	}
	w.forge.Now = func() time.Time { return now.Add(2 * time.Hour) }
	w.say(1, "owner", "OWNER", "tend apply 2")
	res = w.tend("run").want(t, 0)
	if !strings.HasPrefix(res.stdout, "tend: updated #1 Pin @fixture/pay to 1.4.0 ") {
		t.Fatalf("after tend apply 2: %q", res.stdout)
	}
	if d := w.diff("tend/npm-fixture-pay-2.0.0"); !strings.Contains(d, `versions: [">=2.0.0"]`) || strings.Contains(d, "package-lock.json") {
		t.Errorf("the pin:\n%s", d)
	}
}

// When no way off can be proven, tend stays put: the PR changes nothing and
// lays out the options. A maintainer's pick, and only a maintainer's, turns
// it into the migration.
func TestNodeDecision(t *testing.T) {
	need(t, "node", "npm")
	w := setup(t, "node-sdk")
	w.commit("Pin the endpoint", map[string]string{"test/endpoint.test.js": `'use strict'

const test = require('node:test')
const assert = require('node:assert')
const { charge } = require('../src/billing')

test('charges go to the charges endpoint', async () => {
  let url
  globalThis.fetch = async (u) => { url = u; return { ok: true, json: async () => ({}) } }
  await charge(5, 'cus_1').catch(() => {})
  assert.match(url, /\/v1\/charges$/)
})
`})
	res := w.tend("run").want(t, 0)
	if !strings.HasPrefix(res.stdout, "tend: opened #1 Decide: migrate off @fixture/pay 1.x ") {
		t.Fatalf("run said %q\n%s", res.stdout, res.stderr)
	}
	if strings.Count(res.stderr, "mechanical only") != 1 || !strings.Contains(res.stderr, "npm/@fixture/pay@2.0.0 needs code edits a model would make") {
		t.Errorf("the run does not say plainly that it had no model:\n%s", res.stderr)
	}
	pr := onlyPR(t, w)
	w.golden("title.txt", pr.Title+"\n")
	w.golden("body.md", pr.Body)
	if d := w.diff(pr.Branch); d != "" {
		t.Errorf("a decision PR changed files:\n%s", d)
	}
	if cs := w.commits(pr.Branch); len(cs) != 1 || cs[0].files != nil || cs[0].body != "" {
		t.Errorf("want one empty commit, got %+v", cs)
	}
	if pr.Draft {
		t.Error("a decision PR is a draft")
	}
	if !strings.Contains(pr.Body, "Cannot read properties of undefined (reading 'create')") {
		t.Errorf("the body does not show why the bump alone fails:\n%s", pr.Body)
	}

	w.forge.Now = func() time.Time { return now.Add(time.Hour) }
	w.say(1, "drive-by", "NONE", "tend apply 2")
	if res := w.tend("run").want(t, 0); !strings.Contains(res.stdout, "waiting on PR #1") {
		t.Fatalf("a comment from outside the repo was honored: %q", res.stdout)
	}
	if rev(t, *w.forge.PR(1)) != 1 {
		t.Fatal("the PR changed")
	}

	w.forge.Now = func() time.Time { return now.Add(2 * time.Hour) }
	w.say(1, "owner", "OWNER", "tend apply 2")
	res = w.tend("run").want(t, 0)
	if !strings.HasPrefix(res.stdout, "tend: updated #1 Pin @fixture/pay to 1.4.0 ") {
		t.Fatalf("after tend apply 2: %q", res.stdout)
	}
	pr = *w.forge.PR(1)
	if rev(t, pr) != 2 || len(w.forge.All()) != 1 || pr.Draft {
		t.Errorf("pr = %+v", pr)
	}
	w.golden("applied-body.md", pr.Body)
	w.golden("applied-diff.patch", w.diff(pr.Branch)+"\n")

	if res := w.tend("run").want(t, 0); !strings.Contains(res.stdout, "waiting on PR #1") {
		t.Errorf("the same command was honored twice: %q", res.stdout)
	}
}

// S1 with a model that chats: vetting takes out the debug line and the
// comment before anything is committed.
func TestNodeVettingDropsChatter(t *testing.T) {
	need(t, "node", "npm")
	w := setup(t, "node-sdk")
	w.model = script(t, "node-sdk-chatty")
	w.tend("run").want(t, 0)
	pr := onlyPR(t, w)
	d := w.diff(pr.Branch)
	if strings.Contains(d, "console.log") || strings.Contains(d, "now we create") {
		t.Errorf("the chatter was committed:\n%s", d)
	}
	if !strings.Contains(d, "+  return pay.paymentIntents.create(") {
		t.Errorf("the migration was lost:\n%s", d)
	}
	for _, want := range []string{"- debug: 1 line dropped\n", "- comments: 1 line dropped\n"} {
		if !strings.Contains(pr.Body, want) {
			t.Errorf("body lacks %q:\n%s", want, pr.Body)
		}
	}
}

// S2. An advisory against the version in use: the smallest fixed release wins
// over the newest one, and the cooldown does not hold a security fix back.
func TestGoSecurity(t *testing.T) {
	need(t, "go")
	w := setup(t, "go-vuln")
	w.golden("check.json", w.tend("check", "--json").want(t, 0).stdout)
	w.tend("check", "--fail-on", "security").want(t, 3)
	w.tend("check", "--fail-on", "deprecation").want(t, 0)

	w.tend("run").want(t, 0)
	pr := onlyPR(t, w)
	w.goldenPR(pr)
	if pr.Branch != "tend/go-example.com-fixture-crypto-v1.0.1" {
		t.Errorf("branch %q", pr.Branch)
	}
	cs := w.commits(pr.Branch)
	if len(cs) != 1 || cs[0].subject != "Bump example.com/fixture/crypto to v1.0.1 (fixes GO-2026-0001)" || !slices.Equal(cs[0].files, []string{"go.mod", "go.sum"}) {
		t.Errorf("commits = %+v", cs)
	}
}

// S2 with minor updates opted in: both changes are queued, security first,
// and still only one PR goes out.
func TestGoMinorQueuedSecond(t *testing.T) {
	need(t, "go")
	w := setup(t, "go-vuln")
	w.commit("Take minor updates", map[string]string{"tend.yml": "updates: {minor: true}\n"})
	var out struct{ Queue []core.Change }
	if err := json.Unmarshal([]byte(w.tend("check", "--json").want(t, 0).stdout), &out); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, ch := range out.Queue {
		ids = append(ids, string(ch.Kind)+" "+ch.ID)
	}
	if want := []string{"security go/example.com/fixture/crypto@v1.0.1", "update go/example.com/fixture/crypto@v1.1.0"}; !slices.Equal(ids, want) {
		t.Errorf("queue = %q", ids)
	}
	w.tend("run").want(t, 0)
	if pr := onlyPR(t, w); !strings.Contains(pr.Title, "v1.0.1") {
		t.Errorf("opened %q", pr.Title)
	}
}

// S2 on a base whose tests already fail: the fix still goes out, as a draft
// that says so.
func TestGoBaselineFails(t *testing.T) {
	need(t, "go")
	w := setup(t, "go-vuln")
	w.commit("Pin the signature format", map[string]string{"format_test.go": `package main

import "testing"

func TestFormat(t *testing.T) {
	if got := len(sign("hello", "k1")); got != 64 {
		t.Fatalf("signature is %d hex digits, want 64", got)
	}
}
`})
	w.tend("run").want(t, 0)
	pr := onlyPR(t, w)
	if !pr.Draft {
		t.Error("not a draft")
	}
	for _, want := range []string{"Tests already fail on `main@BASE`", "| `go vet ./... && go test ./...` | failed (exit 1)"} {
		if !strings.Contains(w.stable(pr.Body), want) {
			t.Errorf("body lacks %q:\n%s", want, pr.Body)
		}
	}
}

// S3. An endpoint the code calls is deprecated with a sunset; later a field
// it reads is removed from the spec. One PR at a time, a human's close parks
// the first, and the model handles the second.
func TestAPIDeprecationThenRemoval(t *testing.T) {
	need(t, "python3")
	w := setup(t, "py-api")
	w.fetch.spec = filepath.Join(testdata, "recorded", "e2e", "fixture-api", "v1.json")
	w.golden("check-v1.json", w.tend("check", "--json").want(t, 0).stdout)

	w.tend("run").want(t, 0)
	pr := onlyPR(t, w)
	if pr.Title != "Migrate off deprecated GET /v1/orders/{id}/legacy" {
		t.Errorf("title %q", pr.Title)
	}
	w.golden("decision-body.md", pr.Body)
	if d := w.diff(pr.Branch); d != "" {
		t.Errorf("the decision changed files:\n%s", d)
	}
	if st := w.state(); st.Specs["fixture"].File == "" {
		t.Errorf("no snapshot of the spec was kept: %+v", st.Specs)
	}

	w.fetch.spec = filepath.Join(testdata, "recorded", "e2e", "fixture-api", "v2.json")
	w.golden("check-v2.json", w.tend("check", "--json").want(t, 0).stdout)
	w.golden("check-v2.txt", w.tend("check").want(t, 0).stdout)
	if res := w.tend("run").want(t, 0); !strings.Contains(res.stdout, "waiting on PR #1") || len(w.forge.All()) != 1 {
		t.Fatalf("a breaking change displaced an open deprecation: %q", res.stdout)
	}

	w.closeByHuman(1)
	w.model = script(t, "py-api")
	res := w.tend("run").want(t, 0)
	if !strings.HasPrefix(res.stdout, "tend: opened #2 Handle removed field total_cents on GET /v1/orders ") {
		t.Fatalf("run said %q", res.stdout)
	}
	if p := w.state().Parked["api/fixture/GET /v1/orders/{id}/legacy"]; p.Reason != "rejected" || p.PR != 1 {
		t.Errorf("parked = %+v", p)
	}
	pr = *w.forge.PR(2)
	w.goldenPR(pr)
	if cs := w.commits(pr.Branch); len(cs) != 1 || !slices.Equal(cs[0].files, []string{"app/orders.py"}) {
		t.Errorf("commits = %+v", cs)
	}
	all := w.tend("check", "--all").want(t, 0).stdout
	if !strings.Contains(all, "parked") || !strings.Contains(all, "GET /v1/orders/{id}/legacy") {
		t.Errorf("check --all does not show the parked deprecation:\n%s", all)
	}
}

// S4. Security work preempts an open update, which comes back once the fix
// merges; a human's close parks it until a newer release.
func TestPreemptAndPark(t *testing.T) {
	need(t, "go")
	w := setup(t, "go-vuln")
	w.commit("Take minor updates", map[string]string{"tend.yml": "updates: {minor: true}\n"})
	w.fetch.osv.ids = nil
	if res := w.tend("run").want(t, 0); !strings.HasPrefix(res.stdout, "tend: opened #1 Bump example.com/fixture/crypto to v1.1.0 ") {
		t.Fatalf("run said %q", res.stdout)
	}
	first := w.forge.PR(1).Branch

	// The update closes before the fix opens; when the fix cannot open, the
	// update still comes back after it.
	w.fetch.osv.ids = []string{"GO-2026-0001"}
	w.forge.CreateErr = errors.New("server error")
	w.tend("run").want(t, 1)
	if w.forge.PR(1).State != "closed" || len(w.forge.All()) != 1 {
		t.Fatalf("after a failed open: #1 %s, %d PRs", w.forge.PR(1).State, len(w.forge.All()))
	}
	if p := w.state().Parked["go/example.com/fixture/crypto@v1.1.0"]; p.Reason != "preempted" || p.PR != 1 {
		t.Errorf("parked = %+v", p)
	}
	if res := w.tend("run").want(t, 0); !strings.HasPrefix(res.stdout, "tend: opened #2 Bump example.com/fixture/crypto to v1.0.1 (fixes GO-2026-0001) ") {
		t.Fatalf("run said %q", res.stdout)
	}
	if w.forge.PR(2).State != "open" {
		t.Fatalf("#2 is %s", w.forge.PR(2).State)
	}
	cs, _ := w.forge.Comments(t.Context(), 1)
	if len(cs) != 1 || !strings.HasPrefix(cs[0].Body, "Closing to make room for a security fix. tend will reopen this after that merges.") {
		t.Errorf("comments on #1: %+v", cs)
	}
	if p := w.state().Parked["go/example.com/fixture/crypto@v1.1.0"]; p.Reason != "preempted" || p.PR != 1 {
		t.Errorf("parked = %+v", p)
	}

	w.merge(2)
	if res := w.tend("run").want(t, 0); !strings.HasPrefix(res.stdout, "tend: opened #3 Bump example.com/fixture/crypto to v1.1.0 ") {
		t.Fatalf("after the fix merged: %q", res.stdout)
	}
	if pr := w.forge.PR(3); pr.Branch != first {
		t.Errorf("#3 is on %s, #1 was on %s", pr.Branch, first)
	}
	if _, ok := w.state().Parked["go/example.com/fixture/crypto@v1.1.0"]; ok {
		t.Error("the preempted change is still parked")
	}

	w.closeByHuman(3)
	if res := w.tend("run").want(t, 0); res.stdout != "tend: up to date\n" {
		t.Errorf("after a human closed #3: %q", res.stdout)
	}
	if p := w.state().Parked["go/example.com/fixture/crypto@v1.1.0"]; p.Reason != "rejected" || p.PR != 3 {
		t.Errorf("parked = %+v", p)
	}
	if all := w.tend("check", "--all").want(t, 0).stdout; !strings.Contains(all, "parked") || !strings.Contains(all, "#3 was closed without merging") {
		t.Errorf("check --all:\n%s", all)
	}
	// The forge remembers the maintainer's no when state does not.
	if err := os.Remove(w.statePath()); err != nil {
		t.Fatal(err)
	}
	if res := w.tend("run").want(t, 0); res.stdout != "tend: up to date\n" || len(w.forge.All()) != 3 {
		t.Errorf("without state: %q, %d PRs", res.stdout, len(w.forge.All()))
	}

	w.publish("v1.2.0")
	if res := w.tend("run").want(t, 0); !strings.HasPrefix(res.stdout, "tend: opened #4 Bump example.com/fixture/crypto to v1.2.0 ") {
		t.Errorf("a newer release did not come back: %q", res.stdout)
	}
}

// S5. Plain bumps are opt-in, ignore rules hold, and the cooldown waits out
// young releases but not a deprecation.
func TestOptInBumps(t *testing.T) {
	need(t, "node", "npm")
	w := setup(t, "node-sdk")
	w.commit("Take patch updates, stay on pay 1.x", map[string]string{"tend.yml": `updates: {patch: true}
ignore:
  - name: "@fixture/pay"
    reason: moving to 2.x after the audit
`})
	w.tend("run").want(t, 0)
	pr := onlyPR(t, w)
	if pr.Title != "Bump @fixture/util to 1.0.1" {
		t.Errorf("title %q", pr.Title)
	}
	if cs := w.commits(pr.Branch); len(cs) != 1 || !slices.Equal(cs[0].files, []string{"package-lock.json"}) {
		t.Errorf("commits = %+v", cs)
	}
	if want := "`^1.0.0` in `package.json` already allows 1.0.1, so only `package-lock.json` changed."; !strings.Contains(pr.Body, want) {
		t.Errorf("body does not explain the lockfile-only bump:\n%s", pr.Body)
	}

	w = setup(t, "node-sdk")
	w.commit("Ignore our own packages", map[string]string{"tend.yml": "ignore:\n  - name: \"@fixture/*\"\n"})
	res := w.tend("check").want(t, 0)
	if !strings.HasPrefix(res.stdout, "tend: 0 queued, 2 skipped") || !strings.Contains(res.stdout, "skipped: 2 ignored") {
		t.Errorf("check:\n%s", res.stdout)
	}

	w = setup(t, "node-sdk")
	w.commit("Wait two months for releases", map[string]string{"tend.yml": "cooldown: 60d\nupdates: {patch: true}\n"})
	var out struct {
		Queue   []core.Change
		Dropped []struct {
			Change core.Change
			Reason string
		}
	}
	if err := json.Unmarshal([]byte(w.tend("check", "--json").want(t, 0).stdout), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Queue) != 1 || out.Queue[0].ID != "npm/@fixture/pay@2.0.0" {
		t.Errorf("queue = %+v", out.Queue)
	}
	if len(out.Dropped) != 1 || out.Dropped[0].Change.ID != "npm/@fixture/util@1.0.1" || out.Dropped[0].Reason != "cooldown" {
		t.Errorf("dropped = %+v", out.Dropped)
	}
}

// S6. Base images past end of life and an Action on a removed Node runtime:
// one PR at a time, most urgent first.
func TestCIImages(t *testing.T) {
	w := setup(t, "ci-images")
	w.golden("check.json", w.tend("check", "--json").want(t, 0).stdout)

	w.tend("run").want(t, 0)
	pr := onlyPR(t, w)
	w.goldenPR(pr)
	if !strings.Contains(pr.Body, "No build or test command was found") {
		t.Errorf("the proof does not say nothing ran:\n%s", pr.Body)
	}
	if res := w.tend("run").want(t, 0); !strings.Contains(res.stdout, "waiting on PR #1") || len(w.forge.All()) != 1 {
		t.Errorf("second run: %q, %d PRs", res.stdout, len(w.forge.All()))
	}

	w.merge(1)
	w.tend("run").want(t, 0)
	if pr := w.forge.PR(2); !strings.HasPrefix(pr.Title, "Bump postgres base image to ") {
		t.Errorf("second PR: %q", pr.Title)
	}

	w.merge(2)
	w.tend("run").want(t, 0)
	pr = *w.forge.PR(3)
	w.golden("checkout-title.txt", pr.Title+"\n")
	w.golden("checkout-body.md", pr.Body)
	w.golden("checkout-diff.patch", w.diff(pr.Branch)+"\n")
	if !strings.Contains(pr.Body, "- scope: 1 file, explained by this change\n") {
		t.Errorf("body:\n%s", pr.Body)
	}
}

// S7. Zero config, no token, no model: a dry run shows what it would open and
// keeps the worktree, and nothing reaches the forge or the remote.
func TestDryRun(t *testing.T) {
	need(t, "node", "npm")
	w := setup(t, "node-sdk")
	w.noForge = true
	delete(w.getenv, "GITHUB_REPOSITORY")
	res := w.tend("run", "--dry-run").want(t, 0)
	if !strings.HasPrefix(res.stdout, "Migrate off deprecated @fixture/pay 1.x\n\n## @fixture/pay 1.4.0 → 2.0.0\n") {
		t.Errorf("dry run printed:\n%s", res.stdout)
	}
	wt := strings.TrimSuffix(res.stdout[strings.LastIndex(res.stdout, " ")+1:], "\n")
	if _, err := os.Stat(filepath.Join(wt, "src", "billing.js")); err != nil {
		t.Errorf("worktree not kept: %v", err)
	}
	if len(w.forge.All()) != 0 {
		t.Error("a dry run opened a PR")
	}
	if got := w.git(w.origin, "branch", "--list"); got != "* main" {
		t.Errorf("a dry run pushed: %q", got)
	}
	if st := w.state(); st.Open != nil {
		t.Errorf("a dry run recorded an open PR: %+v", st.Open)
	}

	var out struct {
		Action string
		Change struct{ ID string }
		Proof  struct{ Patched struct{ Pass bool } }
	}
	w.model = script(t, "node-sdk")
	res = w.tend("run", "--dry-run", "--json").want(t, 0)
	if err := json.Unmarshal([]byte(res.stdout), &out); err != nil {
		t.Fatalf("%v\n%s", err, res.stdout)
	}
	if out.Action != "dry-run" || out.Change.ID != "npm/@fixture/pay@2.0.0" || !out.Proof.Patched.Pass {
		t.Errorf("json = %+v", out)
	}
}

// S7. tend init writes the commented defaults, and with --ci the workflow
// alone, then offers to run: yes, or --now, runs tend at once.
func TestInit(t *testing.T) {
	w := setup(t, "empty")
	if res := w.tend("init").want(t, 0); strings.Contains(res.stdout, "nothing to watch") {
		t.Errorf("init ran tend with nobody saying yes:\n%s", res.stdout)
	}
	w.golden("tend.yml", string(must(os.ReadFile(filepath.Join(w.dir, "tend.yml")))))
	if res := w.tend("init").want(t, 1); !strings.Contains(res.stderr, "--force") {
		t.Errorf("init over an existing file: %q", res.stderr)
	}
	w.tend("init", "--ci").want(t, 0)
	w.golden("workflow.yml", string(must(os.ReadFile(filepath.Join(w.dir, ".github", "workflows", "tend.yml")))))

	w.stdin = "y\n"
	if res := w.tend("init", "--force").want(t, 0); !strings.HasSuffix(res.stdout, "[y/N] tend: nothing to watch: no manifests or API calls found in "+w.real()+"\n") {
		t.Errorf("init answered yes said:\n%s", res.stdout)
	}
	w.stdin = ""
	if res := w.tend("init", "--ci", "--force", "--now").want(t, 0); !strings.Contains(res.stdout, "wrote .github/workflows/tend.yml\ntend: nothing to watch") {
		t.Errorf("init --now said:\n%s", res.stdout)
	}
}

// §27: a runtime pinned in a version file is followed like a package; past
// its end of life it moves to the newest LTS, spelled the same way.
func TestToolchainEndOfLife(t *testing.T) {
	w := setup(t, "toolchain")
	w.golden("check.txt", w.tend("check").want(t, 0).stdout)
	w.tend("run").want(t, 0)
	w.goldenPR(onlyPR(t, w))
}

// §27: a go line on a Go release past its end of life moves to the oldest
// release still maintained, and nothing else in go.mod changes.
func TestRuntimeMinimum(t *testing.T) {
	need(t, "go")
	w := setup(t, "go-minimum")
	w.golden("check.txt", w.tend("check").want(t, 0).stdout)
	w.tend("run").want(t, 0)
	pr := onlyPR(t, w)
	w.goldenPR(pr)
	if !strings.Contains(pr.Body, "| `go build ./...` | ok") {
		t.Errorf("the proof did not build the module:\n%s", pr.Body)
	}
}

// §27: a version in a script, which no ecosystem reads, followed through a
// deps entry in tend.yml and its GitHub releases.
func TestPinnedInScript(t *testing.T) {
	w := setup(t, "pinned")
	w.golden("check.txt", w.tend("check").want(t, 0).stdout)
	w.tend("run").want(t, 0)
	w.goldenPR(onlyPR(t, w))
}

// §27: hosts the code calls that no catalog entry covers. One has a spec in
// the APIs.guru directory that deprecates the endpoint the code calls, which
// becomes a decision PR; the other has no spec anywhere, which check lists
// as untracked with the tend.yml line that would fix it, and gets no PR.
func TestUnknownAPIs(t *testing.T) {
	w := setup(t, "unknown-apis")
	w.golden("check.txt", w.tend("check").want(t, 0).stdout)
	w.tend("run").want(t, 0)
	pr := onlyPR(t, w)
	w.golden("title.txt", pr.Title+"\n")
	w.golden("body.md", pr.Body)
	if found := w.state().Found["api.example-unknown.com"]; found.Spec == "" {
		t.Errorf("the discovered spec was not kept: %+v", w.state().Found)
	}
}

// §27.4: the base commit's test run prints a DeprecationWarning from a
// library whose registry says nothing about it. The warning becomes a change
// to the call it points at, and a model that calls tools moves the call.
func TestRuntimeWarning(t *testing.T) {
	need(t, "python3")
	w := setup(t, "py-warnings")
	if res := w.tend("check").want(t, 0); !strings.HasPrefix(res.stdout, "tend: 0 queued") {
		t.Fatalf("a check that ran nothing knows of warnings:\n%s", res.stdout)
	}
	call := func(id, name string, args map[string]any) model.ToolCall {
		return model.ToolCall{ID: id, Name: name, Args: must(json.Marshal(args))}
	}
	m := &model.Steps{Replies: []model.Reply{
		{Calls: []model.ToolCall{call("1", "read", map[string]any{"path": "app/report.py"})}},
		{Calls: []model.ToolCall{call("2", "edit", map[string]any{"path": "app/report.py", "old": "rows = fixturelib.fetch_all()", "new": "rows = fixturelib.iter_rows()"})}},
		{Calls: []model.ToolCall{call("3", "run", map[string]any{"name": "test"})}},
		{Calls: []model.ToolCall{call("4", "done", map[string]any{"summary": "iterate instead of fetching everything"})}},
	}}
	for range 12 {
		m.Script.Replies = append(m.Script.Replies, "NONE")
	}
	w.model = m
	res := w.tend("run").want(t, 0)
	if !strings.HasPrefix(res.stdout, "tend: opened #1 Fix deprecation warnings from fixturelib ") {
		t.Fatalf("run said %q\n%s", res.stdout, res.stderr)
	}
	pr := onlyPR(t, w)
	w.goldenPR(pr)
	if cs := w.commits(pr.Branch); len(cs) != 1 || !slices.Equal(cs[0].files, []string{"app/report.py"}) {
		t.Errorf("commits = %+v", cs)
	}
}

// `tend skip` from someone with write access closes the PR for good; the same
// words from anyone else do nothing.
func TestCommentSkip(t *testing.T) {
	need(t, "node", "npm")
	w := setup(t, "node-sdk")
	w.model = script(t, "node-sdk")
	w.tend("run").want(t, 0)

	w.forge.Now = func() time.Time { return now.Add(time.Hour) }
	w.say(1, "drive-by", "CONTRIBUTOR", "tend skip")
	if res := w.tend("run").want(t, 0); !strings.Contains(res.stdout, "waiting on PR #1") {
		t.Fatalf("a contributor's skip was honored: %q", res.stdout)
	}

	w.say(1, "maintainer", "MEMBER", "tend skip")
	if res := w.tend("run").want(t, 0); res.stdout != "tend: up to date\n" {
		t.Errorf("after skip: %q", res.stdout)
	}
	if w.forge.PR(1).State != "closed" {
		t.Errorf("#1 is %s", w.forge.PR(1).State)
	}
	all := w.tend("check", "--all").want(t, 0).stdout
	if !strings.Contains(all, "parked") || !strings.Contains(all, "@fixture/pay") {
		t.Errorf("check --all does not show the parked change:\n%s", all)
	}
	if len(w.forge.All()) != 1 {
		t.Errorf("tend reopened a skipped change")
	}
}

// `tend retry` redoes the work even when nothing moved.
func TestCommentRetry(t *testing.T) {
	need(t, "node", "npm")
	w := setup(t, "node-sdk")
	w.model = script(t, "node-sdk")
	w.tend("run").want(t, 0)

	w.forge.Now = func() time.Time { return now.Add(time.Hour) }
	w.say(1, "maintainer", "OWNER", "tend retry")
	w.model = script(t, "node-sdk")
	if res := w.tend("run").want(t, 0); !strings.HasPrefix(res.stdout, "tend: updated #1 ") {
		t.Errorf("retry: %q", res.stdout)
	}
}

// A PR a human closed, branch and all, parks its change; tend does not
// propose it again at the same version.
func TestClosedByHuman(t *testing.T) {
	need(t, "node", "npm")
	w := setup(t, "node-sdk")
	w.model = script(t, "node-sdk")
	w.tend("run").want(t, 0)
	w.closeByHuman(1)

	if res := w.tend("run").want(t, 0); res.stdout != "tend: up to date\n" {
		t.Errorf("after close: %q", res.stdout)
	}
	if p := w.state().Parked["npm/@fixture/pay@2.0.0"]; p.Reason != "rejected" || p.PR != 1 {
		t.Errorf("parked = %+v", p)
	}
	if len(w.forge.All()) != 1 {
		t.Errorf("tend opened another PR")
	}
}

// Two open tend PRs (a crash, or someone reopened one): the newest stays.
func TestTwoOpenPRs(t *testing.T) {
	need(t, "node", "npm")
	w := setup(t, "node-sdk")
	for _, p := range [][2]string{{"tend/npm-old-1.0.0", "npm/old@1.0.0"}, {"tend/npm-fixture-pay-2.0.0", "npm/@fixture/pay@2.0.0"}} {
		b, id := p[0], p[1]
		w.git(w.dir, "push", "--quiet", "origin", "main:refs/heads/"+b)
		body := fmt.Sprintf("<!-- tend id=%q base=\"0\" rev=\"1\" -->", id)
		if _, err := w.forge.CreatePR(t.Context(), core.PRInput{Title: b, Branch: b, Base: "main", Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	w.forge.Peak = 1
	w.model = script(t, "node-sdk")
	res := w.tend("run").want(t, 0)
	if w.forge.PR(1).State != "closed" || w.forge.PR(2).State != "open" || len(w.forge.All()) != 2 {
		t.Errorf("PRs: %+v", w.forge.All())
	}
	cs, _ := w.forge.Comments(t.Context(), 1)
	if len(cs) != 1 || !strings.Contains(cs[0].Body, "one pull request open at a time") {
		t.Errorf("comments on #1: %+v", cs)
	}
	if !strings.HasPrefix(res.stdout, "tend: updated #2 ") {
		t.Errorf("run said %q", res.stdout)
	}
}

// Uncommitted edits and untracked files in the clone are never touched: the
// work happens in a worktree of the base commit.
func TestDirtyTree(t *testing.T) {
	need(t, "node", "npm")
	w := setup(t, "node-sdk")
	billing := filepath.Join(w.dir, "src", "billing.js")
	dirty := string(must(os.ReadFile(billing))) + "\n// half-done refactor\n"
	for p, body := range map[string]string{billing: dirty, filepath.Join(w.dir, "notes.txt"): "todo\n"} {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	before := w.git(w.dir, "status", "--porcelain")

	w.model = script(t, "node-sdk")
	w.tend("run").want(t, 0)
	if after := w.git(w.dir, "status", "--porcelain"); after != before {
		t.Errorf("status changed:\n%s\nwas\n%s", after, before)
	}
	if got := string(must(os.ReadFile(billing))); got != dirty {
		t.Error("the uncommitted edit was touched")
	}
	if d := w.diff(onlyPR(t, w).Branch); strings.Contains(d, "half-done") || strings.Contains(d, "notes.txt") {
		t.Errorf("the PR carries uncommitted work:\n%s", d)
	}
}

// A clone on a detached HEAD still works from origin's main.
func TestDetachedHEAD(t *testing.T) {
	need(t, "node", "npm")
	w := setup(t, "node-sdk")
	w.git(w.dir, "checkout", "--quiet", "--detach")
	w.model = script(t, "node-sdk")
	w.tend("run").want(t, 0)
	if pr := onlyPR(t, w); pr.Base != "main" {
		t.Errorf("base = %q", pr.Base)
	}
}

// A module below the root gets its commands run from its own directory.
func TestMonorepo(t *testing.T) {
	need(t, "go")
	w := setup(t, "empty")
	files := map[string]string{}
	src := filepath.Join(testdata, "projects", "go-vuln")
	ents, _ := os.ReadDir(src)
	for _, e := range ents {
		files["svc/"+e.Name()] = string(must(os.ReadFile(filepath.Join(src, e.Name()))))
	}
	w.commit("Add the service", files)
	w.tend("run").want(t, 0)
	pr := onlyPR(t, w)
	if !strings.Contains(pr.Body, "| `cd svc && go build ./...` | ok") {
		t.Errorf("the proof did not run in svc:\n%s", pr.Body)
	}
	if d := w.diff(pr.Branch); !strings.Contains(d, "svc/go.mod") {
		t.Errorf("diff:\n%s", d)
	}
}

func TestNoManifests(t *testing.T) {
	w := setup(t, "empty")
	if res := w.tend("check").want(t, 0); !strings.HasPrefix(res.stdout, "tend: nothing to watch: ") {
		t.Errorf("check said %q", res.stdout)
	}
	if res := w.tend("run").want(t, 0); !strings.HasPrefix(res.stdout, "tend: nothing to watch: ") {
		t.Errorf("run said %q", res.stdout)
	}
	if err := os.RemoveAll(filepath.Join(w.dir, ".git")); err != nil {
		t.Fatal(err)
	}
	w.git(w.dir, "init", "--quiet")
	if res := w.tend("run").want(t, 0); res.stdout != "tend: nothing to watch: the repository has no commits\n" {
		t.Errorf("run without commits said %q", res.stdout)
	}
}

func TestLockHeld(t *testing.T) {
	w := setup(t, "empty")
	w.tend("check").want(t, 0)
	dir := filepath.Dir(w.statePath())
	if err := os.WriteFile(filepath.Join(dir, "lock"), []byte(fmt.Sprintf("4242 %d\n", time.Now().Unix())), 0o644); err != nil {
		t.Fatal(err)
	}
	res := w.tend("run").want(t, 1)
	if !strings.Contains(res.stderr, "pid 4242") {
		t.Errorf("stderr %q does not name the holder", res.stderr)
	}
}

// Without a GitHub origin or token the run still patches and proves, then
// leaves the commit on a local branch and prints the PR it would open.
func TestNoForge(t *testing.T) {
	need(t, "node", "npm")
	w := setup(t, "node-sdk")
	w.noForge = true
	delete(w.getenv, "GITHUB_REPOSITORY")
	w.model = script(t, "node-sdk")
	res := w.tend("run").want(t, 0)
	if !strings.Contains(res.stdout, "## @fixture/pay 1.4.0 → 2.0.0") {
		t.Errorf("the body was not printed:\n%s", res.stdout)
	}
	last := res.stdout[strings.LastIndex(strings.TrimRight(res.stdout, "\n"), "\n")+1:]
	if last != "tend: branch tend/npm-fixture-pay-2.0.0 has the change; no GitHub origin remote, so no pull request was opened\n" {
		t.Errorf("last line %q", last)
	}
	if got := w.git(w.dir, "branch", "--list", "tend/*"); got != "tend/npm-fixture-pay-2.0.0" {
		t.Errorf("branches: %q", got)
	}
}

// One package the registry does not know becomes a note; one upstream
// source down becomes a warning; the plan goes on.
func TestPartialFailure(t *testing.T) {
	need(t, "node", "npm")
	w := setup(t, "node-sdk")
	pkg := string(must(os.ReadFile(filepath.Join(w.dir, "package.json"))))
	w.commit("Add a private package", map[string]string{
		"package.json": strings.Replace(pkg, `"@fixture/util": "^1.0.0"`, `"@fixture/util": "^1.0.0",
    "@fixture/gone": "^1.0.0"`, 1),
	})
	w.fetch.fail["api.osv.dev"] = fmt.Errorf("503 Service Unavailable")
	res := w.tend("check").want(t, 0)
	for _, want := range []string{"@fixture/gone", "osv: unavailable, planned without it: 503 Service Unavailable", "deprecation  @fixture/pay"} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("check lacks %q:\n%s", want, res.stdout)
		}
	}
}

// A bump that breaks the build with nothing to decide gets no PR: exit 4,
// recorded, and the queue moves on next time.
func TestUnproven(t *testing.T) {
	need(t, "go")
	w := setup(t, "go-vuln")
	w.publish("v1.3.0")
	w.commit("Take minor updates", map[string]string{"tend.yml": `updates: {minor: true}
ignore:
  - name: example.com/fixture/crypto
    versions: ["<1.3.0"]
`})
	res := w.tend("run").want(t, 4)
	if !strings.HasPrefix(res.stdout, "tend: could not prove go/example.com/fixture/crypto@v1.3.0: ") {
		t.Errorf("run said %q", res.stdout)
	}
	if len(w.forge.All()) != 0 {
		t.Error("an unproven change got a PR")
	}
	all := w.tend("check", "--all").want(t, 0).stdout
	if !strings.Contains(all, "unsupported") {
		t.Errorf("check --all:\n%s", all)
	}
	if res := w.tend("run").want(t, 0); res.stdout != "tend: up to date\n" {
		t.Errorf("second run: %q", res.stdout)
	}
	w.commit("Touch the readme", map[string]string{"README.md": "crypto\n"})
	if res := w.tend("run").want(t, 4); !strings.HasPrefix(res.stdout, "tend: could not prove go/example.com/fixture/crypto@v1.3.0: ") {
		t.Errorf("on a new base: %q", res.stdout)
	}
}

// Commit mode pushes straight to the base branch; the next run sees it there.
func TestCommitMode(t *testing.T) {
	need(t, "go")
	w := setup(t, "go-vuln")
	w.commit("Commit straight to main", map[string]string{"tend.yml": "deliver: {mode: commit}\n"})
	res := w.tend("run").want(t, 0)
	if !strings.HasPrefix(res.stdout, "tend: committed ") || len(w.forge.All()) != 0 {
		t.Fatalf("run said %q", res.stdout)
	}
	if log := w.git(w.origin, "log", "-1", "--format=%s", "main"); log != "Bump example.com/fixture/crypto to v1.0.1 (fixes GO-2026-0001)" {
		t.Errorf("main on origin: %q", log)
	}
	if st := w.state(); st.Open == nil || st.Open.PR != 0 {
		t.Errorf("open = %+v", st.Open)
	}
	w.tend("run").want(t, 0)
	if st := w.state(); st.Open != nil || st.Seen["go/example.com/fixture/crypto@v1.0.1"].Verdict != "delivered" {
		t.Errorf("after the next run: open %+v, seen %+v", st.Open, st.Seen)
	}
}

// Commit mode, unattended: tend waits while the repository's CI runs on its
// commit, reverts the commit when CI fails on it, backs off the change, and
// says so in its one tracking issue instead of trying again.
func TestCommitModeRevert(t *testing.T) {
	need(t, "go")
	w := setup(t, "go-vuln")
	w.commit("Commit straight to main", map[string]string{"tend.yml": "deliver: {mode: commit}\n"})
	w.tend("run").want(t, 0)
	sha := w.git(w.origin, "rev-parse", "main")

	w.forge.CI[sha] = core.CI{State: "pending"}
	if res := w.tend("run").want(t, 0); !strings.HasPrefix(res.stdout, "tend: waiting on CI for "+core.Short(sha)+", which tend pushed to main") {
		t.Fatalf("while CI runs: %q\n%s", res.stdout, res.stderr)
	}
	w.forge.CI[sha] = core.CI{State: "failure", Failed: []string{"test (ubuntu-latest)"}}
	res := w.tend("run").want(t, 0)
	if !strings.HasPrefix(res.stdout, "tend: reverted "+core.Short(sha)+" on main: CI failed (test (ubuntu-latest))") {
		t.Fatalf("after CI failed: %q\n%s", res.stdout, res.stderr)
	}
	msg := w.git(w.origin, "log", "-1", "--format=%B", "main")
	for _, want := range []string{
		`Revert "Bump example.com/fixture/crypto to v1.0.1 (fixes GO-2026-0001)"`,
		"CI failed on " + core.Short(sha) + ": test (ubuntu-latest).",
		"will not try go/example.com/fixture/crypto@v1.0.1 again",
		"This reverts commit " + sha + ".",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the revert says %q, want %q", msg, want)
		}
	}
	if d := w.git(w.origin, "diff", sha+"^", "main"); d != "" {
		t.Errorf("main after the revert is not the tree before tend's commit:\n%s", d)
	}
	if p := w.state().Parked["go/example.com/fixture/crypto@v1.0.1"]; p.Reason != "reverted" {
		t.Errorf("parked = %+v", p)
	}

	if res := w.tend("run").want(t, 0); res.stdout != "tend: up to date\n" {
		t.Errorf("after the revert: %q\n%s", res.stdout, res.stderr)
	}
	is := w.forge.Issues()
	if len(is) != 1 || !is[0].Open || !strings.Contains(is[0].Body, "- `go/example.com/fixture/crypto@v1.0.1`: reverted in ") || !strings.Contains(is[0].Body, "CI failed on "+core.Short(sha)) {
		t.Fatalf("issues = %+v", is)
	}
	w.tend("run").want(t, 0)
	if n := len(w.forge.Issues()); n != 1 || w.forge.Issues()[0].Body != is[0].Body {
		t.Errorf("a run with nothing new touched the issue: %+v", w.forge.Issues())
	}
	if n := strings.Count(w.git(w.origin, "log", "--format=%s", "main")+"\n", "\nBump example.com/fixture/crypto"); n != 1 {
		t.Errorf("the change was pushed %d times", n)
	}
}

// real is the clone's path as git reports it, symlinks resolved.
func (w *world) real() string {
	w.t.Helper()
	return must(filepath.EvalSymlinks(w.dir))
}

func (w *world) statePath() string {
	w.t.Helper()
	ents, err := os.ReadDir(filepath.Join(w.home, "repos"))
	if err != nil || len(ents) != 1 {
		w.t.Fatalf("state dirs: %v, %v", ents, err)
	}
	return filepath.Join(w.home, "repos", ents[0].Name(), "state.json")
}
