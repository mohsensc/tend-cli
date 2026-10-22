// Package gitx runs git for tend, on guard against what the project's own commands leave in a repository.
package gitx

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/mohsensc/tend-cli/internal/core"
)

// Cmd is one git call: where it runs, what it adds to the environment and
// what it reads on stdin.
type Cmd struct {
	Dir   string
	Env   []string
	Stdin string
}

// Run runs git in dir and returns its output with the surrounding space trimmed.
func Run(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := Cmd{Dir: dir}.Run(ctx, args...)
	return strings.TrimSpace(out), err
}

// Raw runs git in dir and returns its output as it is.
func Raw(ctx context.Context, dir string, args ...string) (string, error) {
	return Cmd{Dir: dir}.Run(ctx, args...)
}

// Run runs git and returns what it wrote to stdout. Hooks and the file
// system monitor never run, whatever a config says, and git sees tend's
// environment without its tokens. A worktree that was pinned uses the git dir
// it had then.
func (c Cmd) Run(ctx context.Context, args ...string) (string, error) {
	null := "/dev/null"
	if runtime.GOOS == "windows" {
		null = "NUL"
	}
	full := append([]string{"-c", "core.hooksPath=" + null, "-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(ctx, bin(), full...)
	cmd.Dir = c.Dir
	cmd.Env = append(local(core.Scrub(os.Environ(), nil)), "GIT_TERMINAL_PROMPT=0")
	if wt, gitDir, ok := pinned(c.Dir); ok {
		cmd.Env = append(cmd.Env, "GIT_DIR="+gitDir, "GIT_WORK_TREE="+wt)
	}
	cmd.Env = append(cmd.Env, c.Env...)
	if c.Stdin != "" {
		cmd.Stdin = strings.NewReader(c.Stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), fmt.Errorf("git %s: %s", args[0], msg)
	}
	return out.String(), nil
}

// located are the variables that tell git which repository, index or object
// store to use. Set by a hook or a wrapper that runs tend, they would point
// every call at the caller's repository instead of the directory asked for.
var located = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_IMPLICIT_WORK_TREE",
	"GIT_PREFIX", "GIT_NAMESPACE", "GIT_SHALLOW_FILE", "GIT_GRAFT_FILE",
	"GIT_NO_REPLACE_OBJECTS", "GIT_REPLACE_REF_BASE", "GIT_CEILING_DIRECTORIES",
	"GIT_DISCOVERY_ACROSS_FILESYSTEM",
}

// local drops the repository-location variables from env.
func local(env []string) []string {
	return slices.DeleteFunc(env, func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return slices.Contains(located, k)
	})
}

// bin is the git found when tend started, so a git that a command later
// drops into a directory on the PATH is never the one tend runs.
var bin = sync.OnceValue(func() string {
	if p, err := exec.LookPath("git"); err == nil {
		return p
	}
	return "git"
})

var pins sync.Map // worktree dir -> its git dir

// Pin ties the worktree at dir to gitDir. A command that later rewrites
// dir/.git to point at a git dir of its own, with hooks or filters in its
// config, then changes nothing tend runs there.
func Pin(dir, gitDir string) { pins.Store(filepath.Clean(dir), gitDir) }

// Unpin forgets the worktree at dir.
func Unpin(dir string) { pins.Delete(filepath.Clean(dir)) }

// pinned finds the pinned worktree dir is in, if any.
func pinned(dir string) (wt, gitDir string, ok bool) {
	if dir == "" {
		return "", "", false
	}
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if v, ok := pins.Load(d); ok {
			return d, v.(string), true
		}
		if filepath.Dir(d) == d {
			return "", "", false
		}
	}
}

// Files lists the files git knows of under root, tracked or not ignored,
// relative to root, each once.
func Files(ctx context.Context, root string) ([]string, error) {
	out, err := Raw(ctx, root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	var res []string
	for _, f := range strings.Split(out, "\x00") {
		// A path with a merge conflict is listed once per stage, one after another.
		if f != "" && (len(res) == 0 || res[len(res)-1] != f) {
			res = append(res, f)
		}
	}
	return res, nil
}
