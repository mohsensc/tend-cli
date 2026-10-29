package patch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/gitx"
	"github.com/mohsensc/tend-cli/internal/model"
	"github.com/mohsensc/tend-cli/internal/scan"
)

const agentSystem = `You move code in a git repository onto an upstream change so that it keeps working. You work through tools: read and grep to look around, edit to change a file, run to run the project's own install, build or test commands, and done when the change is complete.

Rules:
- Change only what the upstream change requires: no renames, reordering or cleanups.
- Do not add comments, logging or prints.
- Keep the existing formatting, indentation and quoting.
- edit replaces text you copied from read; give enough of it to be unique in the file.
- Run the tests before you call done. done runs every command; when one fails you get its output and keep going.`

const (
	readMax  = 300  // lines one read returns
	grepMax  = 100  // lines one grep returns
	toolText = 8000 // bytes of a tool's answer the model sees
)

func schema(props map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func num(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }

var agentTools = []model.Tool{
	{Name: "read", Description: "Read a file's lines, numbered. from and to are 1-based and inclusive; leave them out for the start of the file.",
		Params: schema(map[string]any{"path": str("repo-relative path"), "from": num("first line"), "to": num("last line")}, "path")},
	{Name: "grep", Description: "Search the repository's files with an extended regular expression. glob narrows the files, such as src/**/*.js.",
		Params: schema(map[string]any{"pattern": str("extended regular expression"), "glob": str("pathspec glob")}, "pattern")},
	{Name: "edit", Description: "Replace old with new in a file. old must occur exactly once.",
		Params: schema(map[string]any{"path": str("repo-relative path"), "old": str("exact text to replace"), "new": str("replacement text")}, "path", "old", "new")},
	{Name: "run", Description: "Run the project's install, build or test commands on the files as they are now.",
		Params: schema(map[string]any{"name": map[string]any{"type": "string", "enum": []string{"install", "build", "test"}}}, "name")},
	{Name: "done", Description: "Say the change is complete. Every command runs; when one fails, its output comes back and the work goes on.",
		Params: schema(map[string]any{"summary": str("what was changed, in a sentence")}, "summary")},
}

// benchTools are the tools of a characterization: write in place of nothing
// but the one test file to edit.
var benchTools = append([]model.Tool{
	{Name: "write", Description: "Create the test file, or replace all of it.",
		Params: schema(map[string]any{"path": str("repo-relative path of the test file"), "content": str("the whole file")}, "path", "content")},
}, agentTools...)

// session is one agent run over a worktree: what it may touch and what it did.
type session struct {
	t       *tree
	in      job
	o       Options
	ctx     context.Context
	steps   int
	counts  map[string]int
	edited  []string
	results []string // pass or fail of each run, in order
	log     *os.File
	bench   string // the one test file a characterization may write, "" for a migration

	generatedPaths []string
}

// work hands the remaining sites to a model that calls tools. It ends when the
// project's commands pass on the tree the model left, or when the budget runs
// out; the model saying it is done is not enough.
func work(ctx context.Context, t *tree, in job, o Options, p *core.Patch, a model.Agent) error {
	s := &session{t: t, in: in, o: o, ctx: ctx, counts: map[string]int{}}
	brief, err := s.brief()
	if err != nil {
		return err
	}
	if err := s.loop(ctx, a, agentSystem, brief, agentTools, p); err != nil {
		return err
	}
	p.Applied = append(p.Applied, "the model edited "+strings.Join(s.edited, ", "))
	return nil
}

// loop runs the conversation until the model's done passes or the budget
// runs out, logging each tool call to the transcript.
func (s *session) loop(ctx context.Context, a model.Agent, system, brief string, tools []model.Tool, p *core.Patch) error {
	o := s.o
	if o.Transcript != "" {
		if err := os.MkdirAll(filepath.Dir(o.Transcript), 0o755); err == nil {
			s.log, _ = os.Create(o.Transcript)
		}
	}
	if s.log != nil {
		defer s.log.Close()
	}
	turns := []model.Turn{{Role: "user", Text: brief}}
	used := 0
	for {
		if s.steps >= o.turns() {
			return fmt.Errorf("the model used all %d of its turns (%s)", o.turns(), s.summary())
		}
		if o.Tokens > 0 && used >= o.Tokens {
			return fmt.Errorf("the model spent its %d tokens (%s)", o.Tokens, s.summary())
		}
		r, err := a.Chat(ctx, system, turns, tools)
		if err != nil {
			return fmt.Errorf("%w (%s)", err, s.summary())
		}
		used += r.Tokens
		turns = append(turns, model.Turn{Role: "assistant", Text: r.Text, Calls: r.Calls})
		if len(r.Calls) == 0 {
			s.steps++
			turns = append(turns, model.Turn{Role: "user", Text: "Use the tools: edit to change files, run to check them, done when the change is complete."})
			continue
		}
		for _, c := range r.Calls {
			s.steps++
			out, finished := s.call(c)
			s.record(c, out)
			turns = append(turns, model.Turn{Role: "tool", CallID: c.ID, Text: core.Clip(out, toolText)})
			if finished {
				p.Model = true
				p.Edited = s.edited
				p.Note = s.summary()
				o.logf("the model finished in %d steps", s.steps)
				return nil
			}
		}
	}
}

func (o Options) turns() int {
	if o.Turns > 0 {
		return o.Turns
	}
	return 30
}

// brief is the first message: the change, what was already done, where the
// code uses it, and the failure that brought tend here, if any.
func (s *session) brief() (string, error) {
	_, user, err := prompt(s.t, s.in, s.o, "", "", false)
	if err != nil {
		return "", err
	}
	// edit also takes the tests of those files, which the diff prompt does not offer.
	user = strings.Replace(user, "Files you may edit:", "Files you may edit, along with the tests that reach them, but never the tests tend wrote:", 1)
	return user, nil
}

func (s *session) call(c model.ToolCall) (string, bool) {
	s.counts[c.Name]++
	var args struct {
		Path, Pattern, Glob, Old, New, Name, Summary, Content string
		From, To                                              int
	}
	if err := json.Unmarshal(c.Args, &args); err != nil {
		return "error: the arguments are not a JSON object: " + err.Error(), false
	}
	switch c.Name {
	case "read":
		return s.read(args.Path, args.From, args.To), false
	case "grep":
		return s.grep(args.Pattern, args.Glob), false
	case "edit":
		return s.edit(args.Path, args.Old, args.New), false
	case "write":
		if s.bench == "" {
			break
		}
		return s.write(args.Path, args.Content), false
	case "run":
		cmds, ok := s.commands(args.Name)
		if !ok {
			return "error: run takes install, build or test", false
		}
		return s.run(cmds), false
	case "done":
		if len(s.edited) == 0 {
			return "error: nothing was edited; the sites listed at the start still need the change", false
		}
		out := s.run(s.o.Commands)
		return out, strings.HasPrefix(out, "passed")
	}
	return "error: no tool named " + c.Name, false
}

// credentials name files that hold keys and tokens by their base name. The
// model never reads them, whatever the repository has committed.
var credentials = []string{".env", ".env.*", "*.pem", "*.key", "*.p12", "id_rsa*", "id_ed25519*", ".npmrc", ".pypirc", ".netrc", ".git-credentials"}

func credential(p string) bool {
	base := path.Base(filepath.ToSlash(p))
	for _, c := range credentials {
		if ok, _ := path.Match(c, base); ok {
			return true
		}
	}
	return false
}

// inside resolves a repo-relative path and refuses anything that leaves the
// worktree, through .. or a symlink, and files named like credentials.
func (s *session) inside(p string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(p))
	if p == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q is not a path inside the repository", p)
	}
	if credential(clean) {
		return "", fmt.Errorf("%q may hold credentials, and tend does not hand those to a model", p)
	}
	root, err := filepath.EvalSymlinks(s.t.wt)
	if err != nil {
		return "", err
	}
	// The file itself may not exist yet; what it would resolve to is decided
	// by the nearest part of the path that does.
	at := filepath.Join(root, clean)
	real, err := filepath.EvalSymlinks(at)
	for err != nil && at != root {
		at = filepath.Dir(at)
		real, err = filepath.EvalSymlinks(at)
	}
	if err != nil || real != root && !strings.HasPrefix(real, root+string(filepath.Separator)) {
		return "", fmt.Errorf("%q leads outside the repository", p)
	}
	if at == filepath.Join(root, clean) && credential(real) {
		return "", fmt.Errorf("%q may hold credentials, and tend does not hand those to a model", p)
	}
	return filepath.ToSlash(clean), nil
}

