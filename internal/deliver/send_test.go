package deliver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/verify"
)

func stripeChange() *core.Change {
	return &core.Change{
		ID: "npm/stripe@16.0.1", Kind: core.Security, Severity: "high", Level: "patch",
		Deps: []core.Dep{dep("npm", "stripe", "16.0.0")}, To: "16.0.1",
		Evidence: []core.Evidence{advisory("osv", "GHSA-xxxx-yyyy-zzzz", "bad")},
	}
}

func okRun() *core.Run { return &core.Run{Pass: true, Steps: []core.Step{{Cmd: "npm test"}}} }

func sendInput(r *remote, wt *verify.Worktree) SendInput {
	return SendInput{
		BodyInput: BodyInput{
			Change:  stripeChange(),
			Patch:   &core.Patch{Files: []string{"package.json"}},
			Proof:   &core.Proof{Base: okRun(), Patched: okRun()},
			Vetting: &core.Vetting{Checks: []string{"scope: 1 file"}, Verdict: "pass"},
			Base:    r.base,
		},
		Remote: r.push,
		Cfg:    config.Deliver{Labels: []string{"tend"}, Reviewers: []string{"alice"}, MergeMethod: "squash"},
	}
}

const stripeBranch = "tend/npm-stripe-16.0.1"

func TestSendCreates(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	commit(t, wt.Dir, "package.json", `{"v":2}`, "Bump stripe")
	f := NewFake("acme", "shop", "trunk")

	pr, err := Send(bg, f, wt, sendInput(r, wt))
	if err != nil {
		t.Fatal(err)
	}
	head := must(t, wt.Dir, "rev-parse", "HEAD")
	if pr.Number != 1 || pr.Branch != stripeBranch || pr.Base != "trunk" || pr.Draft {
		t.Errorf("pr = %+v", pr)
	}
	if pr.Title != "Bump stripe to 16.0.1 (fixes GHSA-xxxx-yyyy-zzzz)" {
		t.Errorf("title = %q", pr.Title)
	}
	if id, base, rev, ok := ParseMarker(pr.Body); !ok || id != "npm/stripe@16.0.1" || base != r.base || rev != 1 {
		t.Errorf("marker = %q %q %d %v", id, base, rev, ok)
	}
	if got := r.tip(t, stripeBranch); got != head {
		t.Errorf("pushed %q, want %q", got, head)
	}
	if got := f.Labels[1]; len(got) != 1 || got[0] != "tend" {
		t.Errorf("labels = %v", got)
	}
	if got := f.Reviewers[1]; len(got) != 1 || got[0] != "alice" {
		t.Errorf("reviewers = %v", got)
	}
	if len(f.Merges) != 0 {
		t.Errorf("auto-merge requested without being configured: %v", f.Merges)
	}
}

func TestSendUsesBaseRefAndPrefix(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	commit(t, wt.Dir, "package.json", `{"v":2}`, "Bump stripe")
	f := NewFake("acme", "shop", "trunk")
	in := sendInput(r, wt)
	in.BaseRef = "develop"
	in.Cfg.TitlePrefix = "chore(deps): "
	pr, err := Send(bg, f, wt, in)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Base != "develop" || !strings.HasPrefix(pr.Title, "chore(deps): Bump stripe") {
		t.Errorf("pr = %+v", pr)
	}
	if !strings.Contains(pr.Body, "develop@"+core.Short(r.base)) {
		t.Errorf("the proof table does not name the base branch:\n%s", pr.Body)
	}
}

