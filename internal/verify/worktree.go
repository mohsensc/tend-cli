// Package verify runs a project's own install, build and test commands in an isolated worktree.
package verify

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

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
	exclude, err := excludes(ctx, dir, gitDir)
	if err != nil {
		return nil, err
	}
	gitx.Pin(dir, gitDir, exclude)
	return &Worktree{Dir: dir, Repo: repo, Base: sha, GitDir: gitDir}, nil
}

// installed are the directories installs fill that no project commits.
var installed = []string{"node_modules", ".venv", "venv", "__pycache__", ".gradle"}

// excludes writes the excludes file git reads in the worktree at dir: the
// user's own, plus the install directories the project does not track. Its
// .gitignore may not list them, and a clean between commands must not take
// an install away, nor an add -A put one in the patch. It lives in the
// worktree's own git dir, so the user's repository is never written.
func excludes(ctx context.Context, dir, gitDir string) (string, error) {
	var b strings.Builder
	if own := userExcludes(ctx, dir); own != "" {
		if data, err := os.ReadFile(own); err == nil {
			b.Write(data)
			b.WriteString("\n")
		}
	}
	var specs []string
	for _, d := range installed {
		specs = append(specs, ":(glob)**/"+d+"/**")
	}
	tracked, err := gitx.Raw(ctx, dir, append([]string{"ls-files", "-z", "--"}, specs...)...)
	if err != nil {
		return "", err
	}
	for _, d := range installed {
		if !slices.ContainsFunc(strings.Split(tracked, "\x00"), func(f string) bool {
			return slices.Contains(strings.Split(f, "/"), d)
		}) {
			b.WriteString(d + "/\n")
		}
	}
	p := filepath.Join(gitDir, "tend-exclude")
	return p, os.WriteFile(p, []byte(b.String()), 0o644)
}

// userExcludes is the excludes file git would read in dir by itself.
func userExcludes(ctx context.Context, dir string) string {
	if p, err := gitx.Run(ctx, dir, "config", "--path", "--get", "core.excludesFile"); err == nil && p != "" {
		return p
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "git", "ignore")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "git", "ignore")
	}
	return ""
}

// Sweep removes the worktrees under parent, all but keep: those a dry
// run, --keep or a killed run left in the repo.
func Sweep(ctx context.Context, repo, parent, keep string) error {
	ents, err := os.ReadDir(parent)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	swept := false
	for _, e := range ents {
		dir := filepath.Join(parent, e.Name())
		if !e.IsDir() || filepath.Clean(dir) == filepath.Clean(keep) {
			continue
		}
		gitx.Unpin(dir)
		_, _ = gitx.Run(ctx, repo, "worktree", "remove", "--force", dir)
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		swept = true
	}
	if !swept {
		return nil
	}
	_, err = gitx.Run(ctx, repo, "worktree", "prune")
	return err
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
