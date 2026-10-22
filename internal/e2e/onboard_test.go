package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/deliver"
)

// The first run on a Node project: the three dependencies nothing uses go,
// the four that only look unused stay (a command a script runs, an eslint
// plugin named for short, types for a package in use, the peer another
// package needs), and the deprecated payments SDK moves, all in one PR that
// opens with a welcome. Later runs wait on it, and once it merges tend is
// past its first PR for good.
func TestOnboardNode(t *testing.T) {
	need(t, "node", "npm")
	w := fresh(t, "node-tidy")
	w.model = script(t, "node-sdk")
	w.golden("check.txt", w.tend("check").want(t, 0).stdout)

	res := w.tend("run").want(t, 0)
	if !strings.HasPrefix(res.stdout, "tend: opened #1 Tidy up: remove 3 unused dependencies, move off 1 deprecated library ") {
		t.Fatalf("run said %q\n%s", res.stdout, res.stderr)
	}
	pr := onlyPR(t, w)
	w.goldenPR(pr)
	if pr.Branch != deliver.TidyBranch || pr.Draft {
		t.Errorf("pr = %+v", pr)
	}
	pkg := w.git(w.origin, "show", pr.Branch+":package.json")
	for _, gone := range []string{"@fixture/colors", "@fixture/strings", "@fixture/old-test-helper"} {
		if strings.Contains(pkg, gone) {
			t.Errorf("%s is still declared:\n%s", gone, pkg)
		}
	}
	for _, kept := range []string{"@fixture/linter", "eslint-plugin-fixture", "@types/fixture__pay", "@fixture/runtime", "@fixture/plugin-host", `"@fixture/pay": "^2.0.0"`} {
		if !strings.Contains(pkg, kept) {
			t.Errorf("%s is gone:\n%s", kept, pkg)
		}
	}
	if cs := w.commits(pr.Branch); len(cs) != 1 || !slices.Equal(cs[0].files, []string{"package-lock.json", "package.json", "src/billing.js"}) {
		t.Errorf("commits = %+v", cs)
	}
	if st := w.state(); st.Onboard != "due" || st.Open == nil || st.Open.ID != core.TidyID {
		t.Errorf("state: onboard %q, open %+v", st.Onboard, st.Open)
	}

	if res := w.tend("run").want(t, 0); !strings.Contains(res.stdout, "waiting on PR #1") || len(w.forge.All()) != 1 {
		t.Errorf("second run: %q, %d PRs", res.stdout, len(w.forge.All()))
	}
	w.merge(1)
	if res := w.tend("run").want(t, 0); res.stdout != "tend: up to date\n" {
		t.Errorf("after the merge: %q", res.stdout)
	}
	if st := w.state(); st.Onboard != "merged" || len(w.forge.All()) != 1 {
		t.Errorf("after the merge: onboard %q, %d PRs", st.Onboard, len(w.forge.All()))
	}
}

// Closing the first PR unmerged is a maintainer's answer: tend never opens
// it again, and what it held back gets PRs of its own. Without a model, the
// deprecated SDK is not moved in the first PR but laid out with its options.
func TestOnboardClosed(t *testing.T) {
	need(t, "node", "npm")
	w := fresh(t, "node-tidy")
	res := w.tend("run").want(t, 0)
	if !strings.HasPrefix(res.stdout, "tend: opened #1 Tidy up: remove 3 unused dependencies ") {
		t.Fatalf("run said %q\n%s", res.stdout, res.stderr)
	}
	body := w.forge.PR(1).Body
	for _, want := range []string{"Deprecated, not moved", "`@fixture/pay` 1.4.0 in `package.json`: no model is configured to move the 2 call sites.", "Pin to 1.4.0 and ignore"} {
		if !strings.Contains(body, want) {
			t.Errorf("the body lacks %q:\n%s", want, body)
		}
	}
	if n := strings.Count(res.stderr, "tend: removed "); n != 1 {
		t.Errorf("the removal is logged %d times, want once:\n%s", n, res.stderr)
	}

	w.closeByHuman(1)
	if res := w.tend("run").want(t, 0); !strings.HasPrefix(res.stdout, "tend: opened #2 Decide: migrate off @fixture/pay 1.x ") {
		t.Fatalf("after the close: %q", res.stdout)
	}
	if st := w.state(); st.Onboard != "closed" {
		t.Errorf("onboard = %q", st.Onboard)
	}
	if res := w.tend("run").want(t, 0); !strings.Contains(res.stdout, "waiting on PR #2") || len(w.forge.All()) != 2 {
		t.Errorf("then: %q, %d PRs", res.stdout, len(w.forge.All()))
	}
}