func TestSendUpdatesTheOpenPR(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	commit(t, wt.Dir, "package.json", `{"v":2}`, "Bump stripe")
	f := NewFake("acme", "shop", "main")
	first, err := Send(bg, f, wt, sendInput(r, wt))
	if err != nil {
		t.Fatal(err)
	}

	must(t, wt.Dir, "reset", "--hard", r.base)
	commit(t, wt.Dir, "package.json", `{"v":3}`, "Bump stripe, again")
	in := sendInput(r, wt)
	in.Notes = []string{"lockfile not regenerated: npm not found"}
	pr, err := Send(bg, f, wt, in)
	if err != nil {
		t.Fatal(err)
	}
	next := must(t, wt.Dir, "rev-parse", "HEAD")
	if pr.Number != first.Number || len(f.All()) != 1 {
		t.Fatalf("a second PR was opened: %+v", f.All())
	}
	if _, _, rev, _ := ParseMarker(f.PR(1).Body); rev != 2 {
		t.Errorf("rev = %d, want 2", rev)
	}
	if !strings.Contains(f.PR(1).Body, "lockfile not regenerated") {
		t.Error("the stored PR body is the old one")
	}
	if got := r.tip(t, stripeBranch); got != next {
		t.Errorf("remote branch = %q, want the new commit %q", got, next)
	}
}

func TestSendRerunChangesNothing(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	commit(t, wt.Dir, "package.json", `{"v":2}`, "Bump stripe")
	f := NewFake("acme", "shop", "main")
	if _, err := Send(bg, f, wt, sendInput(r, wt)); err != nil {
		t.Fatal(err)
	}
	pushed := r.tip(t, stripeBranch)
	body := f.PR(1).Body

	must(t, wt.Dir, "commit", "--amend", "--no-edit", "--date", "2030-01-01T00:00:00Z", "-m", "Bump stripe, same files")
	in := sendInput(r, wt)
	in.Proof = &core.Proof{Base: okRun(), Patched: &core.Run{Pass: true, Steps: []core.Step{{Cmd: "npm test", Took: 9e9}}}}
	pr, err := Send(bg, f, wt, in)
	if err != nil {
		t.Fatal(err)
	}
	if r.tip(t, stripeBranch) != pushed {
		t.Error("the branch was force-pushed although the files are the same")
	}
	if f.PR(1).Body != body || pr.Body != body {
		t.Error("the body changed on a re-run that changed nothing")
	}
}

func TestSendRerunWithNewBaseUpdates(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	commit(t, wt.Dir, "package.json", `{"v":2}`, "Bump stripe")
	f := NewFake("acme", "shop", "main")
	if _, err := Send(bg, f, wt, sendInput(r, wt)); err != nil {
		t.Fatal(err)
	}
	in := sendInput(r, wt)
	in.Base = "newbase0000"
	if _, err := Send(bg, f, wt, in); err != nil {
		t.Fatal(err)
	}
	if id, base, rev, _ := ParseMarker(f.PR(1).Body); id == "" || base != "newbase0000" || rev != 2 {
		t.Errorf("marker base %q rev %d", base, rev)
	}
}

func TestSendDraftToggles(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	commit(t, wt.Dir, "package.json", `{"v":2}`, "Bump stripe")
	f := NewFake("acme", "shop", "main")

	in := sendInput(r, wt)
	in.Vetting = &core.Vetting{Verdict: "draft"}
	pr, err := Send(bg, f, wt, in)
	if err != nil || !pr.Draft || !f.PR(1).Draft {
		t.Fatalf("flagged findings should open a draft: %+v, %v", pr, err)
	}
	pr, err = Send(bg, f, wt, sendInput(r, wt))
	if err != nil || pr.Draft || f.PR(1).Draft {
		t.Fatalf("a clean run should make it ready: %+v, %v", pr, err)
	}
	in = sendInput(r, wt)
	in.Cfg.Draft = true
	if _, err := Send(bg, f, wt, in); err != nil || !f.PR(1).Draft {
		t.Fatalf("deliver.draft should hold it as a draft: %v", err)
	}
}

