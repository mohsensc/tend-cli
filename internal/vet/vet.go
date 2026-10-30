// Package vet is the skeptical pass over a patch before it leaves the machine.
package vet

import (
	"cmp"
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/api"
	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/gitx"
)

type Input struct {
	Dir    string // worktree
	Base   string
	Change *core.Change
	Patch  *core.Patch
	Proof  *core.Proof
	Index  core.ScanIndex // worktree index after the patch
	Ecos   map[string]core.Ecosystem
	Model  core.Model
	Fetch  core.Fetcher
	Spec   *api.Doc // API changes: the provider's current spec, nil when it could not be loaded
	Hosts  []string // API changes: the provider's hosts, to find endpoint literals still in use
	Option int      // the option a maintainer picked, 0 for none
	// Commands are the project's install, build and test lines as the base
	// has them, which the proof ran; Detect reads them again from the
	// patched worktree. Nil skips the question.
	Commands core.Commands
	Detect   func() core.Commands
}

// applied is the option the patch carries: the maintainer's pick, else the
// one tend took by its own rule, nil for none.
func (in Input) applied() *core.Option {
	ch, n := in.Change, in.Option
	if n == 0 && ch.Choice != nil {
		n = ch.Choice.N
	}
	if n < 1 || n > len(ch.Options) {
		return nil
	}
	return &ch.Options[n-1]
}

// A check asks one question of the patch. It returns what it verified, or why
// it could not run. A quiet check is left out of the list when it finds
// nothing: its question rarely arises, and a line saying so on every PR is
// noise.
type check struct {
	name  string
	fn    func(context.Context, *pass) (res []core.Finding, skip string)
	quiet bool
}

// scope runs first, so no later check reports a file it reverts.
var checks = []check{
	{name: "scope", fn: scope},
	{name: "symbols", fn: symbols},
	{name: "fields", fn: fields},
	{name: "leftovers", fn: leftovers},
	{name: "lockfile", fn: lockfile},
	{name: "debug", fn: debug},
	{name: "comments", fn: comments},
	{name: "commands", fn: commands, quiet: true},
	{name: "tests", fn: tests, quiet: true},
	{name: "dead", fn: dead},
	{name: "question", fn: questions},
}

// pass is the state the checks share: the patch as a parsed diff, kept current
// when a check edits the worktree.
type pass struct {
	in    Input
	files []*fileDiff
	model map[string]bool  // files a model edited
	drops map[string][]int // lines removed by the current check, by file
	dirty bool
	note  string              // what the current check verified, in a few words
	known []string            // what symbols and fields verified, for the question check
	decls map[string][]string // new declarations of the calls a patch wrote, by file
}

// na is the reason a check gives when its question does not arise for this
// patch, as opposed to a check that could not run.
const na = "not applicable"

// Check runs every check in order. Vetting.Checks has one entry per check:
// "name: what it verified" (or just the name), "name: not applicable", or
// "name: not run: why".
func Check(ctx context.Context, in Input) (*core.Vetting, error) {
	if in.Change == nil || in.Patch == nil {
		return nil, fmt.Errorf("vet: no change or patch")
	}
	p := newPass(in)
	res := &core.Vetting{}
	for _, c := range checks {
		p.note = ""
		found, skip := c.fn(ctx, p)
		if c.quiet && len(found) == 0 {
			continue
		}
		switch {
		case skip == na:
			res.Checks = append(res.Checks, c.name+": "+na)
			continue
		case skip != "":
			res.Checks = append(res.Checks, c.name+": not run: "+skip)
			continue
		case p.note != "":
			res.Checks = append(res.Checks, c.name+": "+p.note)
		default:
			res.Checks = append(res.Checks, c.name)
		}
		if len(found) == 0 && (c.name == "symbols" || c.name == "fields") {
			p.known = append(p.known, cmp.Or(p.note, clean[c.name]))
		}
		res.Findings = append(res.Findings, found...)
		shift(res.Findings, p.drops)
		clear(p.drops)
		if !p.dirty {
			continue
		}
		res.Changed = true
		p.dirty = false
		if err := p.refresh(ctx); err != nil {
			return nil, fmt.Errorf("vet: %s: %w", c.name, err)
		}
	}
	res.Verdict = Verdict(res, in.Proof)
	return res, nil
}

