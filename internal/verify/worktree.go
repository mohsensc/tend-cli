// Package verify runs a project's own install, build and test commands in an isolated worktree.
package verify

import (
	"context"
	"os"
	"path/filepath"

	"github.com/mohsensc/tend-cli/internal/gitx"
)

// Worktree is a detached checkout of Base at Dir. GitDir is its git dir as
// it was made, which every later git call in Dir uses.
type Worktree struct{ Dir, Repo, Base, GitDir string }
type Author struct{ Name, Email string }

var fallbackAuthor = Author{Name: "tend", Email: "tend@users.noreply.github.com"}

// AuthorFrom picks the commit author: git config in repo, else TEND_GIT_USER and
// TEND_GIT_EMAIL, else tend's own identity. A source counts only when it has both.
func AuthorFrom(repo string, getenv func(string) string) Author {
	name, _ := gitx.Run(context.Background(), repo, "config", "user.name")
	email, _ := gitx.Run(context.Background(), repo, "config", "user.email")
	if name != "" && email != "" {
		return Author{name, email}
	}
	name, email = getenv("TEND_GIT_USER"), getenv("TEND_GIT_EMAIL")
	if name != "" && email != "" {
		return Author{name, email}
	}
	return fallbackAuthor
}

// Checkout adds a detached worktree of base at dir. dir is under the state dir.
// A leftover worktree at dir from a crashed run is removed first.
func Checkout(ctx context.Context, repo, dir, base string) (*Worktree, error) {
	sha, err := gitx.Run(ctx, repo, "rev-parse", "--verify", base+"^{commit}")
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(dir); err == nil {
		gitx.Unpin(dir)
		_, _ = gitx.Run(ctx, repo, "worktree", "remove", "--force", dir)
		if err := os.RemoveAll(dir); err != nil {
			return nil, err
		}
	}
	if _, err := gitx.Run(ctx, repo, "worktree", "prune"); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return nil, err
	}
	if _, err := gitx.Run(ctx, repo, "worktree", "add", "--detach", dir, sha); err != nil {
		return nil, err
	}
	gitDir, err := gitx.Run(ctx, dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	gitx.Pin(dir, gitDir)
	return &Worktree{Dir: dir, Repo: repo, Base: sha, GitDir: gitDir}, nil
}

func (w *Worktree) Remove(ctx context.Context) error {
	gitx.Unpin(w.Dir)
	if _, err := gitx.Run(ctx, w.Repo, "worktree", "remove", "--force", w.Dir); err != nil {
		if err := os.RemoveAll(w.Dir); err != nil {
			return err
		}
	}
	_, err := gitx.Run(ctx, w.Repo, "worktree", "prune")
	return err
}

// Commit stages everything .gitignore allows and commits it as author, who is
// also the committer. It returns the new sha.
func (w *Worktree) Commit(ctx context.Context, msg string, author Author, allowEmpty bool) (string, error) {
	if _, err := gitx.Run(ctx, w.Dir, "add", "-A"); err != nil {
		return "", err
	}
	args := []string{"-c", "commit.gpgsign=false", "commit", "--quiet", "--no-verify", "-m", msg}
	if allowEmpty {
		args = append(args, "--allow-empty")
	}
	// The environment outranks git config, so a GIT_AUTHOR_NAME left in the
	// caller's environment cannot put someone else's name on the commit.
	env := []string{
		"GIT_AUTHOR_NAME=" + author.Name, "GIT_AUTHOR_EMAIL=" + author.Email,
		"GIT_COMMITTER_NAME=" + author.Name, "GIT_COMMITTER_EMAIL=" + author.Email,
	}
	if _, err := (gitx.Cmd{Dir: w.Dir, Env: env}).Run(ctx, args...); err != nil {
		return "", err
	}
	return gitx.Run(ctx, w.Dir, "rev-parse", "HEAD")
}