func TestWantDraft(t *testing.T) {
	fail := &core.Run{Pass: false}
	tests := []struct {
		name string
		mod  func(*SendInput)
		want bool
	}{
		{"clean", func(*SendInput) {}, false},
		{"config", func(in *SendInput) { in.Cfg.Draft = true }, true},
		{"verdict draft", func(in *SendInput) { in.Vetting = &core.Vetting{Verdict: "draft"} }, true},
		{"baseline fails", func(in *SendInput) { in.Proof = &core.Proof{Base: fail, Patched: okRun()} }, true},
		{"patched fails", func(in *SendInput) { in.Proof = &core.Proof{Base: okRun(), Patched: fail} }, true},
		{"no baseline, patched passes", func(in *SendInput) { in.Proof = &core.Proof{Patched: okRun()} }, false},
		{"nothing to run", func(in *SendInput) { in.Proof = &core.Proof{Nothing: true} }, false},
		{"no proof at all", func(in *SendInput) { in.Proof = nil }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := SendInput{BodyInput: BodyInput{Proof: &core.Proof{Base: okRun(), Patched: okRun()}}}
			tc.mod(&in)
			if got := wantDraft(in); got != tc.want {
				t.Errorf("wantDraft = %v", got)
			}
		})
	}
}

func TestSendAfterAClosedPR(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	commit(t, wt.Dir, "package.json", `{"v":2}`, "Bump stripe")
	f := NewFake("acme", "shop", "main")
	if _, err := Send(bg, f, wt, sendInput(r, wt)); err != nil {
		t.Fatal(err)
	}
	noErr(t, f.CloseByHuman(1))

	must(t, wt.Dir, "reset", "--hard", r.base)
	commit(t, wt.Dir, "package.json", `{"v":4}`, "Bump stripe, retried")
	pr, err := Send(bg, f, wt, sendInput(r, wt))
	if err != nil {
		t.Fatal(err)
	}
	again := must(t, wt.Dir, "rev-parse", "HEAD")
	if pr.Number != 2 {
		t.Errorf("pr = %+v, want a new PR", pr)
	}
	if r.tip(t, stripeBranch) != again {
		t.Error("the stale branch was not replaced")
	}
	if _, _, rev, _ := ParseMarker(pr.Body); rev != 1 {
		t.Errorf("rev = %d for a new PR", rev)
	}
}

func TestSendPushRejected(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	commit(t, wt.Dir, "package.json", `{"v":2}`, "Bump stripe")
	f := NewFake("acme", "shop", "main")
	if _, err := Send(bg, f, wt, sendInput(r, wt)); err != nil {
		t.Fatal(err)
	}
	other := r.elsewhere(t)
	must(t, other, "checkout", "-q", stripeBranch)
	commit(t, other, "notes.txt", "mine\n", "human fix")
	must(t, other, "push", "origin", stripeBranch)

	must(t, wt.Dir, "reset", "--hard", r.base)
	commit(t, wt.Dir, "package.json", `{"v":3}`, "Bump stripe, again")
	for range 2 {
		if _, err := Send(bg, f, wt, sendInput(r, wt)); !errors.Is(err, ErrForeign) {
			t.Fatalf("err = %v, want ErrForeign", err)
		}
	}
	if _, _, rev, _ := ParseMarker(f.PR(1).Body); rev != 1 {
		t.Error("the PR was updated although its branch was not")
	}
	cs, _ := f.Comments(bg, 1)
	if len(cs) != 1 || !Foreign(cs) {
		t.Errorf("comments = %+v, want one note", cs)
	}
}

