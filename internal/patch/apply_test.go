package patch

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/model"
)

const payJSON = `{
  "dependencies": {
    "@fixture/pay": "^1.4.0"
  }
}
`

// fixture is a node project whose billing code uses two calls removed in @fixture/pay 2.0.0.
type fixture struct {
	dir string
	eco *fakeEco
	ch  *core.Change
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := repo(t, map[string]string{
		"package.json":   payJSON,
		"src/billing.js": billingJS,
		".env":           "KEY=1\n",
	})
	useIndex(t, &fakeIndex{imports: []core.Import{{File: "src/billing.js", Line: 1, Package: "@fixture/pay", Binding: "pay"}}})
	ch := &core.Change{
		ID:    "npm/@fixture/pay@2.0.0",
		Kind:  core.Breaking,
		Level: "major",
		Deps:  []core.Dep{npmDep("@fixture/pay", "1.4.0")},
		To:    "2.0.0",
		Evidence: []core.Evidence{{
			Source: "registry",
			Title:  "@fixture/pay 1.4.0 is deprecated",
			Text:   "use 2.x: charges.* was replaced by paymentIntents.*",
		}},
		Sites: []core.Site{
			site(billingJS, "src/billing.js", 4, "pay", "charges", "create"),
			site(billingJS, "src/billing.js", 5, "pay", "charges", "retrieve"),
		},
	}
	return &fixture{dir: dir, eco: &fakeEco{name: core.Npm, imports: []string{"@fixture/pay"}}, ch: ch}
}

func (f *fixture) opts(m core.Model) Options {
	o := Options{Ecos: map[string]core.Ecosystem{core.Npm: f.eco}, Attempts: 2}
	if m != nil {
		o.Model = m
	}
	return o
}

// edit returns a diff of src/billing.js against the base with old replaced by repl,
// as git would write it, then puts the file back.
func (f *fixture) edit(t *testing.T, old, repl string, args ...string) string {
	t.Helper()
	writeFile(t, f.dir, "src/billing.js", strings.Replace(billingJS, old, repl, 1))
	diff := run(t, f.dir, append([]string{"diff", "--src-prefix=a/", "--dst-prefix=b/"}, args...)...)
	run(t, f.dir, "checkout", "--", "src/billing.js")
	return diff
}

const migrated = `  const res = await pay.paymentIntents.create({ amount, currency: 'usd' });
  return pay?.paymentIntents?.retrieve(res.id);`

const unmigrated = `  const res = await pay.charges.create({ amount, currency: 'usd' });
  return pay?.charges?.retrieve(res.id);`

func TestApplyModelFirstTry(t *testing.T) {
	f := newFixture(t)
	m := &model.Script{Replies: []string{"```diff\n" + f.edit(t, unmigrated, migrated) + "```"}}
	p, err := Apply(context.Background(), f.dir, f.ch, f.opts(m))
	if err != nil {
		t.Fatal(err)
	}
	if p.Decision || !p.Model || len(m.Calls) != 1 {
		t.Fatalf("decision=%v model=%v calls=%d note=%q", p.Decision, p.Model, len(m.Calls), p.Note)
	}
	want := []string{"bumped @fixture/pay 1.4.0 -> 2.0.0 (package.json)", "the model edited src/billing.js"}
	if !slices.Equal(p.Applied, want) {
		t.Errorf("applied = %q", p.Applied)
	}
	if !slices.Equal(p.Edited, []string{"src/billing.js"}) {
		t.Errorf("edited = %q", p.Edited)
	}
	if !slices.Equal(p.Files, []string{"package.json", "src/billing.js"}) {
		t.Errorf("files = %q", p.Files)
	}
	if !strings.Contains(p.Diff, "+  return pay?.paymentIntents?.retrieve(res.id);") {
		t.Errorf("diff:\n%s", p.Diff)
	}
	if !strings.Contains(m.Calls[0].System, "NOCHANGE") || !strings.Contains(m.Calls[0].Prompt, "4| pay.charges.create") {
		t.Errorf("prompt:\n%s", m.Calls[0].Prompt)
	}
}

