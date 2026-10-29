package vet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/eco"
	"github.com/mohsensc/tend-cli/internal/gitx"
)

// lockfile: did the lockfile change more than this change needs?
func lockfile(ctx context.Context, p *pass) ([]core.Finding, string) {
	var locks []string
	byLock := map[string][]core.Dep{}
	for _, d := range p.in.Change.Deps {
		lock := d.Manifest.Lock
		// go.sum holds checksums, not choices: go.mod is where Go pins versions.
		if lock == "" || path.Base(lock) == "go.sum" || p.file(lock) == nil {
			continue
		}
		if byLock[lock] == nil {
			locks = append(locks, lock)
		}
		byLock[lock] = append(byLock[lock], d)
	}
	if len(locks) == 0 {
		return nil, na
	}
	var res []core.Finding
	var moved []string
	checked := 0
	for _, lock := range locks {
		f, names, ok := checkLock(ctx, p, lock, byLock[lock])
		if ok {
			checked++
		}
		res = append(res, f...)
		for _, n := range names {
			if !slices.Contains(moved, n) {
				moved = append(moved, n)
			}
		}
	}
	if checked == 0 {
		return nil, "tend cannot read " + strings.Join(locks, ", ")
	}
	if len(res) == 0 {
		p.note = moves(p.in.Change, moved)
	}
	return res, ""
}

// moves says which packages a clean lockfile change moved: only the bumped
// ones the lockfile names, or those and so many of their dependencies.
func moves(ch *core.Change, moved []string) string {
	in := func(n string) bool {
		return len(moved) == 0 || slices.ContainsFunc(moved, func(m string) bool { return strings.EqualFold(m, n) })
	}
	var roots []string
	for _, d := range ch.Deps {
		if !slices.Contains(roots, d.Name) && in(d.Name) {
			roots = append(roots, d.Name)
		}
	}
	if ch.ToName != "" && in(ch.ToName) {
		roots = append(roots, ch.ToName)
	}
	if len(roots) == 0 {
		return "only the bumped packages and their dependencies changed"
	}
	rest := 0
	for _, n := range moved {
		if !slices.ContainsFunc(roots, func(r string) bool { return strings.EqualFold(r, n) }) {
			rest++
		}
	}
	names := strings.Join(roots, ", ")
	if len(roots) > 8 {
		names = fmt.Sprintf("the %d bumped packages", len(roots))
	}
	its := "its"
	if len(roots) > 1 {
		its = "their"
	}
	switch {
	case rest == 1:
		return fmt.Sprintf("only %s and 1 of %s dependencies changed", names, its)
	case rest > 1:
		return fmt.Sprintf("only %s and %d of %s dependencies changed", names, rest, its)
	}
	return "only " + names + " changed"
}

// checkLock compares one lockfile before and after the patch. It returns the
// findings, the packages whose versions moved, and whether it could tell.
func checkLock(ctx context.Context, p *pass, lock string, deps []core.Dep) ([]core.Finding, []string, bool) {
	base, err := gitx.Raw(ctx, p.in.Dir, "show", p.in.Base+":"+lock)
	if err != nil {
		return nil, nil, false
	}
	file := filepath.Join(p.in.Dir, filepath.FromSlash(lock))
	patched, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, false
	}
	ecosystem := deps[0].Ecosystem
	var roots []string
	for _, d := range deps {
		roots = append(roots, eco.Canon(ecosystem, d.Name))
	}
	if p.in.Change.ToName != "" {
		roots = append(roots, eco.Canon(ecosystem, p.in.Change.ToName))
	}
	extra, err := unrelated(lock, []byte(base), patched, roots)
	if err != nil {
		return nil, nil, false
	}
	if len(extra) == 0 {
		return nil, moved(lock, []byte(base), patched), true
	}
	m := deps[0].Manifest
	if ecosystem != core.Npm {
		return []core.Finding{finding("lockfile", lock, 0, "flagged", "unrelated lockfile churn: %s", names(extra))}, nil, true
	}
	why := fixLock(ctx, p, file, lock, []byte(base), patched, m, deps, roots)
	if why == "" {
		return []core.Finding{finding("lockfile", lock, 0, "fixed", "relocked from base to drop unrelated churn: %s", names(extra))}, nil, true
	}
	return []core.Finding{finding("lockfile", lock, 0, "flagged", "unrelated lockfile churn: %s (%s)", names(extra), why)}, nil, true
}

