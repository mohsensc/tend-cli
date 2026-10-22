// Package patch applies a change to a worktree: bump, codemods, then a model migration if needed.
package patch

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/gitx"
	"github.com/mohsensc/tend-cli/internal/model"
	"github.com/mohsensc/tend-cli/internal/scan"
)

type Options struct {
	Ecos     map[string]core.Ecosystem
	Index    core.ScanIndex // index of the worktree before the patch
	Model    core.Model     // nil = deterministic only
	Attempts int
	Option   int  // 0 = tend's choice; N = maintainer picked option N
	Proven   bool // the project's own build and tests will run on the result
	// Strict makes a bump that would leave call sites for the proof to judge
	// a decision instead: a tidy-up carries only moves that are made.
	Strict bool
	// Bumped is called once the deps are bumped, before codemods or a model
	// touch the code.
	Bumped func(context.Context) error
	Log    func(string, ...any)

	// A model that calls tools works the change itself, with the project's
	// commands and a budget.
	Commands   core.Commands
	Prove      func(context.Context, core.Commands) (*core.Run, error)
	Turns      int
	Tokens     int
	Transcript string // file the tool calls are written to, "" for none
}

func (o Options) logf(format string, args ...any) {
	if o.Log != nil {
		o.Log(format, args...)
	}
}

// head is the commit the worktree sits on; Apply never commits, so it stays the base.
const head = "HEAD"

const undecided = "no mechanical fix; options listed"

// rescan builds a fresh index of the worktree. Tests replace it.
var rescan = func(wt string) (core.ScanIndex, error) {
	return scan.Build(scan.Options{Root: wt})
}

// Apply patches the worktree wt for ch and stages the result. It does not commit.
//
// Without a model, call sites a bump leaves broken stay as they are: a bump the
// project's tests will prove goes out and a failed proof makes it a decision;
// one nothing can prove becomes a decision right away. A security fix always
// goes out as a plain bump, since leaving the hole open is worse. A bump the
// ecosystem refuses, such as a version a parent POM outside the repo manages
// or a digest pin the registry cannot move, becomes a decision too.
func Apply(ctx context.Context, wt string, ch *core.Change, o Options) (*core.Patch, error) {
	opt, err := choose(ch, o.Option)
	if err != nil {
		return nil, err
	}
	to, toName := ch.To, ch.ToName
	var cm *core.Codemod
	if opt != nil {
		cm = opt.Codemod
		if opt.To != "" {
			to = opt.To
		}
		if opt.ToName != "" {
			toName = opt.ToName
		}
	}
	if len(ch.Deps) > 0 && to == "" && toName == "" {
		return decide(ctx, wt, "There is no single replacement to move to, so the choice is yours.")
	}
	if opt != nil && stays(ch, opt) {
		file, err := pin(wt, ch.Deps[0], ch.To)
		if err != nil {
			return decide(ctx, wt, fmt.Sprintf("Pinning did not work out (%v); add the snippet under the option by hand.", err))
		}
		p := &core.Patch{Applied: []string{fmt.Sprintf("pinned %s at %s, ignoring %s and later (%s)", ch.Deps[0].Name, opt.To, ch.To, file)}}
		return finish(ctx, wt, p)
	}

	p := &core.Patch{}
	bumped, err := bump(ctx, wt, ch.Deps, to, toName, o, p)
	if errors.Is(err, core.ErrNotSupported) {
		return decide(ctx, wt, "tend cannot make this change itself: "+refusal(err)+". Make it by hand, or comment `tend skip`.")
	}
	if err != nil {
		return nil, err
	}
	if o.Bumped != nil {
		if err := o.Bumped(ctx); err != nil {
			return nil, err
		}
	}

	t := &tree{wt: wt}
	switch x, ok := o.Index.(*scan.Index); {
	case ok:
		t.idx = x.With(wt, bumped)
	case reusable(o.Index, bumped):
		t.idx = o.Index
	}
	if cm != nil {
		lines, err := codemod(t, cm, ch.Sites)
		if err != nil {
			return nil, err
		}
		p.Applied = append(p.Applied, lines...)
	}

	rest := remaining(ctx, t, ch.Sites)
	if len(rest) == 0 || !needsCode(ch, opt) {
		return finish(ctx, wt, p)
	}
	if o.Model != nil {
		in := job{ch: ch, to: to, toName: toName, cm: cm, rest: rest, allowed: allowed(ch, bumped)}
		a, agent := o.Model.(model.Agent)
		agent = agent && o.Prove != nil
		var err error
		if agent {
			err = work(ctx, t, in, o, p, a)
		} else {
			err = migrate(ctx, t, in, o, p)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if agent {
				return decide(ctx, wt, "The model did not get there: "+err.Error()+".")
			}
			return decide(ctx, wt, "The model's attempts did not apply: "+err.Error()+".")
		}
		return finish(ctx, wt, p)
	}
	if ch.Kind == core.Security || len(bumped) > 0 && o.Proven && !o.Strict {
		p.Applied = append(p.Applied, fmt.Sprintf("%s left as written, with no model to move them", sites(len(rest))))
		return finish(ctx, wt, p)
	}
	why := ""
	switch {
	case len(bumped) == 0:
		why = ", and there is nothing to bump"
	case !o.Proven:
		why = ", and there is no build or test command to prove a plain bump"
	}
	return decide(ctx, wt, fmt.Sprintf("No model is configured to move the %s%s.", sites(len(rest)), why))
}

