package vet

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/gitx"
)

// scope: does the diff touch only files this change explains? Those are the
// manifests and lockfiles of the deps, files with affected call sites, files
// the patch's own steps name, and files the model was given to edit.
func scope(ctx context.Context, p *pass) ([]core.Finding, string) {
	ok := p.depFiles()
	for _, s := range p.in.Change.Sites {
		ok[s.File] = true
	}
	for _, f := range mentioned(p.in.Patch.Applied) {
		ok[f] = true
	}
	for f := range p.model {
		ok[f] = true
	}
	var res []core.Finding
	kept := 0
	for _, f := range p.files {
		file := f.Path()
		if ok[file] {
			kept++
			continue
		}
		if err := revert(ctx, p, f); err != nil {
			res = append(res, finding("scope", file, 0, "flagged", "not explained by this change, and reverting failed: %v", err))
			continue
		}
		p.dirty = true
		res = append(res, finding("scope", file, 0, "dropped", "not explained by this change; reverted"))
	}
	switch kept {
	case 0:
	case 1:
		p.note = "1 file, explained by this change"
	default:
		p.note = fmt.Sprintf("%d files, all explained by this change", kept)
	}
	return res, ""
}

func revert(ctx context.Context, p *pass, f *fileDiff) error {
	if f.Old != "" {
		_, err := gitx.Raw(ctx, p.in.Dir, "checkout", p.in.Base, "--", f.Old)
		return err
	}
	if _, err := gitx.Raw(ctx, p.in.Dir, "rm", "-q", "--cached", "--ignore-unmatch", "--", f.New); err != nil {
		return err
	}
	return os.Remove(filepath.Join(p.in.Dir, filepath.FromSlash(f.New)))
}
