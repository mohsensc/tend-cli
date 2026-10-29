package eco

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/scan"
)

// unusedIn lays files out as a repo, reads its manifests and code the way an
// inventory does, and returns the names Unused removes and those it keeps,
// with why.
func unusedIn(t *testing.T, files map[string]string, keep ...string) ([]string, map[string]string) {
	t.Helper()
	root := t.TempDir()
	write(t, root, files)
	inv := &core.Inventory{Root: root}
	for _, e := range []core.Ecosystem{npm{}, gomod{}, python{}} {
		ms, err := e.Detect(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ms {
			deps, err := e.Deps(root, m)
			if err != nil {
				t.Fatal(err)
			}
			inv.Manifests = append(inv.Manifests, m)
			inv.Deps = append(inv.Deps, deps...)
		}
	}
	idx, err := scan.Build(scan.Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	inv.Index = idx
	gone, kept, err := Unused(inv, func(d core.Dep) bool { return slices.Contains(keep, d.Name) })
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range gone {
		names = append(names, d.Name)
	}
	why := map[string]string{}
	for _, a := range kept {
		why[a.Dep.Name] = a.Why
	}
	return names, why
}

// npmRepo is a package.json with deps in dependencies, devDeps in
// devDependencies, the scripts given, and a lockfile v3 that installs every
// one of them at 1.0.0 with the extra fields lock gives it, plus the files
// in more.
func npmRepo(deps, devDeps, scripts []string, lock map[string]string, more map[string]string) map[string]string {
	block := func(names []string) string {
		var parts []string
		for _, n := range names {
			parts = append(parts, fmt.Sprintf("    %q: \"^1.0.0\"", n))
		}
		return "{\n" + strings.Join(parts, ",\n") + "\n  }"
	}
	var s []string
	for _, l := range scripts {
		k, v, _ := strings.Cut(l, "=")
		s = append(s, fmt.Sprintf("    %q: %q", k, v))
	}
	pkg := "{\n  \"name\": \"app\",\n  \"scripts\": {\n" + strings.Join(s, ",\n") + "\n  },\n" +
		"  \"dependencies\": " + block(deps) + ",\n  \"devDependencies\": " + block(devDeps) + "\n}\n"
	installed := map[string]string{}
	for _, n := range append(slices.Clone(deps), devDeps...) {
		installed[n] = ""
	}
	maps.Copy(installed, lock)
	parts := []string{`"": {"name": "app"}`}
	for _, n := range slices.Sorted(maps.Keys(installed)) {
		f := `"version": "1.0.0"`
		if extra := installed[n]; extra != "" {
			f += ", " + extra
		}
		parts = append(parts, fmt.Sprintf("%q: {%s}", "node_modules/"+n, f))
	}
	files := map[string]string{
		"package.json":      pkg,
		"package-lock.json": `{"name": "app", "lockfileVersion": 3, "requires": true, "packages": {` + strings.Join(parts, ", ") + "}}\n",
	}
	maps.Copy(files, more)
	return files
}

type unusedCase struct {
	name  string
	files map[string]string
	keep  []string
	gone  []string
	kept  map[string]string // name -> a part of why
}

func checkUnused(t *testing.T, tests []unusedCase) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gone, kept := unusedIn(t, tt.files, tt.keep...)
			if !slices.Equal(gone, tt.gone) {
				t.Errorf("removes %q, want %q", gone, tt.gone)
			}
			if len(kept) != len(tt.kept) {
				t.Errorf("keeps %q, want %q", kept, tt.kept)
			}
			for n, part := range tt.kept {
				if !strings.Contains(kept[n], part) {
					t.Errorf("keeps %s because %q, want %q in it", n, kept[n], part)
				}
			}
		})
	}
}

const usesNothing = "module.exports = function main() { return 1 }\n"

