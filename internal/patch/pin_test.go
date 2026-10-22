package patch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
)

func TestAddIgnore(t *testing.T) {
	entry := []string{`- name: "@fixture/pay"`, `  versions: [">=2.0.0"]`, "  reason: staying on 1.4.0"}
	item := "  - name: \"@fixture/pay\"\n    versions: [\">=2.0.0\"]\n    reason: staying on 1.4.0\n"
	tests := []struct {
		name, in, want string
	}{
		{"no file", "", "ignore:\n" + item},
		{"no ignore key", "cooldown: 5d\n", "cooldown: 5d\nignore:\n" + item},
		{"no trailing newline", "cooldown: 5d", "cooldown: 5d\nignore:\n" + item},
		{
			"empty list from tend init, comments kept",
			"ignore: []\n# - name: lodash\n\ngroups: []\n",
			"ignore:\n" + item + "# - name: lodash\n\ngroups: []\n",
		},
		{"empty key", "ignore:\nbase: main\n", "ignore:\n" + item + "base: main\n"},
		{
			"appended after the last item",
			"ignore:\n  - name: lodash\n    reason: pinned\n\n  # more later\nbase: main\n",
			"ignore:\n  - name: lodash\n    reason: pinned\n" + item + "\n  # more later\nbase: main\n",
		},
		{
			"list at the end of the file, other indentation",
			"base: main\nignore:\n- name: lodash\n",
			"base: main\nignore:\n- name: lodash\n" + "- name: \"@fixture/pay\"\n  versions: [\">=2.0.0\"]\n  reason: staying on 1.4.0\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := addIgnore([]byte(tt.in), entry)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got\n%s\nwant\n%s", got, tt.want)
			}
			cfg, err := config.Parse(got)
			if err != nil {
				t.Fatal(err)
			}
			d := core.Dep{Ecosystem: core.Npm, Name: "@fixture/pay"}
			if _, ok := cfg.Ignored(d, "2.0.0"); !ok {
				t.Error("2.0.0 is not ignored")
			}
			if _, ok := cfg.Ignored(d, "1.4.1"); ok {
				t.Error("1.4.1 is ignored")
			}
		})
	}
	for _, bad := range []string{"ignore: [{name: x}]\n", "ignore: nope\n", "- a\n"} {
		if _, err := addIgnore([]byte(bad), entry); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestPinPrefersTheConfigInUse(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".github"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, ".github/tend.yml", "cooldown: 5d\n")
	name, err := pin(dir, core.Dep{Ecosystem: core.Npm, Name: "left-pad", Version: "1.1.0"}, "1.3.0")
	if err != nil || name != ".github/tend.yml" {
		t.Fatalf("pin = %q, %v", name, err)
	}
	if got := readFile(t, dir, ".github/tend.yml"); got != "cooldown: 5d\nignore:\n  - name: \"left-pad\"\n    versions: [\">=1.3.0\"]\n    reason: staying on 1.1.0\n" {
		t.Errorf("tend.yml:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "tend.yml")); err == nil {
		t.Error("a second tend.yml appeared")
	}
}

func TestPinWholePackage(t *testing.T) {
	dir := t.TempDir()
	if _, err := pin(dir, core.Dep{Ecosystem: core.Npm, Name: "request", Version: "2.88.2"}, ""); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, dir, "tend.yml"); got != "ignore:\n  - name: \"request\"\n    reason: staying on 2.88.2\n" {
		t.Errorf("tend.yml:\n%s", got)
	}
}

// The baseline's line goes after the project's own test lines, into the
// list tend.yml has or a new one, and every other line stays.
func TestAddTest(t *testing.T) {
	line := "node --test test/tend/smoke.js"
	tests := []struct {
		name, in, want string
		own            []string
	}{
		{"no file", "", "verify:\n  test:\n    - node --test test/tend/smoke.js\n", nil},
		{"own lines", "cooldown: 5d\n", "cooldown: 5d\nverify:\n  test:\n    - npm test\n    - node --test test/tend/smoke.js\n", []string{"npm test"}},
		{"verify without test", "verify:\n  timeout: 30m\n# done\n", "verify:\n  test:\n    - node --test test/tend/smoke.js\n  timeout: 30m\n# done\n", nil},
		{"a test list", "verify:\n  test:\n    - npm test\n  timeout: 30m\n", "verify:\n  test:\n    - npm test\n    - node --test test/tend/smoke.js\n  timeout: 30m\n", []string{"npm test"}},
		{"a line that needs quotes", "", "verify:\n  test:\n    - '- node --test x.js'\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := line
			if tt.name == "a line that needs quotes" {
				l = "- node --test x.js"
			}
			got, err := addTest([]byte(tt.in), tt.own, l)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got\n%s\nwant\n%s", got, tt.want)
			}
			if _, err := config.Parse(got); err != nil {
				t.Errorf("does not parse: %v", err)
			}
		})
	}
	if _, err := addTest([]byte("verify: {test: [npm test]}\n"), nil, line); err == nil {
		t.Error("an inline verify was rewritten")
	}
}