func TestApplyThreeWay(t *testing.T) {
	f := newFixture(t)
	// The maintainer picked an option that renames create but not retrieve. The model
	// answers against the base text with context that spans the renamed lines, so
	// only a three-way merge can apply it.
	f.ch.Options = []core.Option{{
		Title:   "Move to paymentIntents",
		Codemod: &core.Codemod{Renames: map[string]string{"charges.create": "paymentIntents.create"}},
	}}
	f.ch.Sites = append(f.ch.Sites,
		site(billingJS, "src/billing.js", 9, "pay", "charges", "retrieve"),
		site(billingJS, "src/billing.js", 12, "pay", "charges", "create"))
	retrieve := f.edit(t, `pay['charges']["retrieve"]`, `pay['paymentIntents']["retrieve"]`, "-U6")
	if !strings.Contains(retrieve, "\n   const res = await pay.charges.create(") {
		t.Fatalf("the reply does not overlap the codemod, so a plain apply would do:\n%s", retrieve)
	}
	m := &model.Script{Replies: []string{retrieve}}
	o := f.opts(m)
	o.Option = 1
	p, err := Apply(context.Background(), f.dir, f.ch, o)
	if err != nil {
		t.Fatal(err)
	}
	if p.Decision || !p.Model || len(m.Calls) != 1 {
		t.Fatalf("decision=%v model=%v calls=%d note=%q", p.Decision, p.Model, len(m.Calls), p.Note)
	}
	got := readFile(t, f.dir, "src/billing.js")
	for _, want := range []string{"pay.paymentIntents.create({", `pay['paymentIntents']["retrieve"]`, "  .paymentIntents\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("billing.js lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(m.Calls[0].Prompt, "- rename charges.create -> paymentIntents.create") {
		t.Errorf("prompt does not say what was already applied:\n%s", m.Calls[0].Prompt)
	}
}

func TestApplyGarbageDecides(t *testing.T) {
	f := newFixture(t)
	m := &model.Script{Replies: []string{"I am not sure.", "Still not sure."}}
	p, err := Apply(context.Background(), f.dir, f.ch, f.opts(m))
	if err != nil {
		t.Fatal(err)
	}
	want := "The model's attempts did not apply: attempt 1: " + errNoDiff.Error() + "; attempt 2: " + errNoDiff.Error() + "."
	if !p.Decision || p.Note != want || len(m.Calls) != 2 {
		t.Fatalf("decision=%v note=%q calls=%d", p.Decision, p.Note, len(m.Calls))
	}
	if !slices.Equal(p.Applied, []string{undecided}) || p.Diff != "" || p.Files != nil {
		t.Errorf("a decision carries changes: %q %q", p.Applied, p.Files)
	}
	if st := run(t, f.dir, "status", "--porcelain"); st != "" {
		t.Errorf("worktree not reset:\n%s", st)
	}
}

func TestApplyRefusedBumpDecides(t *testing.T) {
	for _, why := range []string{
		"pom.xml: the version of org.example:web comes from the dependencyManagement of parent org.example:parent:3.3.4, outside this repository",
		"docker: node: pinned by digest, and the digest of library/node:22 is unknown (HTTP 404)",
	} {
		f := newFixture(t)
		f.eco.err = fmt.Errorf("%s: %w", why, core.ErrNotSupported)
		p, err := Apply(context.Background(), f.dir, f.ch, f.opts(nil))
		if err != nil {
			t.Fatal(err)
		}
		want := "tend cannot make this change itself: " + why + ". Make it by hand, or comment `tend skip`."
		if !p.Decision || p.Note != want || p.Files != nil {
			t.Errorf("decision=%v files=%q note=%q, want %q", p.Decision, p.Files, p.Note, want)
		}
		if st := run(t, f.dir, "status", "--porcelain"); st != "" {
			t.Errorf("worktree not reset:\n%s", st)
		}
	}

	f := newFixture(t)
	f.eco.err = errors.New("npm: registry down")
	if _, err := Apply(context.Background(), f.dir, f.ch, f.opts(nil)); err == nil {
		t.Error("a failed bump became a decision")
	}
}

func TestApplyNoChange(t *testing.T) {
	f := newFixture(t)
	m := &model.Script{Replies: []string{"NOCHANGE"}}
	p, err := Apply(context.Background(), f.dir, f.ch, f.opts(m))
	if err != nil {
		t.Fatal(err)
	}
	if p.Decision || p.Model || !slices.Contains(p.Applied, "the model found nothing to change") {
		t.Fatalf("decision=%v model=%v applied=%q", p.Decision, p.Model, p.Applied)
	}
	if !slices.Equal(p.Files, []string{"package.json"}) {
		t.Errorf("files = %q", p.Files)
	}
}

func TestApplyAttempts(t *testing.T) {
	for _, n := range []int{1, 3} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			f := newFixture(t)
			m := &model.Script{Replies: []string{"no", "no", "no", goodDiff}}
			o := f.opts(m)
			o.Attempts = n
			p, err := Apply(context.Background(), f.dir, f.ch, o)
			if err != nil {
				t.Fatal(err)
			}
			if !p.Decision || len(m.Calls) != n {
				t.Errorf("decision=%v calls=%d, want %d", p.Decision, len(m.Calls), n)
			}
		})
	}
}

