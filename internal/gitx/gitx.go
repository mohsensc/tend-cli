// Package gitx runs git for tend, on guard against what the project's own commands leave in a repository.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/proc"
)

// Cmd is one git call: where it runs, what it adds to the environment and
// what it reads on stdin. Git runs in a process group of its own, so an
// interrupt at the terminal reaches tend alone, unless Terminal says it may
// need the terminal, as ssh does to ask for a passphrase.
type Cmd struct {
	Dir      string
	Env      []string
	Stdin    string
	Terminal bool
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

// waitDelay is how long Run waits, once git is killed, for what git started
// to let go of its output: a stuck git-remote-https or ssh would hold it,
// and the run with it, long after a cancel.
const waitDelay = 2 * time.Second

// Run runs git and returns what it wrote to stdout. Hooks and the file
// system monitor never run, whatever a config says, and git sees tend's
// environment without its tokens. A worktree that was pinned uses the git dir
// it had then.
func (c Cmd) Run(ctx context.Context, args ...string) (string, error) {
	null := "/dev/null"
	if runtime.GOOS == "windows" {
		null = "NUL"
	}
	full := []string{"-c", "core.hooksPath=" + null, "-c", "core.fsmonitor=false"}
	pin, isPinned := pinned(c.Dir)
	if isPinned && pin.exclude != "" {
		full = append(full, "-c", "core.excludesFile="+pin.exclude)
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, bin(), full...)
	cmd.WaitDelay = waitDelay
	if !c.Terminal {
		proc.Group(cmd)
	}
	cmd.Dir = c.Dir
	cmd.Env = append(local(core.Scrub(os.Environ(), nil)), "GIT_TERMINAL_PROMPT=0")
	if isPinned {
		cmd.Env = append(cmd.Env, "GIT_DIR="+pin.gitDir, "GIT_WORK_TREE="+pin.dir)
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
		return out.String(), &Error{msg: fmt.Sprintf("git %s: %s", args[0], msg), err: err}
	}
	return out.String(), nil
}

// Error is a git call that failed: what git said, and how it exited.
type Error struct {
	msg string
	err error
}

func (e *Error) Error() string { return e.msg }
func (e *Error) Unwrap() error { return e.err }

// Code is the status git exited with, or -1 when err is not git exiting.
func Code(err error) int {
	var x *exec.ExitError
	if errors.As(err, &x) {
		return x.ExitCode()
	}
	return -1
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

var pins sync.Map // worktree dir -> pin

type pin struct{ dir, gitDir, exclude string }

// Pin ties the worktree at dir to gitDir. A command that later rewrites
// dir/.git to point at a git dir of its own, with hooks or filters in its
// config, then changes nothing tend runs there. Exclude, when set, is the
// excludes file git reads there in place of the user's own.
func Pin(dir, gitDir, exclude string) {
	dir = filepath.Clean(dir)
	pins.Store(dir, pin{dir, gitDir, exclude})
}

// Unpin forgets the worktree at dir.
func Unpin(dir string) { pins.Delete(filepath.Clean(dir)) }

// pinned finds the pinned worktree dir is in, if any.
func pinned(dir string) (pin, bool) {
	if dir == "" {
		return pin{}, false
	}
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if v, ok := pins.Load(d); ok {
			return v.(pin), true
		}
		if filepath.Dir(d) == d {
			return pin{}, false
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