// A package loaded by a name built at run time looks unused, and removing it
// breaks the tests. The proof of the whole fails, so tend leaves parts out
// until the rest passes: the other two removals go, and the PR says which
// one stayed and why.
func TestOnboardBisect(t *testing.T) {
	need(t, "node", "npm")
	w := fresh(t, "node-tidy")
	w.commit("Load a helper by name", map[string]string{
		"test/strings.test.js": "'use strict'\n\nconst test = require('node:test')\nconst name = ['@fixture', 'strings'].join('/')\n\ntest('pads', () => { require(name).pad('a', 2) })\n",
	})
	res := w.tend("run").want(t, 0)
	if !strings.HasPrefix(res.stdout, "tend: opened #1 Tidy up: remove 2 unused dependencies ") {
		t.Fatalf("run said %q\n%s", res.stdout, res.stderr)
	}
	pr := onlyPR(t, w)
	if want := "- `@fixture/strings` 1.0.0 in `package.json`: with it removed, `npm test` fails (exit 1)"; !strings.Contains(pr.Body, want) {
		t.Errorf("the body does not say why @fixture/strings stayed:\n%s", pr.Body)
	}
	pkg := w.git(w.origin, "show", pr.Branch+":package.json")
	if !strings.Contains(pkg, "@fixture/strings") || strings.Contains(pkg, "@fixture/colors") || strings.Contains(pkg, "@fixture/old-test-helper") {
		t.Errorf("package.json on the branch:\n%s", pkg)
	}
	if n := strings.Count(res.stderr, "proving patched"); n != 4 {
		t.Errorf("%d proofs of a patch, want the whole, a half, then each of the other half:\n%s", n, res.stderr)
	}
}

// A Go module requires a module nothing imports, and the first PR removes
// it. A security fix that arrives while that PR is open takes its place,
// leaving the unused module alone, and the first PR comes back once the fix
// merges, whatever was said on it since, to remove it.
func TestOnboardGo(t *testing.T) {
	need(t, "go")
	w := fresh(t, "go-tidy")
	w.fetch.osv.ids = nil
	res := w.tend("run").want(t, 0)
	if !strings.HasPrefix(res.stdout, "tend: opened #1 Tidy up: remove 1 unused dependency ") {
		t.Fatalf("run said %q\n%s", res.stdout, res.stderr)
	}
	w.goldenPR(onlyPR(t, w))

	w.fetch.osv.ids = []string{"GO-2026-0001"}
	if res := w.tend("run").want(t, 0); !strings.HasPrefix(res.stdout, "tend: opened #2 Bump example.com/fixture/crypto to v1.0.1 (fixes GO-2026-0001) ") {
		t.Fatalf("with the advisory: %q\n%s", res.stdout, res.stderr)
	}
	if d := w.diff(w.forge.PR(2).Branch); !strings.Contains(d, "example.com/fixture/crypto v1.0.1") || regexp.MustCompile(`(?m)^-.*example.com/fixture/logs`).MatchString(d) {
		t.Errorf("the security fix moves more than the bump:\n%s", d)
	}
	cs, _ := w.forge.Comments(t.Context(), 1)
	if w.forge.PR(1).State != "closed" || len(cs) == 0 || !strings.HasPrefix(cs[0].Body, "Closing to make room for a security fix. tend will reopen this after that merges.") {
		t.Errorf("#1 is %s, comments %+v", w.forge.PR(1).State, cs)
	}

	// A word on the closed PR is not a person closing it.
	w.say(1, "ada", "OWNER", "ok, the fix first")
	w.merge(2)
	if res := w.tend("run").want(t, 0); !strings.HasPrefix(res.stdout, "tend: opened #3 Tidy up: remove 1 unused dependency ") {
		t.Fatalf("after the fix merged: %q\n%s", res.stdout, res.stderr)
	}
	if pr := w.forge.PR(3); pr.Branch != deliver.TidyBranch || !strings.Contains(w.diff(pr.Branch), "-\texample.com/fixture/logs") {
		t.Errorf("#3 = %+v\n%s", pr, w.diff(pr.Branch))
	}
	w.merge(3)
	if res := w.tend("run").want(t, 0); res.stdout != "tend: up to date\n" || w.state().Onboard != "merged" {
		t.Errorf("after the first PR merged: %q, onboard %q", res.stdout, w.state().Onboard)
	}
}