func TestUnusedNpm(t *testing.T) {
	checkUnused(t, []unusedCase{
		{name: "declared and never imported",
			files: npmRepo([]string{"left-pad"}, []string{"is-odd"}, nil, nil, map[string]string{"index.js": usesNothing}),
			gone:  []string{"left-pad", "is-odd"}},
		{name: "imported, required and loaded with import()",
			files: npmRepo([]string{"a", "b", "c"}, nil, nil, nil, map[string]string{
				"index.js": "import a from 'a'\nconst b = require('b/sub')\nasync function f() { return await import('c') }\n",
			})},
		{name: "a module id in a string: require.resolve, jest.mock, a worker",
			files: npmRepo([]string{"a", "b"}, []string{"c"}, nil, nil, map[string]string{
				"index.js":       "const p = require.resolve('a/package.json')\nnew Worker(new URL(\"b\", import.meta.url))\n",
				"test/x.test.js": "jest.mock('c')\n",
			})},
		{name: "run by a package.json script",
			files: npmRepo(nil, []string{"rimraf", "concurrently"}, []string{"clean=rimraf dist", "dev=concurrently 'a' 'b'"}, nil, map[string]string{"index.js": usesNothing})},
		{name: "a command it installs, run by a script",
			files: npmRepo(nil, []string{"typescript-ish"}, []string{"build=tsc-ish -p ."}, map[string]string{"typescript-ish": `"bin": {"tsc-ish": "bin/tsc"}`}, map[string]string{"index.js": usesNothing})},
		{name: "a command run from node_modules/.bin, a Makefile and CI",
			files: npmRepo(nil, []string{"x-cli", "y-cli", "z-cli"}, nil, map[string]string{
				"x-cli": `"bin": {"xx": "x.js"}`, "y-cli": `"bin": "y.js"`, "z-cli": `"bin": {"zz": "z.js"}`,
			}, map[string]string{
				"index.js":                 usesNothing,
				"scripts/go.sh":            "./node_modules/.bin/xx --fast\n",
				"Makefile":                 "lint:\n\tnpx y-cli .\n",
				".github/workflows/ci.yml": "steps:\n  - run: zz check\n",
			})},
		{name: "named by a tool's config",
			files: npmRepo(nil, []string{"@tsconfig/node20", "jest-fetch", "autoprefixer", "ts-plugin-css"}, nil, nil, map[string]string{
				"index.js":           usesNothing,
				"tsconfig.base.json": `{"extends": "@tsconfig/node20/tsconfig.json", "compilerOptions": {"plugins": [{"name": "ts-plugin-css"}]}}`,
				"jest.config.js":     "module.exports = { setupFiles: ['jest-fetch'] }\n",
				"postcss.config.cjs": "module.exports = { plugins: { autoprefixer: {} } }\n",
			})},
		{name: "configured by a file named for it",
			files: npmRepo(nil, []string{"prettier", "eslint", "lint-staged", "tailwindcss", "nodemon", "vite", "webpack", "babel-jest-x"}, nil, nil, map[string]string{
				"index.js":           usesNothing,
				".prettierrc":        "{\"semi\": false}\n",
				".eslintrc.json":     "{\"root\": true}\n",
				".lintstagedrc":      "{}\n",
				"tailwind.config.js": "module.exports = {}\n",
				"nodemon.json":       "{\"watch\": [\"src\"]}\n",
				"vite.config.ts":     "export default {}\n",
				"webpack.config.js":  "module.exports = {}\n",
			}),
			gone: []string{"babel-jest-x"}},
		{name: "a vite plugin its config imports",
			files: npmRepo(nil, []string{"@vitejs/plugin-react"}, nil, nil, map[string]string{
				"index.js":       usesNothing,
				"vite.config.ts": "import react from '@vitejs/plugin-react'\nexport default { plugins: [react()] }\n",
			})},
		{name: "@types follow their package or the TypeScript compiler",
			files: npmRepo([]string{"express"}, []string{"@types/express", "@types/babel__core", "@types/left-pad"}, nil, nil, map[string]string{
				"index.js": "const express = require('express')\nconst core = require('@babel/core')\n",
			}),
			gone: []string{"@types/left-pad"}},
		{name: "TypeScript reads every @types package, and needs tslib",
			files: npmRepo([]string{"tslib"}, []string{"typescript", "@types/node", "@types/left-pad"}, nil, nil, map[string]string{
				"src/main.ts":   "export const one = 1\n",
				"tsconfig.json": `{"compilerOptions": {"importHelpers": true}}`,
			})},
		{name: "code compilers emit imports into",
			files: npmRepo([]string{"core-js", "regenerator-runtime"}, nil, nil, nil, map[string]string{
				"index.js":          usesNothing,
				"babel.config.json": `{"presets": [["@babel/env", {"useBuiltIns": "usage", "corejs": 3}]]}`,
			})},
		{name: "a peer another installed package needs",
			files: npmRepo([]string{"react", "react-ui", "sass"}, nil, nil, map[string]string{
				"react-ui":    `"peerDependencies": {"react": "^18"}`,
				"sass-loader": `"peerDependenciesMeta": {"sass": {"optional": true}}`,
			}, map[string]string{"index.js": "require('react-ui')\n"})},
		{name: "peer and optional dependencies are never touched",
			files: map[string]string{
				"package.json":      `{"name": "lib", "peerDependencies": {"react": "^18"}, "optionalDependencies": {"fsevents": "^2"}, "devDependencies": {"react": "^18"}}`,
				"package-lock.json": `{"lockfileVersion": 3, "packages": {"": {}, "node_modules/react": {"version": "18.0.0"}, "node_modules/fsevents": {"version": "2.0.0"}}}`,
				"index.js":          usesNothing,
			}},
		{name: "plugins a tool loads by a short name",
			files: npmRepo(nil, []string{"eslint-plugin-unicorn", "eslint-plugin-ghost", "@babel/preset-env", "sass-loader", "prettier-plugin-x"}, nil, nil, map[string]string{
				"index.js":       usesNothing,
				".eslintrc.json": `{"plugins": ["unicorn"]}`,
				"webpack.cfg.js": "module.exports = { module: { rules: [{ use: ['sass'] }] } }\n",
			}),
			kept: map[string]string{"eslint-plugin-ghost": "plugin eslint loads", "@babel/preset-env": "plugin babel loads", "prettier-plugin-x": "plugin prettier loads"}},
		{name: "documentation and its own declaration do not count",
			files: npmRepo([]string{"left-pad"}, nil, nil, nil, map[string]string{
				"index.js":     usesNothing,
				"README.md":    "We use `left-pad` for the reports.\n",
				"docs/old.rst": "left-pad\n",
			}),
			gone: []string{"left-pad"}},
		{name: "longer names are other packages",
			files: npmRepo([]string{"pad", "colors"}, nil, nil, nil, map[string]string{
				"index.js": "const l = require('left-pad')\nconst c = require('@acme/colors')\nconst x = 'pad-utils' + colors2\n",
			}),
			gone: []string{"pad", "colors"}},
		{name: "tend.yml ignores it",
			files: npmRepo([]string{"left-pad", "is-odd"}, nil, nil, nil, map[string]string{"index.js": usesNothing}),
			keep:  []string{"left-pad"},
			gone:  []string{"is-odd"}},
		{name: "a lockfile that records no peers",
			files: map[string]string{
				"package.json": `{"dependencies": {"left-pad": "^1.0.0"}}`,
				"yarn.lock":    "left-pad@^1.0.0:\n  version \"1.3.0\"\n",
				"index.js":     usesNothing,
			},
			kept: map[string]string{"left-pad": "does not say which packages need it as a peer"}},
		{name: "no lockfile at all",
			files: map[string]string{"package.json": `{"dependencies": {"left-pad": "^1.0.0"}}`, "index.js": usesNothing},
			kept:  map[string]string{"left-pad": "there is no lockfile"}},
		{name: "a command whose name the lockfile does not give",
			files: map[string]string{
				"package.json":   `{"scripts": {"x": "something"}, "dependencies": {"left-pad": "^1.0.0", "tool": "^1.0.0"}}`,
				"pnpm-lock.yaml": "lockfileVersion: '9.0'\nimporters:\n  .:\n    dependencies:\n      left-pad: {specifier: ^1.0.0, version: 1.3.0}\n      tool: {specifier: ^1.0.0, version: 1.0.0}\npackages:\n  left-pad@1.3.0:\n    resolution: {integrity: x}\n  tool@1.0.0:\n    resolution: {integrity: y}\n    hasBin: true\nsnapshots:\n  left-pad@1.3.0: {}\n  tool@1.0.0: {}\n",
				"index.js":       usesNothing,
			},
			gone: []string{"left-pad"},
			kept: map[string]string{"tool": "does not say what the command is called"}},
		{name: "a script on the one line that declares the dependencies",
			files: map[string]string{
				"package.json":   `{"scripts": {"lint": "tool --fix ."}, "dependencies": {"left-pad": "^1.0.0", "tool": "^1.0.0"}}`,
				"pnpm-lock.yaml": "lockfileVersion: '9.0'\nimporters:\n  .:\n    dependencies:\n      left-pad: {specifier: ^1.0.0, version: 1.3.0}\n      tool: {specifier: ^1.0.0, version: 1.0.0}\npackages:\n  left-pad@1.3.0:\n    resolution: {integrity: x}\n  tool@1.0.0:\n    resolution: {integrity: y}\nsnapshots:\n  left-pad@1.3.0: {}\n  tool@1.0.0: {}\n",
				"index.js":       usesNothing,
			},
			gone: []string{"left-pad"}},
	})
}

