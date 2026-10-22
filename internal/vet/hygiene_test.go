package vet

import (
	"context"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

func TestDebug(t *testing.T) {
	base := map[string]string{
		"src/a.js":       "function run() {\n  return 1\n}\nmodule.exports = run\n",
		"src/b.js":       "function run() {\n  console.log(pay.charges.create())\n}\n",
		"test/a.test.js": "test('x', () => {})\n",
		"main.go":        "package main\n\nimport \"os\"\n\nfunc main() {\n\tos.Exit(0)\n}\n",
		"tool/b.go":      "package tool\n\nimport \"fmt\"\n\nfunc S() string {\n\treturn fmt.Sprint(1)\n}\n",
		"app.py":         "def f(x):\n    return x\n",
		"src/c.js":       "const a = 1\n",
	}
	patched := map[string]string{
		"src/a.js":       "function run() {\n  console.log(\"debugging\", 1)\n  const x = wrap(console.log(1))\n  debugger;\n  return 1\n}\nmodule.exports = run\n",
		"src/b.js":       "function run() {\n  console.log(pay.paymentIntents.create())\n}\n",
		"test/a.test.js": "test('x', () => {\n  console.log('fine in tests')\n})\n",
		"main.go":        "package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nfunc main() {\n\tfmt.Println(\"here\")\n\tos.Exit(0)\n}\n",
		"tool/b.go":      "package tool\n\nimport \"fmt\"\n\nfunc S() string {\n\tfmt.Printf(\"%d\\n\", 1)\n\treturn fmt.Sprint(1)\n}\n",
		"app.py":         "def f(x):\n    print(x)  # look\n    printer = print\n    return x\n",
		"src/c.js":       "const a = 1\nconst msg = \"console.log(x)\"\n",
	}
	dir, sha, diff := worktree(t, base, patched)
	p := newPass(Input{Dir: dir, Base: sha, Change: &core.Change{}, Patch: &core.Patch{Diff: diff}})
	got, skip := debug(context.Background(), p)
	if skip != "" {
		t.Fatalf("skipped: %s", skip)
	}
	eq(t, got,
		"dropped app.py:2 removed `print(x)  # look`",
		"dropped main.go:9 removed `fmt.Println(\"here\")`",
		"dropped src/a.js:2 removed `console.log(\"debugging\", 1)`",
		"flagged src/a.js:3 debug output: `const x = wrap(console.log(1))`",
		"dropped src/a.js:4 removed `debugger;`",
		"dropped tool/b.go:6 removed `fmt.Printf(\"%d\\n\", 1)`",
	)
	if !p.dirty {
		t.Error("worktree edited but not marked dirty")
	}
	want := map[string]string{
		"src/a.js":  "function run() {\n  const x = wrap(console.log(1))\n  return 1\n}\nmodule.exports = run\n",
		"main.go":   "package main\n\nimport (\n\t\"os\"\n)\n\nfunc main() {\n\tos.Exit(0)\n}\n",
		"tool/b.go": base["tool/b.go"],
		"app.py":    "def f(x):\n    printer = print\n    return x\n",
		"src/b.js":  patched["src/b.js"],
	}
	for file, body := range want {
		if got := read(t, dir, file); got != body {
			t.Errorf("%s =\n%s\nwant\n%s", file, got, body)
		}
	}
}

func TestComments(t *testing.T) {
	base := map[string]string{
		"src/billing.js": "const pay = require('pay')\n\nfunction run() {\n  // keep me\n  return pay.charges.create({})\n}\n",
		"src/other.js":   "const a = 1\n",
		"app.py":         "def f():\n    return 1\n",
	}
	patched := map[string]string{
		"src/billing.js": `const pay = require('pay')

function run() {
  // keep me
  // Use the new payment intents API
  /*
   * multi-line explanation
   */
  // eslint-disable-next-line no-unused-vars
  return pay.paymentIntents.create({}) // trailing stays
}
`,
		"src/other.js": "const a = 1\n// not the model's\n",
		"app.py":       "def f():\n    # switched to v2\n    return 2\n",
	}
	dir, sha, diff := worktree(t, base, patched)
	patch := &core.Patch{Diff: diff, Model: true, Applied: []string{"bumped pay 1.0.0 -> 2.0.0 (package.json)"}, Edited: []string{"src/billing.js", "app.py"}}
	p := newPass(Input{Dir: dir, Base: sha, Change: &core.Change{}, Patch: patch})
	got, skip := comments(context.Background(), p)
	if skip != "" {
		t.Fatalf("skipped: %s", skip)
	}
	eq(t, got,
		"dropped app.py:2 removed comment `# switched to v2`",
		"dropped src/billing.js:5 removed comment `// Use the new payment intents API`",
		"dropped src/billing.js:6 removed comment `/*`",
		"dropped src/billing.js:7 removed comment `* multi-line explanation`",
		"dropped src/billing.js:8 removed comment `*/`",
	)
	wantBilling := `const pay = require('pay')

function run() {
  // keep me
  // eslint-disable-next-line no-unused-vars
  return pay.paymentIntents.create({}) // trailing stays
}
`
	if got := read(t, dir, "src/billing.js"); got != wantBilling {
		t.Errorf("billing.js =\n%s", got)
	}
	if got := read(t, dir, "src/other.js"); got != patched["src/other.js"] {
		t.Errorf("a file the model did not write was edited:\n%s", got)
	}

	patch.Model = false
	if _, skip := comments(context.Background(), newPass(Input{Dir: dir, Base: sha, Change: &core.Change{}, Patch: patch})); skip != na {
		t.Errorf("skip = %q", skip)
	}
}

func TestDebugPythonBlocks(t *testing.T) {
	tests := []struct {
		name          string
		base, patched string
		want          string
		found         []string
	}{
		{
			"only statement becomes pass",
			"def f(x):\n    if x:\n        return 1\n    return 2\n",
			"def f(x):\n    if x:\n        print(x)\n    return 2\n",
			"def f(x):\n    if x:\n        pass\n    return 2\n",
			[]string{"fixed app.py:3 replaced `print(x)` with `pass`"},
		},
		{
			"other statement left",
			"def f(x):\n    return x\n",
			"def f(x):\n    print(x)\n    return x\n",
			"def f(x):\n    return x\n",
			[]string{"dropped app.py:2 removed `print(x)`"},
		},
		{
			"two prints, one pass",
			"for x in xs:\n    go(x)\nend()\n",
			"for x in xs:\n    print(x)\n\n    # why\n    print(x, 2)\nend()\n",
			"for x in xs:\n    pass\n\n    # why\nend()\n",
			[]string{"fixed app.py:2 replaced `print(x)` with `pass`", "dropped app.py:5 removed `print(x, 2)`"},
		},
		{
			"top level",
			"x = 1\n",
			"x = 1\nprint(x)\n",
			"x = 1\n",
			[]string{"dropped app.py:2 removed `print(x)`"},
		},
		{
			"docstring is a statement",
			"def f():\n    \"\"\"Doc.\"\"\"\n",
			"def f():\n    \"\"\"Doc.\"\"\"\n    print(1)\n",
			"def f():\n    \"\"\"Doc.\"\"\"\n",
			[]string{"dropped app.py:3 removed `print(1)`"},
		},
		{
			"nested block at end of file, crlf",
			"if a:\r\n    if b:\r\n        go()\r\n",
			"if a:\r\n    if b:\r\n        print(b)\r\n",
			"if a:\r\n    if b:\r\n        pass\r\n",
			[]string{"fixed app.py:3 replaced `print(b)` with `pass`"},
		},
		{
			"pass then a later drop shifts",
			"def f():\n    go()\n\ndef g():\n    return 1\n",
			"def f():\n    print(1)\n\ndef g():\n    print(2)\n    return 1\n",
			"def f():\n    pass\n\ndef g():\n    return 1\n",
			[]string{"fixed app.py:2 replaced `print(1)` with `pass`", "dropped app.py:5 removed `print(2)`"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, sha, diff := worktree(t, map[string]string{"app.py": tt.base}, map[string]string{"app.py": tt.patched})
			p := newPass(Input{Dir: dir, Base: sha, Change: &core.Change{}, Patch: &core.Patch{Diff: diff}})
			got, skip := debug(context.Background(), p)
			if skip != "" {
				t.Fatalf("skipped: %s", skip)
			}
			eq(t, got, tt.found...)
			if got := read(t, dir, "app.py"); got != tt.want {
				t.Errorf("app.py = %q, want %q", got, tt.want)
			}
		})
	}
}
