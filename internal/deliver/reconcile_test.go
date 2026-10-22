package deliver

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/state"
)

var clock = time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)

func noErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mustOpen(t *testing.T, f *FakeForge, in core.PRInput) {
	t.Helper()
	_, err := f.CreatePR(bg, in)
	noErr(t, err)
}

// tendPR opens a PR the way Send would, with a marker for change id.
func tendPR(t *testing.T, f *FakeForge, branch, id string) *core.PR {
	t.Helper()
	body := Body(BodyInput{Change: &core.Change{ID: id, Kind: core.Update}, Base: "abc", Rev: 1})
	pr, err := f.CreatePR(bg, core.PRInput{Title: "Bump", Body: body, Branch: branch, Base: "main"})
	if err != nil {
		t.Fatal(err)
	}
	return pr
}

func TestReconcile(t *testing.T) {
	now = func() time.Time { return clock }
	t.Cleanup(func() { now = time.Now })
	earlier := clock.Add(-24 * time.Hour)

	tests := []struct {
		name  string
		setup func(f *FakeForge) *state.State
		check func(t *testing.T, f *FakeForge, st *state.State, got *core.PR)
	}{
		{
			name: "nothing open, nothing remembered",
			setup: func(f *FakeForge) *state.State {
				return &state.State{}
			},
			check: func(t *testing.T, f *FakeForge, st *state.State, got *core.PR) {
				if got != nil || st.Open != nil {
					t.Errorf("got %+v, open %+v", got, st.Open)
				}
			},
		},
		{
			name: "someone else's PRs are ignored",
			setup: func(f *FakeForge) *state.State {
				mustOpen(t, f, core.PRInput{Branch: "feature/x"})
				return &state.State{}
			},
			check: func(t *testing.T, f *FakeForge, st *state.State, got *core.PR) {
				if got != nil || st.Open != nil {
					t.Errorf("got %+v, open %+v", got, st.Open)
				}
			},
		},
		{
			name: "one open PR, state empty: adopted from its marker",
			setup: func(f *FakeForge) *state.State {
				tendPR(t, f, "tend/npm-a-2.0.0", "npm/a@2.0.0")
				return &state.State{}
			},
			check: func(t *testing.T, f *FakeForge, st *state.State, got *core.PR) {
				if got == nil || got.Number != 1 {
					t.Fatalf("got %+v", got)
				}
				want := state.OpenPR{ID: "npm/a@2.0.0", PR: 1, Branch: "tend/npm-a-2.0.0", Base: "main", At: clock}
				if st.Open == nil || !reflect.DeepEqual(*st.Open, want) {
					t.Errorf("open = %+v, want %+v", st.Open, want)
				}
			},
		},
		{
			name: "one open PR, state agrees: left alone",
			setup: func(f *FakeForge) *state.State {
				tendPR(t, f, "tend/npm-a-2.0.0", "npm/a@2.0.0")
				return &state.State{Open: &state.OpenPR{ID: "npm/a@2.0.0", PR: 1, Branch: "tend/npm-a-2.0.0", Base: "main", Commit: "c0ffee", At: earlier}}
			},
			check: func(t *testing.T, f *FakeForge, st *state.State, got *core.PR) {
				if got == nil || got.Number != 1 {
					t.Fatalf("got %+v", got)
				}
				if st.Open.Commit != "c0ffee" || !st.Open.At.Equal(earlier) {
					t.Errorf("open was rewritten: %+v", st.Open)
				}
			},
		},
		{
			name: "two open: the newest stays, the other is closed with a comment",
			setup: func(f *FakeForge) *state.State {
				tendPR(t, f, "tend/npm-a-2.0.0", "npm/a@2.0.0")
				tendPR(t, f, "tend/npm-b-3.0.0", "npm/b@3.0.0")
				return &state.State{Open: &state.OpenPR{ID: "npm/a@2.0.0", PR: 1, Branch: "tend/npm-a-2.0.0", Base: "main", At: earlier}}
			},
			check: func(t *testing.T, f *FakeForge, st *state.State, got *core.PR) {
				if got == nil || got.Number != 2 {
					t.Fatalf("got %+v", got)
				}
				if f.PR(1).State != "closed" || f.PR(2).State != "open" {
					t.Errorf("states: %s %s", f.PR(1).State, f.PR(2).State)
				}
				cs, _ := f.Comments(bg, 1)
				if len(cs) != 1 || !strings.HasPrefix(cs[0].Body, "Closing: tend keeps one pull request open at a time, and #2 is the current one.") {
					t.Errorf("comments = %+v", cs)
				}
				if declined, err := Declined(bg, f, f.PR(1)); err != nil || declined {
					t.Errorf("tend's own closing reads as a maintainer's: %v, %v", declined, err)
				}
				if st.Open == nil || st.Open.PR != 2 || st.Open.ID != "npm/b@3.0.0" {
					t.Errorf("open = %+v", st.Open)
				}
				if len(st.Parked) != 0 {
					t.Errorf("a PR tend closed itself was parked as rejected: %+v", st.Parked)
				}
			},
		},
		{
			name: "three open",
			setup: func(f *FakeForge) *state.State {
				tendPR(t, f, "tend/a", "npm/a@1")
				tendPR(t, f, "tend/b", "npm/b@1")
				tendPR(t, f, "tend/c", "npm/c@1")
				return &state.State{}
			},
			check: func(t *testing.T, f *FakeForge, st *state.State, got *core.PR) {
				if got.Number != 3 || f.PR(1).State != "closed" || f.PR(2).State != "closed" {
					t.Errorf("got %+v, states %s %s", got, f.PR(1).State, f.PR(2).State)
				}
			},
		},
		{
			name: "merged since the last run",
			setup: func(f *FakeForge) *state.State {
				tendPR(t, f, "tend/npm-a-2.0.0", "npm/a@2.0.0")
				noErr(t, f.Merge(1))
				return &state.State{
					Seen: map[string]state.Seen{"npm/a@2.0.0": {Verdict: "queued", First: earlier}},
					Open: &state.OpenPR{ID: "npm/a@2.0.0", PR: 1, Branch: "tend/npm-a-2.0.0", Base: "main"},
				}
			},
			check: func(t *testing.T, f *FakeForge, st *state.State, got *core.PR) {
				if got != nil || st.Open != nil {
					t.Errorf("got %+v, open %+v", got, st.Open)
				}
				if s := st.Seen["npm/a@2.0.0"]; s.Verdict != "delivered" || !s.First.Equal(earlier) {
					t.Errorf("seen = %+v", s)
				}
				if len(st.Parked) != 0 {
					t.Errorf("parked = %+v", st.Parked)
				}
			},
		},
		{
			name: "merged, then a new PR is already open",
			setup: func(f *FakeForge) *state.State {
				tendPR(t, f, "tend/npm-a-2.0.0", "npm/a@2.0.0")
				noErr(t, f.Merge(1))
				tendPR(t, f, "tend/npm-b-3.0.0", "npm/b@3.0.0")
				return &state.State{Open: &state.OpenPR{ID: "npm/a@2.0.0", PR: 1, Branch: "tend/npm-a-2.0.0", Base: "main"}}
			},
			check: func(t *testing.T, f *FakeForge, st *state.State, got *core.PR) {
				if got == nil || got.Number != 2 || st.Open.ID != "npm/b@3.0.0" {
					t.Errorf("got %+v, open %+v", got, st.Open)
				}
				if st.Seen["npm/a@2.0.0"].Verdict != "delivered" {
					t.Errorf("seen = %+v", st.Seen)
				}
			},
		},
		{
			name: "closed by a human since the last run",
			setup: func(f *FakeForge) *state.State {
				tendPR(t, f, "tend/npm-a-2.0.0", "npm/a@2.0.0")
				noErr(t, f.CloseByHuman(1))
				return &state.State{Open: &state.OpenPR{ID: "npm/a@2.0.0", PR: 1, Branch: "tend/npm-a-2.0.0", Base: "main"}}
			},
			check: func(t *testing.T, f *FakeForge, st *state.State, got *core.PR) {
				if got != nil || st.Open != nil {
					t.Errorf("got %+v, open %+v", got, st.Open)
				}
				want := state.Parked{PR: 1, Reason: "rejected", To: "2.0.0", At: clock}
				if st.Parked["npm/a@2.0.0"] != want {
					t.Errorf("parked = %+v, want %+v", st.Parked, want)
				}
			},
		},
		{
			name: "closed by a human, an unrelated PR open",
			setup: func(f *FakeForge) *state.State {
				tendPR(t, f, "tend/npm-a-2.0.0", "npm/a@2.0.0")
				noErr(t, f.CloseByHuman(1))
				tendPR(t, f, "tend/npm-b-3.0.0", "npm/b@3.0.0")
				return &state.State{Open: &state.OpenPR{ID: "npm/a@2.0.0", PR: 1, Branch: "tend/npm-a-2.0.0", Base: "main"}}
			},
			check: func(t *testing.T, f *FakeForge, st *state.State, got *core.PR) {
				if got == nil || got.Number != 2 || st.Open.PR != 2 {
					t.Errorf("got %+v, open %+v", got, st.Open)
				}
				if st.Parked["npm/a@2.0.0"].Reason != "rejected" {
					t.Errorf("parked = %+v", st.Parked)
				}
			},
		},
		{
			name: "closed by a bot since the last run: not a rejection",
			setup: func(f *FakeForge) *state.State {
				tendPR(t, f, "tend/npm-a-2.0.0", "npm/a@2.0.0")
				noErr(t, f.Say(1, "stale[bot]", "NONE", "Closing as stale."))
				noErr(t, f.CloseAs(1, "stale[bot]"))
				return &state.State{Open: &state.OpenPR{ID: "npm/a@2.0.0", PR: 1, Branch: "tend/npm-a-2.0.0", Base: "main"}}
			},
			check: func(t *testing.T, f *FakeForge, st *state.State, got *core.PR) {
				if got != nil || st.Open != nil {
					t.Errorf("got %+v, open %+v", got, st.Open)
				}
				if len(st.Parked) != 0 {
					t.Errorf("parked = %+v", st.Parked)
				}
			},
		},
		{
			name: "branch gone: no PR on the forge at all",
			setup: func(f *FakeForge) *state.State {
				return &state.State{Open: &state.OpenPR{ID: "api/fixture/GET /v1/orders#total_cents", PR: 4, Branch: "tend/api-fixture-get-v1-orders", Base: "main"}}
			},
			check: func(t *testing.T, f *FakeForge, st *state.State, got *core.PR) {
				if got != nil || st.Open != nil {
					t.Errorf("got %+v, open %+v", got, st.Open)
				}
				p := st.Parked["api/fixture/GET /v1/orders#total_cents"]
				if p.Reason != "rejected" || p.PR != 4 || p.To != "" {
					t.Errorf("parked = %+v", p)
				}
			},
		},
		{
			name: "already parked as preempted stays so",
			setup: func(f *FakeForge) *state.State {
				tendPR(t, f, "tend/npm-a-2.0.0", "npm/a@2.0.0")
				noErr(t, f.ClosePR(bg, 1))
				return &state.State{
					Parked: map[string]state.Parked{"npm/a@2.0.0": {PR: 1, Reason: "preempted", To: "2.0.0", At: earlier}},
					Open:   &state.OpenPR{ID: "npm/a@2.0.0", PR: 1, Branch: "tend/npm-a-2.0.0", Base: "main"},
				}
			},
			check: func(t *testing.T, f *FakeForge, st *state.State, got *core.PR) {
				if p := st.Parked["npm/a@2.0.0"]; p.Reason != "preempted" || !p.At.Equal(earlier) {
					t.Errorf("parked = %+v", p)
				}
			},
		},
		{
			name: "commit mode entry is not touched",
			setup: func(f *FakeForge) *state.State {
				return &state.State{Open: &state.OpenPR{ID: "npm/a@2.0.0", Branch: "main", Commit: "deadbeef"}}
			},
			check: func(t *testing.T, f *FakeForge, st *state.State, got *core.PR) {
				if got != nil || st.Open == nil || st.Open.Commit != "deadbeef" {
					t.Errorf("got %+v, open %+v", got, st.Open)
				}
			},
		},
		{
			name: "a PR made by hand on a tend/ branch is not tend's",
			setup: func(f *FakeForge) *state.State {
				mustOpen(t, f, core.PRInput{Branch: "tend/hand-made", Base: "main", Body: "no marker"})
				return &state.State{}
			},
			check: func(t *testing.T, f *FakeForge, st *state.State, got *core.PR) {
				if got != nil || st.Open != nil || f.PR(1).State != "open" || len(f.comments[1]) != 0 {
					t.Errorf("got %+v, open %+v, pr %+v", got, st.Open, f.PR(1))
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := NewFake("acme", "shop", "main")
			st := tc.setup(f)
			got, err := Reconcile(bg, f, st)
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, f, st, got)
		})
	}
}

func TestReconcileForgeError(t *testing.T) {
	boom := errors.New("bad credentials")
	f := NewFake("acme", "shop", "main")
	f.Err = boom
	st := &state.State{Open: &state.OpenPR{ID: "x", PR: 1, Branch: "tend/x"}}
	if _, err := Reconcile(bg, f, st); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if st.Open == nil || len(st.Parked) != 0 {
		t.Error("state changed although the forge could not be asked")
	}
}