// Packages a project loads without naming them in its code: by an alias,
// by a name the code builds, through a loader or a framework's convention,
// or from a docs page.
func TestUnusedNpmConventions(t *testing.T) {
	checkUnused(t, []unusedCase{
		{name: "imported under an npm: alias",
			files: map[string]string{
				"package.json":      "{\n  \"dependencies\": {\n    \"even\": \"npm:is-even@^1.0.0\",\n    \"left-pad\": \"^1.0.0\"\n  }\n}\n",
				"package-lock.json": `{"lockfileVersion": 3, "packages": {"": {}, "node_modules/even": {"name": "is-even", "version": "1.0.0"}, "node_modules/left-pad": {"version": "1.3.0"}}}`,
				"index.js":          "const even = require('even')\n",
			},
			gone: []string{"left-pad"}},
		{name: "a name the code builds",
			files: npmRepo([]string{"@fontsource/inter", "@fontsource/roboto", "plugin-a", "left-pad"}, nil, nil, nil, map[string]string{
				"fonts.js":   "export const load = (font) => import(`@fontsource/${font}`)\n",
				"plugins.js": "module.exports = (n) => require(\"plugin-\" + n)\n",
			}),
			gone: []string{"left-pad"},
			kept: map[string]string{"@fontsource/inter": "builds a package name from @fontsource/ in fonts.js:1", "@fontsource/roboto": "@fontsource/", "plugin-a": "plugins.js:1"}},
		{name: "gulp and grunt plugins a loader takes",
			files: npmRepo(nil, []string{"gulp", "gulp-load-plugins", "gulp-uglify", "@acme/gulp-zip", "load-grunt-tasks", "grunt-contrib-copy"}, []string{"build=gulp"}, nil, map[string]string{
				"gulpfile.js":  "const gulp = require('gulp')\nconst $ = require('gulp-load-plugins')()\ngulp.task('min', () => gulp.src('a.js').pipe($.uglify()))\n",
				"Gruntfile.js": "module.exports = (grunt) => { require('load-grunt-tasks')(grunt); grunt.registerTask('default', ['copy']) }\n",
			}),
			kept: map[string]string{"gulp-uglify": "gulp-load-plugins loads every gulp-*", "@acme/gulp-zip": "gulp-load-plugins", "grunt-contrib-copy": "load-grunt-tasks loads every grunt-*"}},
		{name: "gulp plugins without a loader",
			files: npmRepo(nil, []string{"gulp", "gulp-uglify"}, []string{"build=gulp"}, nil, map[string]string{"gulpfile.js": "const gulp = require('gulp')\n"}),
			gone:  []string{"gulp-uglify"}},
		{name: "a package of a declared tool's own scope",
			files: npmRepo(nil, []string{"vitest", "@vitest/coverage-v8", "@acme/thing"}, []string{"test=vitest run", "coverage=vitest run --coverage"}, nil, map[string]string{
				"a.test.js": "import { test } from 'vitest'\n",
			}),
			gone: []string{"@acme/thing"},
			kept: map[string]string{"@vitest/coverage-v8": "vitest loads packages of its own scope"}},
		{name: "plugins Prettier 2 and eslint-plugin-import load by themselves",
			files: npmRepo(nil, []string{"prettier", "@prettier/plugin-xml", "eslint", "eslint-import-resolver-webpack", "eslint-formatter-pretty"}, []string{"format=prettier --write .", "lint=eslint -f pretty ."}, nil, map[string]string{"index.js": usesNothing}),
			kept:  map[string]string{"@prettier/plugin-xml": "plugin prettier loads", "eslint-import-resolver-webpack": "plugin eslint loads"}},
		{name: "a framework loads it when it is installed",
			files: npmRepo([]string{"next", "sharp"}, nil, []string{"build=next build"}, nil, map[string]string{"pages/index.js": "import Image from 'next/image'\n"})},
		{name: "sharp without a framework that loads it",
			files: npmRepo([]string{"sharp"}, nil, nil, nil, map[string]string{"index.js": usesNothing}),
			gone:  []string{"sharp"}},
		{name: "native modules a React Native app links",
			files: npmRepo([]string{"react-native", "expo-dev-client", "react-native-svg", "@react-native-community/cli", "left-pad"}, nil, []string{"start=expo start --dev-client"}, nil, map[string]string{
				"index.js": "import { Text } from 'react-native'\n",
			}),
			gone: []string{"left-pad"},
			kept: map[string]string{"expo-dev-client": "React Native", "react-native-svg": "React Native", "@react-native-community/cli": "React Native"}},
		{name: "@types tsc reads when it checks JavaScript without a tsconfig.json",
			files: npmRepo(nil, []string{"typescript", "@types/node"}, []string{"typecheck=tsc --allowJs --checkJs --noEmit src/index.js"}, map[string]string{"typescript": `"bin": {"tsc": "bin/tsc"}`}, map[string]string{
				"src/index.js": "// @ts-check\nconst fs = require('fs')\n",
			})},
		{name: "Yarn's Plug'n'Play maps and its own release name every package",
			files: map[string]string{
				"package.json":              "{\n  \"dependencies\": {\n    \"left-pad\": \"^1.0.0\"\n  }\n}\n",
				"yarn.lock":                 "# yarn lockfile\n\n__metadata:\n  version: 8\n\n\"left-pad@npm:^1.0.0\":\n  version: 1.3.0\n  resolution: \"left-pad@npm:1.3.0\"\n",
				".pnp.cjs":                  "[\"left-pad\", [[\"npm:1.3.0\", {\"packageLocation\": \"./.yarn/cache/left-pad.zip/node_modules/left-pad/\"}]]]\n",
				".pnp.loader.mjs":           "// left-pad\n",
				".yarn/releases/yarn-4.cjs": "require('left-pad')\n",
				"index.js":                  usesNothing,
			},
			gone: []string{"left-pad"}},
		{name: "imports in a docs page, not in its code blocks",
			files: npmRepo([]string{"react-player", "left-pad"}, nil, nil, nil, map[string]string{
				"index.js":      usesNothing,
				"docs/intro.md": "---\ntitle: Intro\n---\nimport Player from 'react-player'\n\n# Intro\n\n```js\nimport pad from 'left-pad'\n```\n\nWe used to import left-pad here.\n",
			}),
			gone: []string{"left-pad"}},
		{name: "a Python import in a docs page is an example",
			files: map[string]string{
				"requirements.txt": "pyyaml==6.0\n",
				"app/main.py":      "def main():\n    return 1\n",
				"docs/usage.md":    "Load it:\n\n    import yaml\n",
			},
			gone: []string{"pyyaml"}},
	})
}