// code says whether the patch touches a source file, and not only manifests,
// lockfiles and version pins.
func (p *pass) code() bool {
	return slices.ContainsFunc(p.files, func(f *fileDiff) bool { return langOf(f.Path()) != "" })
}

func newPass(in Input) *pass {
	p := &pass{in: in, model: map[string]bool{}, drops: map[string][]int{}}
	for _, f := range in.Patch.Edited {
		p.model[f] = true
	}
	p.load(in.Patch.Diff)
	return p
}

func (p *pass) load(diff string) {
	p.files = parseDiff(diff)
	explained := p.depFiles()
	for _, f := range p.files {
		for _, h := range f.Hunks {
			if len(p.model) > 0 {
				h.Model = p.in.Patch.Model && p.model[h.File]
				continue
			}
			// No file list from the patch: anything beyond the manifests is the model's.
			h.Model = p.in.Patch.Model && !explained[h.File]
		}
	}
}

func (p *pass) refresh(ctx context.Context) error {
	if _, err := gitx.Raw(ctx, p.in.Dir, "add", "-A"); err != nil {
		return err
	}
	diff, err := gitx.Raw(ctx, p.in.Dir, "diff", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames", "--src-prefix=a/", "--dst-prefix=b/", p.in.Base)
	if err != nil {
		return err
	}
	p.load(diff)
	return nil
}

// depFiles are the manifests and lockfiles of the changed deps.
func (p *pass) depFiles() map[string]bool {
	res := map[string]bool{}
	for _, d := range p.in.Change.Deps {
		m := d.Manifest
		if m.Path != "" {
			res[m.Path] = true
		}
		if m.Lock != "" {
			res[m.Lock] = true
		}
		if m.Ecosystem == core.Go {
			res[path.Join(path.Dir(m.Path), "go.sum")] = true
		}
	}
	return res
}

// drop removes lines from a file in the worktree and remembers them, so the
// line numbers of every finding can follow.
func (p *pass) drop(file string, nums []int) error {
	if err := dropLines(p.in.Dir, file, nums); err != nil {
		return err
	}
	p.drops[file] = append(p.drops[file], nums...)
	p.dirty = true
	return nil
}

// replace swaps the text of one line in the worktree for text, at the line's
// indentation. Line numbers do not move.
func (p *pass) replace(file string, n int, text string) error {
	if err := replaceLine(p.in.Dir, file, n, text); err != nil {
		return err
	}
	p.dirty = true
	return nil
}

// shift moves findings up past the lines removed above them. A removed line
// ends up at the position it had in the final file.
func shift(res []core.Finding, drops map[string][]int) {
	for i, f := range res {
		n := 0
		for _, d := range drops[f.File] {
			if d < f.Line {
				n++
			}
		}
		res[i].Line -= n
	}
}

func (p *pass) file(name string) *fileDiff {
	for _, f := range p.files {
		if f.Path() == name {
			return f
		}
	}
	return nil
}

// added maps the new line numbers of a file's added lines to their text.
func (p *pass) added(name string) map[int]string {
	res := map[int]string{}
	if f := p.file(name); f != nil {
		for _, h := range f.Hunks {
			for _, l := range h.Lines {
				if l.Op == '+' {
					res[l.New] = l.Text
				}
			}
		}
	}
	return res
}

func (p *pass) changed() []string {
	var res []string
	for _, f := range p.files {
		res = append(res, f.Path())
	}
	return res
}

func finding(check, file string, line int, action, format string, args ...any) core.Finding {
	return core.Finding{Check: check, File: file, Line: line, Msg: fmt.Sprintf(format, args...), Action: action}
}

func sortFindings(res []core.Finding) {
	slices.SortStableFunc(res, func(a, b core.Finding) int {
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		return a.Line - b.Line
	})
}
