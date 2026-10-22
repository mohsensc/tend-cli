package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/deliver"
	"github.com/mohsensc/tend-cli/internal/gitx"
)

// workflow runs tend on GitHub Actions on tend.yml's schedule, by hand, and
// once as soon as the workflow itself lands on the default branch, so adding
// tend starts its first run. One run at a time. The
// checkout keeps no token in the repository's config, where the project's
// own commands could read it; tend is handed the token itself. PRs opened
// with the default token do not trigger other workflows; a fine-grained or
// App token changes that. Issues are for tend's tracking issue, and checks
// and statuses for reading CI on what commit mode pushed. TEND_MODEL_KEY
// holds the key for whichever model provider tend.yml names.
const workflow = `name: tend
on:
  push:
    branches: [%q]
    paths: [.github/workflows/tend.yml]
  schedule: [{cron: %q}]
  workflow_dispatch:
permissions: {contents: write, pull-requests: write, issues: write, checks: read, statuses: read}
concurrency: {group: tend, cancel-in-progress: false}
jobs:
  tend:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
        with: {persist-credentials: false}
      - uses: mohsensc/tend-cli@v1
        with: {model-key: "${{ secrets.TEND_MODEL_KEY }}"}
`

func initRepo(ctx context.Context, e Env, o opts) int {
	cfg, root, _, err := settings(ctx, e, o)
	if err != nil && o.ci {
		return fail(e, err)
	}
	name, body := "tend.yml", config.Sample
	if o.ci {
		name, body = filepath.Join(".github", "workflows", "tend.yml"), fmt.Appendf(nil, workflow, branch(ctx, root, cfg), cron(time.Duration(cfg.Schedule)))
	}
	p := filepath.Join(root, name)
	_, err = os.Stat(p)
	if err == nil && !o.force {
		return fail(e, fmt.Errorf("%s exists; --force overwrites it", name))
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(e, err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fail(e, err)
	}
	if err := os.WriteFile(p, body, 0o644); err != nil {
		return fail(e, err)
	}
	if !o.quiet {
		fmt.Fprintf(e.Stdout, "tend: wrote %s\n", name)
	}
	if o.now || !o.quiet && ask(e, "Run tend now? Its first pull request tidies up what it finds. [y/N] ") {
		return run(ctx, e, opts{dir: o.dir, config: o.config, verbose: o.verbose, quiet: o.quiet})
	}
	return exitOK
}

// ask puts a yes or no question to a person at a terminal. Anything but yes
// is no, and so is a stdin that is not a terminal, such as CI's.
func ask(e Env, q string) bool {
	if !interactive(e.Stdin) {
		return false
	}
	fmt.Fprint(e.Stdout, q)
	line, _ := bufio.NewReader(e.Stdin).ReadString('\n')
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes"
}

// interactive reports whether r is a terminal, or a reader a test put in
// its place. /dev/null is a character device too, and answers nothing.
func interactive(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return r != nil
	}
	fi, err := f.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err != nil || !os.SameFile(fi, null)
}

// branch is the one a push to starts tend's workflow: tend.yml's base, else
// origin's default branch, else the current one, else main.
func branch(ctx context.Context, root string, cfg *config.Config) string {
	if cfg.Base != "" {
		return cfg.Base
	}
	if r, err := deliver.DefaultRemote(ctx, root); err == nil {
		if ref, err := gitx.Run(ctx, root, "symbolic-ref", "--short", "refs/remotes/"+r+"/HEAD"); err == nil {
			return strings.TrimPrefix(ref, r+"/")
		}
	}
	if b, err := gitx.Run(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil && b != "" {
		return b
	}
	return "main"
}

// cron is the line that runs every d, for the intervals config.Schedules
// allows. The odd minute keeps tend off the top of the hour, when the
// scheduler is busiest.
func cron(d time.Duration) string {
	switch {
	case d >= 7*24*time.Hour:
		return "17 6 * * 1"
	case d >= 24*time.Hour:
		return "17 6 * * *"
	case d <= time.Hour:
		return "17 * * * *"
	}
	return fmt.Sprintf("17 */%d * * *", int(d.Hours()))
}
