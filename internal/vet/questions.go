package vet

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/mohsensc/tend-cli/internal/core"
)

const questionSystem = `You check the hunks a patch made to one file, moving code to a new version of a dependency or API.
Report a problem only when the code, the upstream notes or the declarations shown prove it. Do not guess, do not hedge, do not repeat code, and do not suggest edits.
Reply with exactly: none
or with one line per problem: file:line: problem
where line is a number from a context block and problem is one sentence naming what breaks.`

// asked is what the model looks for, put as statements: a question that
// presumes a problem gets one.
var asked = []string{
	"An argument the new call does not accept, or needs in a different shape.",
	"A response the code reads that can now be empty or missing.",
	"State the old call carried that the new one drops: retries, pagination tokens, idempotency keys.",
}

const (
	contextLines = 10
	notesChars   = 1500 // upstream notes shown per call
	batchHunks   = 8    // hunks of one file per model call
	parallel     = 4    // model calls in flight
)

// maxQuestions caps the model calls of one vetting; a var so tests can lower it.
var maxQuestions = 8

// batch is one model call: hunks of one file.
type batch struct {
	file  string
	hunks []*hunk
}

// questions asks the model for problems of three fixed kinds in the hunks it
// wrote, one call per file, and reports only answers that point at a line it
// was shown and name a problem plainly.
func questions(ctx context.Context, p *pass) ([]core.Finding, string) {
	if p.in.Model == nil {
		return nil, "no model configured"
	}
	all := batches(p)
	if len(all) == 0 {
		return nil, na
	}
	run := all[:min(maxQuestions, len(all))]
	found := make([][]core.Finding, len(run))
	errs := make([]error, len(run))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, b := range run {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			found[i], errs[i] = ask(ctx, p, b)
		})
	}
	wg.Wait()
	var res []core.Finding
	failed := 0
	var last error
	for i := range run {
		if errs[i] != nil {
			failed++
			last = errs[i]
			continue
		}
		res = append(res, found[i]...)
	}
	if failed == len(run) {
		return nil, "model failed: " + last.Error()
	}
	if len(run) < len(all) {
		p.note = fmt.Sprintf("asked about %d of %d hunks", hunks(run), hunks(all))
	}
	sortFindings(res)
	return dedupe(res), ""
}

// batches groups the hunks a model wrote by file, at most batchHunks to a call.
func batches(p *pass) []batch {
	var res []batch
	for _, f := range p.files {
		if f.New == "" {
			continue
		}
		var curr []*hunk
		for _, h := range f.Hunks {
			if !h.Model {
				continue
			}
			curr = append(curr, h)
			if len(curr) == batchHunks {
				res = append(res, batch{f.New, curr})
				curr = nil
			}
		}
		if len(curr) > 0 {
			res = append(res, batch{f.New, curr})
		}
	}
	return res
}

func hunks(bs []batch) int {
	n := 0
	for _, b := range bs {
		n += len(b.hunks)
	}
	return n
}

func ask(ctx context.Context, p *pass, b batch) ([]core.Finding, error) {
	prompt, shown := questionPrompt(p, b)
	reply, err := p.in.Model.Complete(ctx, questionSystem, prompt)
	if err != nil {
		return nil, err
	}
	var res []core.Finding
	for _, a := range parseAnswers(reply) {
		if a.File != b.file {
			continue
		}
		if text, ok := shown[a.Line]; ok && !echoes(a.Msg, text) {
			res = append(res, a)
		}
	}
	return res, nil
}

