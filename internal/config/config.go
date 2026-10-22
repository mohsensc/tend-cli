// Package config reads tend.yml.
package config

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/mohsensc/tend-cli/internal/core"
)

type Config struct {
	Base       string   `yaml:"base"`
	Cooldown   Duration `yaml:"cooldown"`
	Schedule   Duration `yaml:"schedule"`
	Onboarding string   `yaml:"onboarding"`
	Ecosystems []string `yaml:"ecosystems"`
	Updates    Updates  `yaml:"updates"`
	Ignore     []Ignore `yaml:"ignore"`
	Groups     []Group  `yaml:"groups"`
	APIs       []API    `yaml:"apis"`
	Deps       []Dep    `yaml:"deps"`
	Model      Model    `yaml:"model"`
	Verify     Verify   `yaml:"verify"`
	Deliver    Deliver  `yaml:"deliver"`
}

type Updates struct {
	Security     bool `yaml:"security"`
	Deprecations bool `yaml:"deprecations"`
	Major        bool `yaml:"major"`
	Minor        bool `yaml:"minor"`
	Patch        bool `yaml:"patch"`
}

type Ignore struct {
	Name      string   `yaml:"name"`
	Ecosystem string   `yaml:"ecosystem"`
	Reason    string   `yaml:"reason"`
	Versions  []string `yaml:"versions"`
}

type Group struct {
	Name  string   `yaml:"name"`
	Match []string `yaml:"match"`
}

type API struct {
	Name      string   `yaml:"name"`
	Hosts     []string `yaml:"hosts"`
	Spec      string   `yaml:"spec"`
	Changelog string   `yaml:"changelog"`
	Probe     bool     `yaml:"probe"`
}

// Dep is a version written anywhere tend has no adapter for: a regexp finds
// it in the files a glob names, and a source says where its releases are.
type Dep struct {
	Name   string `yaml:"name"`
	File   string `yaml:"file"`
	Match  string `yaml:"match"`  // has a group named v around the version
	Source string `yaml:"source"` // github:<owner>/<repo>, npm:<pkg>, pypi:<pkg>, <ecosystem>:<name>, url:<template with {name}>
}

// Model is the provider that migrates code. A model with tool calling works
// the change itself, within Turns tool calls and Tokens tokens (0 for no
// limit); one without writes a diff, in at most Attempts tries.
type Model struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	BaseURL  string `yaml:"base_url"`
	KeyEnv   string `yaml:"key_env"`
	Command  string `yaml:"command"`
	Attempts int    `yaml:"attempts"`
	Turns    int    `yaml:"turns"`
	Tokens   int    `yaml:"tokens"`
}

// UnmarshalYAML reads the mapping, or one line that names the provider and
// the model it serves: "groq/openai/gpt-oss-120b".
func (m *Model) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		provider, id, ok := strings.Cut(n.Value, "/")
		if !ok || provider == "" || id == "" {
			return fmt.Errorf("line %d: model: want provider/model or a mapping, got %q", n.Line, n.Value)
		}
		m.Provider, m.Model = provider, id
		return nil
	}
	if err := idKey(n); err != nil {
		return err
	}
	if err := known(n, Model{}); err != nil {
		return err
	}
	type plain Model
	return n.Decode((*plain)(m))
}

// sendsKey says whether the provider sends an API key with every request.
func (m Model) sendsKey() bool {
	switch m.Provider {
	case "anthropic", "openai", "groq":
		return true
	case "ollama":
		return m.KeyEnv != ""
	}
	return false
}

// private says whether a key sent to u stays between tend and the server:
// over https, or to this machine.
func private(u string) bool {
	p, err := url.Parse(u)
	if err != nil {
		return false
	}
	switch p.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return p.Scheme == "http" || p.Scheme == "https"
	}
	return p.Scheme == "https"
}

// idKey lets the model id be written as name, the other word people use for it.
func idKey(n *yaml.Node) error {
	var name, model *yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		switch k := n.Content[i]; k.Value {
		case "name":
			name = k
		case "model":
			model = k
		}
	}
	if name != nil && model != nil {
		return fmt.Errorf("line %d: model: name and model are the same key; keep one", name.Line)
	}
	if name != nil {
		name.Value = "model"
	}
	return nil
}

type Verify struct {
	Install   []string `yaml:"install"`
	Build     []string `yaml:"build"`
	Test      []string `yaml:"test"`
	Timeout   Duration `yaml:"timeout"`
	Container string   `yaml:"container"`
	Env       []string `yaml:"env"`
}