// A repo with nothing to tidy gets a first PR all the same: the welcome, and
// a tend.yml with every key commented out so the PR has something in it. A
// dry run shows that PR and remembers nothing.
func TestOnboardClean(t *testing.T) {
	need(t, "go")
	w := fresh(t, "go-vuln")
	w.fetch.osv.ids = nil
	res := w.tend("run", "--dry-run").want(t, 0)
	if !strings.HasPrefix(res.stdout, "Set up tend: nothing to tidy up\n\nThanks for adding tend: ") || len(w.forge.All()) != 0 {
		t.Fatalf("dry run printed:\n%s", res.stdout)
	}
	if st := w.state(); st.Onboard != "" {
		t.Errorf("a dry run remembered %q", st.Onboard)
	}

	res = w.tend("run").want(t, 0)
	if !strings.HasPrefix(res.stdout, "tend: opened #1 Set up tend: nothing to tidy up ") {
		t.Fatalf("run said %q\n%s", res.stdout, res.stderr)
	}
	pr := onlyPR(t, w)
	w.goldenPR(pr)
	if got := w.git(w.origin, "show", pr.Branch+":tend.yml") + "\n"; got != string(config.Starter) {
		t.Errorf("tend.yml on the branch:\n%s", got)
	}
}

// onboarding: quiet opens no first PR: the run goes straight to work and says
// once what nothing uses.
func TestOnboardQuiet(t *testing.T) {
	need(t, "go")
	w := fresh(t, "go-tidy")
	w.fetch.osv.ids = nil
	w.commit("Start quietly", map[string]string{"tend.yml": "onboarding: quiet\n"})
	res := w.tend("run").want(t, 0)
	if res.stdout != "tend: up to date\n" || len(w.forge.All()) != 0 {
		t.Fatalf("run said %q, %d PRs", res.stdout, len(w.forge.All()))
	}
	if !strings.Contains(res.stderr, "onboarding is quiet, so there is no tidy-up PR; nothing in the project uses example.com/fixture/logs") {
		t.Errorf("stderr:\n%s", res.stderr)
	}
	if res := w.tend("run").want(t, 0); strings.Contains(res.stderr, "onboarding is quiet") || w.state().Onboard != "quiet" {
		t.Errorf("said it again:\n%s", res.stderr)
	}
}

