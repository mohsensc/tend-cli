package deliver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/gitx"
	"github.com/mohsensc/tend-cli/internal/verify"
)

// ErrRejected means the push lost a race: the branch moved since tend last saw it.
var ErrRejected = errors.New("push rejected")

// ErrRefused means the remote will not take this push however often it is
// tried: a hook or a branch rule said no, or the token may not change
// workflow files.
var ErrRefused = errors.New("push refused")

// Remote is where tend pushes and fetches. Name is the remote whose
// remote-tracking refs tend keeps. With a token, the traffic goes to the
// GitHub URL with that token and with nothing the repository's config says
// about credentials; without one, it goes to the remote as configured, with
// the user's own credentials.
//
// Every call runs in the main repository, never in a worktree the project's
// commands ran in, and only while the repository's git settings are what
// they were when the Remote was made: a command tend ran could otherwise
// have pointed github.com somewhere else for the token to follow.
type Remote struct {
	Name, URL string

	repo, token, config string
}

// NewRemote is the remote name of the repository repo, with owner/slug on
// GitHub and the token for it when there is one. Make it before any of the
// project's commands run.
func NewRemote(ctx context.Context, repo, name, token, owner, slug string) (*Remote, error) {
	r := &Remote{Name: name, URL: name, repo: repo}
	if token != "" && owner != "" && slug != "" {
		r.URL, r.token = "https://github.com/"+owner+"/"+slug+".git", token
	}
	var err error
	r.config, err = settings(ctx, repo)
	return r, err
}

func settings(ctx context.Context, repo string) (string, error) {
	out, err := gitx.Raw(ctx, repo, "config", "--list", "--show-scope")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(out))
	return hex.EncodeToString(sum[:]), nil
}

// git runs one call that reaches the remote.
func (r *Remote) git(ctx context.Context, args ...string) (string, error) {
	now, err := settings(ctx, r.repo)
	if err != nil {
		return "", err
	}
	if now != r.config {
		return "", errors.New("git: the repository's git settings changed while the project's commands ran, so tend will not reach the remote with them")
	}
	out, err := (gitx.Cmd{Dir: r.repo, Env: r.auth(), Terminal: r.token == ""}).Run(ctx, args...)
	return strings.TrimSpace(out), err
}

// auth hands git the token for github.com for one call, after clearing any
// header a checkout left in the repository's config. Nothing is written to
// a config file.
func (r *Remote) auth() []string {
	if r.token == "" {
		return nil
	}
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + r.token))
	kv := [][2]string{
		{"http.extraheader", ""},
		{"http.https://github.com/.extraheader", ""},
		{"http.https://github.com/.extraheader", "AUTHORIZATION: basic " + basic},
		{"credential.helper", ""},
	}
	res := []string{fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(kv))}
	for i, p := range kv {
		res = append(res, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, p[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, p[1]))
	}
	return res
}

// Fetch brings the remote-tracking ref of branch up to date.
func (r *Remote) Fetch(ctx context.Context, branch string) error {
	_, err := r.git(ctx, "fetch", "--quiet", "--no-tags", r.URL, "+refs/heads/"+branch+":"+r.tracking(branch))
	return err
}

// Deepen makes sure a shallow clone, such as the one actions/checkout makes
// by default, has sha and the commit before it on branch's history. It first
// fetches what landed from a while before since, and when that is not
// enough, the whole history. A clone that is not shallow is left alone.
func (r *Remote) Deepen(ctx context.Context, branch, sha string, since time.Time) error {
	if out, err := gitx.Run(ctx, r.repo, "rev-parse", "--is-shallow-repository"); err != nil || out != "true" {
		return err
	}
	spec := "+refs/heads/" + branch + ":" + r.tracking(branch)
	after := since.Add(-48 * time.Hour).UTC().Format("2006-01-02 15:04:05 +0000")
	if _, err := r.git(ctx, "fetch", "--quiet", "--no-tags", "--shallow-since="+after, r.URL, spec); err == nil && r.has(ctx, sha+"^") {
		return nil
	}
	_, err := r.git(ctx, "fetch", "--quiet", "--no-tags", "--unshallow", r.URL, spec)
	return err
}

