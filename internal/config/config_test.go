package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/mohsensc/tend-cli/internal/core"
)

func TestDuration(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"3d", 72 * time.Hour, false},
		{"0d", 0, false},
		{"20m", 20 * time.Minute, false},
		{"1h30m", 90 * time.Minute, false},
		{"\"6h\"", 6 * time.Hour, false},
		{"3", 0, true},
		{"d", 0, true},
		{"-1d", 0, true},
		{"106751d", 106751 * 24 * time.Hour, false},
		{"106752d", 0, true},
		{"300000d", 0, true},
		{"1.5d", 0, true},
		{"soon", 0, true},
		{"[1d]", 0, true},
	}
	for _, tt := range tests {
		var v struct {
			D Duration `yaml:"d"`
		}
		err := yaml.Unmarshal([]byte("d: "+tt.in), &v)
		if (err != nil) != tt.err {
			t.Errorf("%s: err = %v, want error %v", tt.in, err, tt.err)
			continue
		}
		if !tt.err && time.Duration(v.D) != tt.want {
			t.Errorf("%s = %s, want %s", tt.in, time.Duration(v.D), tt.want)
		}
	}
}

func TestDurationRoundTrip(t *testing.T) {
	for _, d := range []time.Duration{72 * time.Hour, 20 * time.Minute, 36 * time.Hour, 0} {
		b, err := yaml.Marshal(Duration(d))
		if err != nil {
			t.Fatal(err)
		}
		var got Duration
		if err := yaml.Unmarshal(b, &got); err != nil || time.Duration(got) != d {
			t.Errorf("%s -> %q -> %s, %v", d, b, time.Duration(got), err)
		}
	}
	for d, want := range map[time.Duration]string{6 * time.Hour: "6h", 90 * time.Minute: "1h30m", 20 * time.Minute: "20m", 45 * time.Second: "45s", 36 * time.Hour: "36h"} {
		if s := Duration(d).String(); s != want {
			t.Errorf("Duration(%s) = %q, want %q", d, s, want)
		}
	}
	if s := Duration(72 * time.Hour).String(); s != "3d" {
		t.Errorf("String = %q, want 3d", s)
	}
}

// The defaults, spelled out as tend.yml.
const defaults = `
base: ""
cooldown: 3d
schedule: 1d
onboarding: pr
ecosystems: []
updates: {security: true, deprecations: true, major: false, minor: false, patch: false}
ignore: []
groups: []
apis: []
deps: []
model: {provider: none, model: "", base_url: "", key_env: "", command: "", attempts: 2, turns: 30, tokens: 400000}
verify: {install: [], build: [], test: [], timeout: 20m, container: "", env: []}
deliver: {mode: pr, branch: "", labels: [tend], draft: false, automerge: none, merge_method: squash, reviewers: [], title_prefix: ""}
`

func TestDefault(t *testing.T) {
	var want Config
	dec := yaml.NewDecoder(strings.NewReader(defaults))
	dec.KnownFields(true)
	if err := dec.Decode(&want); err != nil {
		t.Fatal(err)
	}
	got := Default()
	if !reflect.DeepEqual(norm(*got), norm(want)) {
		t.Errorf("Default() = %+v\nwant        %+v", *got, want)
	}
}

// norm treats empty and nil slices alike.
func norm(c Config) Config {
	for _, p := range []*[]string{&c.Ecosystems, &c.Verify.Install, &c.Verify.Build, &c.Verify.Test, &c.Verify.Env, &c.Deliver.Reviewers} {
		if len(*p) == 0 {
			*p = nil
		}
	}
	if len(c.Ignore) == 0 {
		c.Ignore = nil
	}
	if len(c.Groups) == 0 {
		c.Groups = nil
	}
	if len(c.APIs) == 0 {
		c.APIs = nil
	}
	if len(c.Deps) == 0 {
		c.Deps = nil
	}
	return c
}

