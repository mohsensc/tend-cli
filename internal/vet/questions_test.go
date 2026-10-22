package vet

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
)

func TestParseAnswers(t *testing.T) {
	tests := []struct {
		name, reply string
		want        []string
	}{
		{"none", "NONE", nil},
		{"numbered with notes", "1. NONE\n2. src/a.js:12: the old call passed capture: false\n- `src/a.js:13`: drops the idempotency key\n3. NONE\n",
			[]string{"flagged src/a.js:12 the old call passed capture: false", "flagged src/a.js:13 drops the idempotency key"}},
		{"plain list", "app/orders.py:4: total is now an object", []string{"flagged app/orders.py:4 total is now an object"}},
		{"garbage", "Looks fine to me. Nothing changed in a meaningful way.", nil},
		{"line zero", "src/a.js:0: nope", nil},
		{"no line", "src/a.js:abc: nope", nil},
		{"no text", "src/a.js:3:", nil},
		{"says nothing changed", "1. src/billing.js:12: argument object unchanged\n2. src/billing.js:12: The arguments did not change.\n3. src/billing.js:13: same as before", nil},
		{"mixed", "src/a.js:4: options unchanged\nsrc/a.js:5: amount is now in cents\nsrc/a.js:6: the options object is not compatible with the new call",
			[]string{"flagged src/a.js:5 amount is now in cents", "flagged src/a.js:6 the options object is not compatible with the new call"}},
		// Replies to the old, leading questions about a correct rename.
		{"hedged", "src/billing.js:20: paymentIntents.retrieve may return an empty or missing response that is no longer checked  \n" +
			"src/billing.js:20:  pay.paymentIntents.retrieve might return an empty/undefined response that isn’t handled\nNONE", nil},
		{"says it is fine", "src/billing.js:12: none\nsrc/billing.js:12: looks fine\nsrc/billing.js:20: OK, same call on the new resource\nsrc/billing.js:20: compatible with the old call", nil},
		{"lowercase none", "none", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eq(t, parseAnswers(tt.reply), tt.want...)
		})
	}
}

// quiz answers by the file a prompt is about and keeps how many calls ran at once.
type quiz struct {
	replies map[string]string
	err     error
	delay   time.Duration

	mu      sync.Mutex
	prompts []string
	busy    int
	peak    int
}

func (q *quiz) Name() string { return "quiz" }

func (q *quiz) Complete(ctx context.Context, system, prompt string) (string, error) {
	q.mu.Lock()
	q.prompts = append(q.prompts, prompt)
	q.busy++
	q.peak = max(q.peak, q.busy)
	q.mu.Unlock()
	time.Sleep(q.delay)
	q.mu.Lock()
	defer q.mu.Unlock()
	q.busy--
	if q.err != nil {
		return "", q.err
	}
	file, _, _ := strings.Cut(strings.TrimPrefix(prompt, "File: "), "\n")
	return q.replies[file], nil
}

