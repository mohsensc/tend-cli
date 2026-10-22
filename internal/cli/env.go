// Package cli is tend's command line: check, run, watch, init, update, version.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/state"
)

// Env is everything a command touches outside its arguments. Tests build one by
// hand; a nil Fetch, Forge or Model is built from config and the environment.
type Env struct {
	Dir            string
	Stdout, Stderr io.Writer
	Stdin          io.Reader
	Fetch          core.Fetcher
	Forge          core.Forge
	Model          core.Model
	Now            func() time.Time
	Home           string
	Getenv         func(string) string
	Version        string
	Commit, Date   string
}

func DefaultEnv() Env {
	dir, err := os.Getwd()
	if err != nil {
		dir = "."
	}
	return Env{
		Dir:     dir,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Stdin:   os.Stdin,
		Now:     time.Now,
		Home:    state.Home(os.Getenv),
		Getenv:  os.Getenv,
		Version: "dev",
	}
}

const usage = `tend keeps the dependencies and the APIs your code calls current, one proven PR at a time.

usage: tend <command> [flags]

commands:
  check     list what would change, most important first
  run       fix the top change, prove it, and open or update its PR
  watch     run on tend.yml's schedule (daily), or as often as --every says
  init      write tend.yml, or with --ci the GitHub Actions workflow, then offer to run
  update    update tend itself
  version   print the version

global flags: -C <dir>  --config <path>  --home <dir>  -v  -q

examples:
  tend check            list the queue; changes nothing
  tend run --dry-run    work the top change and print its PR instead of sending it
  tend init --ci        write a workflow that runs tend on GitHub Actions
`

// Exit codes.
const (
	exitOK       = 0
	exitErr      = 1
	exitUsage    = 2
	exitFound    = 3 // check --fail-on matched a queued change
	exitUnproven = 4 // run could not prove the fix
)

type opts struct {
	dir, config, home string
	verbose, quiet    bool

	json, all, reset bool
	failOn           string
	dryRun, keep     bool
	every            time.Duration
	ci, force, check bool
	now              bool
}

type command struct {
	about string
	flags func(*flag.FlagSet, *opts)
	run   func(context.Context, Env, opts) int
}

var commands = map[string]command{
	"check": {"list what would change, most important first", func(fs *flag.FlagSet, o *opts) {
		fs.BoolVar(&o.json, "json", false, "print JSON")
		fs.BoolVar(&o.all, "all", false, "list skipped changes too")
		fs.StringVar(&o.failOn, "fail-on", "", "exit 3 when a change of this kind is queued: security|breaking|deprecation|any")
		fs.BoolVar(&o.reset, "reset", false, "forget everything tend remembers about this repo first")
	}, check},
	"run": {"fix the top change, prove it, and open or update its PR", func(fs *flag.FlagSet, o *opts) {
		fs.BoolVar(&o.dryRun, "dry-run", false, "stop before delivery; print the PR and keep the worktree")
		fs.BoolVar(&o.json, "json", false, "print JSON")
		fs.BoolVar(&o.keep, "keep", false, "keep the worktree")
	}, run},
	"watch": {"run on tend.yml's schedule (daily), or as often as --every says", func(fs *flag.FlagSet, o *opts) {
		fs.DurationVar(&o.every, "every", 0, "interval between runs; 0 means tend.yml's schedule")
	}, watch},
	"init": {"write tend.yml, or with --ci the GitHub Actions workflow, then offer to run", func(fs *flag.FlagSet, o *opts) {
		fs.BoolVar(&o.ci, "ci", false, "write .github/workflows/tend.yml instead of tend.yml")
		fs.BoolVar(&o.force, "force", false, "overwrite existing files")
		fs.BoolVar(&o.now, "now", false, "run tend right after, without asking")
	}, initRepo},
	"update": {"replace this binary with the latest signed release", func(fs *flag.FlagSet, o *opts) {
		fs.BoolVar(&o.check, "check", false, "only say whether a newer release exists")
	}, update},
	"version": {"print the version", func(fs *flag.FlagSet, o *opts) {
		fs.BoolVar(&o.check, "check", false, "also say whether a newer release exists")
	}, version},
}

var valued = map[string]bool{"-C": true, "-config": true, "--config": true, "-home": true, "--home": true, "-every": true, "--every": true, "-fail-on": true, "--fail-on": true}

func isHelp(a string) bool { return a == "-h" || a == "-help" || a == "--help" }

// split finds the command among the arguments, so global flags may come
// before or after it.
func split(args []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if valued[a] {
			i++
			continue
		}
		if !strings.HasPrefix(a, "-") {
			rest := append(args[:i:i], args[i+1:]...)
			return a, rest
		}
	}
	return "", args
}

// Main runs one command and returns the process exit code.
func Main(args []string, e Env) int {
	name, rest := split(args)
	if name == "help" || name == "" && slices.ContainsFunc(rest, isHelp) {
		fmt.Fprint(e.Stdout, usage)
		return exitOK
	}
	c, ok := commands[name]
	if !ok {
		if name != "" {
			fmt.Fprintf(e.Stderr, "tend: unknown command %q\n", name)
		}
		fmt.Fprint(e.Stderr, usage)
		return exitUsage
	}
	var o opts
	own := flag.NewFlagSet(name, flag.ContinueOnError)
	c.flags(own, &o)
	fs := flag.NewFlagSet("tend "+name, flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	fs.Usage = func() {}
	own.VisitAll(func(f *flag.Flag) { fs.Var(f.Value, f.Name, f.Usage) })
	fs.StringVar(&o.dir, "C", "", "run as if started in `dir`")
	fs.StringVar(&o.config, "config", "", "path to tend.yml")
	fs.StringVar(&o.home, "home", "", "state `dir`; default $TEND_HOME or the user cache dir")
	fs.BoolVar(&o.verbose, "v", false, "stream command output")
	fs.BoolVar(&o.quiet, "q", false, "print only the final line")
	help := func(w io.Writer) {
		fmt.Fprintf(w, "usage: tend %s [flags]\n\n%s\n", name, c.about)
		n := 0
		own.VisitAll(func(*flag.Flag) { n++ })
		if n > 0 {
			fmt.Fprintf(w, "\nflags:\n")
			own.SetOutput(w)
			own.PrintDefaults()
		}
		fmt.Fprintf(w, "\nglobal flags: see tend --help\n")
	}
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			help(e.Stdout)
			return exitOK
		}
		help(e.Stderr)
		return exitUsage
	}
	switch {
	case o.dir == "":
		o.dir = e.Dir
	case !filepath.IsAbs(o.dir):
		o.dir = filepath.Join(e.Dir, o.dir)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(e.Stderr, "tend %s: unexpected argument %q\n", name, fs.Arg(0))
		return exitUsage
	}
	if o.failOn != "" && !slices.Contains([]string{"security", "breaking", "deprecation", "any"}, o.failOn) {
		fmt.Fprintf(e.Stderr, "tend check: --fail-on wants security, breaking, deprecation or any, not %q\n", o.failOn)
		return exitUsage
	}
	if o.home != "" {
		e.Home = o.home
	}
	if e.Now == nil {
		e.Now = time.Now
	}
	if e.Getenv == nil {
		e.Getenv = os.Getenv
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return c.run(ctx, e, o)
}
