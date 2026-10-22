package eco

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRegistry(t *testing.T) {
	var names []string
	for _, e := range All() {
		names = append(names, e.Name())
	}
	want := []string{"npm", "go", "pip", "maven", "gradle", "cargo", "rubygems", "composer", "nuget", "docker", "actions", "toolchain", "git", "terraform", "helm"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("All = %v, want %v", names, want)
	}
	for _, n := range want {
		if e := ByName(n); e == nil || e.Name() != n {
			t.Errorf("ByName(%q) = %v", n, e)
		}
	}
	if ByName("bower") != nil {
		t.Error("ByName found an unknown ecosystem")
	}
}

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

var tree = map[string]string{
	"package.json":                       "{}",
	"web/package.json":                   "{}",
	"node_modules/left-pad/package.json": "{}",
	"vendor/x/package.json":              "{}",
	"build/package.json":                 "{}",
	"ignored/package.json":               "{}",
	"Dockerfile":                         "FROM node:22",
	"api/Dockerfile.prod":                "FROM node:22",
	".github/workflows/ci.yml":           "on: push",
	"docs/ci.yml":                        "x",
	".gitignore":                         "ignored/\n",
}

func TestWalk(t *testing.T) {
	tests := []struct {
		names []string
		git   []string
		plain []string // without git, ignore rules do not apply
	}{
		{[]string{"package.json"}, []string{"package.json", "web/package.json"}, []string{"ignored/package.json", "package.json", "web/package.json"}},
		{[]string{"Dockerfile*"}, []string{"Dockerfile", "api/Dockerfile.prod"}, nil},
		{[]string{".github/workflows/*.yml"}, []string{".github/workflows/ci.yml"}, nil},
		{[]string{"package.json", "Dockerfile"}, []string{"Dockerfile", "package.json", "web/package.json"}, []string{"Dockerfile", "ignored/package.json", "package.json", "web/package.json"}},
		{[]string{"go.mod"}, nil, nil},
	}
	check := func(t *testing.T, root string, git bool) {
		for _, tt := range tests {
			got, err := walk(root, tt.names...)
			if err != nil {
				t.Fatal(err)
			}
			want := tt.git
			if !git && tt.plain != nil {
				want = tt.plain
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("walk(%v) = %v, want %v", tt.names, got, want)
			}
		}
	}

	t.Run("git", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not found")
		}
		root := t.TempDir()
		write(t, root, tree)
		if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
		check(t, root, true)
	})
	t.Run("plain dir", func(t *testing.T) {
		root := t.TempDir()
		write(t, root, tree)
		check(t, root, false)
	})
}

func TestHave(t *testing.T) {
	if !have("sh") {
		t.Error("sh not found")
	}
	if have("tend-no-such-tool") {
		t.Error("found a tool that does not exist")
	}
}

func TestRewriteLine(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		line     int
		from, to string
		want     string
		err      bool
	}{
		{"lf", "a\n\"x\": \"^1.0.0\",\nc\n", 2, "1.0.0", "2.0.0", "a\n\"x\": \"^2.0.0\",\nc\n", false},
		{"crlf", "a\r\nb 1.0\r\nc\r\n", 2, "1.0", "1.1", "a\r\nb 1.1\r\nc\r\n", false},
		{"bom", "\xef\xbb\xbfFROM node:16\nRUN x\n", 1, "16", "22", "\xef\xbb\xbfFROM node:22\nRUN x\n", false},
		{"no trailing newline", "a\nb 1.0", 2, "1.0", "1.1", "a\nb 1.1", false},
		{"first occurrence only", "x 1.0 1.0\n", 1, "1.0", "2.0", "x 2.0 1.0\n", false},
		{"only that line", "1.0\n1.0\n1.0\n", 2, "1.0", "2.0", "1.0\n2.0\n1.0\n", false},
		{"longer", "v: 9\n", 1, "9", "10", "v: 10\n", false},
		{"not on line", "a 1.0\nb\n", 2, "1.0", "2.0", "", true},
		{"no such line", "a\n", 3, "a", "b", "", true},
		{"line zero", "a\n", 0, "a", "b", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "f")
			if err := os.WriteFile(p, []byte(tt.in), 0o640); err != nil {
				t.Fatal(err)
			}
			err := rewriteLine(p, tt.line, tt.from, tt.to)
			got, _ := os.ReadFile(p)
			if tt.err {
				if err == nil || string(got) != tt.in {
					t.Fatalf("err = %v, file = %q; want an error and the file untouched", err, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
			if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o640 {
				t.Errorf("mode = %v, want 0640", fi.Mode().Perm())
			}
		})
	}
}

func TestRun(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	out, err := run(ctx, dir, []string{"TEND_X=hi"}, "echo $TEND_X; pwd; echo oops >&2")
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(dir)
	if !strings.Contains(out, "hi\n") || !strings.Contains(out, real) || !strings.Contains(out, "oops") {
		t.Errorf("out = %q", out)
	}

	out, err = run(ctx, dir, nil, "echo before; exit 3")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("err = %v, want exit status 3", err)
	}
	if out != "before\n" {
		t.Errorf("out = %q, want the output before the failure", out)
	}

	defer func(d time.Duration) { runTimeout = d }(runTimeout)
	runTimeout = 200 * time.Millisecond
	start := time.Now()
	_, err = run(ctx, dir, nil, "sleep 5")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("timeout took %s", took)
	}
}

func TestRead(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"Cargo.toml":   "[package]\nname = \"x\"\n",
		"package.json": `{"name": "x"}`,
		"pnpm.yaml":    "name: x\n",
		"bad.json":     "{",
	})
	type pkg struct {
		Name string `json:"name" yaml:"name"`
	}
	var a struct{ Package pkg }
	var b, c pkg
	if err := readTOML(filepath.Join(dir, "Cargo.toml"), &a); err != nil || a.Package.Name != "x" {
		t.Errorf("readTOML = %+v, %v", a, err)
	}
	if err := readJSON(filepath.Join(dir, "package.json"), &b); err != nil || b.Name != "x" {
		t.Errorf("readJSON = %+v, %v", b, err)
	}
	if err := readYAML(filepath.Join(dir, "pnpm.yaml"), &c); err != nil || c.Name != "x" {
		t.Errorf("readYAML = %+v, %v", c, err)
	}
	if err := readJSON(filepath.Join(dir, "bad.json"), &b); err == nil || !strings.Contains(err.Error(), "bad.json") {
		t.Errorf("readJSON(bad) = %v, want an error naming the file", err)
	}
}

func TestTraits(t *testing.T) {
	for _, e := range All() {
		tr := e.Traits()
		if tr.OSV != "" && tr.Page == nil {
			t.Errorf("%s: OSV knows it, and there is no page to read about a package", e.Name())
		}
		if shaped := e.Name() == "docker" || e.Name() == "actions"; tr.KeepShape != shaped {
			t.Errorf("%s: KeepShape = %v", e.Name(), tr.KeepShape)
		}
	}
	if p := ByName("maven").Traits().Page("org.slf4j:slf4j-api"); p != "https://central.sonatype.com/artifact/org.slf4j/slf4j-api" {
		t.Errorf("maven page = %s", p)
	}
	if p := ByName("docker").Traits().Page("library/node"); p != "https://hub.docker.com/_/node" {
		t.Errorf("docker page = %s", p)
	}
}