func TestSendRefusesToOverwriteWhatItDidNotPush(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	commit(t, wt.Dir, "package.json", `{"v":2}`, "Bump stripe")
	f := NewFake("acme", "shop", "main")
	if _, err := Send(bg, f, wt, sendInput(r, wt)); err != nil {
		t.Fatal(err)
	}
	pushed := must(t, wt.Dir, "rev-parse", "HEAD")
	other := r.elsewhere(t)
	must(t, other, "checkout", "-q", stripeBranch)
	theirs := commit(t, other, "notes.txt", "mine\n", "human fix")
	must(t, other, "push", "origin", stripeBranch)

	must(t, wt.Dir, "reset", "--hard", r.base)
	commit(t, wt.Dir, "package.json", `{"v":3}`, "Bump stripe, again")
	must(t, wt.Dir, "update-ref", "-d", "refs/remotes/origin/"+stripeBranch)
	in := sendInput(r, wt)
	in.Last = pushed
	if _, err := Send(bg, f, wt, in); !errors.Is(err, ErrForeign) {
		t.Fatalf("err = %v, want ErrForeign", err)
	}
	if r.tip(t, stripeBranch) != theirs {
		t.Error("the human's commit was overwritten")
	}
}

func TestSendNeedsACommit(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	f := NewFake("acme", "shop", "main")
	_, err := Send(bg, f, wt, sendInput(r, wt))
	if err == nil || !strings.Contains(err.Error(), "no commit") {
		t.Fatalf("err = %v", err)
	}
	if len(f.All()) != 0 {
		t.Error("a PR was opened for nothing")
	}
}

func TestSendDecisionPR(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	must(t, wt.Dir, "commit", "--allow-empty", "-m", "Decide: migrate off moment")
	f := NewFake("acme", "shop", "main")
	in := sendInput(r, wt)
	in.Change = &core.Change{
		ID: "npm/moment@", Kind: core.Deprecation, Deps: []core.Dep{dep("npm", "moment", "2.30.1")},
		Options: []core.Option{{Title: "dayjs", Safe: true}, {Title: "luxon"}},
	}
	in.Patch = &core.Patch{Decision: true}
	in.Proof = nil
	in.Cfg.AutoMerge = "minor"
	pr, err := Send(bg, f, wt, in)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Title != "Decide: migrate off moment" || pr.Branch != "tend/npm-moment-decide" {
		t.Errorf("pr = %+v", pr)
	}
	if !strings.Contains(pr.Body, "`tend apply 2`") || len(f.Merges) != 0 {
		t.Errorf("merges = %v\n%s", f.Merges, pr.Body)
	}
}

func TestSendAutoMerge(t *testing.T) {
	tests := []struct {
		name string
		mode string
		mod  func(*SendInput)
		want string // merge method requested, "" for none
	}{
		{"off", "none", nil, ""},
		{"patch covers a patch bump", "patch", nil, "squash"},
		{"patch does not cover minor", "patch", func(in *SendInput) { in.Change.Level = "minor" }, ""},
		{"minor covers patch", "minor", nil, "squash"},
		{"minor covers minor", "minor", func(in *SendInput) { in.Change.Level = "minor" }, "squash"},
		{"minor does not cover major", "minor", func(in *SendInput) { in.Change.Level = "major" }, ""},
		{"security covers a security minor", "security", func(in *SendInput) { in.Change.Level = "minor" }, "squash"},
		{"security does not cover a security major", "security", func(in *SendInput) { in.Change.Level = "major" }, ""},
		{"minor does not cover a security major", "minor", func(in *SendInput) { in.Change.Level = "major" }, ""},
		{"security does not cover an API change", "security", func(in *SendInput) {
			in.Change.Level, in.Change.API = "", &core.APIChange{Provider: "stripe", Method: "POST", Path: "/v1/charges"}
		}, ""},
		{"security does not cover an update", "security", func(in *SendInput) { in.Change.Kind = core.Update }, ""},
		{"merge method comes from config", "patch", func(in *SendInput) { in.Cfg.MergeMethod = "rebase" }, "rebase"},
		{"default method is squash", "patch", func(in *SendInput) { in.Cfg.MergeMethod = "" }, "squash"},
		{"vetting below pass", "patch", func(in *SendInput) { in.Vetting.Verdict = "draft" }, ""},
		{"no vetting", "patch", func(in *SendInput) { in.Vetting = nil }, ""},
		{"draft by config", "patch", func(in *SendInput) { in.Cfg.Draft = true }, ""},
		{"patched proof red", "patch", func(in *SendInput) { in.Proof.Patched = &core.Run{} }, ""},
		{"nothing was proven", "patch", func(in *SendInput) { in.Proof = &core.Proof{Nothing: true} }, ""},
		{"no proof", "patch", func(in *SendInput) { in.Proof = nil }, ""},
		{"a model wrote part of it", "security", func(in *SendInput) { in.Patch.Model = true }, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRemote(t)
			wt := r.worktree(t)
			commit(t, wt.Dir, "package.json", `{"v":2}`, "Bump stripe")
			f := NewFake("acme", "shop", "main")
			in := sendInput(r, wt)
			in.Cfg.AutoMerge = tc.mode
			if tc.mod != nil {
				tc.mod(&in)
			}
			if _, err := Send(bg, f, wt, in); err != nil {
				t.Fatal(err)
			}
			if got := f.Merges[1]; got != tc.want {
				t.Errorf("auto-merge = %q, want %q", got, tc.want)
			}
		})
	}
}

