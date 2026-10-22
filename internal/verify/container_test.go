package verify

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func TestDockerArgv(t *testing.T) {
	var b strings.Builder
	for _, tt := range []struct{ user string }{{"501:20"}, {""}} {
		argv := dockerArgv("/state/repos/k/wt/1", "node:22-alpine", "npm ci && npm test", "tend-abc", tt.user, []string{"CI", "TEND", "NODE_ENV"})
		b.WriteString(strings.Join(argv, "\n"))
		b.WriteString("\n\n")
	}
	golden(t, "docker_argv.golden", b.String())
}

func TestContainerStepsEnv(t *testing.T) {
	lookPath = func(string) (string, error) { return "/usr/bin/docker", nil }
	defer func() { lookPath = exec.LookPath }()
	steps, err := containerSteps("/wt", "node:22", []string{"npm test"}, []string{"PATH=/bin", "HOME=/h", "CI=1", "DOCKER_HOST=unix:///x", "NODE_ENV=test"})
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(steps[0].argv, " ")
	for _, bad := range []string{"-e PATH", "-e HOME=/h", "-e HOME ", "-e DOCKER_HOST"} {
		if strings.Contains(argv, bad) {
			t.Errorf("argv passes host var %q: %s", bad, argv)
		}
	}
	for _, good := range []string{"-e CI ", "-e NODE_ENV ", "node:22 sh -c npm test"} {
		if !strings.Contains(argv, good) {
			t.Errorf("argv misses %q: %s", good, argv)
		}
	}
	if steps[0].cmd != "npm test" || steps[0].stop == nil {
		t.Errorf("step = %+v", steps[0])
	}
}

func TestRunDockerMissing(t *testing.T) {
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	defer func() { lookPath = exec.LookPath }()
	res, err := Run(context.Background(), &Worktree{Dir: t.TempDir()}, Options{
		Commands:  core.Commands{Test: []string{"npm test"}},
		Container: "node:22",
	})
	var missing core.ToolMissing
	if !errors.As(err, &missing) || missing.Tool != "docker" {
		t.Errorf("err = %v, want ToolMissing{docker}", err)
	}
	if res == nil || res.Pass || len(res.Steps) != 1 || res.Steps[0].Skipped != "docker not found" {
		t.Errorf("Run = %+v", res)
	}
}
