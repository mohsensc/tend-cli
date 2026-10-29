package deliver

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

// A first PR tend closed for a security fix comes back; one a person closed,
// or skipped, does not.
func TestDisplaced(t *testing.T) {
	ctx := context.Background()
	f := NewFake("acme", "shop", "main")
	open := func() int {
		pr, err := f.CreatePR(ctx, core.PRInput{Branch: FirstBranch, Base: "main", Body: "<!-- tend id=\"tidy-up\" base=\"abc\" rev=\"1\" -->"})
		if err != nil {
			t.Fatal(err)
		}
		return pr.Number
	}
	byTend := open()
	if err := Close(ctx, f, byTend, "Closing to make room for a security fix."); err != nil {
		t.Fatal(err)
	}
	byPerson := open()
	if err := f.CloseByHuman(byPerson); err != nil {
		t.Fatal(err)
	}
	skipped := open()
	if err := f.Say(skipped, "ada", "OWNER", "tend skip"); err != nil {
		t.Fatal(err)
	}
	if err := Close(ctx, f, skipped, "Skipped."); err != nil {
		t.Fatal(err)
	}
	thanked := open()
	if err := Close(ctx, f, thanked, "Closing to make room for a security fix."); err != nil {
		t.Fatal(err)
	}
	if err := f.Say(thanked, "ada", "OWNER", "ok, the fix first"); err != nil {
		t.Fatal(err)
	}
	still := open()
	for n, want := range map[int]bool{byTend: true, byPerson: false, skipped: false, thanked: true, still: false} {
		got, err := Displaced(ctx, f, f.PR(n))
		if err != nil || got != want {
			t.Errorf("#%d: Displaced = %v, %v; want %v", n, got, err, want)
		}
	}
	if got, err := Displaced(ctx, f, nil); got || err != nil {
		t.Errorf("no PR: %v, %v", got, err)
	}
}