type Deliver struct {
	Mode        string   `yaml:"mode"`
	Branch      string   `yaml:"branch"`
	Labels      []string `yaml:"labels"`
	Draft       bool     `yaml:"draft"`
	AutoMerge   string   `yaml:"automerge"`
	MergeMethod string   `yaml:"merge_method"`
	Reviewers   []string `yaml:"reviewers"`
	TitlePrefix string   `yaml:"title_prefix"`
}

// Duration is a time.Duration written as Go syntax ("20m", "1h30m") or whole days ("3d").
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

func (d Duration) String() string {
	v := time.Duration(d)
	if v > 0 && v%(24*time.Hour) == 0 {
		return strconv.Itoa(int(v/(24*time.Hour))) + "d"
	}
	return v.String()
}

func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("bad duration %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("bad duration %q", s)
	}
	return v, nil
}

func Default() *Config {
	return &Config{
		Cooldown:   Duration(72 * time.Hour),
		Schedule:   Duration(24 * time.Hour),
		Onboarding: "pr",
		Updates:    Updates{Security: true, Deprecations: true},
		Model:      Model{Provider: "none", Attempts: 2, Turns: 30, Tokens: 400_000},
		Verify:     Verify{Timeout: Duration(20 * time.Minute)},
		Deliver: Deliver{
			Mode:        "pr",
			Labels:      []string{"tend"},
			AutoMerge:   "none",
			MergeMethod: "squash",
		},
	}
}

// Sample is what `tend init` writes: every key at its default, commented out,
// so the file changes nothing until a line is uncommented.
//
//go:embed sample.yml
var Sample []byte

// Starter is the tend.yml a first PR adds to a repo with nothing to catch up
// on: a few keys people change first, commented out, so it changes nothing.
//
//go:embed starter.yml
var Starter []byte

// Load reads tend.yml, then .github/tend.yml, under root, or path when set. It
// returns the path it used, "" when there was no file and the defaults apply.
func Load(root, path string) (*Config, string, error) {
	if path != "" {
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, "", err
		}
		cfg, err := Parse(b)
		if err != nil {
			return nil, "", at(shown(root, path), err)
		}
		return cfg, path, nil
	}
	for _, name := range []string{"tend.yml", filepath.Join(".github", "tend.yml")} {
		p := filepath.Join(root, name)
		b, err := os.ReadFile(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		cfg, err := Parse(b)
		if err != nil {
			return nil, "", at(name, err)
		}
		return cfg, p, nil
	}
	return Default(), "", nil
}

// at puts the file's name in front of each line of err.
func at(name string, err error) error {
	return errors.New(name + ": " + strings.ReplaceAll(err.Error(), "\n", "\n"+name+": "))
}

// shown is p as a person would type it: relative to root when it is inside.
func shown(root, p string) string {
	if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return p
}