type refusesMerge struct{ *FakeForge }

func (refusesMerge) AutoMerge(context.Context, *core.PR, string) error {
	return errors.New("auto-merge: Pull request Auto merge is not allowed for this repository")
}

func TestSendAutoMergeRefusedIsNotFatal(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	commit(t, wt.Dir, "package.json", `{"v":2}`, "Bump stripe")
	f := NewFake("acme", "shop", "main")
	in := sendInput(r, wt)
	in.Cfg.AutoMerge = "patch"
	pr, err := Send(bg, refusesMerge{f}, wt, in)
	if err != nil {
		t.Fatal(err)
	}
	want := "Auto-merge was not enabled: auto-merge: Pull request Auto merge is not allowed for this repository"
	for _, body := range []string{pr.Body, f.PR(1).Body} {
		if !strings.Contains(body, "### Notes\n\n- "+want) {
			t.Errorf("body does not report the refusal:\n%s", body)
		}
	}
	if _, _, rev, _ := ParseMarker(f.PR(1).Body); rev != 1 {
		t.Errorf("rev = %d, the refusal note should not count as an update", rev)
	}
}

func TestSendForgeErrors(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	commit(t, wt.Dir, "package.json", `{"v":2}`, "Bump stripe")
	f := NewFake("acme", "shop", "main")
	f.Err = errors.New("bad credentials")
	if _, err := Send(bg, f, wt, sendInput(r, wt)); !errors.Is(err, f.Err) {
		t.Fatalf("err = %v", err)
	}
	if r.tip(t, stripeBranch) != "" {
		t.Error("pushed although the forge could not be reached")
	}
}

// State that lags the forge, say a cache saved before tend's last push, does
// not make tend take its own branch for someone else's.
func TestSendStaleStateIsNotForeign(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	commit(t, wt.Dir, "package.json", `{"v":2}`, "Bump stripe")
	f := NewFake("acme", "shop", "main")
	if _, err := Send(bg, f, wt, sendInput(r, wt)); err != nil {
		t.Fatal(err)
	}
	first := must(t, wt.Dir, "rev-parse", "HEAD")
	must(t, wt.Dir, "reset", "--hard", r.base)
	commit(t, wt.Dir, "package.json", `{"v":3}`, "Bump stripe, again")
	in := sendInput(r, wt)
	in.Last = first
	if _, err := Send(bg, f, wt, in); err != nil {
		t.Fatal(err)
	}

	must(t, wt.Dir, "reset", "--hard", r.base)
	commit(t, wt.Dir, "package.json", `{"v":4}`, "Bump stripe, a third time")
	must(t, wt.Dir, "update-ref", "-d", "refs/remotes/origin/"+stripeBranch)
	in = sendInput(r, wt)
	in.Last = first
	pr, err := Send(bg, f, wt, in)
	if err != nil {
		t.Fatalf("err = %v, want the PR updated", err)
	}
	next := must(t, wt.Dir, "rev-parse", "HEAD")
	if r.tip(t, stripeBranch) != next || pr.Head != next {
		t.Errorf("branch at %q, pr head %q, want %q", r.tip(t, stripeBranch), pr.Head, next)
	}
	if cs, _ := f.Comments(bg, 1); Foreign(cs) {
		t.Errorf("tend called its own branch foreign: %+v", cs)
	}
}

