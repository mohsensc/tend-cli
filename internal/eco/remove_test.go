package eco

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

func TestNpmDrop(t *testing.T) {
	tests := []struct {
		name, in string
		drop     []string
		want     string
	}{
		{"middle", `{
  "dependencies": {
    "a": "^1.0.0",
    "b": "^1.0.0",
    "c": "^1.0.0"
  }
}`, []string{"b"}, `{
  "dependencies": {
    "a": "^1.0.0",
    "c": "^1.0.0"
  }
}`},
		{"last", `{
  "dependencies": {
    "a": "^1.0.0",
    "b": "^1.0.0"
  },
  "scripts": {"b": "x"}
}`, []string{"b"}, `{
  "dependencies": {
    "a": "^1.0.0"
  },
  "scripts": {"b": "x"}
}`},
		{"the only one", `{
  "devDependencies": {
    "a": "^1.0.0"
  }
}`, []string{"a"}, `{
  "devDependencies": {}
}`},
		{"on one line", `{"dependencies": {"a": "1", "b": "2", "c": "3"}}`, []string{"a", "c"}, `{"dependencies": {"b": "2"}}`},
		{"both blocks, and an alias", `{
  "dependencies": {
    "a": "^1.0.0",
    "old": "npm:b@^2.0.0"
  },
  "devDependencies": {
    "a": "^1.0.0",
    "x": "^1.0.0"
  }
}`, []string{"a", "b"}, `{
  "dependencies": {},
  "devDependencies": {
    "x": "^1.0.0"
  }
}`},
		{"peers stay", `{"peerDependencies": {"a": "^1"}, "devDependencies": {"a": "^1"}}`, []string{"a"}, `{"peerDependencies": {"a": "^1"}, "devDependencies": {}}`},
		{"CRLF", "{\r\n  \"dependencies\": {\r\n    \"a\": \"1\",\r\n    \"b\": \"2\"\r\n  }\r\n}\r\n", []string{"a"}, "{\r\n  \"dependencies\": {\r\n    \"b\": \"2\"\r\n  }\r\n}\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := npmDrop([]byte(tt.in), tt.drop)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

// Remove edits package.json itself and has the package manager lock again,
// the same command whatever was removed.
func TestNpmRemove(t *testing.T) {
	ctx := context.Background()
	t.Run("locks again", func(t *testing.T) {
		calls := fakeTool(t, "npm")
		root := t.TempDir()
		write(t, root, npmRepo([]string{"left-pad", "kept"}, []string{"is-odd"}, nil, nil, nil))
		deps := depsIn(t, npm{}, root, "package.json")
		var gone []core.Dep
		for _, d := range deps {
			if d.Name == "left-pad" || d.Name == "is-odd" {
				gone = append(gone, d)
			}
		}
		res, err := (npm{}).Remove(ctx, root, gone)
		if err != nil || !res.Locked || !slices.Equal(res.Files, []string{"package.json"}) {
			t.Fatalf("remove = %+v, %v", res, err)
		}
		if got := calls(root); !slices.Equal(got, []string{". install --package-lock-only --ignore-scripts --no-audit --no-fund"}) {
			t.Errorf("npm ran %q", got)
		}
		left := depsIn(t, npm{}, root, "package.json")
		if slices.ContainsFunc(left, func(d core.Dep) bool { return d.Direct && d.Name != "kept" }) {
			t.Errorf("left %+v", left)
		}
	})
	t.Run("no tool", func(t *testing.T) {
		old := have
		have = func(string) bool { return false }
		t.Cleanup(func() { have = old })
		root := t.TempDir()
		write(t, root, npmRepo([]string{"left-pad"}, nil, nil, nil, nil))
		before, _ := os.ReadFile(filepath.Join(root, "package.json"))
		res, err := (npm{}).Remove(ctx, root, depsIn(t, npm{}, root, "package.json")[:1])
		if err == nil || !strings.Contains(err.Error(), "npm not found to update package-lock.json") {
			t.Errorf("remove = %+v, %v", res, err)
		}
		if after, _ := os.ReadFile(filepath.Join(root, "package.json")); string(after) != string(before) {
			t.Errorf("package.json changed with no tool to lock again:\n%s", after)
		}
	})
}

// The go command drops the requirement and tidies; nothing is fetched for a
// module nothing imports.
func TestGoRemove(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not found in PATH")
	}
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	root := t.TempDir()
	write(t, root, map[string]string{
		"go.mod":  "module example.com/app\n\ngo 1.22\n\nrequire example.com/gone v1.2.0\n",
		"main.go": "package main\n\nfunc main() {}\n",
	})
	deps := depsIn(t, gomod{}, root, "go.mod")
	res, err := (gomod{}).Remove(context.Background(), root, deps)
	if err != nil || !res.Locked || !slices.Equal(res.Files, []string{"go.mod"}) {
		t.Fatalf("remove = %+v, %v", res, err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "go.mod"))
	if strings.Contains(string(b), "example.com/gone") {
		t.Errorf("go.mod still requires it:\n%s", b)
	}
}

// A module other modules require comes back from go mod tidy at the version
// they ask for. The version the removed line held stays, marked indirect,
// rather than going down.
func TestGoRemoveKeepsFloor(t *testing.T) {
	if !have("go") {
		t.Skip("go not found")
	}
	goOffline(t, goFileProxy(t,
		goModule{"example.com/dep", "v1.0.0", map[string]string{"dep.go": "package dep\n"}},
		goModule{"example.com/dep", "v1.2.0", map[string]string{"dep.go": "package dep\n"}},
		goModule{"example.com/lib", "v1.0.0", map[string]string{
			"go.mod": "module example.com/lib\n\ngo 1.21\n\nrequire example.com/dep v1.0.0\n",
			"lib.go": "package lib\n\nimport _ \"example.com/dep\"\n",
		}},
	))
	root := t.TempDir()
	write(t, root, map[string]string{
		"go.mod":  "module example.com/app\n\ngo 1.21\n\nrequire (\n\texample.com/lib v1.0.0\n\texample.com/dep v1.2.0\n)\n",
		"main.go": "package main\n\nimport _ \"example.com/lib\"\n\nfunc main() {}\n",
	})
	if out, err := run(context.Background(), root, goEnv(), "go mod download all"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	res, err := (gomod{}).Remove(context.Background(), root, []core.Dep{depIn(t, gomod{}, root, "go.mod", "example.com/dep", true)})
	if err != nil {
		t.Fatalf("remove = %+v, %v", res, err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "go.mod"))
	if !strings.Contains(string(b), "example.com/dep v1.2.0 // indirect") {
		t.Errorf("go.mod lost the version it held:\n%s", b)
	}
}

// Code the scan did not read, a generated file too large for it, imports the
// module. go mod tidy puts it back at its latest version, which would be a
// bump under a removal's name, so the removal fails and go.mod is as it was.
func TestGoRemoveImported(t *testing.T) {
	if !have("go") {
		t.Skip("go not found")
	}
	tool := func(v string) goModule {
		return goModule{"example.com/tool", v, map[string]string{"tool.go": "package tool\n\nfunc V() string { return \"" + v + "\" }\n"}}
	}
	goOffline(t, goFileProxy(t, tool("v1.0.0"), tool("v1.1.0")))
	root := t.TempDir()
	mod := "module example.com/app\n\ngo 1.21\n\nrequire example.com/tool v1.0.0\n"
	write(t, root, map[string]string{
		"go.mod":      mod,
		"gen/blob.go": "// Code generated. DO NOT EDIT.\n\npackage gen\n\nimport \"example.com/tool\"\n\nvar V = tool.V()\n",
	})
	if out, err := run(context.Background(), root, goEnv(), "go mod download all && go mod tidy"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	_, err := (gomod{}).Remove(context.Background(), root, []core.Dep{depIn(t, gomod{}, root, "go.mod", "example.com/tool", true)})
	if err == nil || !strings.Contains(err.Error(), "imports it") {
		t.Errorf("err = %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "go.mod")); string(b) != mod {
		t.Errorf("go.mod changed:\n%s", b)
	}
}

func TestGoRemoveNoTool(t *testing.T) {
	old := have
	have = func(string) bool { return false }
	t.Cleanup(func() { have = old })
	_, err := (gomod{}).Remove(context.Background(), t.TempDir(), []core.Dep{{Ecosystem: core.Go, Name: "x", Manifest: core.Manifest{Path: "go.mod"}}})
	var tm core.ToolMissing
	if !errors.As(err, &tm) || tm.Tool != "go" {
		t.Errorf("err = %v", err)
	}
}

func TestPyDrop(t *testing.T) {
	tests := []struct {
		name, file, in string
		drop           []string
		want           string
	}{
		{"requirements", "requirements.txt", "flask==3.0\nPyYAML==6.0  # config\nrequests>=2\n", []string{"pyyaml"}, "flask==3.0\nrequests>=2\n"},
		{"hashed, over several lines", "requirements.txt", "a==1 \\\n    --hash=sha256:aa \\\n    --hash=sha256:bb\nb==2\n", []string{"a"}, "b==2\n"},
		{"a pyproject array", "pyproject.toml", "[project]\nname = \"x\"\ndependencies = [\n  \"flask>=3\",\n  \"pyyaml\",\n  \"requests\"\n]\n", []string{"pyyaml", "requests"}, "[project]\nname = \"x\"\ndependencies = [\n  \"flask>=3\",\n]\n"},
		{"an array on one line", "pyproject.toml", "[project]\nname = \"x\"\ndependencies = [\"a\", \"b\", \"c\"]\n", []string{"a", "c"}, "[project]\nname = \"x\"\ndependencies = [\"b\"]\n"},
		{"a Poetry table", "pyproject.toml", "[tool.poetry]\nname = \"x\"\n\n[tool.poetry.dependencies]\npython = \"^3.12\"\npyyaml = \"^6.0\"\nflask = {version = \"^3\"}\n", []string{"pyyaml", "flask"}, "[tool.poetry]\nname = \"x\"\n\n[tool.poetry.dependencies]\npython = \"^3.12\"\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, map[string]string{tt.file: tt.in})
			m := core.Manifest{Ecosystem: core.Pip, Path: tt.file}
			entries, err := pyEntries(root, m)
			if err != nil {
				t.Fatal(err)
			}
			var deps []core.Dep
			for _, n := range tt.drop {
				deps = append(deps, core.Dep{Ecosystem: core.Pip, Name: n})
			}
			got, err := pyDrop(tt.in, entries, deps, strings.HasSuffix(tt.file, ".toml"))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
	if _, err := pyDrop("flask\n", pyReqEntries([]byte("flask\n")), []core.Dep{{Name: "django"}}, false); err == nil {
		t.Error("dropping a requirement that is not there worked")
	}
}

// A compiled requirements file is compiled again from its input, which no
// longer asks for what was removed.
func TestPythonRemoveRelocks(t *testing.T) {
	calls := fakeTool(t, "pip-compile")
	root := t.TempDir()
	write(t, root, map[string]string{
		"requirements.in":  "flask\npyyaml\n",
		"requirements.txt": "# This file is autogenerated by pip-compile with Python 3.12\nflask==3.0.0\npyyaml==6.0.1\n",
	})
	ms, err := (python{}).Detect(root)
	if err != nil || len(ms) != 1 || ms[0].Lock != "requirements.txt" {
		t.Fatalf("detect = %+v, %v", ms, err)
	}
	var gone []core.Dep
	for _, d := range depsIn(t, python{}, root, "requirements.in") {
		if d.Name == "pyyaml" && d.Direct {
			gone = append(gone, d)
		}
	}
	res, err := (python{}).Remove(context.Background(), root, gone)
	if err != nil || !res.Locked {
		t.Fatalf("remove = %+v, %v", res, err)
	}
	if got := calls(root); !slices.Equal(got, []string{". --quiet requirements.in -o requirements.txt"}) {
		t.Errorf("pip-compile ran %q", got)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "requirements.in")); string(b) != "flask\n" {
		t.Errorf("requirements.in = %q", b)
	}
}
