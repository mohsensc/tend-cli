// Command demo runs tend once, live, against a copy of a small Node project
// whose payments SDK is deprecated. It is tend's first run there, so the PR
// is the tidy-up: a model with tool calling moves the calls, a package
// nothing uses goes, the project's tests prove both, and the PR body is
// printed instead of opened.
// The package registry is local; the model is not. It needs a Groq key, in
// TEND_MODEL_KEY or GROQ_API_KEY.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/cli"
	"github.com/mohsensc/tend-cli/internal/e2e"
)

const model = "groq/openai/gpt-oss-120b"

func main() {
	if !keyed(os.Getenv) {
		fmt.Println("demo: set TEND_MODEL_KEY or GROQ_API_KEY to watch a model migrate the project; skipping")
		return
	}
	for _, tool := range []string{"git", "node", "npm"} {
		if _, err := exec.LookPath(tool); err != nil {
			fmt.Printf("demo: %s not found in PATH; skipping\n", tool)
			return
		}
	}
	if err := demo(); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

// keyed reports whether tend will find a key for the model: the same
// variables it reads, TEND_MODEL_KEY first.
func keyed(getenv func(string) string) bool {
	for _, k := range []string{"TEND_MODEL_KEY", "GROQ_API_KEY"} {
		if strings.TrimSpace(getenv(k)) != "" {
			return true
		}
	}
	return false
}

func demo() error {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	reg, err := e2e.NewRegistry(filepath.Join(root, "testdata", "npmreg"))
	if err != nil {
		return err
	}
	defer reg.Close()

	tmp, err := os.MkdirTemp("", "tend-demo-")
	if err != nil {
		return err
	}
	dir, origin := filepath.Join(tmp, "project"), filepath.Join(tmp, "origin.git")
	if err := e2e.CopyProject(filepath.Join(root, "testdata", "projects", "node-sdk"), dir, reg.Vars()); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "tend.yml"), []byte("model: "+model+"\n"), 0o644); err != nil {
		return err
	}
	steps := [][]string{
		{"init", "--quiet", "--initial-branch=main"},
		{"-c", "user.name=Demo", "-c", "user.email=demo@example.com", "add", "-A"},
		{"-c", "user.name=Demo", "-c", "user.email=demo@example.com", "commit", "--quiet", "-m", "Start"},
		{"init", "--quiet", "--bare", origin},
		{"remote", "add", "origin", origin},
		{"push", "--quiet", "origin", "main"},
	}
	for _, args := range steps {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	os.Setenv("npm_config_registry", reg.URL+"/")
	os.Setenv("npm_config_cache", filepath.Join(tmp, "npm-cache"))

	fmt.Printf("demo: %s, a copy of testdata/projects/node-sdk, with %s\n\n", dir, model)
	e := cli.DefaultEnv()
	e.Dir, e.Home, e.Now = dir, filepath.Join(tmp, "home"), time.Now
	if code := cli.Main([]string{"run", "--dry-run"}, e); code != 0 {
		return fmt.Errorf("tend exited %d", code)
	}
	return nil
}