func TestQuestions(t *testing.T) {
	a := "const pay = require('pay')\n\nfunction run() {\n  return pay.charges.create({capture: false})\n}\n" + strings.Repeat("\n", 30) + "function list() {\n  return pay.charges.list()\n}\n"
	base := map[string]string{
		"src/a.js": a,
		"src/b.js": "const pay = require('pay')\npay.charges.create()\n",
	}
	patched := map[string]string{
		"src/a.js": strings.NewReplacer("charges.create({capture: false})", "paymentIntents.create({})", "charges.list()", "paymentIntents.list()").Replace(a),
		"src/b.js": "const pay = require('pay')\npay.paymentIntents.create()\n",
	}
	dir, sha, diff := worktree(t, base, patched)
	patch := &core.Patch{Diff: diff, Model: true, Edited: []string{"src/a.js", "src/b.js"}}
	in := Input{Dir: dir, Base: sha, Change: &core.Change{}, Patch: patch}

	m := &quiz{replies: map[string]string{
		"src/a.js": "1. src/a.js:4: capture: false became nothing; the new call needs capture_method: 'manual'\n2. src/a.js:36: list no longer pages\n3. src/a.js:20: between the hunks\n4. src/b.js:2: another file",
		// Echoes of the line it points at, as a live model sent for a correct rename.
		"src/b.js": "src/b.js:2: pay.paymentIntents.create()\nsrc/b.js:2: `pay.paymentIntents.create()`\nsrc/b.js:2: paymentIntents.create",
	}}
	in.Model = m
	p := newPass(in)
	got, skip := questions(context.Background(), p)
	if skip != "" {
		t.Fatalf("skipped: %s", skip)
	}
	eq(t, got,
		"flagged src/a.js:4 capture: false became nothing; the new call needs capture_method: 'manual'",
		"flagged src/a.js:36 list no longer pages",
	)
	if len(m.prompts) != 2 || p.note != "" {
		t.Fatalf("calls = %d, note = %q", len(m.prompts), p.note)
	}
	slices.Sort(m.prompts)
	prompt := m.prompts[0]
	for _, want := range append([]string{"File: src/a.js", "Hunk 1:", "Hunk 2:", "-  return pay.charges.create({capture: false})", "4:   return pay.paymentIntents.create({})", "lines 1-17", "lines 24-39"}, asked...) {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt misses %q:\n%s", want, prompt)
		}
	}
	for _, miss := range []string{"Upstream notes", "Already checked", "New declarations"} {
		if strings.Contains(prompt, miss) {
			t.Errorf("prompt has %q with nothing to show:\n%s", miss, prompt)
		}
	}

	// What upstream said, what earlier checks verified and the new
	// declarations go in the prompt; overlapping context is shown once.
	in.Change = &core.Change{Kind: core.First, Batch: &core.Batch{Changes: []core.Change{{Evidence: []core.Evidence{{Title: "pay 1.4.0 is deprecated", Text: "charges.* was replaced by paymentIntents.*"}}}}}}
	in.Proof = &core.Proof{Patched: &core.Run{Pass: true}}
	m = &quiz{}
	in.Model = m
	p = newPass(in)
	p.known = []string{"2/2 changed calls resolve in pay@2.0.0 typings"}
	p.decls = map[string][]string{"src/b.js": {"`paymentIntents.create` in pay@2.0.0: create(params: object): Promise<object>"}}
	if _, skip := questions(context.Background(), p); skip != "" {
		t.Fatalf("skipped: %s", skip)
	}
	slices.Sort(m.prompts)
	for _, want := range []string{
		"Upstream notes:\n```\npay 1.4.0 is deprecated\ncharges.* was replaced by paymentIntents.*\n```",
		"Already checked, do not report:\n- 2/2 changed calls resolve in pay@2.0.0 typings\n- the project's build and tests pass on the patched code\n",
		"New declarations of the calls the patch wrote:\n- `paymentIntents.create` in pay@2.0.0: create(params: object): Promise<object>\n",
	} {
		if !strings.Contains(m.prompts[1], want) {
			t.Errorf("prompt misses %q:\n%s", want, m.prompts[1])
		}
	}
	if strings.Contains(m.prompts[0], "New declarations") {
		t.Errorf("declarations of src/b.js shown for src/a.js:\n%s", m.prompts[0])
	}
	in.Change, in.Proof = &core.Change{}, nil

	maxQuestions = 1
	m = &quiz{replies: m.replies}
	in.Model = m
	p = newPass(in)
	if _, skip := questions(context.Background(), p); skip != "" {
		t.Fatalf("skipped: %s", skip)
	}
	if len(m.prompts) != 1 || p.note != "asked about 2 of 3 hunks" {
		t.Errorf("calls = %d, note = %q", len(m.prompts), p.note)
	}
	maxQuestions = 8

	in.Model = &quiz{err: errors.New("boom")}
	if _, skip := questions(context.Background(), newPass(in)); skip != "model failed: boom" {
		t.Errorf("skip = %q", skip)
	}
	in.Model = nil
	if _, skip := questions(context.Background(), newPass(in)); skip != "no model configured" {
		t.Errorf("skip = %q", skip)
	}
	in.Model, in.Patch = m, &core.Patch{Diff: diff}
	if _, skip := questions(context.Background(), newPass(in)); skip != na {
		t.Errorf("skip = %q", skip)
	}
}

