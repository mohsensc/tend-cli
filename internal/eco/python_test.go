package eco

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/fetch"
)

func TestPythonDetect(t *testing.T) {
	root := copyTestdata(t, "python/projects")
	got := manifestsIn(t, python{}, root)
	want := []core.Manifest{
		{Ecosystem: "pip", Path: "pipfile/Pipfile", Lock: "pipfile/Pipfile.lock", Tool: "pipenv"},
		{Ecosystem: "pip", Path: "poetry/pyproject.toml", Lock: "poetry/poetry.lock", Tool: "poetry"},
		{Ecosystem: "pip", Path: "reqs/constraints.txt", Tool: "pip"},
		{Ecosystem: "pip", Path: "reqs/dev-requirements.in", Lock: "reqs/dev-requirements.txt", Tool: "pip"},
		{Ecosystem: "pip", Path: "reqs/requirements-dev.txt", Tool: "pip"},
		{Ecosystem: "pip", Path: "reqs/requirements.txt", Tool: "pip"},
		{Ecosystem: "pip", Path: "reqs/requirements/base.txt", Tool: "pip"},
		{Ecosystem: "pip", Path: "uv/pyproject.toml", Lock: "uv/uv.lock", Tool: "uv"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Detect = %+v\nwant %+v", got, want)
	}
}

func TestPythonDeps(t *testing.T) {
	root := copyTestdata(t, "python/projects")
	for _, tt := range []struct{ name, manifest string }{
		{"requirements", "reqs/requirements.txt"},
		{"requirements-base", "reqs/requirements/base.txt"},
		{"pip-compile", "reqs/dev-requirements.in"},
		{"pep621-uv", "uv/pyproject.toml"},
		{"poetry", "poetry/pyproject.toml"},
		{"pipfile", "pipfile/Pipfile"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			goldenCheck(t, "python/deps/"+tt.name+".golden.json", jsonOf(t, depsIn(t, python{}, root, tt.manifest)))
		})
	}
}

func TestPyParseReq(t *testing.T) {
	tests := []struct {
		in                 string
		name, extras, spec string
		ok                 bool
	}{
		{"requests", "requests", "", "", true},
		{"requests>=2.31,<3", "requests", "", ">=2.31,<3", true},
		{`requests[security,socks] >= 2.31, < 3 ; python_version >= "3.8"`, "requests", "[security,socks]", ">= 2.31, < 3", true},
		{"Pillow (>=10.0,<11)", "Pillow", "", ">=10.0,<11", true},
		{"zope.interface==6.4", "zope.interface", "", "==6.4", true},
		{"pkg @ https://example.com/pkg.tar.gz", "", "", "", false},
		{"pkg[extra] @ git+https://example.com/pkg.git", "", "", "", false},
		{"pkg latest", "", "", "", false},
		{"[no-name]", "", "", "", false},
	}
	for _, tt := range tests {
		e, ok := pyParseReq(tt.in)
		if ok != tt.ok {
			t.Errorf("pyParseReq(%q) ok = %v", tt.in, ok)
			continue
		}
		if !ok {
			continue
		}
		extras := strings.TrimSpace(tt.in[len(e.name):e.nameEnd])
		if e.name != tt.name || extras != tt.extras || e.spec != tt.spec || tt.in[e.specAt:e.specAt+len(e.spec)] != tt.spec {
			t.Errorf("pyParseReq(%q) = name %q extras %q spec %q at %d", tt.in, e.name, extras, e.spec, e.specAt)
		}
	}
}