func sites(n int) string {
	if n == 1 {
		return "call site"
	}
	return fmt.Sprintf("%d call sites", n)
}

// stays reports whether opt keeps the version in use: the way off a deprecation
// that is no way off at all.
func stays(ch *core.Change, opt *core.Option) bool {
	return opt.ToName == "" && opt.Codemod == nil && opt.To != "" && opt.To == from(ch.Deps[0])
}

// reusable reports whether idx still describes the worktree: the bump only moved
// manifests and lockfiles, which the index does not read.
func reusable(idx core.ScanIndex, bumped []string) bool {
	if idx == nil {
		return false
	}
	files := idx.Files()
	return !slices.ContainsFunc(bumped, func(f string) bool { return slices.Contains(files, f) })
}

// choose returns the option to apply: the maintainer's pick, else the first safe one.
func choose(ch *core.Change, n int) (*core.Option, error) {
	if n > 0 {
		if n > len(ch.Options) {
			return nil, fmt.Errorf("patch: option %d of %d", n, len(ch.Options))
		}
		return &ch.Options[n-1], nil
	}
	for i := range ch.Options {
		if ch.Options[i].Safe {
			return &ch.Options[i], nil
		}
	}
	return nil, nil
}

// needsCode reports whether sites left untouched by the deterministic steps must change.
// A safe option is trusted to be complete; the proof says whether it was.
func needsCode(ch *core.Change, opt *core.Option) bool {
	if opt != nil {
		return !opt.Safe
	}
	return ch.Kind == core.Breaking || ch.Kind == core.Deprecation || ch.Level == "major"
}

// remaining returns the sites whose line still reads as it does at the base commit.
func remaining(ctx context.Context, t *tree, sites []core.Site) []core.Site {
	base := map[string][]byte{}
	var res []core.Site
	for _, s := range sites {
		old, ok := base[s.File]
		if !ok {
			b, _ := gitx.Raw(ctx, t.wt, "show", head+":"+s.File)
			old = []byte(b)
			base[s.File] = old
		}
		curr := t.read(s.File)
		if curr == nil || line(old, s.Line) != line(curr, s.Line) {
			continue
		}
		res = append(res, s)
	}
	return res
}

// allowed lists the files a model may edit: the site files, the manifests and what the bump touched.
func allowed(ch *core.Change, bumped []string) []string {
	var res []string
	for _, s := range ch.Sites {
		res = append(res, s.File)
	}
	for _, d := range ch.Deps {
		res = append(res, d.Manifest.Path)
		if d.Manifest.Lock != "" {
			res = append(res, d.Manifest.Lock)
		}
	}
	res = append(res, bumped...)
	return uniq(res)
}

// decide throws away whatever was applied and leaves the choice to the maintainer.
// refusal is why an ecosystem refused a bump, without the sentinel.
func refusal(err error) string {
	return strings.TrimSuffix(err.Error(), ": "+core.ErrNotSupported.Error())
}

func decide(ctx context.Context, wt, note string) (*core.Patch, error) {
	if _, err := gitx.Raw(ctx, wt, "reset", "-q", "--hard", head); err != nil {
		return nil, fmt.Errorf("patch: %w", err)
	}
	if _, err := gitx.Raw(ctx, wt, "clean", "-fdq"); err != nil {
		return nil, fmt.Errorf("patch: %w", err)
	}
	return &core.Patch{Decision: true, Applied: []string{undecided}, Note: note}, nil
}

func finish(ctx context.Context, wt string, p *core.Patch) (*core.Patch, error) {
	if _, err := gitx.Raw(ctx, wt, "add", "-A"); err != nil {
		return nil, fmt.Errorf("patch: %w", err)
	}
	diff, files, err := Diff(ctx, wt, head)
	if err != nil {
		return nil, fmt.Errorf("patch: %w", err)
	}
	p.Diff, p.Files = diff, files
	return p, nil
}

// line returns the 1-based line n of src without its newline, "" when out of range.
func line(src []byte, n int) string {
	start, end, ok := lineSpan(src, n)
	if !ok {
		return ""
	}
	return strings.TrimSuffix(string(src[start:end]), "\r")
}
