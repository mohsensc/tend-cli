package patch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/model"
)

func call(id, name string, args map[string]any) model.ToolCall {
	b, _ := json.Marshal(args)
	return model.ToolCall{ID: id, Name: name, Args: b}
}

func calls(cs ...model.ToolCall) model.Reply { return model.Reply{Calls: cs, Tokens: 100} }

// prover answers the agent's runs from a list, in order, and remembers what
// each run saw in src/billing.js.
type prover struct {
	dir  string
	pass []bool
	saw  []string
}

func (p *prover) prove(ctx context.Context, cmds core.Commands) (*core.Run, error) {
	b, _ := os.ReadFile(filepath.Join(p.dir, "src", "billing.js"))
	p.saw = append(p.saw, string(b))
	ok := len(p.pass) > 0 && p.pass[0]
	if len(p.pass) > 0 {
		p.pass = p.pass[1:]
	}
	st := core.Step{Cmd: "npm test"}
	if !ok {
		st.Exit, st.Tail = 1, "TypeError: Cannot read properties of undefined (reading 'retrieve')"
	}
	return &core.Run{Pass: ok, Steps: []core.Step{st}}, nil
}

func (f *fixture) agentOpts(m model.Agent, p *prover) Options {
	o := f.opts(m)
	o.Commands = core.Commands{Test: []string{"npm test"}}
	o.Prove = p.prove
	o.Transcript = filepath.Join(f.dir, ".git", "agent.jsonl")
	return o
}

func TestAgentMigrates(t *testing.T) {
	f := newFixture(t)
	p := &prover{dir: f.dir, pass: []bool{false, true, true}}
	m := &model.Steps{Replies: []model.Reply{
		calls(call("1", "read", map[string]any{"path": "src/billing.js", "to": 6})),
		calls(call("2", "edit", map[string]any{"path": "src/billing.js", "old": "pay.charges.create(", "new": "pay.paymentIntents.create("})),
		calls(call("3", "run", map[string]any{"name": "test"})),
		calls(call("4", "edit", map[string]any{"path": "src/billing.js", "old": "pay?.charges?.retrieve(", "new": "pay?.paymentIntents?.retrieve("})),
		calls(call("5", "run", map[string]any{"name": "test"}), call("6", "done", map[string]any{"summary": "moved both calls"})),
	}}
	pt, err := Apply(context.Background(), f.dir, f.ch, f.agentOpts(m, p))
	if err != nil {
		t.Fatal(err)
	}
	if pt.Decision || !pt.Model || !slices.Contains(pt.Applied, "the model edited src/billing.js") {
		t.Fatalf("patch = %+v", pt)
	}
	if !slices.Equal(pt.Edited, []string{"src/billing.js"}) {
		t.Errorf("edited = %q", pt.Edited)
	}
	if want := "6 steps: 1 read, 2 edits, 2 runs; the commands failed, then passed twice"; pt.Note != want {
		t.Errorf("note = %q, want %q", pt.Note, want)
	}
	if !slices.Equal(pt.Files, []string{"package.json", "src/billing.js"}) || !strings.Contains(pt.Diff, "+  return pay?.paymentIntents?.retrieve(res.id);") {
		t.Errorf("files %q, diff:\n%s", pt.Files, pt.Diff)
	}
	// The first run saw the half-done edit: edits are on disk before a run.
	if !strings.Contains(p.saw[0], "pay.paymentIntents.create(") || strings.Contains(p.saw[0], "paymentIntents?.retrieve") {
		t.Errorf("first run saw:\n%s", p.saw[0])
	}
	first := m.Chats[0][0].Text
	if !strings.Contains(first, "src/billing.js") || !strings.Contains(first, "charges.* was replaced by paymentIntents.*") {
		t.Errorf("the brief lacks the sites or the evidence:\n%s", first)
	}
	if got := m.Chats[3][len(m.Chats[3])-1].Text; !strings.Contains(got, "failed: npm test exited 1") || !strings.Contains(got, "reading 'retrieve'") {
		t.Errorf("the failing run was not fed back: %q", got)
	}
	log, err := os.ReadFile(filepath.Join(f.dir, ".git", "agent.jsonl"))
	if err != nil || strings.Count(string(log), "\n") != 6 {
		t.Errorf("transcript: %v\n%s", err, log)
	}
}