func TestUnusedGo(t *testing.T) {
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOMODCACHE", t.TempDir())
	t.Setenv("GOFLAGS", "-modcacherw")
	mod := func(requires ...string) string {
		return "module example.com/app\n\ngo 1.22\n\nrequire (\n\t" + strings.Join(requires, "\n\t") + "\n)\n"
	}
	checkUnused(t, []unusedCase{
		{name: "required and never imported",
			files: map[string]string{
				"go.mod":  mod("example.com/used v1.0.0", "example.com/gone v1.2.0", "example.com/dep v1.0.0 // indirect"),
				"main.go": "package main\n\nimport \"example.com/used/pkg\"\n\nfunc main() { pkg.Run() }\n",
			},
			gone: []string{"example.com/gone"}},
		{name: "a tools.go blank import",
			files: map[string]string{
				"go.mod":   mod("golang.org/x/tools v0.30.0"),
				"main.go":  "package main\n\nfunc main() {}\n",
				"tools.go": "//go:build tools\n\npackage main\n\nimport _ \"golang.org/x/tools/cmd/stringer\"\n",
			}},
		{name: "a tool directive",
			files: map[string]string{
				"go.mod":  mod("golang.org/x/tools v0.30.0") + "\ntool golang.org/x/tools/cmd/stringer\n",
				"main.go": "package main\n\nfunc main() {}\n",
			}},
		{name: "go:generate and a Makefile",
			files: map[string]string{
				"go.mod":   mod("github.com/a/gen v1.0.0", "github.com/b/lint/v2 v2.0.0"),
				"main.go":  "package main\n\n//go:generate go run github.com/a/gen/cmd/gen -o x.go\nfunc main() {}\n",
				"Makefile": "lint:\n\tgo run github.com/b/lint/v2/cmd/lint ./...\n",
			}},
		{name: "a config file named for the tool",
			files: map[string]string{
				"go.mod":        mod("github.com/golangci/golangci-lint/v2 v2.1.0"),
				"main.go":       "package main\n\nfunc main() {}\n",
				".golangci.yml": "linters: {}\n",
			}},
	})
}