// Parse decodes a tend.yml over the defaults and validates it. Unknown keys are
// errors, so a typo cannot silently turn something off.
func Parse(b []byte) (*Config, error) {
	cfg := Default()
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, plain(err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

var (
	providers    = []string{"none", "anthropic", "openai", "groq", "ollama", "command"}
	onboardings  = []string{"pr", "quiet", "off"}
	modes        = []string{"pr", "commit"}
	automerges   = []string{"none", "patch", "minor", "security"}
	mergeMethods = []string{"merge", "squash", "rebase"}
)

const maxCooldown = 90 * 24 * time.Hour

// Schedules are the intervals a cron line can keep exactly: hours that divide
// a day, a day, and a week.
var Schedules = []time.Duration{
	time.Hour, 2 * time.Hour, 3 * time.Hour, 4 * time.Hour, 6 * time.Hour, 8 * time.Hour,
	12 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour,
}

func (c *Config) Validate() error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	oneOf := func(key, v string, allowed []string) {
		if !slices.Contains(allowed, v) {
			bad("%s: want one of %s, got %q", key, strings.Join(allowed, ", "), v)
		}
	}

	if cd := time.Duration(c.Cooldown); cd < 0 || cd > maxCooldown {
		bad("cooldown: want 0d to 90d, got %s", c.Cooldown)
	}
	if !slices.Contains(Schedules, time.Duration(c.Schedule)) {
		bad("schedule: want 1h, 2h, 3h, 4h, 6h, 8h, 12h, 1d or 7d, got %s", c.Schedule)
	}
	oneOf("onboarding", c.Onboarding, onboardings)
	for i, ig := range c.Ignore {
		if ig.Name == "" {
			bad("ignore[%d]: name is required", i)
		}
		for _, r := range ig.Versions {
			if _, err := parseRange(r); err != nil {
				bad("ignore[%d]: %v", i, err)
			}
		}
	}
	for i, g := range c.Groups {
		if g.Name == "" || len(g.Match) == 0 {
			bad("groups[%d]: name and match are required", i)
		}
	}
	for i, a := range c.APIs {
		if a.Name == "" || len(a.Hosts) == 0 || a.Spec == "" && a.Changelog == "" {
			bad("apis[%d]: name, hosts and a spec or a changelog are required", i)
		}
	}
	for i, d := range c.Deps {
		if d.Name == "" || d.File == "" || d.Match == "" || d.Source == "" {
			bad("deps[%d]: name, file, match and source are required", i)
			continue
		}
		if re, err := regexp.Compile(d.Match); err != nil || re.SubexpIndex("v") < 0 {
			bad("deps[%d]: match must be a regexp with a group (?P<v>...) around the version", i)
		}
		if scheme, rest, _ := strings.Cut(d.Source, ":"); scheme == "" || rest == "" {
			bad("deps[%d]: source wants github:, npm:, pypi:, url: or another ecosystem's name, got %q", i, d.Source)
		}
	}
	oneOf("model.provider", c.Model.Provider, providers)
	if c.Model.Provider == "command" && c.Model.Command == "" {
		bad("model.command: required when model.provider is command")
	}
	if k := c.Model.KeyEnv; k == "GITHUB_TOKEN" || k == "GH_TOKEN" || strings.HasPrefix(k, "TEND_") && k != "TEND_MODEL_KEY" {
		bad("model.key_env: %s is not a model key; name the variable that holds one", k)
	}
	if u := c.Model.BaseURL; u != "" && c.Model.sendsKey() && !private(u) {
		bad("model.base_url: http is only allowed for a local server")
	}
	if c.Model.Attempts < 1 {
		bad("model.attempts: want at least 1, got %d", c.Model.Attempts)
	}
	if c.Model.Turns < 1 {
		bad("model.turns: want at least 1, got %d", c.Model.Turns)
	}
	if c.Model.Tokens < 0 {
		bad("model.tokens: want 0 or more, got %d", c.Model.Tokens)
	}
	if c.Verify.Timeout <= 0 {
		bad("verify.timeout: want more than 0, got %s", c.Verify.Timeout)
	}
	for i, k := range c.Verify.Env {
		switch {
		case !envName.MatchString(k):
			bad("verify.env[%d]: %q is not a variable name", i, k)
		case k == "GITHUB_TOKEN" || k == "GH_TOKEN" || strings.HasPrefix(k, "TEND_"):
			bad("verify.env[%d]: %s is tend's own and never reaches the project's commands", i, k)
		}
	}
	oneOf("deliver.mode", c.Deliver.Mode, modes)
	oneOf("deliver.automerge", c.Deliver.AutoMerge, automerges)
	oneOf("deliver.merge_method", c.Deliver.MergeMethod, mergeMethods)
	return errors.Join(errs...)
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Off reports whether tend.yml turns an ecosystem off: ecosystems lists
// others but not it, or an ignore entry with the name "*" names it.
func (c *Config) Off(eco string) bool {
	if len(c.Ecosystems) > 0 && !slices.Contains(c.Ecosystems, eco) {
		return true
	}
	return slices.ContainsFunc(c.Ignore, func(ig Ignore) bool {
		return ig.Name == "*" && ig.Ecosystem == eco && len(ig.Versions) == 0
	})
}

// Wants reports whether a change of kind k at level gets a PR. A breaking
// change always does: it breaks code this project has, whatever the level.
// The flags decide only about plain bumps.
func (c *Config) Wants(k core.Kind, level string) bool {
	u := c.Updates
	switch k {
	case core.Security:
		return u.Security
	case core.Deprecation:
		return u.Deprecations
	case core.Breaking:
		return true
	case core.Update:
		switch level {
		case "major":
			return u.Major
		case "minor":
			return u.Minor
		case "patch":
			return u.Patch
		}
	}
	return false
}

// known rejects keys of a mapping that v's yaml tags do not name. A custom
// UnmarshalYAML decodes without the caller's strictness, so it checks itself.
func known(n *yaml.Node, v any) error {
	t := reflect.TypeOf(v)
	names := map[string]bool{}
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		names[name] = true
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if k := n.Content[i]; !names[k.Value] {
			return errors.New(unknownKey(strconv.Itoa(k.Line), k.Value, t))
		}
	}
	return nil
}
