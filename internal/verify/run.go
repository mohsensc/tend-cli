package verify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/gitx"
	"github.com/mohsensc/tend-cli/internal/proc"
)

const defaultTimeout = 20 * time.Minute

type Options struct {
	Commands  core.Commands
	Timeout   time.Duration
	Container string
	Env       []string // names passed through
	LogDir    string
	Stream    io.Writer // -v: live output
	Warnings  bool      // turn the runtimes' deprecation warnings on and collect them
	Listen    []Listener
}

// step is one command line as the proof shows it and as it is executed.
type step struct {
	cmd  string
	argv []string
	stop func() // extra cleanup after a timeout, nil when killing the process group is enough
}

// Run runs install, build and test in order and stops at the first failure.
// A failing or timed-out command is a result, not an error; errors are for
// when the commands could not be run at all.
func Run(ctx context.Context, w *Worktree, o Options) (*core.Run, error) {
	start := time.Now()
	res := &core.Run{}
	res.Commit, _ = gitx.Run(ctx, w.Dir, "rev-parse", "HEAD")
	var lines, phases []string
	for _, ph := range []struct {
		name  string
		lines []string
	}{{core.PhaseInstall, o.Commands.Install}, {core.PhaseBuild, o.Commands.Build}, {core.PhaseTest, o.Commands.Test}} {
		lines = append(lines, ph.lines...)
		for range ph.lines {
			phases = append(phases, ph.name)
		}
	}
	if len(lines) == 0 {
		res.Pass = true
		return res, nil
	}

	env := Filter(os.Environ(), o.Env)
	if o.Container == "" {
		defer guardConfig(ctx, w)()
	}
	if o.Warnings {
		env = warningsOn(env)
	}
	steps := hostSteps(lines)
	if o.Container != "" {
		var err error
		if steps, err = containerSteps(w.Dir, o.Container, lines, env); err != nil {
			res.Steps = []core.Step{{Cmd: "docker run " + o.Container, Skipped: "docker not found"}}
			return res, err
		}
	}
	if o.LogDir != "" {
		if err := os.MkdirAll(o.LogDir, 0o755); err != nil {
			return nil, err
		}
	}

	timeout := o.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	why := ""
	for i, s := range steps {
		if why != "" {
			res.Steps = append(res.Steps, core.Step{Cmd: s.cmd, Phase: phases[i], Skipped: why})
			continue
		}
		st, err := runStep(runCtx, w.Dir, env, i+1, s, o, timeout)
		if err != nil {
			return nil, err
		}
		st.Phase = phases[i]
		res.Steps = append(res.Steps, st)
		switch {
		case ctx.Err() != nil:
			why = "canceled"
		case runCtx.Err() != nil:
			why = "timed out"
		case st.Exit != 0:
			why = "previous step failed"
		}
	}
	res.Took = time.Since(start)
	res.Pass = why == ""
	if o.Warnings {
		at := w.Dir
		if o.Container != "" {
			at = workDir
		}
		ws := Warnings(res, at)
		if o.Container == "" && runCtx.Err() == nil && len(o.Listen) > 0 {
			ws = tidy(append(ws, listen(runCtx, w.Dir, env, o.Listen)...))
		}
		res.Warnings = ws
	}
	return res, ctx.Err()
}

// guardConfig keeps the repository's git config as it was while the
// project's commands run in a linked worktree, which shares that config: a
// husky prepare script, or any step that calls git config, would otherwise
// write the user's .git/config. The returned func puts it back.
func guardConfig(ctx context.Context, w *Worktree) func() {
	dir, err := gitx.Run(ctx, w.Dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || dir == "" {
		return func() {}
	}
	p := filepath.Join(dir, "config")
	was, err := os.ReadFile(p)
	if err != nil {
		return func() {}
	}
	return func() {
		if now, err := os.ReadFile(p); err != nil || !bytes.Equal(now, was) {
			_ = os.WriteFile(p, was, 0o644)
		}
	}
}

func hostSteps(lines []string) []step {
	res := make([]step, len(lines))
	for i, l := range lines {
		res[i] = step{cmd: l, argv: []string{"sh", "-c", l}}
	}
	return res
}

func runStep(ctx context.Context, dir string, env []string, n int, s step, o Options, timeout time.Duration) (core.Step, error) {
	res := core.Step{Cmd: s.cmd}
	t := &tail{}
	outs := []io.Writer{t}
	if o.LogDir != "" {
		res.Log = filepath.Join(o.LogDir, fmt.Sprintf("%d-%s.log", n, logName(s.cmd)))
		f, err := os.Create(res.Log)
		if err != nil {
			return res, err
		}
		defer f.Close()
		outs = append(outs, f)
	}
	if o.Stream != nil {
		fmt.Fprintf(o.Stream, "$ %s\n", s.cmd)
		outs = append(outs, o.Stream)
	}
	out := io.MultiWriter(outs...)

	start := time.Now()
	res.Exit = execute(ctx, dir, env, s, out)
	res.Took = time.Since(start)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		res.Exit = -1
		fmt.Fprintf(out, "timed out after %s\n", timeout)
	}
	tl := local(t.String(), dir)
	if o.Container != "" {
		tl = local(tl, workDir)
	}
	res.Tail = redact(tl, env, o.Env)
	return res, nil
}

