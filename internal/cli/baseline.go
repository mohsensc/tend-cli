package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/gitx"
	"github.com/mohsensc/tend-cli/internal/model"
	"github.com/mohsensc/tend-cli/internal/patch"
	"github.com/mohsensc/tend-cli/internal/scan"
	"github.com/mohsensc/tend-cli/internal/verify"
)

// probeTimeout bounds loading one module or running one check on the base.
const probeTimeout = 2 * time.Minute

// groundwork gives the first catch-up PR a test baseline when the project's
// tests are too thin to prove the catch-up by, before anything else is
// made: the modules that load on the base commit, loaded again by a smoke
// test; the typecheck its ecosystem offers when it passes on the base; and
// tests a model writes of how the files the catch-up changes behave now.
// Whatever does not pass on the base is left out. The baseline goes into
// the trial's commands and becomes the base the patch is proven against.
// It is nil when the tests are not thin, or tend cannot add a baseline.
func (r *runner) groundwork(ctx context.Context, ch *core.Change, t *trial) (*part, error) {
	s, wt := r.s, r.wt
	files := touched(ch.Batch)
	why := weak(t.cmds, r.inv.Index, files)
	if why == "" || len(ch.Batch.Changes)+len(ch.Batch.Next) == 0 {
		return nil, nil
	}
	t.thin = why
	su := r.suite()
	if su == nil {
		s.logf("the tests are thin (%s), and tend has no way to add a baseline for this project", why)
		return nil, nil
	}
	s.logf("the tests are thin (%s); adding a baseline first", why)
	if err := reset(ctx, wt); err != nil {
		return nil, err
	}
	if _, err := r.prove(ctx, core.Commands{Install: t.cmds.Install, Build: t.cmds.Build}, "baseline"); err != nil {
		return nil, err
	}
	bl := &core.Baseline{Why: why}
	pt := &core.Patch{}
	var loaded []string
	if su.Smoke != "" {
		for _, m := range su.Modules {
			if r.probe(ctx, su.Load(m)) {
				loaded = append(loaded, m)
			}
		}
	}
	for _, c := range su.Checks {
		if r.probe(ctx, c) {
			bl.Checks = append(bl.Checks, c)
		}
	}
	if len(loaded) > 0 {
		if err := write(wt.Dir, su.Smoke, su.Code(loaded)); err != nil {
			return nil, err
		}
		bl.Files, bl.Modules = append(bl.Files, su.Smoke), len(loaded)
	}
	if r.model != nil && len(files) > 0 {
		cmds := withTest(t.cmds, runLine(su, bl, su.Tests))
		o := r.options(nil, cmds, t.proof)
		o.Commands = cmds
		o.Prove = func(ctx context.Context, c core.Commands) (*core.Run, error) { return r.prove(ctx, c, "baseline") }
		if _, ok := r.model.(model.Agent); ok {
			o.Turns, o.Tokens = s.cfg.Model.Turns, s.cfg.Model.Tokens
			o.Transcript = filepath.Join(s.dir, "logs", "baseline", "agent.jsonl")
			scale(&o, min(1+len(files)/5, shareMax))
		}
		cpt, err := patch.Characterize(ctx, wt.Dir, patch.Bench{Path: su.Tests, Lang: su.Lang, Files: files}, o)
		switch {
		case err != nil && ctx.Err() != nil:
			return nil, ctx.Err()
		case err != nil:
			s.logf("the model wrote no tests that pass on the base, so the baseline goes without them: %v", err)
			if err := os.Remove(filepath.Join(wt.Dir, filepath.FromSlash(su.Tests))); err != nil && !os.IsNotExist(err) {
				return nil, err
			}
		default:
			bl.Files, bl.Model = append(bl.Files, su.Tests), true
			pt.Model, pt.Edited, pt.Note = true, cpt.Edited, cpt.Note
		}
	}
	if len(bl.Files) == 0 && len(bl.Checks) == 0 {
		s.logf("nothing that would make a baseline passes on the base")
		return nil, reset(ctx, wt)
	}
	slices.Sort(bl.Files)
	written := slices.Clone(bl.Files)
	line := runLine(su, bl)
	if line != "" {
		f, err := patch.Test(wt.Dir, t.cmds.Test, line)
		if err != nil {
			s.logf("cannot add the baseline to tend.yml, so there is none: %v", err)
			return nil, reset(ctx, wt)
		}
		bl.Command, written = line, append(written, f)
	}
	pt.Applied = []string{"added a test baseline (" + strings.Join(written, ", ") + ")"}
	cmds := withTest(t.cmds, line)
	if _, err := gitx.Run(ctx, wt.Dir, "add", "-A"); err != nil {
		return nil, err
	}
	run, err := r.prove(ctx, cmds, "base")
	if err != nil {
		return nil, err
	}
	if !run.Pass {
		s.logf("the baseline does not pass on the base, so there is none: %s", broke(run))
		return nil, reset(ctx, wt)
	}
	code, _, err := patch.Diff(ctx, wt.Dir, wt.Base)
	if err != nil {
		return nil, err
	}
	t.cmds, t.proof.Base, t.proof.Nothing, t.thin = cmds, run, false, ""
	if line != "" {
		r.tests = []string{line}
	}
	return &part{n: -1, base: bl, pt: pt, code: code}, reset(ctx, wt)
}