// A PR on a tend/ branch without tend's marker is not tend's to push to.
func TestSendLeavesAHandMadePR(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	theirs := commit(t, wt.Dir, "package.json", `{"v":2}`, "Hand-made bump")
	must(t, wt.Dir, "push", "origin", "HEAD:refs/heads/"+stripeBranch)
	f := NewFake("acme", "shop", "main")
	mustOpen(t, f, core.PRInput{Title: "Bump stripe", Body: "by hand", Branch: stripeBranch, Base: "main"})

	must(t, wt.Dir, "reset", "--hard", r.base)
	commit(t, wt.Dir, "package.json", `{"v":3}`, "Bump stripe")
	if _, err := Send(bg, f, wt, sendInput(r, wt)); !errors.Is(err, ErrForeign) {
		t.Fatalf("err = %v, want ErrForeign", err)
	}
	if r.tip(t, stripeBranch) != theirs {
		t.Error("the hand-made branch was overwritten")
	}
}

func TestSendSignsItsCommit(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	commit(t, wt.Dir, "package.json", `{"v":2}`, "Bump stripe")
	f := NewFake("acme", "shop", "main")
	if _, err := Send(bg, f, wt, sendInput(r, wt)); err != nil {
		t.Fatal(err)
	}
	tip := r.tip(t, stripeBranch)
	tree := must(t, r.bare, "rev-parse", tip+"^{tree}")
	if msg := must(t, r.bare, "log", "-1", "--format=%B", tip); msg != "Bump stripe\n\nTend-Tree: "+tree {
		t.Errorf("message = %q", msg)
	}
	if who := must(t, r.bare, "log", "-1", "--format=%an <%ae> %cn", tip); who != "Ada <ada@example.test> Ada" {
		t.Errorf("made by %q", who)
	}
	if err := sign(bg, wt); err != nil || must(t, wt.Dir, "rev-parse", "HEAD") != tip {
		t.Errorf("signing a signed commit changed it: %v", err)
	}
}

func TestSignKeepsATrailerBlock(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	commit(t, wt.Dir, "package.json", `{"v":2}`, "Bump stripe\n\nSigned-off-by: Ada <ada@example.test>\nTend-Tree: 0000")
	if err := sign(bg, wt); err != nil {
		t.Fatal(err)
	}
	tree := must(t, wt.Dir, "rev-parse", "HEAD^{tree}")
	want := "Bump stripe\n\nSigned-off-by: Ada <ada@example.test>\nTend-Tree: " + tree
	if msg := must(t, wt.Dir, "log", "-1", "--format=%B"); msg != want {
		t.Errorf("message = %q, want %q", msg, want)
	}
	if must(t, wt.Dir, "rev-parse", "HEAD^") != r.base {
		t.Error("the signed commit is not on the base")
	}
}

// amend rewrites the commit at the tip of the PR's branch from another
// clone, as a maintainer fixing up tend's PR would, and force-pushes it.
func amend(t *testing.T, r *remote, file, body string, args ...string) string {
	t.Helper()
	other := r.elsewhere(t)
	must(t, other, "checkout", "-q", stripeBranch)
	if file != "" {
		write(t, other, file, body)
		must(t, other, "add", "-A")
	}
	must(t, other, append([]string{"commit", "--amend"}, args...)...)
	must(t, other, "push", "-q", "-f", "origin", stripeBranch)
	return must(t, other, "rev-parse", "HEAD")
}