// moved lists the packages whose versions differ between two lockfiles.
func moved(lock string, base, patched []byte) []string {
	old, err := parseLock(lock, base)
	if err != nil {
		return nil
	}
	now, err := parseLock(lock, patched)
	if err != nil {
		return nil
	}
	return churn(old, now)
}

// unrelated lists packages whose versions changed between the two lockfiles
// although they are neither a root nor reachable from one, before or after.
func unrelated(lock string, base, patched []byte, roots []string) ([]string, error) {
	old, err := parseLock(lock, base)
	if err != nil {
		return nil, err
	}
	now, err := parseLock(lock, patched)
	if err != nil {
		return nil, err
	}
	ok := old.closure(roots)
	for n := range now.closure(roots) {
		ok[n] = true
	}
	var res []string
	for _, n := range churn(old, now) {
		if !ok[n] {
			res = append(res, n)
		}
	}
	return res, nil
}

// fixLock puts the base lockfile back and has the package manager lock again
// against the patched manifest. It keeps the result only when the churn is gone
// and the bumped packages still have the versions the patch gave them; it
// returns why it gave up otherwise.
func fixLock(ctx context.Context, p *pass, file, lock string, base, patched []byte, m core.Manifest, deps []core.Dep, roots []string) string {
	if err := os.WriteFile(file, base, 0o644); err != nil {
		return err.Error()
	}
	why := ""
	if err := relock(ctx, p.in.Dir, m, deps, bytes.Contains(base, []byte("__metadata:"))); err != nil {
		why = "relock failed: " + err.Error()
	} else if now, err := os.ReadFile(file); err != nil {
		why = err.Error()
	} else if extra, err := unrelated(lock, base, now, roots); err != nil || len(extra) > 0 {
		why = "relocking did not remove it"
	} else if !sameRoots(lock, patched, now, roots) {
		why = "relocking lost the bump"
	}
	if why != "" {
		if err := os.WriteFile(file, patched, 0o644); err != nil {
			p.dirty = true
			return why + "; restoring the patched lockfile failed: " + err.Error()
		}
		return why
	}
	p.dirty = true
	return ""
}

func sameRoots(lock string, a, b []byte, roots []string) bool {
	ga, err := parseLock(lock, a)
	if err != nil {
		return false
	}
	gb, err := parseLock(lock, b)
	if err != nil {
		return false
	}
	for _, r := range roots {
		if !sameSet(ga.vers[r], gb.vers[r]) {
			return false
		}
	}
	return true
}

// relock regenerates an npm, pnpm or yarn lockfile from the one on disk, as
// narrowly as the tool allows. A variable so tests do not need the tools.
var relock = func(ctx context.Context, dir string, m core.Manifest, deps []core.Dep, berry bool) error {
	line := relockLine(m.Tool, deps, berry)
	tool := strings.Fields(line)[0]
	if _, err := exec.LookPath(tool); err != nil {
		return core.ToolMissing{Tool: tool}
	}
	out, err := eco.Run(ctx, filepath.Join(dir, filepath.FromSlash(path.Dir(m.Path))), line)
	if err != nil {
		msg := strings.TrimSpace(out)
		if msg == "" {
			msg = err.Error()
		}
		if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
			msg = msg[i+1:]
		}
		return errors.New(line + ": " + msg)
	}
	return nil
}

// relockLine: direct deps are already rewritten in the manifest, so a plain
// lock-only install resolves just them; transitive ones need a targeted update.
func relockLine(tool string, deps []core.Dep, berry bool) string {
	var names []string
	direct := true
	for _, d := range deps {
		names = append(names, "'"+d.Name+"'")
		direct = direct && d.Direct
	}
	pkgs := strings.Join(names, " ")
	switch {
	case tool == "pnpm" && direct:
		return "pnpm install --lockfile-only --ignore-scripts"
	case tool == "pnpm":
		return "pnpm update " + pkgs + " --lockfile-only --ignore-scripts"
	case tool == "yarn" && berry && direct:
		return "yarn install --mode update-lockfile"
	case tool == "yarn" && berry:
		return "yarn up -R " + pkgs + " --mode update-lockfile"
	case tool == "yarn" && direct:
		return "yarn install --ignore-scripts --non-interactive"
	case tool == "yarn":
		return "yarn upgrade " + pkgs + " --ignore-scripts --non-interactive"
	case direct:
		return "npm install --package-lock-only --ignore-scripts --no-audit --no-fund"
	}
	return "npm update " + pkgs + " --package-lock-only --ignore-scripts --no-audit --no-fund"
}

func names(list []string) string {
	if len(list) <= 8 {
		return strings.Join(list, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(list[:8], ", "), len(list)-8)
}