// An open first PR that tend.yml no longer asks for, or that has nothing left
// to tidy, is closed with why, and tend goes on with its usual work instead
// of waiting on it.
func TestOnboardDropped(t *testing.T) {
	need(t, "go")
	tests := []struct {
		name  string
		files map[string]string
		why   string
	}{
		{"onboarding off", map[string]string{"tend.yml": "onboarding: off\n"}, "Closing: tend.yml sets onboarding: off now."},
		{"nothing left", map[string]string{
			"tend.yml": "cooldown: 3d\n",
			"go.mod":   "module example.com/gotidy\n\ngo 1.26\n\nrequire example.com/fixture/crypto v1.0.0\n",
		}, "Closing: nothing is left to tidy up."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := fresh(t, "go-tidy")
			w.fetch.osv.ids = nil
			if res := w.tend("run").want(t, 0); !strings.HasPrefix(res.stdout, "tend: opened #1 Tidy up: ") {
				t.Fatalf("run said %q", res.stdout)
			}
			w.commit("Change course", tt.files)
			if res := w.tend("run").want(t, 0); res.stdout != "tend: up to date\n" {
				t.Errorf("then: %q", res.stdout)
			}
			cs, _ := w.forge.Comments(t.Context(), 1)
			if w.forge.PR(1).State != "closed" || len(cs) != 1 || !strings.HasPrefix(cs[0].Body, tt.why) {
				t.Errorf("#1 is %s, comments %+v", w.forge.PR(1).State, cs)
			}
			if res := w.tend("run").want(t, 0); res.stdout != "tend: up to date\n" || len(w.forge.All()) != 1 {
				t.Errorf("after: %q, %d PRs", res.stdout, len(w.forge.All()))
			}
		})
	}
}

// State lost while the first PR is open, the forge still has it: tend
// updates that PR on the new base and opens no other.
func TestOnboardLostState(t *testing.T) {
	need(t, "go")
	w := fresh(t, "go-tidy")
	w.fetch.osv.ids = nil
	w.tend("run").want(t, 0)
	if err := os.RemoveAll(w.home); err != nil {
		t.Fatal(err)
	}
	w.commit("Add a readme", map[string]string{"README.md": "gotidy\n"})
	if res := w.tend("run").want(t, 0); !strings.HasPrefix(res.stdout, "tend: updated #1 Tidy up: remove 1 unused dependency ") {
		t.Fatalf("after losing state: %q", res.stdout)
	}
	if st := w.state(); st.Onboard != "due" || len(w.forge.All()) != 1 {
		t.Errorf("onboard %q, %d PRs", st.Onboard, len(w.forge.All()))
	}
}

// tend check reads the checkout, local edits and all. What it finds there
// does not settle the first PR: the run after works from the base commit.
func TestOnboardCheckLocal(t *testing.T) {
	need(t, "go")
	w := fresh(t, "go-tidy")
	w.fetch.osv.ids = nil
	w.commit("Configure", map[string]string{"tend.yml": "cooldown: 3d\n"})
	w.tend("run").want(t, 0)
	gomod := filepath.Join(w.dir, "go.mod")
	orig, err := os.ReadFile(gomod)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gomod, []byte("module example.com/gotidy\n\ngo 1.26\n\nrequire example.com/fixture/crypto v1.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.tend("check").want(t, 0)
	if err := os.WriteFile(gomod, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	if res := w.tend("run").want(t, 0); !strings.Contains(res.stdout, "waiting on PR #1") || w.forge.PR(1).State != "open" {
		t.Errorf("run: %q, #1 %s", res.stdout, w.forge.PR(1).State)
	}
}

// tend init --now writes tend.yml and runs before it is committed. With
// nothing to tidy, there is no first PR to add a second tend.yml.
func TestOnboardInitNow(t *testing.T) {
	need(t, "go")
	w := fresh(t, "go-vuln")
	w.fetch.osv.ids = nil
	res := w.tend("init", "--now").want(t, 0)
	if !strings.HasSuffix(res.stdout, "tend: up to date\n") || len(w.forge.All()) != 0 {
		t.Errorf("init --now: %q, %d PRs", res.stdout, len(w.forge.All()))
	}
}