func TestUnusedPython(t *testing.T) {
	venv := func(dist, mods string) map[string]string {
		return map[string]string{".venv/lib/python3.12/site-packages/" + dist + "-1.0.dist-info/top_level.txt": mods + "\n"}
	}
	points := func(dist, src string) map[string]string {
		return map[string]string{".venv/lib/python3.12/site-packages/" + dist + "-1.0.dist-info/entry_points.txt": src}
	}
	metadata := func(dist, headers string) map[string]string {
		return map[string]string{".venv/lib/python3.12/site-packages/" + dist + "-1.0.dist-info/METADATA": "Metadata-Version: 2.1\nName: " + dist + "\n" + headers}
	}
	with := func(files ...map[string]string) map[string]string {
		res := map[string]string{"app/main.py": "def main():\n    return 1\n"}
		for _, f := range files {
			maps.Copy(res, f)
		}
		return res
	}
	checkUnused(t, []unusedCase{
		{name: "a known module never imported",
			files: with(map[string]string{"requirements.txt": "pyyaml==6.0\npython-dateutil>=2\n"}),
			gone:  []string{"pyyaml", "python-dateutil"}},
		{name: "imported under its module name",
			files: with(map[string]string{"requirements.txt": "pyyaml==6.0\npillow\n", "app/io.py": "import yaml\nfrom PIL import Image\n"})},
		{name: "modules tend cannot name stay",
			files: with(map[string]string{"requirements.txt": "pyserial==3.5\n"}),
			kept:  map[string]string{"pyserial": "cannot tell which modules"}},
		{name: "an installed distribution says what it installs",
			files: with(map[string]string{"requirements.txt": "pyserial==3.5\nrequests==2.0\n", "app/s.py": "import serial\n"}, venv("pyserial", "serial"), venv("requests", "requests")),
			gone:  []string{"requests"}},
		{name: "command line tools in tox, nox, pre-commit, a Makefile and CI",
			files: with(map[string]string{
				"requirements-dev.txt":     "black\nisort\nruff\nmypy\npytest\n",
				"tox.ini":                  "[testenv]\ncommands = pytest -q\n",
				"noxfile.py":               "import nox\n@nox.session\ndef lint(s):\n    s.run('isort', '.')\n",
				".pre-commit-config.yaml":  "repos:\n  - repo: local\n    hooks: [{id: ruff, entry: ruff check}]\n",
				"Makefile":                 "fmt:\n\tblack .\n",
				".github/workflows/ci.yml": "steps:\n  - run: mypy app\n",
			}, venv("black", "black"), venv("isort", "isort"), venv("ruff", "ruff"), venv("mypy", "mypy"), venv("pytest", "pytest"))},
		{name: "configured in pyproject.toml, or by a file named for it",
			files: with(map[string]string{
				"pyproject.toml": "[project]\nname = \"app\"\ndependencies = [\n  \"coverage\",\n  \"black\",\n]\n\n[tool.black]\nline-length = 100\n",
				".coveragerc":    "[run]\nbranch = True\n",
			}, venv("coverage", "coverage"), venv("black", "black"))},
		{name: "an entry point into a dependency",
			files: with(map[string]string{
				"pyproject.toml": "[project]\nname = \"app\"\ndependencies = [\"othertool\"]\n\n[project.scripts]\nmine = \"othertool.cli:main\"\n",
			}, venv("othertool", "othertool"))},
		{name: "build tools and what frameworks load by themselves",
			files: with(map[string]string{"requirements.txt": "setuptools\nwheel\npsycopg2-binary\npillow\npython-dotenv\ngunicorn\n"})},
		{name: "loaded by a library next to it",
			files: with(map[string]string{
				"requirements.txt": "fastapi\npython-multipart\nemail-validator\npandas\nopenpyxl\nsqlalchemy\ngreenlet\nasyncpg\n",
				"app/api.py":       "import pandas as pd\nfrom fastapi import FastAPI, Form\nfrom sqlalchemy.ext.asyncio import create_async_engine\n",
			}, venv("fastapi", "fastapi"), venv("python_multipart", "multipart"), venv("email_validator", "email_validator"), venv("pandas", "pandas"),
				venv("openpyxl", "openpyxl"), venv("SQLAlchemy", "sqlalchemy"), venv("greenlet", "greenlet")),
			kept: map[string]string{
				"python-multipart": "parse forms", "email-validator": "EmailStr", "openpyxl": "Excel", "greenlet": "asyncio",
			}},
		{name: "the same packages with nothing to load them",
			files: with(map[string]string{"requirements.txt": "openpyxl\ngreenlet\n"}, venv("openpyxl", "openpyxl"), venv("greenlet", "greenlet")),
			gone:  []string{"openpyxl", "greenlet"}},
		{name: "a command it installs, run from a Makefile",
			files: with(map[string]string{"requirements-dev.txt": "awscli\n", "Makefile": "deploy:\n\taws s3 sync build/ s3://bucket\n"},
				venv("awscli", "awscli"), points("awscli", "[console_scripts]\naws = awscli.clidriver:main\n"))},
		{name: "a plugin by its entry points",
			files: with(map[string]string{"requirements-dev.txt": "pep8-naming\nflake8\n", ".flake8": "[flake8]\nmax-line-length = 100\n"},
				venv("pep8_naming", "pep8ext_naming"), points("pep8_naming", "[flake8.extension]\nN8 = pep8ext_naming:NamingChecker\n"), venv("flake8", "flake8")),
			kept: map[string]string{"pep8-naming": "flake8.extension"}},
		{name: "pins in a constraints file",
			files: with(map[string]string{
				"requirements.txt":     "-c constraints.txt\nrequests==2.32.3\n",
				"constraints.txt":      "urllib3==2.2.2\npyyaml==6.0.1\n",
				"requirements/dev.txt": "--constraint=../pins.txt\n",
				"pins.txt":             "python-dateutil==2.9.0\n",
				"app/net.py":           "import requests\n",
			}, venv("requests", "requests"), venv("urllib3", "urllib3"))},
		{name: "a frozen file pins what another package requires",
			files: with(map[string]string{
				"requirements.txt": "flask==3.0.3\nclick==8.1.7\nrequests==2.32.3\n",
				"app/web.py":       "import flask\n",
			}, venv("flask", "flask"), venv("click", "click"), venv("requests", "requests"),
				metadata("flask", "Requires-Dist: Werkzeug>=3.0.0\nRequires-Dist: click>=8.1.3\nRequires-Dist: asgiref>=3.2; extra == \"async\"\n\nRequires-Dist: body-text\n")),
			gone: []string{"requests"}},
		{name: "a pin on what another package requires through a third",
			files: with(map[string]string{
				"requirements.txt": "google-cloud-storage==2.18.0\nprotobuf<5\n",
				"app/store.py":     "from google.cloud import storage\n",
			}, metadata("google_cloud_storage", "Requires-Dist: google-api-core<3,>=2.15\n"), metadata("google_api_core", "Requires-Dist: protobuf>=3.19\n"))},
		{name: "extras offered to users, and dev extras",
			files: with(map[string]string{
				"pyproject.toml": "[project]\nname = \"lib\"\ndependencies = []\n\n[project.optional-dependencies]\nfast = [\"orjson\"]\ntest = [\"pyyaml\"]\n",
			}, venv("orjson", "orjson")),
			gone: []string{"pyyaml"},
			kept: map[string]string{"orjson": `the extra "fast"`}},
		{name: "a Poetry dependency an extra offers",
			files: with(map[string]string{
				"pyproject.toml": "[tool.poetry]\nname = \"lib\"\n\n[tool.poetry.dependencies]\npython = \"^3.10\"\npyyaml = {version = \"^6.0\", optional = true}\n\n[tool.poetry.extras]\nconf = [\"pyyaml\"]\n",
			}),
			kept: map[string]string{"pyyaml": "an extra offers it"}},
		{name: "imported in a notebook its plots make large",
			files: with(map[string]string{
				"requirements.txt":    "scikit-learn\npyyaml\n",
				"notebooks/fit.ipynb": `{"cells": [{"cell_type": "markdown", "source": "pyyaml"}, {"cell_type": "code", "source": ["from sklearn import svm\\n"], "outputs": [{"data": {"image/png": "` + strings.Repeat("A", maxText) + `"}}]}], "nbformat": 4}`,
			}),
			gone: []string{"pyyaml"}},
		{name: "a frozen file with nothing installed keeps every pin",
			files: with(map[string]string{"requirements.txt": "aiosmtpd==1.4.4\natpublic==4.0\nattrs==23.1.0\nrequests==2.31.0\ncertifi==2023.11.17\n", "app/mail.py": "import aiosmtpd\nimport requests\n"})},
		{name: "the test runner tend runs",
			files: with(map[string]string{"requirements.txt": "pytest==8.3.3\n", "tests/test_a.py": "def test_a():\n    assert True\n"})},
		{name: "plugins and stubs",
			files: with(map[string]string{"requirements-dev.txt": "pytest-cov\ntypes-requests\npandas-stubs\n"}),
			kept:  map[string]string{"pytest-cov": "plugin pytest loads", "types-requests": "plugin mypy loads", "pandas-stubs": "stubs mypy reads"}},
	})
}