func TestQuestionsBatches(t *testing.T) {
	base, patched := map[string]string{}, map[string]string{}
	var long, moved []string
	for i := range 2 * batchHunks * 10 {
		long = append(long, fmt.Sprintf("old(%d)", i))
		moved = append(moved, fmt.Sprintf("old(%d)", i))
		if i%10 == 0 {
			moved[i] = fmt.Sprintf("now(%d)", i)
		}
	}
	base["a.js"] = strings.Join(long, "\n") + "\n"
	patched["a.js"] = strings.Join(moved, "\n") + "\n"
	edited := []string{"a.js"}
	for i := range 2 * parallel {
		name := fmt.Sprintf("f%d.js", i)
		base[name], patched[name] = "old()\n", "now()\n"
		edited = append(edited, name)
	}
	dir, sha, diff := worktree(t, base, patched)
	m := &quiz{delay: 20 * time.Millisecond}
	maxQuestions = 100
	in := Input{Dir: dir, Base: sha, Change: &core.Change{}, Model: m,
		Patch: &core.Patch{Diff: diff, Model: true, Edited: edited}}
	if _, skip := questions(context.Background(), newPass(in)); skip != "" {
		t.Fatalf("skipped: %s", skip)
	}
	// a.js has 2*batchHunks hunks: two calls; each other file one.
	if want := 2 + 2*parallel; len(m.prompts) != want {
		t.Errorf("calls = %d, want %d", len(m.prompts), want)
	}
	if m.peak > parallel || m.peak < 2 {
		t.Errorf("%d calls ran at once, want 2 to %d", m.peak, parallel)
	}

	m = &quiz{}
	in.Model, maxQuestions = m, 8
	p := newPass(in)
	if _, skip := questions(context.Background(), p); skip != "" {
		t.Fatalf("skipped: %s", skip)
	}
	if len(m.prompts) != maxQuestions {
		t.Errorf("calls = %d, want the cap %d", len(m.prompts), maxQuestions)
	}
	if want := fmt.Sprintf("asked about %d of %d hunks", 2*batchHunks+maxQuestions-2, 2*batchHunks+2*parallel); p.note != want {
		t.Errorf("note = %q, want %q", p.note, want)
	}
}

func TestQuestionPromptMergesContext(t *testing.T) {
	src := "const pay = require('pay')\n\nfunction a() {\n  return pay.charges.create({})\n}\n\nfunction b(id) {\n  return pay.charges.retrieve(id)\n}\n"
	dir, sha, diff := worktree(t, map[string]string{"src/a.js": src}, map[string]string{"src/a.js": strings.ReplaceAll(src, "charges", "paymentIntents")})
	in := Input{Dir: dir, Base: sha, Change: &core.Change{}, Patch: &core.Patch{Diff: diff, Model: true, Edited: []string{"src/a.js"}}}
	p := newPass(in)
	b := batches(p)[0]
	prompt, shown := questionPrompt(p, b)
	if n := strings.Count(prompt, "Context after the patch"); n != 1 || !strings.Contains(prompt, "lines 1-10:") {
		t.Errorf("%d context blocks, want one for lines 1-10:\n%s", n, prompt)
	}
	if shown[8] != "  return pay.paymentIntents.retrieve(id)" {
		t.Errorf("line 8 = %q", shown[8])
	}
	if _, ok := shown[11]; ok {
		t.Error("line 11 is past the end of the file")
	}
}

func TestEchoes(t *testing.T) {
	line := "  return pay.paymentIntents.create({ amount, currency: 'usd', customer })"
	for msg, want := range map[string]bool{
		"return pay.paymentIntents.create({ amount, currency: 'usd', customer })":   true,
		"`return pay.paymentIntents.create({ amount, currency: 'usd', customer })`": true,
		"pay.paymentIntents.create":                              true,
		"`customer` is not a parameter of paymentIntents.create": false,
	} {
		if got := echoes(msg, line); got != want {
			t.Errorf("echoes(%q) = %v, want %v", msg, got, want)
		}
	}
}