func TestSendLeavesAnAmendedBranch(t *testing.T) {
	for _, known := range []bool{true, false} {
		t.Run(map[bool]string{true: "state knows the last push", false: "state lost"}[known], func(t *testing.T) {
			r := newRemote(t)
			wt := r.worktree(t)
			commit(t, wt.Dir, "package.json", `{"v":2}`, "Bump stripe")
			f := NewFake("acme", "shop", "main")
			if _, err := Send(bg, f, wt, sendInput(r, wt)); err != nil {
				t.Fatal(err)
			}
			pushed := must(t, wt.Dir, "rev-parse", "HEAD")
			theirs := amend(t, r, "app.txt", "fixed by hand\n", "--no-edit")

			must(t, wt.Dir, "reset", "--hard", r.base)
			commit(t, wt.Dir, "package.json", `{"v":3}`, "Bump stripe, again")
			in := sendInput(r, wt)
			if known {
				in.Last = pushed
			}
			if _, err := Send(bg, f, wt, in); !errors.Is(err, ErrForeign) {
				t.Fatalf("err = %v, want ErrForeign", err)
			}
			if r.tip(t, stripeBranch) != theirs {
				t.Error("the maintainer's amend was overwritten")
			}
		})
	}
}

func TestSendTakesARewordedCommitAsItsOwn(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	commit(t, wt.Dir, "package.json", `{"v":2}`, "Bump stripe")
	f := NewFake("acme", "shop", "main")
	if _, err := Send(bg, f, wt, sendInput(r, wt)); err != nil {
		t.Fatal(err)
	}
	pushed := must(t, wt.Dir, "rev-parse", "HEAD")
	msg := must(t, wt.Dir, "log", "-1", "--format=%B")
	amend(t, r, "", "", "-m", strings.Replace(msg, "Bump stripe", "Bump stripe (reworded)", 1))

	must(t, wt.Dir, "reset", "--hard", r.base)
	commit(t, wt.Dir, "package.json", `{"v":3}`, "Bump stripe, again")
	in := sendInput(r, wt)
	in.Last = pushed
	if _, err := Send(bg, f, wt, in); err != nil {
		t.Fatalf("err = %v, want the PR updated", err)
	}
	if r.tip(t, stripeBranch) != must(t, wt.Dir, "rev-parse", "HEAD") {
		t.Error("the branch was not updated")
	}
}

// A commit tend pushed before it signed its work is still its own, unless
// state knows tend pushed something else.
func TestSendUnsignedCommit(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(map[bool]string{false: "state lost", true: "state knows another push"}[known], func(t *testing.T) {
			r := newRemote(t)
			wt := r.worktree(t)
			commit(t, wt.Dir, "package.json", `{"v":2}`, "Bump stripe")
			f := NewFake("acme", "shop", "main")
			if _, err := Send(bg, f, wt, sendInput(r, wt)); err != nil {
				t.Fatal(err)
			}
			pushed := must(t, wt.Dir, "rev-parse", "HEAD")
			old := amend(t, r, "", "", "-m", "Bump stripe")

			must(t, wt.Dir, "reset", "--hard", r.base)
			commit(t, wt.Dir, "package.json", `{"v":3}`, "Bump stripe, again")
			in := sendInput(r, wt)
			if known {
				in.Last = pushed
			}
			_, err := Send(bg, f, wt, in)
			if known {
				if !errors.Is(err, ErrForeign) || r.tip(t, stripeBranch) != old {
					t.Fatalf("err = %v, want ErrForeign and the branch left alone", err)
				}
				return
			}
			if err != nil || r.tip(t, stripeBranch) != must(t, wt.Dir, "rev-parse", "HEAD") {
				t.Fatalf("err = %v, want the branch updated", err)
			}
		})
	}
}