// Only npm, Go and pip dependencies are looked at: Maven and Gradle plugins,
// and every other ecosystem, are never removed, used or not.
func TestUnusedLeavesOtherEcosystemsAlone(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"App.java": "class App {}\n"})
	idx, err := scan.Build(scan.Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	var deps []core.Dep
	for _, eco := range []string{core.Maven, core.Gradle, core.Cargo, core.Gems, core.Composer, core.Nuget, core.Docker, core.Actions} {
		deps = append(deps, core.Dep{Ecosystem: eco, Name: "org.apache.maven.plugins:maven-shade-plugin", Direct: true, Manifest: core.Manifest{Path: "pom.xml"}})
	}
	gone, kept, err := Unused(&core.Inventory{Root: root, Deps: deps, Index: idx}, func(core.Dep) bool { return false })
	if err != nil || len(gone) != 0 || len(kept) != 0 {
		t.Errorf("gone %+v, kept %+v, %v", gone, kept, err)
	}
	if gone, kept, _ := Unused(&core.Inventory{Root: root, Deps: deps}, func(core.Dep) bool { return false }); len(gone)+len(kept) != 0 {
		t.Error("without a scan index, tend proved something unused")
	}
}

func TestNamed(t *testing.T) {
	tests := []struct {
		text, word string
		want       bool
	}{
		{`require("lodash")`, "lodash", true},
		{`require("lodash/fp")`, "lodash", true},
		{`npx lodash@4 x`, "lodash", true},
		{`import yaml.safe_load`, "yaml", true},
		{`./node_modules/.bin/tsc -p .`, "tsc", true},
		{`require("lodash-es")`, "lodash", false},
		{`require("@acme/lodash")`, "lodash", false},
		{`x.lodash()`, "lodash", false},
		{`my_lodash`, "lodash", false},
		{`"@fixture/pay"`, "@fixture/pay", true},
		{`"@fixture/payments"`, "@fixture/pay", false},
		{`go run github.com/a/gen/cmd/gen`, "github.com/a/gen", true},
		{`github.com/a/generate`, "github.com/a/gen", false},
	}
	for _, tt := range tests {
		if got := named([]byte(tt.text), tt.word) >= 0; got != tt.want {
			t.Errorf("named(%q, %q) = %v, want %v", tt.text, tt.word, got, tt.want)
		}
	}
}