func TestAgentTools(t *testing.T) {
	f := newFixture(t)
	if err := os.Symlink(t.TempDir(), filepath.Join(f.dir, "out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".env", filepath.Join(f.dir, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	p := &prover{dir: f.dir, pass: []bool{true}}
	m := &model.Steps{Replies: []model.Reply{
		{Text: "I would rename charges to paymentIntents."},
		calls(
			call("a", "read", map[string]any{"path": "../secrets"}),
			call("b", "read", map[string]any{"path": "out/x"}),
			call("c", "edit", map[string]any{"path": ".env", "old": "KEY=1", "new": "KEY=2"}),
			call("d", "edit", map[string]any{"path": "src/billing.js", "old": "charges", "new": "x"}),
			call("e", "edit", map[string]any{"path": "src/billing.js", "old": "nothing like this", "new": "x"}),
			call("f", "done", map[string]any{"summary": "nothing"}),
			call("g", "run", map[string]any{"name": "deploy"}),
			call("h", "grep", map[string]any{"pattern": "retrieve|KEY"}),
			call("i", "fly", map[string]any{}),
			call("k", "read", map[string]any{"path": "notes.txt"}),
		),
		calls(call("j", "edit", map[string]any{"path": "test/billing.test.js", "old": "x", "new": "y"})),
	}}
	o := f.agentOpts(m, p)
	o.Turns = 12
	pt, err := Apply(context.Background(), f.dir, f.ch, o)
	if err != nil {
		t.Fatal(err)
	}
	if !pt.Decision || !strings.HasPrefix(pt.Note, "The model did not get there: the model used all 12 of its turns (12 steps: ") {
		t.Errorf("patch = %+v", pt)
	}
	answers := map[string]string{}
	for _, turn := range m.Chats[2] {
		if turn.Role == "tool" {
			answers[turn.CallID] = turn.Text
		}
	}
	for id, want := range map[string]string{
		"a": "is not a path inside the repository",
		"b": "leads outside the repository",
		"c": `".env" may hold credentials`,
		"d": "occurs 8 times",
		"e": "does not occur",
		"f": "nothing was edited",
		"g": "run takes install, build or test",
		"h": "src/billing.js:5:",
		"i": "no tool named fly",
		"k": `"notes.txt" may hold credentials`,
	} {
		if !strings.Contains(answers[id], want) {
			t.Errorf("answer to %s = %q, want it to contain %q", id, answers[id], want)
		}
	}
	if strings.Contains(answers["h"], ".env") {
		t.Errorf("grep showed a credentials file: %q", answers["h"])
	}
	if got := m.Chats[1][2]; got.Role != "user" || !strings.HasPrefix(got.Text, "Use the tools") {
		t.Errorf("a reply in prose was not sent back to the tools: %+v", got)
	}
	if got := readFile(t, f.dir, ".env"); got != "KEY=1\n" {
		t.Errorf(".env = %q", got)
	}
	if st := run(t, f.dir, "status", "--porcelain"); st != "" {
		t.Errorf("a decision left changes:\n%s", st)
	}
}

func TestAgentBudget(t *testing.T) {
	f := newFixture(t)
	p := &prover{dir: f.dir}
	m := &model.Steps{Replies: []model.Reply{
		{Calls: []model.ToolCall{call("1", "read", map[string]any{"path": "src/billing.js"})}, Tokens: 900},
		{Calls: []model.ToolCall{call("2", "read", map[string]any{"path": "src/billing.js"})}, Tokens: 900},
	}}
	o := f.agentOpts(m, p)
	o.Tokens = 1500
	pt, err := Apply(context.Background(), f.dir, f.ch, o)
	if err != nil {
		t.Fatal(err)
	}
	if !pt.Decision || pt.Note != "The model did not get there: the model spent its 1500 tokens (2 steps: 2 reads)." {
		t.Errorf("note = %q", pt.Note)
	}
}

// A model without tool calling, or no way to run the project's commands,
// keeps to the one-shot diff.
func TestAgentNeedsAProof(t *testing.T) {
	f := newFixture(t)
	m := &model.Steps{Script: model.Script{Replies: []string{f.edit(t, unmigrated, migrated)}}}
	o := f.opts(m)
	pt, err := Apply(context.Background(), f.dir, f.ch, o)
	if err != nil {
		t.Fatal(err)
	}
	if pt.Decision || len(m.Chats) != 0 || len(m.Calls) != 1 {
		t.Errorf("decision %v, chats %d, completions %d", pt.Decision, len(m.Chats), len(m.Calls))
	}
}

func TestOutcomes(t *testing.T) {
	tests := []struct {
		in   []string
		want string
	}{
		{[]string{"passed"}, "passed"},
		{[]string{"passed", "passed"}, "passed both times they ran"},
		{[]string{"passed", "passed", "passed"}, "passed all 3 times they ran"},
		{[]string{"failed", "failed", "passed"}, "failed twice, then passed"},
		{[]string{"failed", "passed", "failed", "failed", "failed", "passed"}, "failed, then passed, then failed 3 times, then passed"},
	}
	for _, tt := range tests {
		if got := outcomes(tt.in); got != tt.want {
			t.Errorf("outcomes(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAgentGrepGlob(t *testing.T) {
	f := newFixture(t)
	s := &session{t: &tree{wt: f.dir}, ctx: context.Background()}
	for _, glob := range []string{"src/**/*.js", "*.js", "src/*.js"} {
		if got := s.grep("charges", glob); !strings.Contains(got, "src/billing.js:") {
			t.Errorf("grep with %s = %q", glob, got)
		}
	}
}