// A remote-tracking ref left from a push long ago does not stop tend from
// replacing the branch of a closed PR.
func TestSendAfterAClosedPRWithAStaleTrackingRef(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	commit(t, wt.Dir, "package.json", `{"v":2}`, "Bump stripe")
	f := NewFake("acme", "shop", "main")
	if _, err := Send(bg, f, wt, sendInput(r, wt)); err != nil {
		t.Fatal(err)
	}
	amend(t, r, "package.json", `{"v":9}`, "-m", "pushed by another run")
	noErr(t, f.ClosePR(bg, 1))

	must(t, wt.Dir, "reset", "--hard", r.base)
	commit(t, wt.Dir, "package.json", `{"v":4}`, "Bump stripe, retried")
	pr, err := Send(bg, f, wt, sendInput(r, wt))
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 2 || r.tip(t, stripeBranch) != must(t, wt.Dir, "rev-parse", "HEAD") {
		t.Errorf("pr #%d, branch at %s", pr.Number, r.tip(t, stripeBranch))
	}
}

// A fetch that fails while tend checks the branch stops the run; it does not
// push over whatever the branch holds.
func TestSendStopsWhenItCannotReadTheBranch(t *testing.T) {
	r := newRemote(t)
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	arm := filepath.Join(t.TempDir(), "arm")
	script := filepath.Join(t.TempDir(), "upload-pack")
	write(t, filepath.Dir(script), filepath.Base(script), "#!/bin/sh\nif [ -e '"+arm+"' ]; then rm -f '"+arm+"'; echo flaky >&2; exit 1; fi\nexec git-upload-pack \"$@\"\n")
	noErr(t, os.Chmod(script, 0o755))
	must(t, r.clone, "config", "remote.origin.uploadpack", script)
	r.push = r.remote(t, "origin")

	wt := r.worktree(t)
	commit(t, wt.Dir, "package.json", `{"v":2}`, "Bump stripe")
	f := NewFake("acme", "shop", "main")
	if _, err := Send(bg, f, wt, sendInput(r, wt)); err != nil {
		t.Fatal(err)
	}
	other := r.elsewhere(t)
	must(t, other, "checkout", "-q", stripeBranch)
	theirs := commit(t, other, "notes.txt", "mine\n", "human fix")
	must(t, other, "push", "-q", "origin", stripeBranch)

	must(t, wt.Dir, "reset", "--hard", r.base)
	commit(t, wt.Dir, "package.json", `{"v":3}`, "Bump stripe, again")
	must(t, wt.Dir, "update-ref", "-d", "refs/remotes/origin/"+stripeBranch)
	write(t, filepath.Dir(arm), filepath.Base(arm), "")
	if _, err := Send(bg, f, wt, sendInput(r, wt)); err == nil || !strings.Contains(err.Error(), "flaky") {
		t.Fatalf("err = %v, want the fetch error", err)
	}
	if r.tip(t, stripeBranch) != theirs {
		t.Error("the human's commit was overwritten")
	}
}

func TestSendRetargetsANewBase(t *testing.T) {
	r := newRemote(t)
	wt := r.worktree(t)
	commit(t, wt.Dir, "package.json", `{"v":2}`, "Bump stripe")
	f := NewFake("acme", "shop", "main")
	if _, err := Send(bg, f, wt, sendInput(r, wt)); err != nil {
		t.Fatal(err)
	}
	in := sendInput(r, wt)
	in.BaseRef = "develop"
	pr, err := Send(bg, f, wt, in)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Base != "develop" || f.PR(1).Base != "develop" {
		t.Errorf("base = %q, stored %q", pr.Base, f.PR(1).Base)
	}
	if _, err := Send(bg, f, wt, in); err != nil {
		t.Fatal(err)
	}
	if _, _, rev, _ := ParseMarker(f.PR(1).Body); rev != 2 {
		t.Errorf("rev = %d, want 2: a re-run on the new base changes nothing", rev)
	}
}