// redact hides the values of the variables the user passed through: the tail
// goes to the model and into the PR body, the full log stays on this machine.
func redact(s string, env, names []string) string {
	for _, kv := range env {
		name, val, _ := strings.Cut(kv, "=")
		if len(val) >= 4 && slices.Contains(names, name) {
			s = strings.ReplaceAll(s, val, "***")
		}
	}
	return s
}

// local writes paths inside the worktree relative to it, so a stack trace in
// the tail reads src/billing.js:12 rather than a temporary directory.
func local(s, dir string) string {
	dirs := []string{dir}
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != dir {
		dirs = append([]string{real}, dirs...)
	}
	for _, d := range dirs {
		s = strings.ReplaceAll(s, d+string(filepath.Separator), "")
	}
	return s
}

// execute runs one step with its output on a pipe tend owns, so that once the
// command exits, anything it left running in its process group is killed and
// the copy is not held open by a straggler.
func execute(ctx context.Context, dir string, env []string, s step, out io.Writer) int {
	r, w, err := os.Pipe()
	if err != nil {
		fmt.Fprintln(out, err)
		return -1
	}
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(out, r)
		close(done)
	}()

	cmd := exec.CommandContext(ctx, s.argv[0], s.argv[1:]...)
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdout, cmd.Stderr = w, w
	proc.Group(cmd)
	err = cmd.Start()
	if err == nil {
		err = cmd.Wait()
		proc.Reap(cmd)
	}
	if ctx.Err() != nil && s.stop != nil {
		s.stop()
	}
	w.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		r.Close()
		<-done
	}
	r.Close()

	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		return exit.ExitCode()
	default:
		fmt.Fprintln(out, err)
		return -1
	}
}

func logName(cmd string) string {
	if s := core.Slug(cmd); s != "" {
		return s
	}
	return "step"
}

const (
	tailLines  = 40
	firstLines = 10
	lineMax    = 4096
)

// failRe spots the line where a run first goes wrong.
var failRe = regexp.MustCompile(`(?i)^\s*not ok\b|\b(error|fail|failed|failure|panic)\b`)

// tail keeps the last tailLines lines written to it in a fixed ring, so a
// multi-gigabyte build log costs the same memory as a short one. It also keeps
// the first failure, which long stack traces can push out of the ring.
type tail struct {
	ring    [tailLines]string
	n       int
	curr    []byte
	first   []string
	firstAt int
}

func (t *tail) Write(p []byte) (int, error) {
	rest := p
	for {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			t.add(rest)
			return len(p), nil
		}
		t.add(rest[:i])
		t.line(string(t.curr))
		t.curr = t.curr[:0]
		rest = rest[i+1:]
	}
}

func (t *tail) line(s string) {
	if t.first == nil && failRe.MatchString(s) {
		t.firstAt = t.n
	}
	if (t.first != nil || failRe.MatchString(s)) && len(t.first) < firstLines {
		t.first = append(t.first, s)
	}
	t.ring[t.n%tailLines] = s
	t.n++
}

func (t *tail) add(b []byte) {
	if room := lineMax - len(t.curr); room > 0 {
		t.curr = append(t.curr, b[:min(len(b), room)]...)
	}
}

func (t *tail) String() string {
	var res []string
	for i := max(0, t.n-tailLines); i < t.n; i++ {
		res = append(res, t.ring[i%tailLines])
	}
	if len(t.curr) > 0 {
		res = append(res, string(t.curr))
	}
	if len(res) > tailLines {
		res = res[len(res)-tailLines:]
	}
	start := t.n + min(len(t.curr), 1) - len(res)
	if t.first != nil && t.firstAt < start {
		head := slices.Clone(t.first[:min(len(t.first), start-t.firstAt)])
		if t.firstAt+len(head) < start {
			head = append(head, "...")
		}
		res = append(head, res...)
	}
	return strings.Join(res, "\n")
}