func TestPyNorm(t *testing.T) {
	for in, want := range map[string]string{
		"Django":            "django",
		"python_dateutil":   "python-dateutil",
		"zope.interface":    "zope-interface",
		"Foo__Bar-.baz":     "foo-bar-baz",
		" ruamel.yaml.clib": "ruamel-yaml-clib",
	} {
		if got := PyNorm(in); got != want {
			t.Errorf("PyNorm(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPythonMeta(t *testing.T) {
	f := fetch.Replay(recordedDir("python"))
	for _, name := range []string{"six", "attrs", "oauth2client"} {
		t.Run(name, func(t *testing.T) {
			meta, err := python{}.Meta(context.Background(), f, name)
			if err != nil {
				t.Fatal(err)
			}
			goldenCheck(t, "python/meta/"+name+".golden.json", jsonOf(t, meta))
		})
	}
	t.Run("not found", func(t *testing.T) {
		if _, err := (python{}).Meta(context.Background(), f, "tend_fixture.nope"); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestPythonMetaRecord(t *testing.T) {
	wantRecord(t)
	f := liveClient(t, "python")
	for _, name := range []string{"six", "attrs", "oauth2client", "tend-fixture-nope"} {
		if _, err := (python{}).Meta(context.Background(), f, name); err != nil {
			t.Log(err)
		}
	}
}

func TestPythonBump(t *testing.T) {
	hideTools(t)
	tests := []struct {
		name, manifest, dep, to, toName string
	}{
		{"exact", "reqs/requirements.txt", "django", "5.1.2", ""},
		{"range-extras-marker", "reqs/requirements.txt", "requests", "3.1.0", ""},
		{"compatible", "reqs/requirements.txt", "python-dateutil", "2.9.0", ""},
		{"inside-range", "reqs/requirements.txt", "requests", "2.40.0", ""},
		{"swap", "reqs/requirements.txt", "attrs", "24.1.0", "cattrs"},
		{"parenthesized", "reqs/requirements/base.txt", "pillow", "11.0.0", ""},
		{"pip-compile", "reqs/dev-requirements.in", "pytest", "9.0.0", ""},
		{"pep621", "uv/pyproject.toml", "httpx", "1.0.0", ""},
		{"pep621-single-quoted", "uv/pyproject.toml", "pyyaml", "7.0", ""},
		{"pep621-extra", "uv/pyproject.toml", "ruff", "0.7.0", ""},
		{"poetry-caret", "poetry/pyproject.toml", "requests", "3.0.0", ""},
		{"poetry-inline-table", "poetry/pyproject.toml", "flask", "3.0.3", ""},
		{"poetry-exact", "poetry/pyproject.toml", "sqlalchemy", "2.0.36", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := copyTestdata(t, "python/projects")
			d := depIn(t, python{}, root, tt.manifest, tt.dep, true)
			before := treeSnapshot(t, root)
			res, err := python{}.Bump(context.Background(), root, d, tt.to, tt.toName)
			if err != nil {
				t.Fatal(err)
			}
			out := fmt.Sprintf("files: %v\nlocked: %v\nnote: %s\n%s", res.Files, res.Locked, res.Note, treeDiff(before, treeSnapshot(t, root)))
			goldenCheck(t, "python/bump/"+tt.name+".golden", out)
		})
	}
}

func TestPythonBumpRefused(t *testing.T) {
	hideTools(t)
	ctx := context.Background()
	root := copyTestdata(t, "python/projects")
	tests := []struct {
		name, manifest, dep string
		direct              bool
		to, toName          string
		want                error
	}{
		{"hash pinned without a lock", "reqs/requirements.txt", "urllib3", true, "2.2.4", "", core.ErrNotSupported},
		{"pipfile is read only", "pipfile/Pipfile", "requests", true, "3.0.0", "", core.ErrNotSupported},
		{"poetry swap", "poetry/pyproject.toml", "requests", true, "1.0.0", "niquests", core.ErrNotSupported},
		{"lock only needs the tool", "uv/pyproject.toml", "pydantic", true, "2.9.2", "", core.ToolMissing{Tool: "uv"}},
		{"transitive needs the tool", "uv/pyproject.toml", "anyio", false, "4.7.0", "", core.ToolMissing{Tool: "uv"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := depIn(t, python{}, root, tt.manifest, tt.dep, tt.direct)
			before := treeSnapshot(t, root)
			if _, err := (python{}).Bump(ctx, root, d, tt.to, tt.toName); !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
			if !reflect.DeepEqual(before, treeSnapshot(t, root)) {
				t.Error("a refused bump changed files")
			}
		})
	}
	write(t, root, map[string]string{"reqs/requirements.txt": "requests>=2,!=3.0.0\n"})
	d := depIn(t, python{}, root, "reqs/requirements.txt", "requests", true)
	if _, err := (python{}).Bump(ctx, root, d, "3.0.0", ""); !errors.Is(err, core.ErrNotSupported) {
		t.Errorf("bump onto an excluded version: err = %v", err)
	}
}

func TestPythonBumpCRLF(t *testing.T) {
	hideTools(t)
	root := t.TempDir()
	src := "flask==2.3.3\r\nrequests>=2.31,<3 ; python_version >= \"3.8\"\r\n"
	write(t, root, map[string]string{"requirements.txt": src})
	d := depIn(t, python{}, root, "requirements.txt", "requests", true)
	if _, err := (python{}).Bump(context.Background(), root, d, "3.0.1", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "requirements.txt"))
	if want := strings.Replace(src, ">=2.31,<3", ">=3.0.1,<4", 1); string(got) != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestPythonBumpCommands(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name, tool, manifest, dep string
		direct                    bool
		to                        string
		want                      []string
		from                      string // the locked version the tool moves to to
	}{
		{"uv edit", "uv", "uv/pyproject.toml", "httpx", true, "1.0.0", []string{"./uv lock --upgrade-package httpx==1.0.0"}, ""},
		{"uv in range", "uv", "uv/pyproject.toml", "pydantic", true, "2.9.2", []string{"./uv lock --upgrade-package pydantic==2.9.2"}, ""},
		{"uv transitive", "uv", "uv/pyproject.toml", "anyio", false, "4.7.0", []string{"./uv lock --upgrade-package anyio==4.7.0"}, "4.6.2"},
		{"poetry edit", "poetry", "poetry/pyproject.toml", "requests", true, "3.0.0", []string{"./poetry --version", "./poetry lock --no-update", "./poetry lock --no-update"}, ""},
		{"poetry transitive", "poetry", "poetry/pyproject.toml", "certifi", false, "2024.9.1", []string{"./poetry update --lock certifi"}, "2024.8.30"},
		{"pip-compile edit", "pip-compile", "reqs/dev-requirements.in", "pytest", true, "9.0.0", []string{"./reqs --quiet --upgrade-package pytest==9.0.0 dev-requirements.in -o dev-requirements.txt"}, ""},
		{"pip-compile transitive", "pip-compile", "reqs/dev-requirements.in", "pluggy", false, "1.6.0", []string{"./reqs --quiet --upgrade-package pluggy==1.6.0 dev-requirements.in -o dev-requirements.txt"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logged := fakeTool(t, tt.tool)
			if tt.from != "" {
				logged = lockTool(t, tt.tool, tt.from, tt.to)
			}
			root := copyTestdata(t, "python/projects")
			d := depIn(t, python{}, root, tt.manifest, tt.dep, tt.direct)
			res, err := python{}.Bump(ctx, root, d, tt.to, "")
			if err != nil {
				t.Fatal(err)
			}
			if !res.Locked {
				t.Errorf("bump = %+v", res)
			}
			if got := logged(root); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ran %q, want %q", got, tt.want)
			}
		})
	}
}

// poetry update takes the newest release the ranges allow. A later release
// of the same major is taken and named; one past it fails the bump and
// leaves the files as they were.
func TestPoetryTargetedLockChecked(t *testing.T) {
	lockTool(t, "poetry", "2024.8.30", "2024.12.14")
	root := copyTestdata(t, "python/projects")
	d := depIn(t, python{}, root, "poetry/pyproject.toml", "certifi", false)
	res, err := python{}.Bump(context.Background(), root, d, "2024.9.1", "")
	if err != nil || res.Landed != "2024.12.14" {
		t.Fatalf("bump = %+v, %v", res, err)
	}

	lockTool(t, "poetry", "2024.8.30", "2025.1.31")
	root = copyTestdata(t, "python/projects")
	d = depIn(t, python{}, root, "poetry/pyproject.toml", "certifi", false)
	before := treeSnapshot(t, root)
	_, err = python{}.Bump(context.Background(), root, d, "2024.9.1", "")
	if err == nil || !strings.Contains(err.Error(), "locked certifi at 2025.1.31, not 2024.9.1 or a later 2024.x") {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(before, treeSnapshot(t, root)) {
		t.Error("a refused bump changed files")
	}
}

func TestPyNewSpec(t *testing.T) {
	tests := []struct {
		spec, to string
		want     string
		err      bool
	}{
		{"==1.0", "2.0", "==2.0", false},
		{"==1.0", "2.0.0", "==2.0", false},
		{"==1.0.0", "2.1", "==2.1", false},
		{"===1.0", "2.0.0", "===2.0.0", false},
		{"===1.0", "2.0", "===2.0", false},
		{"== 1.0", "2.0", "== 2.0", false},
		{">=2.31,<3", "3.1.0", ">=3.1,<4", false},
		{">=2.31,<3", "3.1.2", ">=3.1.2,<4", false},
		{">=2.31, <3.0", "3.1.0", ">=3.1, <4.0", false},
		{">=2.31,<3", "2.40.0", ">=2.31,<3", false},
		{">=1.0", "9.0", ">=1.0", false},
		{">1.0,<2", "2.5", ">=2.5,<3", false},
		{">=1.2 <2.0", "2.1.0", ">=2.1 <3.0", false},
		{">= 1.2 < 2.0", "2.1.0", ">=2.1 <3.0", false},
		{"<=1.4", "1.5", "<=1.5", false},
		{"~=1.4", "2.0.0", "~=2.0", false},
		{"~=1.4", "2.0.1", "~=2.0.1", false},
		{"~=1.4.2", "1.5.0", "~=1.5.0", false},
		{"==1.4.*", "1.5.2", "==1.5.*", false},
		{"==1.*", "2.3.0", "==2.*", false},
		{"==1.0", "2.0rc1", "==2.0rc1", false},
		{"!=2.0,>=1", "2.0", "", true},
		{"^1.0 || ^2.0", "3.0", "", true},
		{"^2.31", "3.0.0", "^3.0", false},
		{"^2.31", "3.1.4", "^3.1.4", false},
		{"^2.31", "2.32.3", "^2.31", false},
		{"~2.3", "3.0.3", "~3.0.3", false},
		{"2.0.35", "2.0.36", "2.0.36", false},
		{"*", "9.9", "*", false},
		{"", "9.9", "", false},
		{"==1.0", "not a version", "", true},
	}
	for _, tt := range tests {
		got, err := pyNewSpec(tt.spec, tt.to)
		if (err != nil) != tt.err || got != tt.want {
			t.Errorf("pyNewSpec(%q, %q) = %q, %v; want %q", tt.spec, tt.to, got, err, tt.want)
		}
	}
}

func TestPySatisfies(t *testing.T) {
	tests := []struct {
		spec, v string
		want    bool
	}{
		{"", "1.0", true},
		{"*", "1.0", true},
		{"==1.0", "1.0.0", true},
		{"==1.0", "1.0.1", false},
		{"==1.0.*", "1.0.7", true},
		{"==1.0.*", "1.1.0", false},
		{"!=1.0.*", "1.1.0", true},
		{"!=1.5", "1.5", false},
		{"===1.0", "1.0", true},
		{"===1.0", "1.0.0", false},
		{">=1.2,<2", "1.9.9", true},
		{">=1.2,<2", "2.0", false},
		{">= 1.2, < 2", "1.5", true},
		{">1.2", "1.2", false},
		{"<=1.2", "1.2", true},
		{"~=1.4.2", "1.4.9", true},
		{"~=1.4.2", "1.5.0", false},
		{"~=1.4", "1.9", true},
		{"~=1.4", "2.0", false},
		{"<2.0", "2.0rc1", true},
		{">=2.0", "2.0rc1", false},
		{">=1.0", "1.0.post1", true},
		{"^1.2.3", "1.9.0", true},
		{"^1.2.3", "2.0.0", false},
		{"^0.2.3", "0.2.9", true},
		{"^0.2.3", "0.3.0", false},
		{"^0.0.3", "0.0.4", false},
		{"^0.0", "0.0.9", true},
		{"^0", "1.0", false},
		{"~1.2.3", "1.2.9", true},
		{"~1.2.3", "1.3.0", false},
		{"~1.2", "1.2.5", true},
		{"~1", "1.9", true},
		{"~1", "2.0", false},
		{"2.0.35", "2.0.35", true},
		{">=1.2 <2.0", "1.5", true},
		{">=1.2 <2.0", "2.0", false},
		{"^1.0 || ^3.0", "3.2", true},
		{"^1.0 || ^3.0", "2.2", false},
		{">=1.0", "garbage", false},
	}
	for _, tt := range tests {
		if got := pySatisfies(tt.spec, tt.v); got != tt.want {
			t.Errorf("pySatisfies(%q, %q) = %v, want %v", tt.spec, tt.v, got, tt.want)
		}
	}
}

func TestPythonImports(t *testing.T) {
	tests := []struct {
		name string
		want []string
	}{
		{"requests", []string{"requests"}},
		{"typing-extensions", []string{"typing_extensions"}},
		{"Pillow", []string{"PIL"}},
		{"beautifulsoup4", []string{"bs4"}},
		{"scikit-learn", []string{"sklearn"}},
		{"python-dateutil", []string{"dateutil"}},
		{"psycopg2-binary", []string{"psycopg2"}},
		{"opencv-python", []string{"cv2"}},
		{"google-cloud-storage", []string{"google.cloud.storage"}},
		{"protobuf", []string{"google.protobuf"}},
		{"PyJWT", []string{"jwt"}},
		{"python_dotenv", []string{"dotenv"}},
		{"msgpack-python", []string{"msgpack"}},
		{"pymongo", []string{"pymongo", "bson", "gridfs"}},
	}
	for _, tt := range tests {
		if got := (python{}).Imports(core.Dep{Name: tt.name}); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Imports(%s) = %v, want %v", tt.name, got, tt.want)
		}
	}

	pyTopLevel.Clear()
	t.Cleanup(pyTopLevel.Clear)
	root := copyTestdata(t, "python/projects")
	write(t, root, map[string]string{
		"uv/.venv/lib/python3.12/site-packages/PyYAML-6.0.2.dist-info/top_level.txt":         "_yaml\nyaml\n",
		"uv/.venv/lib/python3.12/site-packages/Markdown-3.7.dist-info/top_level.txt":         "markdown\n",
		"uv/.venv/lib/python3.12/site-packages/no_top_level-1.0.dist-info/METADATA":          "Name: no-top-level\n",
		"uv/.venv/lib/python3.12/site-packages/google_cloud_storage-2.18.2.dist-info/RECORD": "",
	})
	depsIn(t, python{}, root, "uv/pyproject.toml")
	if got := (python{}).Imports(core.Dep{Name: "pyyaml"}); !reflect.DeepEqual(got, []string{"_yaml", "yaml"}) {
		t.Errorf("Imports(pyyaml) with a venv = %v", got)
	}
	if got := (python{}).Imports(core.Dep{Name: "google-cloud-storage"}); !reflect.DeepEqual(got, []string{"google.cloud.storage"}) {
		t.Errorf("Imports(google-cloud-storage) = %v", got)
	}
}

func TestPythonCommands(t *testing.T) {
	root := copyTestdata(t, "python/projects")
	write(t, root, map[string]string{"reqs/requirements-dev.txt": "-r requirements.txt\npytest>=8.0\n"})
	tests := []struct {
		manifest string
		want     core.Commands
	}{
		{"reqs/requirements.txt", core.Commands{
			Install: []string{"cd reqs && python3 -m venv .venv && .venv/bin/pip install -r requirements.txt -r requirements-dev.txt"},
			Test:    []string{"cd reqs && .venv/bin/python -m unittest discover"},
		}},
		{"reqs/requirements-dev.txt", core.Commands{
			Install: []string{"cd reqs && python3 -m venv .venv && .venv/bin/pip install -r requirements-dev.txt"},
			Test:    []string{"cd reqs && .venv/bin/python -m pytest"},
		}},
		{"reqs/dev-requirements.in", core.Commands{
			Install: []string{"cd reqs && python3 -m venv .venv && .venv/bin/pip install -r dev-requirements.txt -r requirements-dev.txt"},
			Test:    []string{"cd reqs && .venv/bin/python -m pytest"},
		}},
		{"uv/pyproject.toml", core.Commands{
			Install: []string{"cd uv && uv sync --frozen"},
			Test:    []string{"cd uv && uv run python -m pytest"},
		}},
		{"poetry/pyproject.toml", core.Commands{
			Install: []string{"cd poetry && poetry install"},
			Test:    []string{"cd poetry && poetry run python -m pytest"},
		}},
		{"pipfile/Pipfile", core.Commands{
			Install: []string{"cd pipfile && pipenv sync --dev"},
			Test:    []string{"cd pipfile && pipenv run python -m pytest"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.manifest, func(t *testing.T) {
			for _, m := range manifestsIn(t, python{}, root) {
				if m.Path != tt.manifest {
					continue
				}
				if got := (python{}).Commands(root, m); !reflect.DeepEqual(got, tt.want) {
					t.Errorf("Commands = %+v\nwant %+v", got, tt.want)
				}
				return
			}
			t.Fatalf("no manifest %s", tt.manifest)
		})
	}

	plain := t.TempDir()
	write(t, plain, map[string]string{
		"pyproject.toml":       "[project]\nname = \"app\"\n\n[project.optional-dependencies]\ntest = [\"coverage\"]\ndev = [\"black\"]\n",
		"tests/test_orders.py": "import unittest\n",
	})
	m := manifestsIn(t, python{}, plain)[0]
	want := core.Commands{
		Install: []string{"python3 -m venv .venv && .venv/bin/pip install -e '.[dev,test]'"},
		Test:    []string{".venv/bin/python -m unittest discover -s tests -t ."},
	}
	if got := (python{}).Commands(plain, m); !reflect.DeepEqual(got, want) {
		t.Errorf("Commands = %+v\nwant %+v", got, want)
	}
}

// wheel writes a pure-Python wheel for name at version into dir.
func wheel(t *testing.T, dir, name, version string) {
	t.Helper()
	mod := strings.ReplaceAll(name, "-", "_")
	f, err := os.Create(filepath.Join(dir, mod+"-"+version+"-py3-none-any.whl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	info := mod + "-" + version + ".dist-info/"
	files := []struct{ name, body string }{
		{mod + "/__init__.py", "VERSION = \"" + version + "\"\n"},
		{info + "METADATA", "Metadata-Version: 2.1\nName: " + name + "\nVersion: " + version + "\n"},
		{info + "WHEEL", "Wheel-Version: 1.0\nGenerator: tend-test\nRoot-Is-Purelib: true\nTag: py3-none-any\n"},
		{info + "RECORD", ""},
	}
	for _, x := range files {
		w, err := zw.Create(x.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(x.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestPythonBumpReal locks against a flat directory of wheels, so uv never
// needs an index.
func TestPythonBumpReal(t *testing.T) {
	if !have("uv") {
		t.Skip("uv not found")
	}
	t.Setenv("UV_CACHE_DIR", t.TempDir())
	ctx := context.Background()
	root := t.TempDir()
	wheels := filepath.Join(root, "wheels")
	if err := os.MkdirAll(wheels, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"1.0.0", "1.1.0", "2.0.0"} {
		wheel(t, wheels, "fixture-dep", v)
	}
	write(t, root, map[string]string{
		"pyproject.toml": "[project]\nname = \"app\"\nversion = \"0.1.0\"\nrequires-python = \">=3.8\"\ndependencies = [\"fixture-dep>=1.0,<1.1\"]\n\n[tool.uv]\nno-index = true\nfind-links = [\"wheels\"]\n",
	})
	if out, err := run(ctx, root, nil, "uv lock"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	m := manifestsIn(t, python{}, root)[0]
	d := core.Dep{Ecosystem: "pip", Name: "fixture-dep", Manifest: m, Direct: true}
	res, err := python{}.Bump(ctx, root, d, "2.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Locked || !reflect.DeepEqual(res.Files, []string{"pyproject.toml", "uv.lock"}) {
		t.Errorf("bump = %+v", res)
	}
	got := treeSnapshot(t, root)
	if !strings.Contains(got["pyproject.toml"], `"fixture-dep>=2.0.0,<3"`) {
		t.Errorf("pyproject.toml:\n%s", got["pyproject.toml"])
	}
	if !strings.Contains(got["uv.lock"], "name = \"fixture-dep\"\nversion = \"2.0.0\"") {
		t.Errorf("uv.lock:\n%s", got["uv.lock"])
	}
}