func TestApplyModelErrorCountsAsAttempt(t *testing.T) {
	f := newFixture(t)
	m := &model.Script{Errs: []error{errors.New("503 overloaded")}, Replies: []string{"", f.edit(t, unmigrated, migrated)}}
	p, err := Apply(context.Background(), f.dir, f.ch, f.opts(m))
	if err != nil {
		t.Fatal(err)
	}
	if p.Decision || len(m.Calls) != 2 {
		t.Fatalf("decision=%v calls=%d note=%q", p.Decision, len(m.Calls), p.Note)
	}
	if m.Calls[1].Prompt != m.Calls[0].Prompt {
		t.Errorf("a failed call changed the prompt:\n%s", m.Calls[1].Prompt)
	}
	m = &model.Script{Errs: []error{errors.New("503 overloaded"), errors.New("timeout")}}
	f = newFixture(t)
	p, err = Apply(context.Background(), f.dir, f.ch, f.opts(m))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Decision || p.Note != "The model's attempts did not apply: attempt 1: model script: 503 overloaded; attempt 2: model script: timeout." {
		t.Errorf("decision=%v note=%q", p.Decision, p.Note)
	}
}

// An unusable reply goes back with the reason, and the next one is applied.
func TestApplyRetriesUnusableReply(t *testing.T) {
	f := newFixture(t)
	m := &model.Script{Replies: []string{"cannot", f.edit(t, unmigrated, migrated)}}
	p, err := Apply(context.Background(), f.dir, f.ch, f.opts(m))
	if err != nil {
		t.Fatal(err)
	}
	if p.Decision || len(m.Calls) != 2 {
		t.Fatalf("decision=%v calls=%d", p.Decision, len(m.Calls))
	}
	if !strings.Contains(m.Calls[1].Prompt, "could not be applied: "+errNoDiff.Error()) {
		t.Errorf("second prompt does not say why the first reply failed:\n%s", m.Calls[1].Prompt)
	}
}

func TestApplyTrustsSafeOption(t *testing.T) {
	f := newFixture(t)
	f.ch.Options = []core.Option{{Safe: true, Codemod: &core.Codemod{Renames: map[string]string{"charges.create": "paymentIntents.create"}}}}
	m := &model.Script{Replies: []string{"NOCHANGE"}}
	if _, err := Apply(context.Background(), f.dir, f.ch, f.opts(m)); err != nil {
		t.Fatal(err)
	}
	if len(m.Calls) != 0 {
		t.Fatalf("a safe option asked the model")
	}
}

