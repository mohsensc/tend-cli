package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/mohsensc/tend-cli/internal/config"
)

func TestInit(t *testing.T) {
	dir := t.TempDir()
	var out, errs bytes.Buffer
	e := DefaultEnv()
	e.Dir, e.Stdout, e.Stderr, e.Stdin = dir, &out, &errs, strings.NewReader("")
	if code := Main([]string{"init"}, e); code != 0 {
		t.Fatalf("init = %d: %s", code, errs.String())
	}
	if code := Main([]string{"init", "--ci"}, e); code != 0 {
		t.Fatalf("init --ci after init = %d: %s", code, errs.String())
	}
	ask := "Run tend now? Its first pull request tidies up what it finds. [y/N] "
	if want := "tend: wrote tend.yml\n" + ask + "tend: wrote .github/workflows/tend.yml\n" + ask; out.String() != want {
		t.Errorf("output = %q", out.String())
	}
	b, err := os.ReadFile(filepath.Join(dir, "tend.yml"))
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "init.golden", b)

	cfg, used, err := config.Load(dir, "")
	if err != nil || used == "" {
		t.Fatalf("load = %v, %q", err, used)
	}
	def := config.Default()
	if !reflect.DeepEqual(cfg, def) {
		t.Errorf("tend init does not load as the defaults:\n%+v\n%+v", cfg, def)
	}

	wf, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", "tend.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		On struct {
			Push struct {
				Branches []string `yaml:"branches"`
				Paths    []string `yaml:"paths"`
			} `yaml:"push"`
		} `yaml:"on"`
		Jobs map[string]struct {
			Steps []map[string]any `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(wf, &doc); err != nil || len(doc.Jobs["tend"].Steps) != 2 {
		t.Fatalf("workflow = %+v, %v", doc, err)
	}
	// Adding the workflow to the default branch is what starts the first run.
	if p := doc.On.Push; !reflect.DeepEqual(p.Branches, []string{"main"}) || !reflect.DeepEqual(p.Paths, []string{".github/workflows/tend.yml"}) {
		t.Errorf("push trigger = %+v", p)
	}
	if uses := doc.Jobs["tend"].Steps[1]["uses"]; uses != "mohsensc/tend-cli@v1" {
		t.Errorf("uses = %v", uses)
	}
	if with, _ := doc.Jobs["tend"].Steps[0]["with"].(map[string]any); with["persist-credentials"] != false {
		t.Errorf("the checkout keeps its token in the repository: %v", doc.Jobs["tend"].Steps[0])
	}
	if !bytes.Contains(wf, []byte(`cron: "17 6 * * *"`)) {
		t.Errorf("the default schedule is not daily:\n%s", wf)
	}
	if err := os.WriteFile(filepath.Join(dir, "tend.yml"), []byte("schedule: 6h\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := Main([]string{"init", "--ci", "--force", "-q"}, e); code != 0 {
		t.Fatalf("init --ci --force = %d: %s", code, errs.String())
	}
	if wf, _ := os.ReadFile(filepath.Join(dir, ".github", "workflows", "tend.yml")); !bytes.Contains(wf, []byte(`cron: "17 */6 * * *"`)) {
		t.Errorf("schedule: 6h did not reach the workflow:\n%s", wf)
	}
	if err := os.WriteFile(filepath.Join(dir, "tend.yml"), []byte("schedule: 5h\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := Main([]string{"init", "--ci", "--force", "-q"}, e); code != 1 || !strings.Contains(errs.String(), "schedule: want") {
		t.Errorf("init --ci with a bad schedule = %d, %q", code, errs.String())
	}
	errs.Reset()

	errs.Reset()
	if code := Main([]string{"init"}, e); code != 1 || !strings.Contains(errs.String(), "tend.yml exists; --force overwrites it") {
		t.Errorf("second init = %d, %q", code, errs.String())
	}
	if err := os.WriteFile(filepath.Join(dir, "tend.yml"), []byte("cooldown: 5d\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := Main([]string{"init", "--force", "-q"}, e); code != 0 {
		t.Errorf("init --force = %d", code)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "tend.yml")); !bytes.Equal(b, config.Sample) {
		t.Error("--force did not overwrite")
	}
}

// Asked, init runs tend once a person says yes, and only then: a stdin that
// is not a terminal never answers.
func TestAsk(t *testing.T) {
	for in, want := range map[string]bool{"y\n": true, "Yes\n": true, " y ": true, "n\n": false, "\n": false, "": false, "yes please\n": false} {
		var out bytes.Buffer
		if got := ask(Env{Stdout: &out, Stdin: strings.NewReader(in)}, "Run? "); got != want || out.String() != "Run? " {
			t.Errorf("ask(%q) = %v, printed %q", in, got, out.String())
		}
	}
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out bytes.Buffer
	if ask(Env{Stdout: &out, Stdin: f}, "Run? ") || out.Len() > 0 {
		t.Errorf("a stdin that is not a terminal was asked: %q", out.String())
	}
}

func TestCron(t *testing.T) {
	for d, want := range map[time.Duration]string{
		time.Hour:          "17 * * * *",
		6 * time.Hour:      "17 */6 * * *",
		12 * time.Hour:     "17 */12 * * *",
		24 * time.Hour:     "17 6 * * *",
		7 * 24 * time.Hour: "17 6 * * 1",
	} {
		if got := cron(d); got != want {
			t.Errorf("cron(%s) = %q, want %q", d, got, want)
		}
	}
}

// A config tend already reads is never put behind a new tend.yml.
func TestInitExisting(t *testing.T) {
	dir := repo(t, map[string]string{".github/tend.yml": "onboarding: off\n"})
	res := tend(t, dir, t.TempDir(), offline{}, "init")
	if res.code != exitErr || !strings.Contains(res.stderr, ".github/tend.yml exists, and tend reads it") {
		t.Errorf("init = %d, %q", res.code, res.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "tend.yml")); !os.IsNotExist(err) {
		t.Errorf("init wrote tend.yml in front of .github/tend.yml: %v", err)
	}
	res = tend(t, dir, t.TempDir(), offline{}, "init", "--force")
	if res.code != exitOK || !strings.Contains(res.out, "wrote .github/tend.yml") {
		t.Errorf("init --force = %d, %q, %q", res.code, res.out, res.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "tend.yml")); !os.IsNotExist(err) {
		t.Errorf("init --force wrote a second config: %v", err)
	}
}

// init writes the file --config names, and the run it offers reads it.
func TestInitConfig(t *testing.T) {
	dir := repo(t, map[string]string{"README.md": "hi\n"})
	res := tend(t, dir, t.TempDir(), offline{}, "init", "--config", "sub/custom.yml")
	if res.code != exitOK || !strings.Contains(res.out, "wrote sub/custom.yml") {
		t.Fatalf("init = %d, %q, %q", res.code, res.out, res.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "tend.yml")); !os.IsNotExist(err) {
		t.Errorf("init wrote tend.yml too: %v", err)
	}
	if res := tend(t, dir, t.TempDir(), offline{}, "check", "--config", "sub/custom.yml"); res.code != exitOK {
		t.Errorf("check = %d, %q", res.code, res.stderr)
	}
	if res := tend(t, dir, t.TempDir(), offline{}, "check", "--config", "nope.yml"); res.code != exitErr || !strings.Contains(res.stderr, "nope.yml: config file not found") {
		t.Errorf("a missing config = %d, %q", res.code, res.stderr)
	}
}
