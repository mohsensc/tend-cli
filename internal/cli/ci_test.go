package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/deliver"
	"github.com/mohsensc/tend-cli/internal/state"
)

// shallow is a depth-1 clone of a remote where someone pushed on top of the
// commit tend pushed, so that commit and the one before it are not in the
// clone. It returns the clone, tend's commit and the one before it.
func shallow(t *testing.T) (string, string, string) {
	t.Helper()
	src := repo(t, map[string]string{"a.txt": "a\n"})
	parent := gitIn(t, src, "rev-parse", "HEAD")
	writeFiles(t, src, map[string]string{"a.txt": "b\n"})
	gitIn(t, src, "commit", "--quiet", "-am", "tend's change")
	mine := gitIn(t, src, "rev-parse", "HEAD")
	writeFiles(t, src, map[string]string{"a.txt": "c\n"})
	gitIn(t, src, "commit", "--quiet", "-am", "someone else's")
	bare := filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, src, "clone", "--quiet", "--bare", src, bare)
	clone := filepath.Join(t.TempDir(), "clone")
	gitIn(t, src, "clone", "--quiet", "--depth", "1", "file://"+bare, clone)
	return clone, mine, parent
}

func settleIn(t *testing.T, clone, mine string, ci map[string]core.CI) (*session, *outcome, string) {
	t.Helper()
	ctx := context.Background()
	remote, err := deliver.NewRemote(ctx, clone, "origin", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	f := deliver.NewFake("acme", "shop", "main")
	f.CI = ci
	now := time.Now()
	var errs bytes.Buffer
	s := &session{e: Env{Stderr: &errs, Now: func() time.Time { return now }}, cfg: config.Default(), st: state.New(), forge: f, root: clone, remote: "origin", push: remote}
	s.st.Open = &state.OpenPR{ID: "npm/a@2.0.0", Branch: "main", Commit: mine, At: now.Add(-time.Hour)}
	res, err := s.settleCommit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return s, res, errs.String()
}

// A shallow checkout, as actions/checkout makes by default, does not hold
// tend's commit once someone pushed on top of it; that is no sign the
// commit was taken off again.
func TestSettleShallow(t *testing.T) {
	clone, mine, _ := shallow(t)
	s, res, logged := settleIn(t, clone, mine, map[string]core.CI{mine: {State: "success"}})
	if res != nil || s.st.Open != nil {
		t.Fatalf("outcome %+v, open %+v; logged %s", res, s.st.Open, logged)
	}
	if _, ok := s.st.Parked["npm/a@2.0.0"]; ok {
		t.Errorf("a commit still on the branch was parked as rejected: %+v", s.st.Parked)
	}
	if s.st.Seen["npm/a@2.0.0"].Verdict != "delivered" {
		t.Errorf("seen = %+v", s.st.Seen)
	}
}

// CI that failed on the commit before tend's too is not tend's doing, and
// the commit before it is found in a shallow checkout as well.
func TestSettleShallowParentRed(t *testing.T) {
	clone, mine, parent := shallow(t)
	red := core.CI{State: "failure", Failed: []string{"test"}}
	s, res, logged := settleIn(t, clone, mine, map[string]core.CI{mine: red, parent: red})
	if res != nil || s.st.Open != nil || s.st.Seen["npm/a@2.0.0"].Verdict != "delivered" {
		t.Fatalf("outcome %+v, open %+v, seen %+v; logged %s", res, s.st.Open, s.st.Seen, logged)
	}
}

// A commit that is no longer on the branch was taken off by someone, and is
// not tried again.
func TestSettleRemoved(t *testing.T) {
	clone, _, _ := shallow(t)
	gone := "1111111111111111111111111111111111111111"
	s, res, logged := settleIn(t, clone, gone, nil)
	if res == nil || res.Action != "waiting" || s.st.Open == nil {
		t.Fatalf("an unknown commit: outcome %+v, open %+v; logged %s", res, s.st.Open, logged)
	}
	top := gitIn(t, clone, "rev-parse", "HEAD")
	gitIn(t, clone, "commit", "--quiet", "--allow-empty", "-m", "local only")
	local := gitIn(t, clone, "rev-parse", "HEAD")
	gitIn(t, clone, "reset", "--quiet", "--hard", top)
	s, res, logged = settleIn(t, clone, local, nil)
	if res != nil || s.st.Open != nil || s.st.Parked["npm/a@2.0.0"].Reason != "rejected" {
		t.Fatalf("a commit off the branch: outcome %+v, open %+v, parked %+v; logged %s", res, s.st.Open, s.st.Parked, logged)
	}
}