// weak says why the project's tests cannot prove changes to files, or ""
// when they can: there is no test command, or a file is reached by no test.
func weak(cmds core.Commands, idx core.ScanIndex, files []string) string {
	if len(cmds.Test) == 0 {
		return "there is no test command"
	}
	x, ok := idx.(*scan.Index)
	if !ok {
		return ""
	}
	var bare []string
	for _, f := range files {
		if !x.Reached(f) {
			bare = append(bare, f)
		}
	}
	switch len(bare) {
	case 0:
		return ""
	case 1:
		return "no test reaches `" + bare[0] + "`, which the catch-up changes"
	}
	return fmt.Sprintf("no test reaches %d of the %d files the catch-up changes", len(bare), len(files))
}

// touched lists the code files outside tests that the changes in b, made
// now or in a later stage, call into.
func touched(b *core.Batch) []string {
	var res []string
	add := func(ch *core.Change) {
		for _, s := range core.Calls(ch) {
			if scan.Lang(s.File) != "" && !scan.IsTest(s.File) && !slices.Contains(res, s.File) {
				res = append(res, s.File)
			}
		}
	}
	for i := range b.Changes {
		add(&b.Changes[i])
	}
	for i := range b.Next {
		add(&b.Next[i].Change)
	}
	slices.Sort(res)
	return res
}

// suite is where the project's generated tests go: the manifest nearest
// the root whose ecosystem can run any, an example's or a fixture's
// package.json never ahead of the project's own.
func (r *runner) suite() *core.Suite {
	ms := slices.Clone(r.inv.Manifests)
	slices.SortStableFunc(ms, func(a, b core.Manifest) int {
		return strings.Count(a.Path, "/") - strings.Count(b.Path, "/")
	})
	for _, m := range ms {
		if b, ok := r.ecos[m.Ecosystem].(core.Baseliner); ok {
			if su := b.Suite(r.wt.Dir, m); su != nil {
				return su
			}
		}
	}
	return nil
}

// runLine is the test line that runs the baseline's files and those in
// more, after its checks, "" when the project's own test lines run them.
func runLine(su *core.Suite, bl *core.Baseline, more ...string) string {
	var lines []string
	lines = append(lines, bl.Checks...)
	if files := append(slices.Clone(bl.Files), more...); len(files) > 0 {
		if l := su.Run(files); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, " && ")
}

func withTest(cmds core.Commands, line string) core.Commands {
	if line != "" {
		cmds.Test = append(slices.Clone(cmds.Test), line)
	}
	return cmds
}

// probe runs one line on the worktree as it is and says whether it passed.
func (r *runner) probe(ctx context.Context, line string) bool {
	cfg := r.s.cfg.Verify
	run, err := verify.Run(ctx, r.wt, verify.Options{
		Commands:  core.Commands{Test: []string{line}},
		Timeout:   probeTimeout,
		Container: cfg.Container,
		Env:       cfg.Env,
		LogDir:    r.logs("probe"),
	})
	if err == nil {
		err = discard(ctx, r.wt)
	}
	return err == nil && run.Pass
}

func write(dir, rel, body string) error {
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(body), 0o644)
}
