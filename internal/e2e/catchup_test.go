package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/deliver"
	"github.com/mohsensc/tend-cli/internal/model"
	"github.com/mohsensc/tend-cli/internal/plan"
)

// replies is a model that answers with each of says in turn, a file under
// replies/ when one is named so, then finds nothing to say when vetting asks.
func replies(t *testing.T, says ...string) *model.Script {
	t.Helper()
	s := &model.Script{}
	for _, say := range says {
		if b, err := os.ReadFile(filepath.Join("testdata", "replies", say)); err == nil {
			say = string(b)
		}
		s.Replies = append(s.Replies, say)
	}
	for range 12 {
		s.Replies = append(s.Replies, "NONE")
	}
	return s
}

// An app nobody has touched since the Node 4 days, with no tests: a runtime
// pin and minimum long past their end of life, a web framework two majors
// behind whose majors each break the app, the deprecated request with no
// single replacement, a vulnerable parser and a dependency nothing uses.
// tend catches it up in four PRs, a stage each and never two open: the
// first adds a smoke test and tests of how the app behaves now, and proves
// the fix and the removal against them; the second moves the runtime; the
// third climbs the framework a major at a time when the jump fails; the
// fourth takes the catalog's recommended replacement for request. Then it
// is up to date and goes on one change at a time.
func TestCatchUpLegacy(t *testing.T) {
	need(t, "node", "npm")
	w := fresh(t, "node-legacy")
	w.fetch.osv.ids = []string{"GHSA-3m7x-8p2r-6fjw"}

	w.model = replies(t, "legacy-baseline.md")
	res := w.tend("run").want(t, 0)
	if !strings.HasPrefix(res.stdout, "tend: opened #1 Catch up: a test baseline, 1 security fix, 1 unused dependency ") {
		t.Fatalf("stage 1: %q\n%s", res.stdout, res.stderr)
	}
	pr := *w.forge.PR(1)
	w.golden("1-body.md", pr.Body)
	w.golden("1-diff.patch", w.diff(pr.Branch)+"\n")
	for _, want := range []string{
		"### Baseline\n\nThis project's tests are too thin to prove the catch-up by: there is no test command.",
		"`test/tend/smoke.js`: loads each of the 3 modules that load on the base commit",
		"### Next stages",
		"2. Runtimes and build tooling",
		"3. Major upgrades\n   - `@fixture/web` 1.0.0 → 3.0.0 in `package.json`: a new major release.",
		"4. Migrations and replacements\n   - `request` 2.88.2 in `package.json`: deprecated, with no single replacement.",
		"tend takes the first of `undici`, `node-fetch` and `axios` its proof passes, in that order.",
	} {
		if !strings.Contains(pr.Body, want) {
			t.Errorf("stage 1 lacks %q:\n%s", want, pr.Body)
		}
	}
	if yml := w.git(w.origin, "show", pr.Branch+":tend.yml"); !strings.Contains(yml, "node --test test/tend/characterization.js test/tend/smoke.js") {
		t.Errorf("tend.yml on the branch:\n%s", yml)
	}
	w.merge(1)
	w.pull()

	w.model = replies(t)
	res = w.tend("run").want(t, 0)
	if !strings.HasPrefix(res.stdout, "tend: opened #2 Catch up: 1 runtime minimum, 1 end-of-life runtime ") {
		t.Fatalf("stage 2: %q\n%s", res.stdout, res.stderr)
	}
	pr = *w.forge.PR(2)
	if pr.Branch != deliver.StageBranch(plan.Runtimes) || !strings.Contains(pr.Body, "node --test test/tend/characterization.js test/tend/smoke.js") {
		t.Errorf("stage 2 = %+v", pr)
	}
	w.golden("2-body.md", pr.Body)
	w.merge(2)
	w.pull()

	w.model = replies(t, "NOCHANGE", "legacy-web-2.diff", "legacy-web-3.diff")
	res = w.tend("run").want(t, 0)
	if !strings.HasPrefix(res.stdout, "tend: opened #3 Catch up: 1 major upgrade ") {
		t.Fatalf("stage 3: %q\n%s", res.stdout, res.stderr)
	}
	for _, want := range []string{"climbing @fixture/web to 2.0.0", "climbing @fixture/web to 3.0.0", "climbed @fixture/web to 3.0.0"} {
		if !strings.Contains(res.stderr, want) {
			t.Errorf("stage 3 does not log %q:\n%s", want, res.stderr)
		}
	}
	pr = *w.forge.PR(3)
	if want := "- `@fixture/web` 1.0.0 → 3.0.0, by way of 2.0.0, each proven: a new major release"; !strings.Contains(pr.Body, want) {
		t.Errorf("stage 3 lacks %q:\n%s", want, pr.Body)
	}
	w.golden("3-body.md", pr.Body)
	w.golden("3-diff.patch", w.diff(pr.Branch)+"\n")
	w.merge(3)
	w.pull()

	w.model = replies(t, "legacy-undici.diff")
	res = w.tend("run").want(t, 0)
	if !strings.HasPrefix(res.stdout, "tend: opened #4 Catch up: 1 replaced library ") {
		t.Fatalf("stage 4: %q\n%s", res.stdout, res.stderr)
	}
	pr = *w.forge.PR(4)
	if want := "- `request` 2.88.2 → `undici` 7.16.0, picked from 4 options: the catalog's recommended replacement"; !strings.Contains(pr.Body, want) {
		t.Errorf("stage 4 lacks %q:\n%s", want, pr.Body)
	}
	w.golden("4-body.md", pr.Body)
	w.golden("4-diff.patch", w.diff(pr.Branch)+"\n")
	w.merge(4)
	w.pull()

	w.model = replies(t)
	if res := w.tend("run").want(t, 0); res.stdout != "tend: up to date\n" {
		t.Errorf("after the catch-up: %q\n%s", res.stdout, res.stderr)
	}
	if st := w.state(); st.Stage != plan.Last || st.Onboard != "merged" {
		t.Errorf("state: stage %d, onboard %q", st.Stage, st.Onboard)
	}
	if n := len(w.forge.All()); n != 4 {
		t.Errorf("%d PRs, want 4", n)
	}
	pkg := w.git(w.origin, "show", "main:package.json")
	if lock := w.git(w.origin, "show", "main:package-lock.json"); !strings.Contains(lock, "parser-1.0.1.tgz") {
		t.Errorf("the lockfile on main does not lock the fixed parser:\n%s", lock)
	}
	for _, want := range []string{`"@fixture/web": "^3.0.0"`, `"undici": "^7.16.0"`} {
		if !strings.Contains(pkg, want) {
			t.Errorf("package.json on main lacks %s:\n%s", want, pkg)
		}
	}
	for _, gone := range []string{"request", "@fixture/colors", `">=4"`} {
		if strings.Contains(pkg, gone) {
			t.Errorf("package.json on main still has %s:\n%s", gone, pkg)
		}
	}
	if v := w.git(w.origin, "show", "main:.nvmrc"); v == "4.9.1" {
		t.Errorf(".nvmrc on main: %s", v)
	}
	for _, is := range w.forge.Issues() {
		if is.Open {
			t.Errorf("a tracking issue is open:\n%s", is.Body)
		}
	}
}
