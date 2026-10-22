package patch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/model"
)

// bench runs pass in order and says whether the tests file was there.
type bench struct {
	dir  string
	pass []bool
	had  []bool
}

func (b *bench) prove(ctx context.Context, cmds core.Commands) (*core.Run, error) {
	_, err := os.Stat(filepath.Join(b.dir, "test", "tend", "characterization.js"))
	b.had = append(b.had, err == nil)
	ok := len(b.pass) > 0 && b.pass[0]
	if len(b.pass) > 0 {
		b.pass = b.pass[1:]
	}
	st := core.Step{Cmd: "node --test test/tend/characterization.js"}
	if !ok {
		st.Exit, st.Tail = 1, "AssertionError: expected 'ok', got 'OK'"
	}
	return &core.Run{Pass: ok, Steps: []core.Step{st}}, nil
}

var shop = Bench{Path: "test/tend/characterization.js", Lang: "JavaScript", Files: []string{"src/app.js"}}

// A model without tools writes the whole file; when the tests fail on the
// code as it is, the failure goes back and the next file replaces it.
func TestCharacterizeText(t *testing.T) {
	dir := repo(t, map[string]string{"src/app.js": "exports.health = () => 'OK'\n"})
	b := &bench{dir: dir, pass: []bool{false, true}}
	m := &model.Script{Replies: []string{
		"```js\nconst assert = require('node:assert')\nassert.strictEqual(require('../../src/app').health(), 'ok')\n```",
		"Here it is.\n\n```js\nconst assert = require('node:assert')\nassert.strictEqual(require('../../src/app').health(), 'OK')\n```\n",
	}}
	pt, err := Characterize(context.Background(), dir, shop, Options{Model: m, Prove: b.prove, Attempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !pt.Model || pt.Edited[0] != shop.Path || pt.Applied[0] != "a model wrote tests of how the code behaves now (test/tend/characterization.js)" || pt.Note != "2 tries" {
		t.Errorf("patch = %+v", pt)
	}
	if got := readFile(t, dir, shop.Path); !strings.Contains(got, "'OK')") {
		t.Errorf("the file kept is not the one that passed:\n%s", got)
	}
	if !strings.Contains(m.Calls[0].Prompt, "=== src/app.js\nexports.health = () => 'OK'") {
		t.Errorf("the prompt does not show the file:\n%s", m.Calls[0].Prompt)
	}
	if !strings.Contains(m.Calls[1].Prompt, "fail on the code as it is") || !strings.Contains(m.Calls[1].Prompt, "expected 'ok', got 'OK'") {
		t.Errorf("the failure was not sent back:\n%s", m.Calls[1].Prompt)
	}
	if strings.Contains(run(t, dir, "status", "--porcelain"), "??") {
		t.Error("the tests file is not staged, so the clean-up after a run would take it")
	}

	b = &bench{dir: dir, pass: []bool{false, false}}
	m = &model.Script{Replies: []string{"```js\nx\n```", "no code at all"}}
	if _, err := Characterize(context.Background(), dir, shop, Options{Model: m, Prove: b.prove}); err == nil || !strings.Contains(err.Error(), "attempt 2: the reply holds no code block") {
		t.Errorf("err = %v", err)
	}
}

// A model with tools writes the one file it may, runs the tests, and is done
// once they pass; it may not edit the code under test.
func TestCharacterizeAgent(t *testing.T) {
	dir := repo(t, map[string]string{"src/app.js": "exports.health = () => 'OK'\n"})
	b := &bench{dir: dir, pass: []bool{true}}
	m := &model.Steps{Replies: []model.Reply{
		calls(call("1", "edit", map[string]any{"path": "src/app.js", "old": "'OK'", "new": "'ok'"})),
		calls(call("2", "write", map[string]any{"path": "src/other.test.js", "content": "x"})),
		calls(call("3", "write", map[string]any{"path": shop.Path, "content": "require('node:assert')\n"})),
		calls(call("4", "done", map[string]any{"summary": "pinned health"})),
	}}
	pt, err := Characterize(context.Background(), dir, shop, Options{Model: m, Prove: b.prove, Commands: core.Commands{Test: []string{"node --test"}}})
	if err != nil {
		t.Fatal(err)
	}
	if pt.Note != "4 steps: 1 edit, 2 writes; the commands passed" || len(b.had) != 1 || !b.had[0] {
		t.Errorf("patch %+v, runs saw the file %v", pt, b.had)
	}
	if got := readFile(t, dir, "src/app.js"); got != "exports.health = () => 'OK'\n" {
		t.Errorf("the code under test was edited:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "src", "other.test.js")); err == nil {
		t.Error("a second file was written")
	}
	for i, want := range []string{"is not one of the files this change explains", "write only creates test/tend/characterization.js"} {
		if got := m.Chats[i+1][len(m.Chats[i+1])-1].Text; !strings.Contains(got, want) {
			t.Errorf("turn %d answered %q, want %q", i+1, got, want)
		}
	}
}