func TestApplyScope(t *testing.T) {
	f := newFixture(t)
	sneaky := "--- a/.env\n+++ b/.env\n@@ -1 +1 @@\n-KEY=1\n+KEY=2\n" + f.edit(t, unmigrated, migrated)
	m := &model.Script{Replies: []string{sneaky, f.edit(t, unmigrated, migrated)}}
	p, err := Apply(context.Background(), f.dir, f.ch, f.opts(m))
	if err != nil {
		t.Fatal(err)
	}
	if p.Decision || len(m.Calls) != 2 || slices.Contains(p.Files, ".env") {
		t.Fatalf("decision=%v calls=%d files=%q", p.Decision, len(m.Calls), p.Files)
	}
	if !strings.Contains(m.Calls[1].Prompt, "touches files outside this change: .env") {
		t.Errorf("second prompt:\n%s", m.Calls[1].Prompt)
	}
	if got := readFile(t, f.dir, ".env"); got != "KEY=1\n" {
		t.Errorf(".env = %q", got)
	}
}

func TestApplyWithoutModel(t *testing.T) {
	tests := []struct {
		name     string
		kind     core.Kind
		noTarget bool
		options  []core.Option
		option   int
		proven   bool
		strict   bool
		decision bool
		calls    int
		files    []string
		note     string
	}{
		{
			name: "breaking with sites and nothing to prove it", kind: core.Breaking, decision: true, calls: 1,
			note: "No model is configured to move the 2 call sites, and there is no build or test command to prove a plain bump.",
		},
		{name: "breaking with sites goes to the proof", kind: core.Breaking, proven: true, calls: 1, files: []string{"package.json"}},
		{
			name: "a strict move leaves no call sites to the proof", kind: core.Deprecation, proven: true, strict: true, decision: true, calls: 1,
			note: "No model is configured to move the 2 call sites.",
		},
		{name: "security ships the bump, strict or not", kind: core.Security, strict: true, calls: 1, files: []string{"package.json"}},
		{name: "security ships the bump", kind: core.Security, calls: 1, files: []string{"package.json"}},
		{
			name:     "options and no single target",
			kind:     core.Deprecation,
			noTarget: true,
			options:  []core.Option{{Title: "dayjs", ToName: "dayjs"}, {Title: "luxon", ToName: "luxon"}},
			proven:   true,
			decision: true,
			note:     "There is no single replacement to move to, so the choice is yours.",
		},
		{
			name:    "staying put is a tend.yml change",
			kind:    core.Deprecation,
			options: []core.Option{{Title: "Pin to 1.4.0 and ignore", To: "1.4.0"}},
			option:  1,
			files:   []string{"tend.yml"},
		},
		{
			name:    "safe option leaves the rest to the proof",
			kind:    core.Deprecation,
			options: []core.Option{{Safe: true, Codemod: &core.Codemod{Renames: map[string]string{"charges.create": "paymentIntents.create"}}}},
			calls:   1,
			files:   []string{"package.json", "src/billing.js"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.ch.Kind, f.ch.Options = tt.kind, tt.options
			if tt.noTarget {
				f.ch.To = ""
			}
			o := f.opts(nil)
			o.Option, o.Proven, o.Strict = tt.option, tt.proven, tt.strict
			p, err := Apply(context.Background(), f.dir, f.ch, o)
			if err != nil {
				t.Fatal(err)
			}
			if p.Decision != tt.decision || p.Note != tt.note || len(f.eco.calls) != tt.calls || !slices.Equal(p.Files, tt.files) {
				t.Errorf("decision=%v note=%q calls=%d files=%q", p.Decision, p.Note, len(f.eco.calls), p.Files)
			}
			if tt.decision && readFile(t, f.dir, "package.json") != payJSON {
				t.Error("a decision left the bump in place")
			}
		})
	}
}

