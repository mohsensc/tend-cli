package deliver

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

// A tidy-up tend closed for a security fix comes back; one a person closed,
// or skipped, does not.
func TestDisplaced(t *testing.T) {
	ctx := context.Background()
	f := NewFake("acme", "shop", "main")
	open := func() int {
		pr, err := f.CreatePR(ctx, core.PRInput{Branch: TidyBranch, Base: "main", Body: "<!-- tend id=\"tidy-up\" base=\"abc\" rev=\"1\" -->"})
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