func (r *Remote) has(ctx context.Context, rev string) bool {
	_, err := gitx.Run(ctx, r.repo, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	return err == nil
}

func (r *Remote) tracking(branch string) string { return "refs/remotes/" + r.Name + "/" + branch }

// Push pushes HEAD of the worktree to refs/heads/<branch>. With force it
// overwrites the branch, but only if nobody else moved it since tend last
// saw it (--force-with-lease).
func Push(ctx context.Context, wt *verify.Worktree, r *Remote, branch string, force bool) error {
	return push(ctx, wt, r, branch, force, "")
}

// push is Push with an optional expected tip: with last set, the lease holds
// only while the remote branch still points at that commit.
func push(ctx context.Context, wt *verify.Worktree, r *Remote, branch string, force bool, last string) error {
	sha, err := head(ctx, wt, "HEAD")
	if err != nil {
		return err
	}
	ref := "refs/heads/" + branch
	args := []string{"push", r.URL, sha + ":" + ref}
	if force {
		want, err := r.expected(ctx, branch, last)
		if err != nil {
			return err
		}
		args = []string{"push", "--force-with-lease=" + ref + ":" + want, r.URL, sha + ":" + ref}
	}
	_, err = r.git(ctx, args...)
	switch {
	case err == nil:
		_, err = gitx.Run(ctx, r.repo, "update-ref", r.tracking(branch), sha)
		return err
	case strings.Contains(err.Error(), "without `workflows` permission"):
		return fmt.Errorf("%w: the change edits .github/workflows, and GitHub takes that only from a token with the workflows scope; pass a fine-grained token or a GitHub App token as the token input", ErrRefused)
	case strings.Contains(err.Error(), "[remote rejected]"):
		return fmt.Errorf("%w: %w", ErrRefused, err)
	case strings.Contains(err.Error(), "[rejected]") || strings.Contains(err.Error(), "stale info"):
		return fmt.Errorf("%w: %s moved on %s since tend last saw it: %w", ErrRejected, branch, r.Name, err)
	}
	return err
}

// expected is the commit the lease holds the remote branch to, "" for
// absent: the commit tend last pushed, when it knows; else what the
// remote-tracking ref says, fetched first when there is none (a fresh CI
// checkout) and dropped when the remote no longer has the branch.
func (r *Remote) expected(ctx context.Context, branch, last string) (string, error) {
	if last != "" {
		return last, nil
	}
	have, _ := gitx.Run(ctx, r.repo, "rev-parse", "--verify", "--quiet", r.tracking(branch))
	tip, err := r.tip(ctx, branch)
	switch {
	case err != nil:
		return "", err
	case tip == "" && have != "":
		_, err = gitx.Run(ctx, r.repo, "update-ref", "-d", r.tracking(branch))
		return "", err
	case tip != "" && have == "":
		return tip, r.Fetch(ctx, branch)
	}
	return have, nil
}

// DeleteBranch removes branch from the remote while it still points at
// last, the commit tend pushed there: a branch someone pushed to since, or
// one tend does not know the tip of, stays.
func DeleteBranch(ctx context.Context, r *Remote, branch, last string) error {
	if last == "" {
		return errors.New("tend does not know the commit it last pushed there")
	}
	ref := "refs/heads/" + branch
	if _, err := r.git(ctx, "push", "--force-with-lease="+ref+":"+last, r.URL, ":"+ref); err != nil {
		return err
	}
	_, err := gitx.Run(ctx, r.repo, "update-ref", "-d", r.tracking(branch))
	return err
}

// BranchExists reports whether the remote has the branch.
func BranchExists(ctx context.Context, r *Remote, branch string) (bool, error) {
	tip, err := r.tip(ctx, branch)
	return tip != "", err
}

// tip is the commit branch points at on the remote, "" when it has no such branch.
func (r *Remote) tip(ctx context.Context, branch string) (string, error) {
	out, err := r.git(ctx, "ls-remote", "--heads", r.URL, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	sha, _, _ := strings.Cut(out, "\t")
	return sha, nil
}

// DefaultRemote is origin when the repo has one, else its only remote.
func DefaultRemote(ctx context.Context, dir string) (string, error) {
	out, err := gitx.Run(ctx, dir, "remote")
	if err != nil {
		return "", err
	}
	names := strings.Fields(out)
	for _, n := range names {
		if n == "origin" {
			return n, nil
		}
	}
	if len(names) == 1 {
		return names[0], nil
	}
	if len(names) == 0 {
		return "", errors.New("git: the repository has no remote")
	}
	return "", fmt.Errorf("git: no remote named origin among %s", strings.Join(names, ", "))
}

// RemoteURL is the fetch URL of a remote.
func RemoteURL(ctx context.Context, dir, remote string) (string, error) {
	return gitx.Run(ctx, dir, "remote", "get-url", remote)
}

func head(ctx context.Context, wt *verify.Worktree, rev string) (string, error) {
	return gitx.Run(ctx, wt.Dir, "rev-parse", "--verify", rev+"^{commit}")
}

// SameTree reports whether the remote branch already holds exactly the files
// at HEAD of the worktree, and returns the commit it is at. It fetches into
// FETCH_HEAD only; updating the remote-tracking ref as well would hand Push a
// fresh lease.
func SameTree(ctx context.Context, wt *verify.Worktree, r *Remote, branch string) (bool, string, error) {
	if _, err := r.git(ctx, "fetch", "--quiet", "--no-tags", "--refmap=", r.URL, "refs/heads/"+branch); err != nil {
		return false, "", err
	}
	tip, err := gitx.Run(ctx, r.repo, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if err != nil {
		return false, "", err
	}
	theirs, err := gitx.Run(ctx, r.repo, "rev-parse", tip+"^{tree}")
	if err != nil {
		return false, "", err
	}
	ours, err := gitx.Run(ctx, wt.Dir, "rev-parse", "HEAD^{tree}")
	return ours == theirs, tip, err
}