func TestApplyOption(t *testing.T) {
	options := []core.Option{
		{Title: "rename", Safe: true, Codemod: &core.Codemod{Renames: map[string]string{"charges.create": "paymentIntents.create"}}},
		{Title: "swap", Safe: true, ToName: "@fixture/payments", To: "3.1.0", Codemod: &core.Codemod{Renames: map[string]string{"charges.retrieve": "payments.get"}}},
	}
	tests := []struct {
		option  int
		call    string
		applied []string
		fail    bool
	}{
		{0, "@fixture/pay 2.0.0", []string{
			"bumped @fixture/pay 1.4.0 -> 2.0.0 (package.json)",
			"renamed charges.create -> paymentIntents.create at src/billing.js:4",
		}, false},
		{2, "@fixture/pay 3.1.0 @fixture/payments", []string{
			"swapped @fixture/pay for @fixture/payments (package.json)",
			"renamed charges.retrieve -> payments.get at src/billing.js:5",
		}, false},
		{3, "", nil, true},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.option), func(t *testing.T) {
			f := newFixture(t)
			f.ch.Options = options
			o := f.opts(nil)
			o.Option = tt.option
			p, err := Apply(context.Background(), f.dir, f.ch, o)
			if tt.fail {
				if err == nil {
					t.Fatal("option out of range applied")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(f.eco.calls, []string{tt.call}) || !slices.Equal(p.Applied, tt.applied) {
				t.Errorf("calls=%q applied=%q", f.eco.calls, p.Applied)
			}
		})
	}
}

func TestPrompt(t *testing.T) {
	var b strings.Builder
	b.WriteString("const pay = require('@fixture/pay');\n")
	for i := 2; i <= 80; i++ {
		switch i {
		case 10:
			b.WriteString("  const res = await pay.charges.create({ amount });\n")
		case 14:
			b.WriteString("  return pay.charges.retrieve(res.id);\n")
		case 70:
			b.WriteString("pay.charges.list({ limit: 3 });\n")
		default:
			fmt.Fprintf(&b, "// line %d\n", i)
		}
	}
	src := b.String()
	dir := repo(t, map[string]string{"src/billing.js": src})
	useIndex(t, &fakeIndex{imports: []core.Import{{File: "src/billing.js", Line: 1, Package: "@fixture/pay", Binding: "pay"}}})
	ch := &core.Change{
		Kind:  core.Breaking,
		Level: "major",
		Deps:  []core.Dep{npmDep("@fixture/pay", "1.4.0")},
		To:    "2.0.0",
		Evidence: []core.Evidence{
			{Source: "releases", Title: "v2.0.0", URL: "https://github.com/fixture/pay/releases/tag/v2.0.0", Text: "## Breaking\n\n- `charges.*` was replaced by `paymentIntents.*`.\n- `charges.list` is gone; use `paymentIntents.list`.\n"},
			{Source: "registry", Title: "1.4.0 deprecated", Text: strings.Repeat("x", 1300)},
		},
		Sites: []core.Site{
			site(src, "src/billing.js", 10, "pay", "charges", "create"),
			site(src, "src/billing.js", 14, "pay", "charges", "retrieve"),
			site(src, "src/billing.js", 70, "pay", "charges", "list"),
		},
	}
	for i := range ch.Sites {
		ch.Sites[i].Call = true
	}
	in := job{
		ch:      ch,
		to:      "2.0.0",
		cm:      &core.Codemod{Renames: map[string]string{"charges.create": "paymentIntents.create"}, Fields: map[string]string{"gone": ""}},
		rest:    ch.Sites,
		allowed: []string{"package.json", "src/billing.js"},
	}
	o := Options{Ecos: map[string]core.Ecosystem{core.Npm: &fakeEco{name: core.Npm, imports: []string{"@fixture/pay"}}}}
	sys, user, err := prompt(&tree{wt: dir}, in, o, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "prompt/system.golden", sys)
	golden(t, "prompt/user.golden", user)
	if strings.Contains(user, strings.Repeat("x", 1201)) {
		t.Error("evidence text is not bounded")
	}
}

