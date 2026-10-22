package deliver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/gitx"
	"github.com/mohsensc/tend-cli/internal/verify"
)

// ErrForeign means the PR's branch has commits tend did not push. tend leaves
// the branch and the PR as they are.
var ErrForeign = errors.New("the PR branch has commits tend did not push")

const foreignNote = "<!-- tend foreign -->"

// Foreign reports whether tend already said, in these comments, that the
// PR's branch is no longer its own.
func Foreign(cs []core.Comment) bool {
	return slices.ContainsFunc(cs, func(c core.Comment) bool { return strings.Contains(c.Body, foreignNote) })
}

// SendInput is what Send needs beyond the worktree: the PR body's content, the
// delivery config and the remote to push to.
type SendInput struct {
	BodyInput
	Remote *Remote
	Cfg    config.Deliver
	// Last is the commit tend last pushed to the PR's branch, if state knows
	// it. An update is refused when the branch has moved off it.
	Last string
}

// Send pushes the worktree's commit and creates the PR for it, or updates the
// open one. A re-run that changes nothing leaves the PR alone.
func Send(ctx context.Context, forge core.Forge, wt *verify.Worktree, in SendInput) (*core.PR, error) {
	ch := in.Change
	branch := Branch(ch)
	title := Title(in.BodyInput, in.Cfg.TitlePrefix)
	draft := wantDraft(in)

	tip, err := head(ctx, wt, "HEAD")
	if err != nil {
		return nil, err
	}
	if start, err := head(ctx, wt, wt.Base); err == nil && start == tip {
		return nil, errors.New("deliver: the worktree has no commit on top of its base")
	}
	remote := in.Remote
	if remote == nil {
		return nil, errors.New("deliver: no remote to push to")
	}
	if in.BaseRef == "" {
		info, err := forge.Repo(ctx)
		if err != nil {
			return nil, err
		}
		in.BaseRef = info.Default
	}
	pr, err := forge.FindPR(ctx, branch)
	if err != nil {
		return nil, err
	}
	if pr != nil && pr.State == "open" {
		return updatePR(ctx, forge, wt, in, pr, remote, title, draft)
	}
	return openPR(ctx, forge, wt, in, remote, branch, title, draft)
}

func openPR(ctx context.Context, forge core.Forge, wt *verify.Worktree, in SendInput, remote *Remote, branch, title string, draft bool) (*core.PR, error) {
	exists, err := BranchExists(ctx, remote, branch)
	if err != nil {
		return nil, err
	}
	if err := Push(ctx, wt, remote, branch, exists); err != nil {
		return nil, err
	}
	tip, err := head(ctx, wt, "HEAD")
	if err != nil {
		return nil, err
	}
	in.Rev = 1
	body := Body(in.BodyInput)
	pr, err := forge.CreatePR(ctx, core.PRInput{
		Title: title, Body: body, Branch: branch, Base: in.BaseRef, Draft: draft,
		Labels: in.Cfg.Labels, Reviewers: in.Cfg.Reviewers,
	})
	if err != nil {
		return pr, err
	}
	pr.Head = tip
	return merge(ctx, forge, in, pr, title, draft)
}

func updatePR(ctx context.Context, forge core.Forge, wt *verify.Worktree, in SendInput, pr *core.PR, remote *Remote, title string, draft bool) (*core.PR, error) {
	branch := pr.Branch
	same, tip, err := SameTree(ctx, wt, remote, branch)
	if err != nil {
		same, tip = false, ""
	}
	_, base, rev, ok := ParseMarker(pr.Body)
	if !ok {
		rev = 1
	}
	res := *pr
	res.Head = tip
	if same && ok && base == in.Base && pr.Title == title && pr.Draft == draft {
		return &res, nil
	}
	if !same {
		last := in.Last
		if tip != "" {
			if !ours(ctx, remote, wt, pr, tip, in.Last) {
				return nil, foreign(ctx, forge, pr)
			}
			last = tip
		}
		err := push(ctx, wt, remote, branch, true, last)
		if errors.Is(err, ErrRejected) {
			return nil, foreign(ctx, forge, pr)
		}
		if err != nil {
			return nil, err
		}
		if res.Head, err = head(ctx, wt, "HEAD"); err != nil {
			return nil, err
		}
	}
	in.Rev = rev + 1
	body := Body(in.BodyInput)
	err = forge.UpdatePR(ctx, pr.Number, core.PRInput{Title: title, Body: body, Branch: branch, Base: pr.Base, Draft: draft})
	if err != nil {
		return nil, err
	}
	res.Title, res.Body, res.Draft = title, body, draft
	return merge(ctx, forge, in, &res, title, draft)
}