func TestDeps(t *testing.T) {
	ok := "deps:\n  - {name: protoc, file: scripts/install.sh, match: 'PROTOC_VERSION=(?P<v>[0-9.]+)', source: github:protocolbuffers/protobuf}\n"
	cfg, err := Parse([]byte(ok))
	if err != nil || len(cfg.Deps) != 1 || cfg.Deps[0].Source != "github:protocolbuffers/protobuf" {
		t.Fatalf("deps = %+v, %v", cfg, err)
	}
	for _, bad := range []string{
		"deps:\n  - {name: x, file: a, match: 'X=([0-9.]+)', source: npm:x}\n",
		"deps:\n  - {name: x, file: a, match: 'X=(?P<v>[0-9.]+', source: npm:x}\n",
		"deps:\n  - {name: x, file: a, match: 'X=(?P<v>[0-9.]+)', source: protobuf}\n",
		"deps:\n  - {name: x, match: 'X=(?P<v>[0-9.]+)', source: npm:x}\n",
	} {
		if _, err := Parse([]byte(bad)); err == nil || !strings.Contains(err.Error(), "deps[0]") {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

// The sample changes nothing as written, and its keys, uncommented, are the
// defaults: what it says about them is true.
func TestSample(t *testing.T) {
	got, err := Parse(Sample)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, Default()) {
		t.Errorf("sample = %+v\nwant     %+v", *got, *Default())
	}
	key := regexp.MustCompile(`(?m)^# ((?:  )?[a-z_]+:.*)$`)
	open := key.ReplaceAll(Sample, []byte("$1"))
	if got, err = Parse(open); err != nil {
		t.Fatalf("%v\n%s", err, open)
	}
	if !reflect.DeepEqual(norm(*got), norm(*Default())) {
		t.Errorf("uncommented sample = %+v\nwant     %+v", *got, *Default())
	}
	for _, f := range reflect.VisibleFields(reflect.TypeOf(Config{})) {
		if name := f.Tag.Get("yaml"); !bytes.Contains(Sample, []byte("\n# "+name+":")) {
			t.Errorf("the sample leaves out %s", name)
		}
	}
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		path  string
		used  string
		check func(*Config) bool
		err   string
	}{
		{name: "no file", used: "", check: func(c *Config) bool { return reflect.DeepEqual(c, Default()) }},
		{name: "empty file", files: map[string]string{"tend.yml": ""}, used: "tend.yml", check: func(c *Config) bool { return reflect.DeepEqual(c, Default()) }},
		{name: "comments only", files: map[string]string{"tend.yml": "# nothing\n"}, used: "tend.yml", check: func(c *Config) bool { return reflect.DeepEqual(c, Default()) }},
		{
			name:  "partial keeps defaults",
			files: map[string]string{"tend.yml": "updates: {minor: true}\n"},
			used:  "tend.yml",
			check: func(c *Config) bool { return c.Updates.Minor && c.Updates.Security && !c.Updates.Patch },
		},
		{
			name:  "github dir",
			files: map[string]string{".github/tend.yml": "base: develop\n"},
			used:  ".github/tend.yml",
			check: func(c *Config) bool { return c.Base == "develop" },
		},
		{
			name:  "root wins",
			files: map[string]string{"tend.yml": "base: main\n", ".github/tend.yml": "base: develop\n"},
			used:  "tend.yml",
			check: func(c *Config) bool { return c.Base == "main" },
		},
		{
			name:  "explicit path",
			files: map[string]string{"tend.yml": "base: main\n", "ci/tend.yml": "cooldown: 0d\n"},
			path:  "ci/tend.yml",
			used:  "ci/tend.yml",
			check: func(c *Config) bool { return c.Cooldown == 0 && c.Base == "" },
		},
		{name: "explicit missing", path: "nope.yml", err: "nope.yml"},
		{name: "typo", files: map[string]string{"tend.yml": "updates: {minr: true}\n"}, err: `tend.yml: line 1: unknown key "minr"; did you mean "minor"?`},
		{name: "bad duration", files: map[string]string{"tend.yml": "cooldown: soon\n"}, err: `bad duration "soon"`},
		{name: "invalid", files: map[string]string{"tend.yml": "deliver: {mode: pull}\n"}, err: `deliver.mode: want one of pr, commit, got "pull"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for name, body := range tt.files {
				p := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cfg, used, err := Load(root, tt.path)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("err = %v, want %q", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := ""
			if tt.used != "" {
				want = filepath.Join(root, tt.used)
			}
			if used != want {
				t.Errorf("used %q, want %q", used, want)
			}
			if !tt.check(cfg) {
				t.Errorf("config = %+v", cfg)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct{ in, want string }{
		{"updats: {minor: true}\n", `line 1: unknown key "updats"; did you mean "updates"?`},
		{"ignore: [lodash]\n", `line 1: ignore entries are maps, for example "- name: lodash"`},
		{"ignore:\n  - lodash\n", `line 2: ignore entries are maps, for example "- name: lodash"`},
		{"updates: {major: yes please}\n", `line 1: want true or false, not "yes please"`},
		{"model: {provider: openai, turns: many}\n", `line 1: want a whole number, not "many"`},
		{"model: {provider: openai, models: x}\n", `line 1: unknown key "models"; did you mean "model"?`},
		{"verify: {test: go test}\n", `line 1: want a list, such as [go test]`},
		{"updates: true\n", `line 1: want a map with the keys security, deprecations, major, minor, patch`},
		{"zzz: 1\n", `line 1: unknown key "zzz"`},
		{"base: main\n---\nbogus: 1\n", "line 3: only one YAML document is allowed"},
		{"cooldown: 200000d\n", `line 1: cooldown: bad duration "200000d"; want e.g. 20m, 1h30m or 3d`},
	}
	for _, tt := range tests {
		_, err := Parse([]byte(tt.in))
		if err == nil || err.Error() != tt.want {
			t.Errorf("Parse(%q) = %v, want %q", tt.in, err, tt.want)
		}
	}
}

// A file may end with a bare document marker or a comment after one.
func TestParseOneDocument(t *testing.T) {
	for _, in := range []string{"base: main\n---\n", "base: main\n---\n# later\n", "---\nbase: main\n"} {
		if cfg, err := Parse([]byte(in)); err != nil || cfg.Base != "main" {
			t.Errorf("Parse(%q) = %+v, %v", in, cfg, err)
		}
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Config)
		err  string
	}{
		{"defaults", func(*Config) {}, ""},
		{"provider", func(c *Config) { c.Model.Provider = "gpt" }, "model.provider"},
		{"command without line", func(c *Config) { c.Model.Provider = "command" }, "model.command"},
		{"command with line", func(c *Config) { c.Model.Provider, c.Model.Command = "command", "llm" }, ""},
		{"attempts", func(c *Config) { c.Model.Attempts = 0 }, "model.attempts"},
		{"turns", func(c *Config) { c.Model.Turns = 0 }, "model.turns"},
		{"tokens", func(c *Config) { c.Model.Tokens = -1 }, "model.tokens"},
		{"mode", func(c *Config) { c.Deliver.Mode = "push" }, "deliver.mode"},
		{"automerge", func(c *Config) { c.Deliver.AutoMerge = "major" }, "deliver.automerge"},
		{"merge method", func(c *Config) { c.Deliver.MergeMethod = "ff" }, "deliver.merge_method"},
		{"cooldown max", func(c *Config) { c.Cooldown = Duration(91 * 24 * time.Hour) }, "cooldown"},
		{"cooldown 90d", func(c *Config) { c.Cooldown = Duration(90 * 24 * time.Hour) }, ""},
		{"timeout", func(c *Config) { c.Verify.Timeout = 0 }, "verify.timeout"},
		{"schedule hourly", func(c *Config) { c.Schedule = Duration(time.Hour) }, ""},
		{"schedule weekly", func(c *Config) { c.Schedule = Duration(7 * 24 * time.Hour) }, ""},
		{"schedule odd hours", func(c *Config) { c.Schedule = Duration(5 * time.Hour) }, "schedule: want 1h"},
		{"schedule too often", func(c *Config) { c.Schedule = Duration(30 * time.Minute) }, "schedule"},
		{"onboarding quiet", func(c *Config) { c.Onboarding = "quiet" }, ""},
		{"onboarding off", func(c *Config) { c.Onboarding = "off" }, ""},
		{"onboarding unknown", func(c *Config) { c.Onboarding = "welcome" }, "onboarding: want one of pr, quiet, off"},
		{"api missing spec", func(c *Config) { c.APIs = []API{{Name: "billing", Hosts: []string{"b.internal"}}} }, "apis[0]"},
		{"api ok", func(c *Config) { c.APIs = []API{{Name: "billing", Hosts: []string{"b.internal"}, Spec: "api.yml"}} }, ""},
		{"ignore name", func(c *Config) { c.Ignore = []Ignore{{Ecosystem: "npm"}} }, "ignore[0]: name"},
		{"ignore range", func(c *Config) { c.Ignore = []Ignore{{Name: "x", Versions: []string{">=five"}}} }, "bad version range"},
		{"ignore api", func(c *Config) { c.Ignore = []Ignore{{Name: "stripe", Ecosystem: "api"}} }, ""},
		{"group", func(c *Config) { c.Groups = []Group{{Name: "aws"}} }, "groups[0]"},
		{"several", func(c *Config) { c.Model.Attempts, c.Deliver.Mode = 0, "y" }, "deliver.mode"},
		{"env passes a registry", func(c *Config) { c.Verify.Env = []string{"NPM_CONFIG_REGISTRY", "_private_1"} }, ""},
		{"env passes a key", func(c *Config) { c.Verify.Env = []string{"STRIPE_TEST_KEY"} }, ""},
		{"env not a name", func(c *Config) { c.Verify.Env = []string{"A=B"} }, `verify.env[0]: "A=B" is not a variable name`},
		{"env empty name", func(c *Config) { c.Verify.Env = []string{""} }, "verify.env[0]"},
		{"env the repo token", func(c *Config) { c.Verify.Env = []string{"PATH", "GITHUB_TOKEN"} }, "verify.env[1]: GITHUB_TOKEN"},
		{"env gh", func(c *Config) { c.Verify.Env = []string{"GH_TOKEN"} }, "verify.env[0]"},
		{"env tend's model key", func(c *Config) { c.Verify.Env = []string{"TEND_MODEL_KEY"} }, "verify.env[0]: TEND_MODEL_KEY"},
		{"key from the repo token", func(c *Config) { c.Model.Provider, c.Model.KeyEnv = "openai", "GITHUB_TOKEN" }, "model.key_env: GITHUB_TOKEN"},
		{"key from gh", func(c *Config) { c.Model.KeyEnv = "GH_TOKEN" }, "model.key_env"},
		{"key from tend's own", func(c *Config) { c.Model.KeyEnv = "TEND_SIGNING_KEY" }, "model.key_env"},
		{"key from the usual place", func(c *Config) { c.Model.Provider, c.Model.Model, c.Model.KeyEnv = "openai", "m", "TEND_MODEL_KEY" }, ""},
		{"key not a name", func(c *Config) { c.Model.Provider, c.Model.Model, c.Model.KeyEnv = "openai", "m", "a b" }, `model.key_env: "a b" is not a variable name`},
		{"key in the clear", func(c *Config) { c.Model.Provider, c.Model.BaseURL = "openai", "http://example.net/v1" }, "model.base_url: http is only allowed for a local server"},
		{"key to this machine", func(c *Config) {
			c.Model.Provider, c.Model.Model, c.Model.BaseURL = "openai", "m", "http://127.0.0.1:8080/v1"
		}, ""},
		{"key over https", func(c *Config) {
			c.Model.Provider, c.Model.Model, c.Model.BaseURL = "openai", "m", "https://llm.internal/v1"
		}, ""},
		{"no key on the lan", func(c *Config) {
			c.Model.Provider, c.Model.Model, c.Model.BaseURL = "ollama", "qwen3", "http://gpu-box:11434/v1"
		}, ""},
		{"base url not http", func(c *Config) {
			c.Model.Provider, c.Model.Model, c.Model.BaseURL = "openai", "m", "ftp://localhost/v1"
		}, `model.base_url: want an http or https URL, got "ftp://localhost/v1"`},
		{"base url without a host", func(c *Config) { c.Model.Provider, c.Model.Model, c.Model.BaseURL = "ollama", "m", "localhost:11434" }, "model.base_url: want an http or https URL"},
		{"model id missing", func(c *Config) { c.Model.Provider = "anthropic" }, "model.model: required for model.provider anthropic"},
		{"model id not needed for a command", func(c *Config) { c.Model.Provider, c.Model.Command = "command", "llm" }, ""},
		{"base", func(c *Config) { c.Base = "release/2.x" }, ""},
		{"base not a branch", func(c *Config) { c.Base = "main..dev" }, `base: "main..dev" is not a branch name`},
		{"base with a space", func(c *Config) { c.Base = "my branch" }, "base: "},
		{"base a ref", func(c *Config) { c.Base = "refs/heads/main^" }, "base: "},
		{"deliver branch not a branch", func(c *Config) { c.Deliver.Branch = "-x" }, `deliver.branch: "-x" is not a branch name`},
		{"dep glob", func(c *Config) {
			c.Deps = []Dep{{Name: "x", File: "[abc", Match: `v(?P<v>\S+)`, Source: "github:a/b"}}
		}, `deps[0]: file "[abc" is not a glob`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Default()
			tt.edit(c)
			err := c.Validate()
			if tt.err == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Fatalf("err = %v, want %q", err, tt.err)
			}
		})
	}
}

func TestOff(t *testing.T) {
	c := Default()
	if c.Off("npm") {
		t.Error("npm is off by default")
	}
	c.Ecosystems = []string{"npm", "docker"}
	c.Ignore = []Ignore{{Name: "*", Ecosystem: "docker"}}
	for eco, want := range map[string]bool{"npm": false, "docker": true, "go": true} {
		if got := c.Off(eco); got != want {
			t.Errorf("Off(%s) = %v, want %v", eco, got, want)
		}
	}
}

func TestWants(t *testing.T) {
	kinds := []core.Kind{core.Security, core.Breaking, core.Deprecation, core.Update}
	levels := []string{"major", "minor", "patch", ""}
	flags := []string{"security", "deprecations", "major", "minor", "patch"}
	// want[flag][kind/level]: with only that flag on, does the change get a PR.
	want := map[string]map[string]bool{
		"security":     {"security/major": true, "security/minor": true, "security/patch": true, "security/": true},
		"deprecations": {"deprecation/major": true, "deprecation/minor": true, "deprecation/patch": true, "deprecation/": true},
		"major":        {"update/major": true},
		"minor":        {"update/minor": true},
		"patch":        {"update/patch": true},
	}
	for _, m := range want {
		for _, l := range levels {
			m["breaking/"+l] = true
		}
	}
	for _, flag := range flags {
		c := Default()
		c.Updates = Updates{}
		switch flag {
		case "security":
			c.Updates.Security = true
		case "deprecations":
			c.Updates.Deprecations = true
		case "major":
			c.Updates.Major = true
		case "minor":
			c.Updates.Minor = true
		case "patch":
			c.Updates.Patch = true
		}
		for _, k := range kinds {
			for _, l := range levels {
				key := string(k) + "/" + l
				if got := c.Wants(k, l); got != want[flag][key] {
					t.Errorf("%s on: Wants(%s) = %v", flag, key, got)
				}
			}
		}
	}
}

func TestIgnored(t *testing.T) {
	c := Default()
	c.Ignore = []Ignore{
		{Name: "lodash", Reason: "pinned on purpose"},
		{Name: "@types/*", Ecosystem: "npm"},
		{Name: "actions/*", Ecosystem: "actions", Versions: []string{">=5"}},
		{Name: "react", Versions: []string{"^19", "~17.0"}},
		{Name: "django", Ecosystem: "pip", Versions: []string{">=5.0,<5.2", "==6.0.1"}},
		{Name: "zero", Versions: []string{"^0.2.3"}},
		{Name: "tiny", Versions: []string{"^0.0.3"}},
		{Name: "stripe", Ecosystem: "api"},
	}
	tests := []struct {
		eco, name, to string
		want          bool
		reason        string
	}{
		{"npm", "lodash", "4.17.21", true, "pinned on purpose"},
		{"npm", "lodash-es", "4.17.21", false, ""},
		{"npm", "@types/node", "22.0.0", true, "ignored by tend.yml (@types/*)"},
		{"pip", "@types/node", "22.0.0", false, ""},
		{"actions", "actions/checkout", "v5", true, ""},
		{"actions", "actions/checkout", "v4.2.2", false, ""},
		{"actions", "docker/login-action", "v5", false, ""},
		{"npm", "react", "19.2.0", true, ""},
		{"npm", "react", "20.0.0", false, ""},
		{"npm", "react", "17.0.9", true, ""},
		{"npm", "react", "17.1.0", false, ""},
		{"pip", "django", "5.1.4", true, ""},
		{"pip", "django", "5.2.0", false, ""},
		{"pip", "django", "6.0.1", true, ""},
		{"pip", "django", "6.0.2", false, ""},
		{"npm", "zero", "0.2.9", true, ""},
		{"npm", "zero", "0.3.0", false, ""},
		{"npm", "tiny", "0.0.3", true, ""},
		{"npm", "tiny", "0.0.4", false, ""},
		{"api", "stripe", "", true, ""},
		{"npm", "stripe", "16.0.0", false, ""},
		{"npm", "react", "not-a-version", false, ""},
	}
	for _, tt := range tests {
		reason, got := c.Ignored(core.Dep{Ecosystem: tt.eco, Name: tt.name}, tt.to)
		if got != tt.want || tt.reason != "" && reason != tt.reason {
			t.Errorf("Ignored(%s %s@%s) = %q, %v; want %v %q", tt.eco, tt.name, tt.to, reason, got, tt.want, tt.reason)
		}
	}
}

// Keeps holds whatever versions an entry names: an entry that pins a
// dependency still says tend.yml cares about it.
func TestKeeps(t *testing.T) {
	c := Default()
	c.Ignore = []Ignore{
		{Name: "lodash"},
		{Name: "@types/*", Ecosystem: "npm"},
		{Name: "react", Versions: []string{">=19"}},
	}
	tests := []struct {
		eco, name string
		want      bool
	}{
		{"npm", "lodash", true},
		{"pip", "lodash", true},
		{"npm", "lodash-es", false},
		{"npm", "@types/node", true},
		{"pip", "@types/node", false},
		{"npm", "react", true},
		{"npm", "react-dom", false},
	}
	for _, tt := range tests {
		if got := c.Keeps(core.Dep{Ecosystem: tt.eco, Name: tt.name}); got != tt.want {
			t.Errorf("Keeps(%s %s) = %v, want %v", tt.eco, tt.name, got, tt.want)
		}
	}
}

// The starter a first PR adds changes nothing as written, and every key it
// shows, uncommented, is a real one.
func TestStarter(t *testing.T) {
	got, err := Parse(Starter)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, Default()) {
		t.Errorf("starter = %+v\nwant      %+v", *got, *Default())
	}
	open := regexp.MustCompile(`(?m)^# ((?:  )*(?:- )?[a-z_]+:(?: .*)?)$`).ReplaceAll(Starter, []byte("$1"))
	if _, err := Parse(open); err != nil {
		t.Fatalf("%v\n%s", err, open)
	}
	if n := bytes.Count(Starter, []byte("\n")); n > 20 {
		t.Errorf("the starter has %d lines; it is meant to be short", n)
	}
}

func TestGlob(t *testing.T) {
	tests := []struct {
		pattern, name string
		want          bool
	}{
		{"lodash", "lodash", true},
		{"lodash", "lodash.merge", false},
		{"lodash*", "lodash.merge", true},
		{"@aws-sdk/*", "@aws-sdk/client-s3", true},
		{"@aws-sdk/*", "aws-sdk", false},
		{"actions/*", "actions/checkout", true},
		{"github.com/aws/*", "github.com/aws/aws-sdk-go-v2/service/s3", true},
		{"*", "anything/at/all", true},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"*-sdk-*", "aws-sdk-s3", true},
		{"", "", true},
		{"", "x", false},
	}
	for _, tt := range tests {
		if got := Glob(tt.pattern, tt.name); got != tt.want {
			t.Errorf("Glob(%q, %q) = %v", tt.pattern, tt.name, got)
		}
	}
}

func TestModelLine(t *testing.T) {
	tests := []struct {
		in       string
		provider string
		model    string
		fail     string
	}{
		{"model: groq/openai/gpt-oss-120b\n", "groq", "openai/gpt-oss-120b", ""},
		{"model: ollama/qwen3\n", "ollama", "qwen3", ""},
		{"model: {provider: openai, model: m, base_url: http://localhost:8080/v1}\n", "openai", "m", ""},
		{"model: groq\n", "", "", "want provider/model"},
		{"model: none\n", "none", "", ""},
		{"model: {provider: groq, name: openai/gpt-oss-120b}\n", "groq", "openai/gpt-oss-120b", ""},
		{"model: {provider: openai, model: m, name: n}\n", "", "", "the same key"},
		{"model: {provider: openai, model: m, temperature: 0}\n", "", "", `unknown key "temperature"`},
		{"model: nobody/m\n", "", "", "model.provider"},
	}
	for _, tt := range tests {
		cfg, err := Parse([]byte(tt.in))
		if tt.fail != "" {
			if err == nil || !strings.Contains(err.Error(), tt.fail) {
				t.Errorf("%q: err = %v, want %q", tt.in, err, tt.fail)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", tt.in, err)
			continue
		}
		if cfg.Model.Provider != tt.provider || cfg.Model.Model != tt.model || cfg.Model.Attempts != 2 {
			t.Errorf("%q: model = %+v", tt.in, cfg.Model)
		}
	}
}

// A bad duration names its key, as every other config error does.
func TestDurationKey(t *testing.T) {
	for in, want := range map[string]string{
		"cooldown: 3x\n":             `line 1: cooldown: bad duration "3x"; want e.g. 20m, 1h30m or 3d`,
		"verify:\n  timeout: soon\n": `line 2: verify.timeout: bad duration "soon"`,
	} {
		_, err := Parse([]byte(in))
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("Parse(%q) = %v, want %s", in, err, want)
		}
	}
}
