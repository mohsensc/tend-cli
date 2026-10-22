package eco

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

func TestToolchainDeps(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		".nvmrc":                       "v16.20.2\n",
		"web/.node-version":            "lts/*\n",
		".python-version":              "# managed by pyenv\n3.8.18\n",
		".tool-versions":               "nodejs 18.19.0\ngolang 1.21.5 # ci\nerlang 26.0\nterraform latest\n",
		"rust-toolchain.toml":          "[toolchain]\nchannel = \"1.70.0\"\ncomponents = [\"rustfmt\"]\n",
		"go.mod":                       "module x\n\ngo 1.21\n\ntoolchain go1.21.5\n",
		"svc/go.mod":                   "module y\n\ngo 1.22\n",
		"testdata/fixture/.nvmrc":      "12\n",
		"node_modules/pkg/.nvmrc":      "10\n",
		"docs/.ruby-version.example":   "2.7\n",
		".terraform-version":           "1.5.7\n",
		"tools/rust-toolchain":         "stable\n",
		".github/workflows/.nvmrc.bak": "8\n",
		"web/package.json":             "{\n  \"name\": \"web\",\n  \"engines\": {\n    \"npm\": \">=8\",\n    \"node\": \">=16.14.0\"\n  }\n}\n",
		"api/package.json":             "{\"name\": \"api\", \"engines\": {\"node\": \"lts/*\"}}\n",
		"lib/pyproject.toml":           "[project]\nname = \"lib\"\nrequires-python = \">=3.8,<4\"\n\n[tool.ruff]\ntarget-version = \"py38\"\n",
		"cli/pyproject.toml":           "[tool.poetry.dependencies]\npython = \"^3.9\"\nrequests = \"^2.31\"\n",
		"old/setup.cfg":                "[metadata]\nname = old\n\n[options]\npython_requires = >=3.7\n",
		"jvm/pom.xml":                  "<project>\n  <properties>\n    <java.version>16</java.version>\n    <maven.compiler.release>${java.version}</maven.compiler.release>\n  </properties>\n</project>\n",
	})
	e := toolchain{}
	var got []string
	for _, m := range manifestsIn(t, e, root) {
		ds, err := e.Deps(root, m)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range ds {
			min := ""
			if d.Min {
				min = " min"
			}
			got = append(got, fmt.Sprintf("%s:%d %s %s (%s)%s", d.Manifest.Path, d.Line, d.Name, d.Version, d.Req, min))
		}
	}
	want := []string{
		".nvmrc:1 nodejs 16.20.2 (v16.20.2)",
		".python-version:2 python 3.8.18 (3.8.18)",
		".terraform-version:1 terraform 1.5.7 (1.5.7)",
		".tool-versions:1 nodejs 18.19.0 (18.19.0)",
		".tool-versions:2 go 1.21.5 (1.21.5)",
		"cli/pyproject.toml:2 python 3.9 (^3.9) min",
		"go.mod:3 go 1.21 (1.21) min",
		"go.mod:5 go 1.21.5 (go1.21.5)",
		"jvm/pom.xml:3 java 16 (16) min",
		"lib/pyproject.toml:3 python 3.8 (>=3.8,<4) min",
		"old/setup.cfg:5 python 3.7 (>=3.7) min",
		"rust-toolchain.toml:2 rust 1.70.0 (1.70.0)",
		"svc/go.mod:3 go 1.22 (1.22) min",
		"web/package.json:5 nodejs 16.14.0 (>=16.14.0) min",
	}
	if !slices.Equal(got, want) {
		t.Errorf("deps =\n%q\nwant\n%q", got, want)
	}
}