func TestConfigStem(t *testing.T) {
	tests := []struct {
		base, tool string
		want       bool
	}{
		{".eslintrc.json", "eslint", true},
		{".prettierrc", "prettier", true},
		{"jest.config.ts", "jest", true},
		{"karma.conf.js", "karma", true},
		{"tailwind.config.js", "tailwindcss", true},
		{".pre-commit-config.yaml", "pre-commit", true},
		{"tox.ini", "tox", true},
		{"conftest.py", "pytest", true},
		{"setup.cfg", "setuptools", true},
		{"nodemon.json", "nodemon", true},
		{"data.json", "dataloader", false},
		{"package.json", "package", false},
		{"index.js", "index", false},
		{"jest.setup.js", "jest", false},
	}
	for _, tt := range tests {
		stem, strong := configStem(tt.base)
		if got := fits(stem, alnum(tt.tool), strong); got != tt.want {
			t.Errorf("%s names %s = %v, want %v (stem %q)", tt.base, tt.tool, got, tt.want, stem)
		}
	}
}

// A requirement no code imports, but that other modules require, holds the
// version the build gets: dropping it would hand the choice to them.
func TestUnusedGoRequiredByOthers(t *testing.T) {
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
		goModule{"example.com/gone", "v1.0.0", nil},
	))
	gone, kept := unusedIn(t, map[string]string{
		"go.mod":  "module example.com/app\n\ngo 1.21\n\nrequire (\n\texample.com/lib v1.0.0\n\texample.com/dep v1.2.0\n\texample.com/gone v1.0.0\n)\n",
		"main.go": "package main\n\nimport _ \"example.com/lib\"\n\nfunc main() {}\n",
	})
	if !slices.Equal(gone, []string{"example.com/gone"}) || len(kept) != 0 {
		t.Errorf("removes %q, keeps %q", gone, kept)
	}
}

