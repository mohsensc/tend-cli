package deliver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
)

func TestFakeForge(t *testing.T) {
	ctx := context.Background()
	f := NewFake("acme", "shop", "main")
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	f.Now = func() time.Time { return at }

	a, err := f.CreatePR(ctx, core.PRInput{Title: "Bump a", Branch: "tend/npm-a-2.0.0", Base: "main", Labels: []string{"tend"}, Body: `<!-- tend id="npm/a@2.0.0" base="abc" rev="1" -->`})
	if err != nil || a.Number != 1 || a.State != "open" || a.URL != "https://github.com/acme/shop/pull/1" {
		t.Fatalf("create = %+v, %v", a, err)
	}
	if _, err := f.CreatePR(ctx, core.PRInput{Branch: "tend/npm-a-2.0.0"}); err == nil {
		t.Fatal("second open PR on the same branch was allowed")
	}
	b, _ := f.CreatePR(ctx, core.PRInput{Title: "Other", Branch: "feature/x"})
	if b.Number != 2 {
		t.Fatalf("second PR is #%d", b.Number)
	}

	open, _ := f.OpenPRs(ctx, "tend/")
	if len(open) != 1 || open[0].Number != 1 {
		t.Fatalf("open tend PRs = %+v", open)
	}

	if err := f.UpdatePR(ctx, 1, core.PRInput{Title: "Bump a to 2.0.1", Body: "rev 2", Draft: true}); err != nil {
		t.Fatal(err)
	}
	if pr := f.PR(1); pr.Title != "Bump a to 2.0.1" || pr.Body != "rev 2" || !pr.Draft {
		t.Fatalf("updated = %+v", pr)
	}

	if err := f.Say(1, "maintainer", "OWNER", "tend skip"); err != nil {
		t.Fatal(err)
	}
	if err := f.Comment(ctx, 1, "Closing."); err != nil {
		t.Fatal(err)
	}
	cs, _ := f.Comments(ctx, 1)
	if len(cs) != 2 || cs[0].Role != "OWNER" || cs[0].Body != "tend skip" || !cs[0].At.Equal(at) || cs[1].Role != "NONE" || cs[0].ID == cs[1].ID {
		t.Fatalf("comments = %+v", cs)
	}

	if err := f.CloseByHuman(1); err != nil {
		t.Fatal(err)
	}
	if err := f.Merge(1); err == nil {
		t.Fatal("merged a closed PR")
	}
	c, err := f.CreatePR(ctx, core.PRInput{Branch: "tend/npm-a-2.0.0"})
	if err != nil || c.Number != 3 {
		t.Fatalf("reopen on a closed branch = %+v, %v", c, err)
	}
	if pr, _ := f.FindPR(ctx, "tend/npm-a-2.0.0"); pr == nil || pr.Number != 3 {
		t.Fatalf("FindPR = %+v, want the newest", pr)
	}
	if pr, _ := f.FindPR(ctx, "tend/none"); pr != nil {
		t.Fatalf("FindPR(none) = %+v", pr)
	}

	if err := f.AutoMerge(ctx, c, "squash"); err != nil || f.Merges[3] != "squash" {
		t.Fatalf("automerge = %v, %v", f.Merges, err)
	}
	if err := f.Merge(3); err != nil || f.PR(3).State != "merged" {
		t.Fatalf("merge = %v, %+v", err, f.PR(3))
	}
	if err := f.ClosePR(ctx, 9); err == nil {
		t.Fatal("closed a PR that does not exist")
	}

	boom := errors.New("bad credentials")
	f.Err = boom
	if _, err := f.OpenPRs(ctx, "tend/"); !errors.Is(err, boom) {
		t.Fatalf("Err not returned: %v", err)
	}
}