func TestApplyReusesIndex(t *testing.T) {
	tests := []struct {
		name   string
		files  []string
		builds int
	}{
		{"bump touched only manifests", []string{"src/billing.js"}, 0},
		{"bump touched an indexed file", []string{"package.json", "src/billing.js"}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			builds := useIndex(t, &fakeIndex{})
			f.ch.Options = []core.Option{{Safe: true, Codemod: &core.Codemod{Imports: map[string]string{"@fixture/pay": "@fixture/payments"}}}}
			o := f.opts(nil)
			o.Index = &fakeIndex{
				files:   tt.files,
				imports: []core.Import{{File: "src/billing.js", Line: 1, Package: "@fixture/pay", Binding: "pay"}},
			}
			p, err := Apply(context.Background(), f.dir, f.ch, o)
			if err != nil {
				t.Fatal(err)
			}
			if *builds != tt.builds {
				t.Errorf("rescans = %d, want %d", *builds, tt.builds)
			}
			rewrote := slices.Contains(p.Applied, "rewrote import @fixture/pay -> @fixture/payments at src/billing.js:1")
			if rewrote != (tt.builds == 0) {
				t.Errorf("applied = %q", p.Applied)
			}
		})
	}
}

func TestApplyStay(t *testing.T) {
	for _, tt := range []struct {
		to   string
		want string
	}{
		{"2.0.0", "pinned @fixture/pay at 1.4.0, ignoring 2.0.0 and later (tend.yml)"},
		{"", "pinned @fixture/pay at 1.4.0, ignoring every release, and tend stops proposing anything for it (tend.yml)"},
	} {
		f := newFixture(t)
		f.ch.Kind, f.ch.To = core.Deprecation, tt.to
		f.ch.Options = []core.Option{{Title: "Pin to 1.4.0 and ignore", To: "1.4.0", Stay: true}}
		o := f.opts(nil)
		o.Option = 1
		p, err := Apply(context.Background(), f.dir, f.ch, o)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(p.Applied, []string{tt.want}) {
			t.Errorf("to %q: applied %q, want %q", tt.to, p.Applied, tt.want)
		}
	}
}

// A model without tools whose diff applies but fails the project's tests
// gets their output back, from the tree as it was before that diff.
func TestApplyModelFeedback(t *testing.T) {
	f := newFixture(t)
	half := f.edit(t, "  return pay?.charges?.retrieve(res.id);", "  return pay?.charges?.retrieve(res.id); // half")
	m := &model.Script{Replies: []string{"```diff\n" + half + "```", "```diff\n" + f.edit(t, unmigrated, migrated) + "```"}}
	pr := &prover{dir: f.dir, pass: []bool{false}}
	o := f.opts(m)
	o.Commands, o.Prove = core.Commands{Test: []string{"npm test"}}, pr.prove
	p, err := Apply(context.Background(), f.dir, f.ch, o)
	if err != nil {
		t.Fatal(err)
	}
	if p.Decision || !p.Model || len(m.Calls) != 2 || len(pr.saw) != 1 {
		t.Fatalf("decision=%v model=%v calls=%d proofs=%d note=%q", p.Decision, p.Model, len(m.Calls), len(pr.saw), p.Note)
	}
	if !strings.Contains(pr.saw[0], "// half") {
		t.Errorf("the proof did not see the first diff:\n%s", pr.saw[0])
	}
	retry := m.Calls[1].Prompt
	if !strings.Contains(retry, "Your previous diff applied, but the project's commands failed:\nnpm test exited 1\nTypeError") || strings.Contains(retry, "could not be applied") {
		t.Errorf("second prompt:\n%s", retry)
	}
	if strings.Contains(p.Diff, "// half") || !strings.Contains(p.Diff, "paymentIntents?.retrieve") {
		t.Errorf("diff:\n%s", p.Diff)
	}
}