// ours reports whether tip, the commit the PR's branch is at, holds nothing
// but tend's own work. The forge decides, not state, which may be stale or
// lost: the PR is tend's when its branch is under tend/ and its body carries
// the marker, and tip is then tend's when it is the one commit tend makes,
// straight on the base the marker names, or adds nothing to the base tend
// builds on. A commit pushed on top, or a merge of the base branch, is
// someone else's. The last push state knows of counts as well.
func ours(ctx context.Context, r *Remote, wt *verify.Worktree, pr *core.PR, tip, last string) bool {
	if tip == last {
		return true
	}
	_, base, _, ok := ParseMarker(pr.Body)
	if !ok || !strings.HasPrefix(pr.Branch, branchPrefix) {
		return false
	}
	if _, err := gitx.Run(ctx, r.repo, "merge-base", "--is-ancestor", tip, wt.Base); err == nil {
		return true
	}
	out, err := gitx.Run(ctx, r.repo, "rev-list", "--parents", "-n", "1", tip)
	if err != nil {
		return false
	}
	f := strings.Fields(out)
	return len(f) == 2 && base != "" && f[1] == base
}

// foreign says once on the PR that its branch has someone else's commits,
// which tend will not push over, and returns ErrForeign.
func foreign(ctx context.Context, forge core.Forge, pr *core.PR) error {
	cs, err := forge.Comments(ctx, pr.Number)
	if err == nil && !Foreign(cs) {
		msg := "This branch has commits tend did not push, so tend leaves it as it is and stops updating this PR. Merge it, or close it to drop the change.\n\n" + foreignNote
		err = forge.Comment(ctx, pr.Number, msg)
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%w (#%d)", ErrForeign, pr.Number)
}

func merge(ctx context.Context, forge core.Forge, in SendInput, pr *core.PR, title string, draft bool) (*core.PR, error) {
	if draft || !mergeable(in) {
		return pr, nil
	}
	method := in.Cfg.MergeMethod
	if method == "" {
		method = "squash"
	}
	err := forge.AutoMerge(ctx, pr, method)
	if err == nil {
		return pr, nil
	}
	in.Notes = append(slices.Clone(in.Notes), fmt.Sprintf("Auto-merge was not enabled: %v", err))
	body := Body(in.BodyInput)
	uerr := forge.UpdatePR(ctx, pr.Number, core.PRInput{Title: title, Body: body, Branch: pr.Branch, Base: pr.Base, Draft: draft})
	if uerr != nil {
		return pr, uerr
	}
	pr.Body = body
	return pr, nil
}

func wantDraft(in SendInput) bool {
	if in.Cfg.Draft {
		return true
	}
	// A decision is a question for the maintainer; its failing proof is the
	// reason it was asked, not a reason to hide it.
	if p := in.Patch; p != nil && p.Decision {
		return false
	}
	if v := in.Vetting; v != nil && v.Verdict == "draft" {
		return true
	}
	if p := in.Proof; p != nil {
		return p.Base != nil && !p.Base.Pass || p.Patched != nil && !p.Patched.Pass
	}
	return false
}

// mergeable says whether deliver.automerge covers this change. The setting
// names a ceiling: "patch" covers patch-level bumps, "minor" patch and minor,
// "security" every security fix short of a major bump. Nothing unproven,
// undecided, vetted below "pass" or written in part by a model is ever merged
// automatically: a model reads upstream prose anyone can publish, and may
// have edited the very tests that proved its work. Nor is a major bump or an
// API change, security fix or not: tests passing says little about what a
// new major does at run time.
func mergeable(in SendInput) bool {
	ch, p := in.Change, in.Patch
	if p == nil || p.Decision || p.Model || in.Vetting == nil || in.Vetting.Verdict != "pass" {
		return false
	}
	if ch.Level == "major" || ch.API != nil {
		return false
	}
	if in.Proof == nil || in.Proof.Nothing || in.Proof.Patched == nil || !in.Proof.Patched.Pass {
		return false
	}
	switch in.Cfg.AutoMerge {
	case "security":
		return ch.Kind == core.Security
	case "patch":
		return ch.Level == "patch"
	case "minor":
		return ch.Level == "patch" || ch.Level == "minor"
	}
	return false
}