// goOffline points the go command at proxy, with a module cache of the
// test's own and no checksum database.
func goOffline(t *testing.T, proxy string) {
	t.Helper()
	goPublic(t)
	t.Setenv("GOPROXY", proxy)
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOFLAGS", "-modcacherw")
	t.Setenv("GOMODCACHE", t.TempDir())
	t.Setenv("GOTOOLCHAIN", "local")
}

func TestPyParsePoints(t *testing.T) {
	got := pyParsePoints("[console_scripts]\naws = awscli.clidriver:main\naws_completer=awscli.completer:main\n\n# a comment\n[pytest11]\ncov = pytest_cov.plugin\n[gui_scripts]\nview = v:main\n")
	want := pyEntryPoints{scripts: []string{"aws", "aws_completer", "view"}, groups: []string{"pytest11"}}
	if !slices.Equal(got.scripts, want.scripts) || !slices.Equal(got.groups, want.groups) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestPyParseRequires(t *testing.T) {
	got := pyParseRequires("Metadata-Version: 2.1\r\nName: x\r\nRequires-Dist: Werkzeug (>=3.0)\r\nRequires-Dist: zope.interface\r\nRequires-Dist: rich[jupyter]>=13; extra == \"cli\"\r\n\r\nRequires-Dist: in-the-body\r\n")
	if want := []string{"werkzeug", "zope-interface", "rich"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
