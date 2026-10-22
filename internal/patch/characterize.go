package patch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/gitx"
	"github.com/mohsensc/tend-cli/internal/model"
)

// Bench is what characterization tests are written for: the file they go
// in, the language they are written in, and the files whose behavior they
// pin down.
type Bench struct {
	Path  string
	Lang  string
	Files []string
}

const benchRules = `Rules:
- Write one test file, at the path given, and touch no other file.
- Test what the files export, or what they serve when they start a server, and assert what the code does today, even where it looks wrong.
- Never reach the network: stub fetch, HTTP clients and anything else that calls out.
- Leave nothing running: close servers, clear timers.
- Do not add comments or prints.`

const benchAgent = `You write characterization tests: tests that pin down what code does today, so that a later change which alters its behavior makes them fail. You work through tools: read and grep to look around, write to create the test file, edit to change it, run to run the tests, and done when they pass.

` + benchRules + `
- Run the tests before you call done. done runs every command; when one fails you get its output and keep going.`

const benchDiff = `You write characterization tests: tests that pin down what code does today, so that a later change which alters its behavior makes them fail.

` + benchRules + `
- Reply with the whole test file in one fenced code block, and nothing else.`

// benchFile is how much of each file the prompt shows.
const benchFile = 300

// Characterize has the model write tests of how b.Files behave now, at
// b.Path, and stops once the project's commands, which run them, pass on
// the tree as it is: a model that calls tools runs them as it works; one
// that does not gets the failure back while attempts remain. o.Prove runs
// the commands.
func Characterize(ctx context.Context, wt string, b Bench, o Options) (*core.Patch, error) {
	if o.Model == nil || o.Prove == nil {
		return nil, errors.New("characterization tests need a model and the project's commands")
	}
	t := &tree{wt: wt}
	brief := benchBrief(t, b)
	p := &core.Patch{}
	if a, ok := o.Model.(model.Agent); ok {
		s := &session{t: t, o: o, ctx: ctx, counts: map[string]int{}, bench: b.Path, in: job{allowed: []string{b.Path}}}
		if err := s.loop(ctx, a, benchAgent, brief, benchTools, p); err != nil {
			return nil, err
		}
	} else if err := benchText(ctx, t, b, brief, o, p); err != nil {
		return nil, err
	}
	p.Model = true
	p.Edited = []string{b.Path}
	p.Applied = []string{"a model wrote tests of how the code behaves now (" + b.Path + ")"}
	return p, nil
}

func benchBrief(t *tree, b Bench) string {
	var s strings.Builder
	fmt.Fprintf(&s, "Write characterization tests in %s, in the file %s, for these files, which are about to change:\n", b.Lang, b.Path)
	for _, f := range b.Files {
		src := t.read(f)
		if src == nil {
			continue
		}
		lines := strings.Split(strings.TrimSuffix(string(src), "\n"), "\n")
		fmt.Fprintf(&s, "\n=== %s\n%s\n", f, strings.Join(lines[:min(len(lines), benchFile)], "\n"))
		if len(lines) > benchFile {
			fmt.Fprintf(&s, "(%d more lines)\n", len(lines)-benchFile)
		}
	}
	return s.String()
}

// benchText asks a model without tools for the whole file, writes it, runs
// the commands, and sends the failure back while attempts remain.
func benchText(ctx context.Context, t *tree, b Bench, brief string, o Options, p *core.Patch) error {
	n := max(o.Attempts, defaultAttempts)
	retry := ""
	var fails []string
	for i := range n {
		reply, err := o.Model.Complete(ctx, benchDiff, brief+retry)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fails = append(fails, fmt.Sprintf("attempt %d: model %s: %v", i+1, o.Model.Name(), err))
			continue
		}
		body := block(strings.ReplaceAll(reply, "\r\n", "\n"))
		if strings.TrimSpace(body) == "" {
			fails = append(fails, fmt.Sprintf("attempt %d: the reply holds no code block", i+1))
			retry = "\n\nYour previous reply held no code block. Reply with the whole test file in one."
			continue
		}
		if err := place(t.wt, b.Path, body); err != nil {
			return err
		}
		// What is staged survives the clean-up after the commands run.
		if _, err := gitx.Raw(ctx, t.wt, "add", "-A"); err != nil {
			return err
		}
		run, err := o.Prove(ctx, o.Commands)
		if err != nil {
			return err
		}
		if run.Pass {
			p.Note = fmt.Sprintf("%d %s", i+1, pick(i+1, "try", "tries"))
			return nil
		}
		st := run.Last()
		why := "the commands failed"
		if st != nil {
			why = fmt.Sprintf("%s exited %d\n%s", st.Cmd, st.Exit, tail(st.Tail, feedbackMax))
		}
		o.logf("characterization attempt %d: %s", i+1, strings.SplitN(why, "\n", 2)[0])
		fails = append(fails, fmt.Sprintf("attempt %d: %s", i+1, strings.SplitN(why, "\n", 2)[0]))
		retry = "\n\nThe tests you wrote fail on the code as it is, so they do not describe it:\n" + why + "\n\nReply with the whole corrected test file."
	}
	return errors.New(strings.Join(fails, "; "))
}

// block is the first fenced code block of a reply, whatever its language.
func block(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if !strings.HasPrefix(strings.TrimSpace(ln), "```") {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(strings.TrimSpace(lines[j]), "```") {
				return strings.Join(lines[i+1:j], "\n")
			}
		}
	}
	return ""
}

// place writes a file in the worktree.
func place(wt, rel, body string) error {
	p := filepath.Join(wt, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(strings.TrimRight(body, "\n")+"\n"), 0o644)
}

func pick(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