func TestFakeHadPR(t *testing.T) {
	ctx := context.Background()
	f := NewFake("acme", "shop", "main")
	if had, _ := f.HadPR(ctx, "tend/"); had {
		t.Error("an empty forge had a PR")
	}
	if _, err := f.CreatePR(ctx, core.PRInput{Branch: "tend/by-hand", Base: "main", Body: "no marker"}); err != nil {
		t.Fatal(err)
	}
	if had, _ := f.HadPR(ctx, "tend/"); had {
		t.Error("a PR without tend's marker counted")
	}
	pr, err := f.CreatePR(ctx, core.PRInput{Branch: "tend/npm-a-1.0.0", Base: "main", Body: "<!-- tend id=\"npm/a@1.0.0\" base=\"abc\" rev=\"1\" -->"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Merge(pr.Number); err != nil {
		t.Fatal(err)
	}
	if had, _ := f.HadPR(ctx, "tend/"); !had {
		t.Error("a merged PR of tend's did not count")
	}
}

// HadPR pages through PRs in every state, newest first, and stops at the
// first of tend's: not someone's own branch, not a fork's.
func TestHadPR(t *testing.T) {
	q := func(page int) string {
		return fmt.Sprintf("GET %s?state=all&sort=created&direction=desc&per_page=100&page=%d", pulls, page)
	}
	marker := `<!-- tend id=\"npm/a@1.0.0\" base=\"abc\" rev=\"1\" -->`
	var full strings.Builder
	full.WriteString("[")
	for i := range 100 {
		if i > 0 {
			full.WriteString(",")
		}
		fmt.Fprintf(&full, `{"number": %d, "state": "closed", "head": {"ref": "feature/%d", "repo": {"full_name": "acme/shop"}}}`, 300-i, i)
	}
	full.WriteString("]")
	fork := `[{"number": 9, "state": "closed", "body": "` + marker + `", "head": {"ref": "tend/npm-a-1.0.0", "repo": {"full_name": "someone/shop"}}}]`
	ours := `[{"number": 8, "state": "closed", "merged_at": "2026-01-01T00:00:00Z", "body": "` + marker + `", "head": {"ref": "tend/npm-a-1.0.0", "repo": {"full_name": "acme/shop"}}}]`

	a := newAPI(t, []step{{req: q(1), resp: full.String()}, {req: q(2), resp: ours}})
	if had, err := a.g.HadPR(context.Background(), "tend/"); err != nil || !had {
		t.Errorf("HadPR = %v, %v", had, err)
	}
	a = newAPI(t, []step{{req: q(1), resp: fork}})
	if had, err := a.g.HadPR(context.Background(), "tend/"); err != nil || had {
		t.Errorf("a fork's PR: HadPR = %v, %v", had, err)
	}
}

func TestFirstTitleActionUpgrade(t *testing.T) {
	checkout := core.Change{Kind: core.Breaking, Level: "major", To: "v7", Deps: []core.Dep{{Ecosystem: core.Actions, Name: "actions/checkout", Version: "v1", Direct: true, Manifest: core.Manifest{Path: ".github/workflows/ci.yml"}}}}
	if got := firstTitle(&core.Batch{Stage: 2, Changes: []core.Change{checkout}}); got != "Catch up: 1 major Action upgrade" {
		t.Errorf("title = %q", got)
	}
}

// Security fixes an earlier stage could not make are listed apart from the
// stage's own changes, those that failed alike on one line, with the
// lockfile a transitive one sits in.
func TestStagesCarried(t *testing.T) {
	m := core.Manifest{Ecosystem: core.Npm, Path: "package.json", Lock: "yarn.lock"}
	b := &core.Batch{Stage: 1}
	for i := range 40 {
		name := fmt.Sprintf("pkg%02d", i)
		ch := core.Change{Kind: core.Security, To: "1.0.1", Deps: []core.Dep{{Ecosystem: core.Npm, Name: name, Version: "1.0.0", Manifest: m}}}
		b.Next = append(b.Next, core.Later{Change: ch, Stage: 2, Why: "making it next to the rest fails: yarn v1 cannot update " + name + " alone"})
	}
	mongoose := core.Dep{Ecosystem: core.Npm, Name: "mongoose", Version: "5.13.20", Direct: true, Manifest: m}
	b.Next = append(b.Next,
		core.Later{Change: core.Change{Kind: core.Breaking, Level: "major", To: "6.13.10", Deps: []core.Dep{mongoose}}, Stage: 3},
		core.Later{Change: core.Change{Kind: core.Breaking, Level: "major", To: "9.11.4", Deps: []core.Dep{mongoose}}, Stage: 3},
		core.Later{Change: core.Change{Kind: core.Deprecation, To: "22", Deps: []core.Dep{{Ecosystem: core.Toolchain, Name: "nodejs", Version: "16", Direct: true, Manifest: core.Manifest{Path: ".nvmrc"}}}}, Stage: 2},
	)
	got := stages(b)
	carried := "   - Carried over from an earlier stage, where they could not be made or proven, and tried again here:\n     - Making it next to the rest fails: yarn v1 cannot update each alone (40 packages): `pkg00` 1.0.0 → 1.0.1"
	if !strings.Contains(got, carried) || !strings.Contains(got, "and 32 more.") {
		t.Errorf("stages:\n%s", got)
	}
	if strings.Index(got, "Carried over") > strings.Index(got, "`nodejs`") {
		t.Errorf("carried fixes come after the stage's own:\n%s", got)
	}
	if strings.Count(got, "`mongoose`") != 1 || !strings.Contains(got, "; 9.11.4 after it: a new major release") {
		t.Errorf("mongoose rows:\n%s", got)
	}
	one := later(core.Later{Change: b.Next[0].Change, Why: "x"})
	if !strings.Contains(one, "(transitive, in `yarn.lock`)") {
		t.Errorf("later = %q", one)
	}
}

func TestWelcomeTriedAll(t *testing.T) {
	ch := core.Change{Kind: core.Security, To: "6.0.1", Deps: []core.Dep{{Ecosystem: core.Pip, Name: "pyyaml", Version: "5.3", Direct: true}}}
	b := &core.Batch{Stage: 1, Next: []core.Later{
		{Change: ch, Stage: 3, Why: "`poetry lock` fails (exit 1)"},
		{Change: core.Change{Kind: core.Breaking, Level: "major", To: "8.0.0", Deps: []core.Dep{{Ecosystem: core.Pip, Name: "pytest", Version: "6.0", Direct: true}}}, Stage: 3},
	}}
	got := welcome(b)
	if strings.Contains(got, "nothing to fix") || !strings.Contains(got, "found 1 security fix, but none could be made and proven yet") {
		t.Errorf("welcome = %s", got)
	}
}

func TestWhere(t *testing.T) {
	dep := func(p string) core.Dep {
		return core.Dep{Ecosystem: core.Actions, Name: "actions/checkout", Manifest: core.Manifest{Path: p}}
	}
	ch := &core.Change{Deps: []core.Dep{dep(".github/workflows/a.yml"), dep(".github/workflows/b.yml"), dep(".github/workflows/a.yml")}}
	if got := where(ch); got != " in `.github/workflows/a.yml` and `.github/workflows/b.yml`" {
		t.Errorf("where = %q", got)
	}
}

// A security bump that raised the go line says so, and the later stage that
// would have raised it is gone.
func TestBatchGoLineMoved(t *testing.T) {
	crypto := core.Change{Kind: core.Security, To: "v0.56.0", Deps: []core.Dep{{Ecosystem: core.Go, Name: "golang.org/x/crypto", Version: "v0.31.0", Direct: true, Manifest: core.Manifest{Path: "go.mod"}}}}
	goline := core.Change{Kind: core.Deprecation, To: "1.26", Deps: []core.Dep{{Ecosystem: core.Toolchain, Name: "go", Version: "1.22", Min: true, Manifest: core.Manifest{Path: "go.mod"}}}}
	teams := core.Change{Kind: core.Deprecation, API: &core.APIChange{Provider: "github", Method: "GET", Path: "/teams/{team_id}"}}
	b := &core.Batch{Stage: 1, Changes: []core.Change{crypto}, Next: []core.Later{{Change: goline, Stage: 2}, {Change: teams, Stage: 2}}}
	diff := "diff --git a/go.mod b/go.mod\n--- a/go.mod\n+++ b/go.mod\n@@ -1,3 +1,3 @@\n module x\n-go 1.22\n+go 1.26.0\n"
	got := Body(BodyInput{Change: &core.Change{ID: core.FirstID, Kind: core.First, Batch: b}, Patch: &core.Patch{Diff: diff}, Base: "abc"})
	if !strings.Contains(got, "The go line in `go.mod` moved from 1.22 to 1.26.0") {
		t.Errorf("no go line note:\n%s", got)
	}
	if strings.Contains(got, "`go` 1.22 → 1.26") || !strings.Contains(got, "/teams/{team_id}") {
		t.Errorf("next stages:\n%s", got)
	}
}
