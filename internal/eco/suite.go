package eco

import (
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/scan"
)

var (
	_ core.Baseliner = npm{}
	_ core.Baseliner = python{}
	_ core.Baseliner = gomod{}
)

// smokeMax is how many modules a smoke test loads at most.
const smokeMax = 50

// unloaded hold code no smoke test should load: dependencies, build output
// and what a build bundles from.
var unloaded = []string{"node_modules", "vendor", "dist", "build", "out", "coverage", "public", "static", "assets", "docs", "examples", "fixtures", "migrations", ".venv", "venv", "site-packages", "__pycache__", "scripts", "bin"}

// modules lists the code files under dir with one of exts that a smoke test
// may load: not tests, not under unloaded, not minified, not a tool's config
// file, and with a plain name. Paths are relative to dir.
func modules(root, dir string, exts []string) []string {
	files, err := Files(root)
	if err != nil {
		return nil
	}
	var res []string
	for _, f := range files {
		rel := f
		if dir != "." {
			var ok bool
			if rel, ok = strings.CutPrefix(f, dir+"/"); !ok {
				continue
			}
		}
		base := path.Base(rel)
		switch {
		case !slices.Contains(exts, path.Ext(rel)), scan.IsTest(rel), strings.ContainsAny(rel, " '\"$`\\"),
			strings.HasPrefix(base, "."), strings.Contains(base, ".min."), strings.Contains(base, ".config."),
			strings.HasPrefix(base, "Gruntfile"), strings.HasPrefix(base, "gulpfile"), base == "setup.py", base == "conftest.py", base == "manage.py":
			continue
		}
		if slices.ContainsFunc(strings.Split(path.Dir(rel), "/"), func(d string) bool { return slices.Contains(unloaded, d) }) {
			continue
		}
		res = append(res, rel)
		if len(res) == smokeMax {
			break
		}
	}
	return res
}

// Suite puts generated tests under test/tend next to the manifest, written
// for node --test, which every maintained Node runs without a dependency.
// A TypeScript project with no build script gets a typecheck too.
func (npm) Suite(root string, m core.Manifest) *core.Suite {
	dir := path.Dir(m.Path)
	su := &core.Suite{
		Lang:    "JavaScript in CommonJS, run by `node --test`, with node:test and node:assert",
		Smoke:   path.Join(dir, "test/tend/smoke.js"),
		Tests:   path.Join(dir, "test/tend/characterization.js"),
		Modules: modules(root, dir, []string{".js", ".cjs", ".mjs"}),
		Load: func(mod string) string {
			return inDir(dir, "node -e "+shArg(`import(require('node:url').pathToFileURL(require('node:path').resolve(`+strconv.Quote(mod)+`)).href).then(() => process.exit(0), (e) => { process.stderr.write(String(e && e.stack || e)); process.exit(1) })`))
		},
		Code: smokeJS,
		Run: func(files []string) string {
			return inDir(dir, "node --test "+shArgs(under(dir, files)))
		},
	}
	info := npmReadInfo(root, m.Path)
	if s, _ := info.Scripts["build"].(string); s == "" && onDisk(path.Join(root, dir), "tsconfig.json") {
		su.Checks = []string{inDir(dir, "npx --no-install tsc --noEmit")}
	}
	return su
}

func smokeJS(mods []string) string {
	var list strings.Builder
	for _, m := range mods {
		list.WriteString("  '" + m + "',\n")
	}
	return `'use strict'

const test = require('node:test')
const assert = require('node:assert')
const { spawnSync } = require('node:child_process')
const path = require('node:path')
const { pathToFileURL } = require('node:url')

const root = path.join(__dirname, '..', '..')

const modules = [
` + list.String() + `]

for (const m of modules) {
  test('loads ' + m, () => {
    const url = pathToFileURL(path.join(root, m)).href
    const code = 'import(' + JSON.stringify(url) + ').then(() => process.exit(0), (e) => { process.stderr.write(String(e && e.stack || e)); process.exit(1) })'
    const res = spawnSync(process.execPath, ['-e', code], { cwd: root, encoding: 'utf8', timeout: 30000 })
    assert.strictEqual(res.status, 0, res.stderr)
  })
}
`
}

// Suite puts generated tests under tests/tend next to the manifest, as
// unittest cases, which pytest collects as well.
func (python) Suite(root string, m core.Manifest) *core.Suite {
	dir, py := path.Dir(m.Path), pyRun(m)
	return &core.Suite{
		Lang:    "Python, as unittest test cases",
		Smoke:   path.Join(dir, "tests/tend/test_smoke.py"),
		Tests:   path.Join(dir, "tests/tend/test_characterization.py"),
		Modules: pyDotted(modules(root, dir, []string{".py"})),
		Load: func(mod string) string {
			return inDir(dir, py+" -c "+shArg("import "+mod))
		},
		Code: smokePy,
		Run: func(files []string) string {
			return inDir(dir, py+" -m unittest discover -s tests/tend")
		},
	}
}

// pyDotted names the modules files are, by their dotted paths: a package's
// __init__.py is the package.
func pyDotted(files []string) []string {
	var res []string
	for _, f := range files {
		m := strings.TrimSuffix(strings.TrimSuffix(f, ".py"), "/__init__")
		if strings.ContainsAny(m, ".-") {
			continue
		}
		res = append(res, strings.ReplaceAll(m, "/", "."))
	}
	return res
}

func smokePy(mods []string) string {
	var list strings.Builder
	for _, m := range mods {
		list.WriteString("    " + strconv.Quote(m) + ",\n")
	}
	return `import os
import subprocess
import sys
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

MODULES = [
` + list.String() + `]


class Smoke(unittest.TestCase):
    def test_modules_load(self):
        for mod in MODULES:
            with self.subTest(mod=mod):
                res = subprocess.run([sys.executable, "-c", "import " + mod], cwd=ROOT, capture_output=True, text=True, timeout=60)
                self.assertEqual(res.returncode, 0, res.stderr)
`
}

// Suite puts characterization tests in the module's root package; go test
// runs them and the build already loads every package, so there is neither
// a smoke test nor a line to add.
func (gomod) Suite(root string, m core.Manifest) *core.Suite {
	dir := path.Dir(m.Path)
	return &core.Suite{
		Lang:  "Go, as tests in the package of the files they cover, named tend_characterization_test.go in that package's directory",
		Tests: path.Join(dir, "tend_characterization_test.go"),
		Run:   func([]string) string { return "" },
	}
}

// under makes repo-relative paths relative to dir.
func under(dir string, files []string) []string {
	var res []string
	for _, f := range files {
		if dir != "." {
			f = strings.TrimPrefix(f, dir+"/")
		}
		res = append(res, f)
	}
	return res
}
