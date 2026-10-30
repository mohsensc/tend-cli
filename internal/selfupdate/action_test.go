package selfupdate

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type actionStep struct {
	Shell string            `yaml:"shell"`
	Run   string            `yaml:"run"`
	Uses  string            `yaml:"uses"`
	If    string            `yaml:"if"`
	Env   map[string]string `yaml:"env"`
	With  map[string]string `yaml:"with"`
}

func actionSteps(t *testing.T) []actionStep {
	t.Helper()
	b, err := os.ReadFile("../../action.yml")
	if err != nil {
		t.Fatal(err)
	}
	var a struct {
		Runs struct {
			Using string       `yaml:"using"`
			Steps []actionStep `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(b, &a); err != nil {
		t.Fatal(err)
	}
	if a.Runs.Using != "composite" || len(a.Runs.Steps) == 0 {
		t.Fatalf("action.yml runs = %+v", a.Runs)
	}
	return a.Runs.Steps
}

var startsTend = regexp.MustCompile(`(^|[;&|]\s*|\n\s*)(exec\s+)?tend\s`)

func TestActionExecsTend(t *testing.T) {
	var found []actionStep
	for _, s := range actionSteps(t) {
		if startsTend.MatchString(s.Run) {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d steps start tend, want 1", len(found))
	}
	s := found[0]
	if _, ok := s.Env["GITHUB_TOKEN"]; !ok {
		t.Fatal("the step that runs tend has no GITHUB_TOKEN; the wrong step was found")
	}
	run := strings.TrimSpace(s.Run)
	if strings.Contains(run, "\n") {
		t.Fatalf("the tend step runs more than one line, so bash outlives tend:\n%s", run)
	}
	last := run[strings.LastIndex(run, ";")+1:]
	if !strings.HasPrefix(strings.TrimSpace(last), "exec tend ") {
		t.Errorf("the tend step runs %q; without exec, bash stays as tend's parent with the token in its environment", run)
	}
}

func TestActionCacheSkipsWorktrees(t *testing.T) {
	var caches int
	for _, s := range actionSteps(t) {
		if !strings.HasPrefix(s.Uses, "actions/cache/") {
			continue
		}
		caches++
		path := s.With["path"]
		for _, want := range []string{"/tend/repos\n", "!${{ runner.temp }}/tend/repos/*/wt\n", "!${{ runner.temp }}/tend/repos/*/baseline\n"} {
			if !strings.Contains(path+"\n", want) {
				t.Errorf("%s path does not hold %q:\n%s", s.Uses, strings.TrimSpace(want), path)
			}
		}
	}
	if caches != 2 {
		t.Errorf("%d cache steps, want a restore and a save", caches)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestScrub(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash may be WSL's here; the action job runs the scrub on Windows")
	}
	for _, tool := range []string{"bash", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not found")
		}
	}
	script, err := filepath.Abs("../../action/scrub.sh")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	repo, temp, elsewhere := filepath.Join(root, "work dir", "repo"), filepath.Join(root, "_temp"), filepath.Join(root, "home")
	for _, d := range []string{repo, temp, elsewhere} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "init", "-q")
	gitDir := filepath.Join(repo, ".git")
	creds := filepath.Join(temp, "git-credentials-1b0c.config")
	second := filepath.Join(temp, "git-credentials-77aa.config")
	outside := filepath.Join(elsewhere, "git-credentials-0000.config")
	mine := filepath.Join(elsewhere, "mine.config")
	for _, f := range []string{creds, second, outside} {
		if err := os.WriteFile(f, []byte("[http \"https://github.com/\"]\n\textraheader = AUTHORIZATION: basic c2VjcmV0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(mine, []byte("[user]\n\tname = me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "config", "--local", "http.https://github.com/.extraheader", "AUTHORIZATION: basic c2VjcmV0")
	git(t, repo, "config", "--local", "includeIf.gitdir:"+gitDir+".path", creds)
	git(t, repo, "config", "--local", "includeIf.gitdir:"+gitDir+"/worktrees/*.path", creds)
	git(t, repo, "config", "--local", "includeIf.gitdir:/github/workspace/.git.path", outside)
	git(t, repo, "config", "--local", "includeIf.gitdir:"+gitDir+"/modules/x.path", mine)
	if !strings.Contains(git(t, repo, "config", "--get-all", "http.https://github.com/.extraheader"), "c2VjcmV0") {
		t.Fatal("the fixture does not resolve the token before the scrub")
	}

	cmd := exec.Command("bash", script)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "RUNNER_TEMP="+temp, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scrub: %v\n%s", err, out)
	}

	cfg := git(t, repo, "config", "--local", "--list")
	if strings.Contains(cfg, "c2VjcmV0") || strings.Contains(cfg, "git-credentials-") {
		t.Errorf("the token is still in the config:\n%s", cfg)
	}
	if !strings.Contains(cfg, "mine.config") {
		t.Errorf("an include that holds no credentials was removed:\n%s", cfg)
	}
	for _, f := range []string{creds, second} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("%s is still there", filepath.Base(f))
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Error("a credentials file outside RUNNER_TEMP was deleted")
	}

	cmd = exec.Command("bash", script)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "RUNNER_TEMP="+temp)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("a second scrub fails: %v\n%s", err, out)
	}
}