func TestToolchainBump(t *testing.T) {
	tests := []struct {
		file, body, to, want string
	}{
		{".nvmrc", "v16.20.2\n", "24.11.1", "v24.11.1\n"},
		{".nvmrc", "16\n", "24", "24\n"},
		{"go.mod", "module x\n\ntoolchain go1.21.5\n", "1.25.3", "module x\n\ntoolchain go1.25.3\n"},
		{".tool-versions", "golang 1.21.5\nnodejs 16.20.2\n", "24.11.1", "golang 1.21.5\nnodejs 24.11.1\n"},
	}
	for _, tt := range tests {
		root := t.TempDir()
		write(t, root, map[string]string{tt.file: tt.body})
		var d core.Dep
		for _, m := range manifestsIn(t, toolchain{}, root) {
			ds, _ := toolchain{}.Deps(root, m)
			d = ds[len(ds)-1]
		}
		b, err := toolchain{}.Bump(context.Background(), root, d, tt.to, "")
		if err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(filepath.Join(root, tt.file))
		if string(got) != tt.want || !slices.Equal(b.Files, []string{tt.file}) {
			t.Errorf("%s: got %q, files %q", tt.file, got, b.Files)
		}
	}
	min := []struct {
		files      map[string]string
		file, to   string
		want, lock string
	}{
		{map[string]string{"go.mod": "module x\n\ngo 1.24\n\ntoolchain go1.24.5\n"}, "go.mod", "1.26", "module x\n\ngo 1.26\n\ntoolchain go1.24.5\n", ""},
		{map[string]string{"pyproject.toml": "[project]\nrequires-python = \">=3.8,<4\"\n"}, "pyproject.toml", "3.10", "[project]\nrequires-python = \">=3.10,<4\"\n", ""},
		{map[string]string{"pom.xml": "<properties>\n  <java.version>16</java.version>\n</properties>\n"}, "pom.xml", "17", "<properties>\n  <java.version>17</java.version>\n</properties>\n", ""},
		{
			map[string]string{
				"package.json":      "{\n  \"engines\": {\"node\": \"^18.18.0 || ^20.9.0 || >=21.1.0\"}\n}\n",
				"package-lock.json": "{\n  \"packages\": {\n    \"\": {\n      \"engines\": {\n        \"node\": \"^18.18.0 || ^20.9.0 || >=21.1.0\"\n      }\n    },\n    \"node_modules/x\": {\n      \"engines\": {\n        \"node\": \"^18.18.0 || ^20.9.0 || >=21.1.0\"\n      }\n    }\n  }\n}\n",
			},
			"package.json", "22.0.0", "{\n  \"engines\": {\"node\": \">=22.0.0\"}\n}\n",
			"{\n  \"packages\": {\n    \"\": {\n      \"engines\": {\n        \"node\": \">=22.0.0\"\n      }\n    },\n    \"node_modules/x\": {\n      \"engines\": {\n        \"node\": \"^18.18.0 || ^20.9.0 || >=21.1.0\"\n      }\n    }\n  }\n}\n",
		},
	}
	for _, tt := range min {
		root := t.TempDir()
		write(t, root, tt.files)
		var d core.Dep
		for _, m := range manifestsIn(t, toolchain{}, root) {
			ds, _ := toolchain{}.Deps(root, m)
			d = ds[0]
		}
		if !d.Min {
			t.Fatalf("%s: %+v is not a minimum", tt.file, d)
		}
		b, err := toolchain{}.Bump(context.Background(), root, d, tt.to, "")
		if err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(filepath.Join(root, tt.file))
		if string(got) != tt.want {
			t.Errorf("%s: got %q", tt.file, got)
		}
		if tt.lock == "" {
			continue
		}
		lock, _ := os.ReadFile(filepath.Join(root, "package-lock.json"))
		if string(lock) != tt.lock || !b.Locked || !slices.Equal(b.Files, []string{"package.json", "package-lock.json"}) {
			t.Errorf("lock: %q, bump %+v", lock, b)
		}
	}
	if _, err := (toolchain{}).Meta(context.Background(), nil, "nodejs"); !errors.Is(err, core.ErrNotSupported) {
		t.Errorf("Meta = %v; versions come from end-of-life data", err)
	}
}

func TestLowestAndRaise(t *testing.T) {
	tests := []struct {
		req, low, to, want string
	}{
		{">=16", "16", "22", ">=22"},
		{">= 16.14.0", "16.14.0", "22.0.0", ">= 22.0.0"},
		{"16.x", "16", "22", "22.x"},
		{"^3.8", "3.8", "3.10", "^3.10"},
		{">=3.8,<4", "3.8", "3.10", ">=3.10,<4"},
		{"~=3.7", "3.7", "3.10", "~=3.10"},
		{"<4,>=3.8", "3.8", "3.10", "<4,>=3.10"},
		{"^18 || ^20", "18", "22", ">=22"},
		{"16 - 20", "16", "22", ">=22"},
		{"1.24", "1.24", "1.26", "1.26"},
		{">=3.8,<3.10", "3.8", "3.10", ""},
		{">=3.8,<=3.9", "3.8", "3.10", ""},
		{"lts/*", "", "", ""},
	}
	for _, tt := range tests {
		if got := lowest(tt.req); got != tt.low {
			t.Errorf("lowest(%q) = %q, want %q", tt.req, got, tt.low)
		}
		if tt.low == "" {
			continue
		}
		got, err := raise(tt.req, tt.low, tt.to)
		if tt.want == "" {
			if err == nil {
				t.Errorf("raise(%q, %s) = %q; the cap is below the target", tt.req, tt.to, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("raise(%q, %s) = %q, %v; want %q", tt.req, tt.to, got, err, tt.want)
		}
	}
}