func (s *session) read(p string, from, to int) string {
	rel, err := s.inside(p)
	if err != nil {
		return "error: " + err.Error()
	}
	b := s.t.read(rel)
	if b == nil {
		return "error: no file " + rel
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	from = max(from, 1)
	if to <= 0 || to > len(lines) {
		to = len(lines)
	}
	to = min(to, from+readMax-1)
	if from > to {
		return fmt.Sprintf("error: %s has %d lines", rel, len(lines))
	}
	var res strings.Builder
	for n := from; n <= to; n++ {
		fmt.Fprintf(&res, "%d| %s\n", n, lines[n-1])
	}
	if to < len(lines) {
		fmt.Fprintf(&res, "(%d more lines)\n", len(lines)-to)
	}
	return res.String()
}

func (s *session) grep(pattern, glob string) string {
	args := []string{"grep", "-n", "-I", "-E", "-e", pattern, "--"}
	if glob != "" {
		// A plain pathspec reads ** as two stars, so src/**/*.js would skip
		// the files right under src.
		if strings.Contains(glob, "**") && !strings.HasPrefix(glob, ":") {
			glob = ":(glob)" + glob
		}
		args = append(args, glob)
	}
	for _, c := range credentials {
		args = append(args, ":(exclude,glob)**/"+c)
	}
	out, err := gitx.Raw(s.ctx, s.t.wt, args...)
	if err != nil && strings.TrimSpace(out) == "" {
		if strings.Contains(err.Error(), "exit status 1") {
			return "no matches"
		}
		return "error: " + err.Error()
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > grepMax {
		lines = append(lines[:grepMax], fmt.Sprintf("(%d more matches)", len(lines)-grepMax))
	}
	return strings.Join(lines, "\n")
}

// edit changes a site file, a manifest the change explains, or a test of
// one of them. A migration never edits the tests tend wrote itself: a model
// that weakens what proves its work would prove nothing.
func (s *session) edit(p, old, now string) string {
	rel, err := s.inside(p)
	if err != nil {
		return "error: " + err.Error()
	}
	switch {
	case s.bench == "" && s.generated(rel):
		return "error: " + rel + " holds tests tend wrote to prove changes, and a migration never edits them; change the code so they pass"
	case slices.Contains(s.in.allowed, rel):
	case s.bench != "" || !scan.IsTest(rel):
		return "error: " + rel + " is not one of the files this change explains; edit the files listed at the start, or their tests"
	case !s.covers(rel):
		return "error: " + rel + " tests none of the files this change explains; edit only the files listed at the start and the tests that reach them"
	}
	b := s.t.read(rel)
	if b == nil {
		return "error: no file " + rel
	}
	switch n := bytes.Count(b, []byte(old)); {
	case old == "":
		return "error: old is empty"
	case n == 0:
		return "error: old does not occur in " + rel + "; read the file and copy the text exactly"
	case n > 1:
		return fmt.Sprintf("error: old occurs %d times in %s; include more of the surrounding text", n, rel)
	}
	if err := s.t.write(rel, bytes.Replace(b, []byte(old), []byte(now), 1)); err != nil {
		return "error: " + err.Error()
	}
	if !slices.Contains(s.edited, rel) {
		s.edited = append(s.edited, rel)
		slices.Sort(s.edited)
	}
	return "edited " + rel
}

// generated reports whether rel is a test tend wrote: the characterization
// tests of a Go package, the smoke and characterization tests of a suite
// next to a manifest the change moves, or a test the worktree has that the
// base does not, which only a baseline tend put there can be.
func (s *session) generated(rel string) bool {
	if path.Base(rel) == "tend_characterization_test.go" || slices.Contains(s.suites(), rel) {
		return true
	}
	if !scan.IsTest(rel) {
		return false
	}
	_, err := gitx.Raw(s.ctx, s.t.wt, "cat-file", "-e", head+":"+rel)
	return err != nil
}

// suites are the paths of the tests tend generates next to each manifest
// the change moves.
func (s *session) suites() []string {
	if s.generatedPaths != nil {
		return s.generatedPaths
	}
	s.generatedPaths = []string{}
	if s.in.ch == nil {
		return s.generatedPaths
	}
	for _, d := range s.in.ch.Deps {
		b, ok := s.o.Ecos[d.Ecosystem].(core.Baseliner)
		if !ok || d.Manifest.Path == "" {
			continue
		}
		if su := b.Suite(s.t.wt, d.Manifest); su != nil {
			for _, f := range []string{su.Smoke, su.Tests} {
				if f != "" && !slices.Contains(s.generatedPaths, f) {
					s.generatedPaths = append(s.generatedPaths, f)
				}
			}
		}
	}
	return s.generatedPaths
}

// covers reports whether a test reaches one of the files the change
// explains.
func (s *session) covers(test string) bool {
	return s.o.Index != nil && slices.ContainsFunc(s.in.allowed, func(f string) bool { return core.Reaches(s.o.Index, test, f) })
}

// write creates the test file a characterization writes, or replaces it.
func (s *session) write(p, content string) string {
	rel, err := s.inside(p)
	if err != nil {
		return "error: " + err.Error()
	}
	if rel != s.bench {
		return "error: write only creates " + s.bench
	}
	if err := place(s.t.wt, rel, content); err != nil {
		return "error: " + err.Error()
	}
	delete(s.t.files, rel)
	if !slices.Contains(s.edited, rel) {
		s.edited = append(s.edited, rel)
	}
	return "wrote " + rel
}

func (s *session) commands(name string) (core.Commands, bool) {
	all := s.o.Commands
	switch name {
	case "install":
		return core.Commands{Install: all.Install}, true
	case "build":
		return core.Commands{Install: all.Install, Build: all.Build}, true
	case "test":
		return all, true
	}
	return core.Commands{}, false
}

// run stages the tree, so the clean-up after the commands keeps the edits,
// and runs the commands on it.
func (s *session) run(cmds core.Commands) string {
	if _, err := gitx.Raw(s.ctx, s.t.wt, "add", "-A"); err != nil {
		return "error: " + err.Error()
	}
	r, err := s.o.Prove(s.ctx, cmds)
	if err != nil {
		return "error: " + err.Error()
	}
	if r.Pass {
		s.results = append(s.results, "passed")
		return fmt.Sprintf("passed: %d commands", len(r.Steps))
	}
	s.results = append(s.results, "failed")
	for _, st := range r.Steps {
		if st.Skipped == "" && st.Exit != 0 {
			return fmt.Sprintf("failed: %s exited %d\n%s", st.Cmd, st.Exit, tail(st.Tail, feedbackMax))
		}
	}
	return "failed"
}

// summary condenses what the model did for the PR: its steps, the files it
// edited and how its runs of the commands went.
func (s *session) summary() string {
	var parts []string
	for _, tool := range []string{"read", "grep", "edit", "write", "run"} {
		if n := s.counts[tool]; n > 0 {
			parts = append(parts, plural(n, tool))
		}
	}
	res := fmt.Sprintf("%d steps", s.steps)
	if len(parts) > 0 {
		res += ": " + strings.Join(parts, ", ")
	}
	if len(s.results) > 0 {
		res += "; the commands " + outcomes(s.results)
	}
	return res
}

// outcomes tells how the runs went, repeats folded: "failed twice, then passed".
func outcomes(results []string) string {
	if !slices.Contains(results, "failed") && len(results) == 2 {
		return "passed both times they ran"
	}
	if !slices.Contains(results, "failed") && len(results) > 2 {
		return fmt.Sprintf("passed all %d times they ran", len(results))
	}
	var res []string
	for i := 0; i < len(results); {
		j := i
		for j < len(results) && results[j] == results[i] {
			j++
		}
		res = append(res, results[i]+times(j-i))
		i = j
	}
	return strings.Join(res, ", then ")
}

func times(n int) string {
	switch n {
	case 1:
		return ""
	case 2:
		return " twice"
	}
	return fmt.Sprintf(" %d times", n)
}

func plural(n int, tool string) string {
	one, many := tool, tool+"s"
	if tool == "grep" {
		one, many = "search", "searches"
	}
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func (s *session) record(c model.ToolCall, out string) {
	if s.log == nil {
		return
	}
	b, _ := json.Marshal(struct {
		Tool   string          `json:"tool"`
		Args   json.RawMessage `json:"args"`
		Result string          `json:"result"`
	}{c.Name, c.Args, core.Clip(out, 2000)})
	_, _ = s.log.Write(append(b, '\n'))
}