// questionPrompt shows the upstream notes, what is already checked, the new
// declarations of the calls, each hunk, and the new file around the hunks,
// numbered, and returns the lines it numbered.
func questionPrompt(p *pass, b batch) (string, map[int]string) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "File: %s\n\n", b.file)
	if notes := upstream(p.in.Change); notes != "" {
		fmt.Fprintf(&sb, "Upstream notes:\n```\n%s\n```\n\n", notes)
	}
	known := p.known
	if pr := p.in.Proof; pr != nil && pr.Patched != nil && pr.Patched.Pass {
		known = append(slices.Clip(known), "the project's build and tests pass on the patched code")
	}
	if len(known) > 0 {
		sb.WriteString("Already checked, do not report:\n")
		for _, k := range known {
			fmt.Fprintf(&sb, "- %s\n", k)
		}
		sb.WriteString("\n")
	}
	if decls := p.decls[b.file]; len(decls) > 0 {
		sb.WriteString("New declarations of the calls the patch wrote:\n")
		for _, d := range decls {
			fmt.Fprintf(&sb, "- %s\n", d)
		}
		sb.WriteString("\n")
	}
	var spans [][2]int
	for i, h := range b.hunks {
		fmt.Fprintf(&sb, "Hunk %d:\n```diff\n", i+1)
		last := h.NewStart
		for _, l := range h.Lines {
			fmt.Fprintf(&sb, "%c%s\n", l.Op, l.Text)
			last = max(last, l.New)
		}
		sb.WriteString("```\n\n")
		start, end := max(1, h.NewStart-contextLines), last+contextLines
		if n := len(spans); n > 0 && start <= spans[n-1][1]+1 {
			spans[n-1][1] = max(spans[n-1][1], end)
			continue
		}
		spans = append(spans, [2]int{start, end})
	}
	shown := map[int]string{}
	src, err := os.ReadFile(filepath.Join(p.in.Dir, filepath.FromSlash(b.file)))
	if err != nil {
		for _, s := range spans {
			for n := s[0]; n <= s[1]; n++ {
				shown[n] = ""
			}
		}
	} else {
		lines := strings.Split(string(src), "\n")
		for _, s := range spans {
			end := min(s[1], len(lines))
			fmt.Fprintf(&sb, "Context after the patch, lines %d-%d:\n```\n", s[0], end)
			for n := s[0]; n <= end; n++ {
				fmt.Fprintf(&sb, "%d: %s\n", n, lines[n-1])
				shown[n] = lines[n-1]
			}
			sb.WriteString("```\n\n")
		}
	}
	sb.WriteString("Look for:\n")
	for i, q := range asked {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, q)
	}
	return sb.String(), shown
}

// upstream joins what the change quotes from upstream, its moves' included,
// cut at notesChars.
func upstream(ch *core.Change) string {
	if ch == nil {
		return ""
	}
	all := []*core.Change{ch}
	if ch.Cleanup != nil {
		for i := range ch.Cleanup.Moves {
			all = append(all, &ch.Cleanup.Moves[i])
		}
	}
	var parts []string
	seen := map[string]bool{}
	for _, c := range all {
		for _, e := range c.Evidence {
			text := strings.TrimSpace(e.Title + "\n" + e.Text)
			if text == "" || seen[text] {
				continue
			}
			seen[text] = true
			parts = append(parts, text)
		}
	}
	res := strings.Join(parts, "\n\n")
	if len(res) > notesChars {
		res = strings.ToValidUTF8(res[:notesChars], "") + "..."
	}
	return res
}

var answerLine = regexp.MustCompile(`^\s*(?:[-*]|\d+[.)])?\s*` + "`?" + `([^\s:` + "`" + `]+):(\d+)` + "`?" + `:\s*(.+?)\s*$`)

var (
	// Models list lines that are fine anyway ("argument object unchanged");
	// a flag has to name a problem.
	noProblem = regexp.MustCompile(`(?i)^(none|n/?a|ok(ay)?|fine|correct|compatible|nothing)\b|\b(unchanged|no change|not changed|(did not|didn't|does not|doesn't) change|same as before|identical|no problem|no issue|(is|looks|seems|stays) (fine|correct|ok|okay|good|compatible))`)
	// A hedged answer is a guess, not a problem the code shows.
	hedge = regexp.MustCompile(`(?i)\b(may|might|could|possibly|perhaps|probably|likely|potentially|presumably|maybe|unclear)\b`)
)

// parseAnswers keeps the file:line: problem lines of a reply that name a
// problem plainly, and ignores everything else, none included.
func parseAnswers(reply string) []core.Finding {
	var res []core.Finding
	for _, l := range strings.Split(reply, "\n") {
		m := answerLine.FindStringSubmatch(l)
		if m == nil || noProblem.MatchString(m[3]) || hedge.MatchString(m[3]) {
			continue
		}
		n, err := strconv.Atoi(m[2])
		if err != nil || n < 1 {
			continue
		}
		res = append(res, finding("question", m[1], n, "flagged", "%s", m[3]))
	}
	return res
}

// echoes reports whether an answer only repeats the code on its line.
func echoes(msg, line string) bool {
	norm := func(s string) string {
		return strings.Join(strings.Fields(strings.Trim(s, "`'\". ")), " ")
	}
	msg, line = norm(msg), norm(line)
	return msg == "" || strings.Contains(line, msg)
}
