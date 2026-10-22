package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/deliver"
	"github.com/mohsensc/tend-cli/internal/plan"
	"github.com/mohsensc/tend-cli/internal/state"
)

// keep drops exactly the parts that fail, whichever they are, proves a set
// that passes as a whole once, and never proves a set twice.
func TestKeep(t *testing.T) {
	tests := []struct {
		name  string
		n     int
		bad   []int
		pairs [][2]int // parts that fail only together; the later one goes
		tries int
	}{
		{name: "all pass", n: 5, tries: 1},
		{name: "nothing to prove", n: 0, tries: 0},
		{name: "one bad", n: 4, bad: []int{2}, tries: 4},
		{name: "two bad", n: 6, bad: []int{0, 5}},
		{name: "every one bad", n: 3, bad: []int{0, 1, 2}},
		{name: "one alone", n: 1, bad: []int{0}, tries: 1},
		{name: "two that clash", n: 4, pairs: [][2]int{{0, 3}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var parts []*part
			for i := range tt.n {
				parts = append(parts, &part{n: i, gone: &core.Dep{Name: fmt.Sprint(i)}})
			}
			tries := 0
			seen := map[string]bool{}
			tr := &trial{check: func(_ context.Context, set []*part) (*drop, error) {
				tries++
				if k := key(set); seen[k] {
					t.Errorf("proved %s twice", k)
				} else {
					seen[k] = true
				}
				in := map[int]bool{}
				for _, x := range set {
					in[x.n] = true
				}
				for _, b := range tt.bad {
					if in[b] {
						return &drop{build: "bad"}, nil
					}
				}
				for _, p := range tt.pairs {
					if in[p[0]] && in[p[1]] {
						return &drop{build: "clash"}, nil
					}
				}
				return nil, nil
			}}
			kept, err := tr.keep(context.Background(), nil, parts, nil)
			if err != nil {
				t.Fatal(err)
			}
			var got, gone []int
			for _, x := range kept {
				got = append(got, x.n)
			}
			for _, d := range tr.dropped {
				gone = append(gone, d.x.n)
			}
			want := []int{}
			for i := range tt.n {
				if !slices.Contains(tt.bad, i) && !slices.ContainsFunc(tt.pairs, func(p [2]int) bool { return p[1] == i }) {
					want = append(want, i)
				}
			}
			slices.Sort(gone)
			if !slices.Equal(got, want) || len(got)+len(gone) != tt.n {
				t.Errorf("kept %v, dropped %v; want kept %v", got, gone, want)
			}
			if tt.tries > 0 && tries != tt.tries {
				t.Errorf("%d tries, want %d", tries, tt.tries)
			}
		})
	}
}

func TestDropWhy(t *testing.T) {
	failed := &core.Run{Steps: []core.Step{{Cmd: "npm ci"}, {Cmd: "npm test", Exit: 1}}}
	gone := &part{gone: &core.Dep{Name: "left-pad"}}
	inPkg := &part{gone: &core.Dep{Name: "left-pad", Manifest: core.Manifest{Path: "package.json"}}}
	move := &part{move: &core.Change{Deps: []core.Dep{{Name: "stripe"}}}}
	for _, tt := range []struct {
		d    drop
		want string
	}{
		{drop{x: gone, run: failed}, "with it removed, `npm test` fails (exit 1)"},
		{drop{x: move, run: failed}, "with the move made, `npm test` fails (exit 1)"},
		{drop{x: gone, build: "npm install failed"}, "removing it fails: npm install failed"},
		{drop{x: inPkg, build: "patch: package.json: npm install --package-lock-only: exit status 1"}, "removing it from package.json fails: npm install --package-lock-only: exit status 1"},
		{drop{x: move, build: "patch: the edits to the code no longer apply: conflict"}, "the move cannot be made next to the rest: the edits to the code no longer apply: conflict"},
		{drop{x: gone, run: &core.Run{Steps: []core.Step{{Cmd: "go test ./..."}}}}, "with it removed, `go test ./...` does not finish"},
	} {
		if got := tt.d.why(); got != tt.want {
			t.Errorf("why = %q, want %q", got, tt.want)
		}
	}
}

// onboardRepo is a repo with one dependency nothing uses, read the way an
// inventory reads it, and a plan with nothing queued.
func onboardRepo(t *testing.T, more map[string]string) (*core.Inventory, string) {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"package.json":      `{"dependencies": {"left-pad": "^1.0.0"}}`,
		"package-lock.json": `{"lockfileVersion": 3, "packages": {"": {}, "node_modules/left-pad": {"version": "1.3.0"}}}`,
		"index.js":          "module.exports = 1\n",
	}
	for k, v := range more {
		files[k] = v
	}
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	inv, _, err := plan.Inventory(root, config.Default())
	if err != nil {
		t.Fatal(err)
	}
	return inv, root
}

// The first PR is due on a repo tend has never opened a PR on, comes back
// after tend closed it for security work, and settles for good once it
// merged or a person closed it, or when tend.yml asks for none.
func TestOnboarding(t *testing.T) {
	ctx := context.Background()
	marker := func(id string) string { return `<!-- tend id="` + id + `" base="abc" rev="1" -->` }
	tests := []struct {
		name    string
		cfg     func(*config.Config)
		st      func(*state.State)
		forge   func(*testing.T, *deliver.FakeForge)
		noForge bool
		more    map[string]string
		local   string // a tend.yml in the checkout tend runs in, not in the base
		due     bool
		settled string
		log     string
	}{
		{name: "a first run", due: true, settled: due},
		{name: "a first run with no forge leaves it to a run with one", noForge: true, due: true, settled: ""},
		{name: "tend opened PRs here before", forge: func(t *testing.T, f *deliver.FakeForge) {
			pr := openPR(t, f, "tend/npm-a-1.0.0", marker("npm/a@1.0.0"))
			if err := f.Merge(pr); err != nil {
				t.Fatal(err)
			}
		}, settled: "before"},
		{name: "a PR on a tend branch without tend's marker is someone's own", forge: func(t *testing.T, f *deliver.FakeForge) {
			openPR(t, f, "tend/mine", "my own work")
		}, due: true, settled: due},
		{name: "state remembers delivering", noForge: true, st: func(st *state.State) {
			st.Seen["npm/a@1.0.0"] = state.Seen{Verdict: "delivered"}
		}, settled: "before"},
		{name: "the first PR is open", st: func(st *state.State) { st.Onboard = due }, forge: func(t *testing.T, f *deliver.FakeForge) {
			openPR(t, f, deliver.TidyBranch, marker(core.TidyID))
		}, due: true, settled: due},
		{name: "a lost state finds the open first PR", forge: func(t *testing.T, f *deliver.FakeForge) {
			openPR(t, f, deliver.TidyBranch, marker(core.TidyID))
		}, due: true, settled: due},
		{name: "a lost state that read the open first PR back", st: func(st *state.State) {
			st.Open = &state.OpenPR{PR: 1, ID: core.TidyID}
		}, forge: func(t *testing.T, f *deliver.FakeForge) {
			openPR(t, f, deliver.TidyBranch, marker(core.TidyID))
		}, due: true, settled: due},
		{name: "a lost state while a security fix went first", forge: func(t *testing.T, f *deliver.FakeForge) {
			openPR(t, f, "tend/npm-a-1.0.1", `<!-- tend id="npm/a@1.0.1" base="abc" rev="1" tidy="due" -->`)
		}, due: true, settled: due},
		{name: "a lost state that read the security fix back", st: func(st *state.State) {
			st.Open = &state.OpenPR{PR: 1, ID: "npm/a@1.0.1"}
		}, forge: func(t *testing.T, f *deliver.FakeForge) {
			openPR(t, f, "tend/npm-a-1.0.1", `<!-- tend id="npm/a@1.0.1" base="abc" rev="1" tidy="due" -->`)
		}, due: true, settled: due},
		{name: "an open PR from before tend's first PR was a thing", forge: func(t *testing.T, f *deliver.FakeForge) {
			openPR(t, f, "tend/npm-a-1.0.1", marker("npm/a@1.0.1"))
		}, settled: "before"},
		{name: "the first PR merged", st: func(st *state.State) { st.Onboard = due }, forge: func(t *testing.T, f *deliver.FakeForge) {
			if err := f.Merge(openPR(t, f, deliver.TidyBranch, marker(core.TidyID))); err != nil {
				t.Fatal(err)
			}
		}, settled: "merged"},
		{name: "a person closed the first PR", forge: func(t *testing.T, f *deliver.FakeForge) {
			if err := f.CloseByHuman(openPR(t, f, deliver.TidyBranch, marker(core.TidyID))); err != nil {
				t.Fatal(err)
			}
		}, settled: "closed"},
		{name: "tend closed it for security work", st: func(st *state.State) { st.Onboard = due }, forge: func(t *testing.T, f *deliver.FakeForge) {
			n := openPR(t, f, deliver.TidyBranch, marker(core.TidyID))
			if err := deliver.Close(context.Background(), f, n, "Closing to make room for a security fix."); err != nil {
				t.Fatal(err)
			}
			openPR(t, f, "tend/npm-a-1.0.1", marker("npm/a@1.0.1"))
		}, due: true, settled: due},
		{name: "someone commented after tend closed it", st: func(st *state.State) { st.Onboard = due }, forge: func(t *testing.T, f *deliver.FakeForge) {
			n := openPR(t, f, deliver.TidyBranch, marker(core.TidyID))
			if err := deliver.Close(context.Background(), f, n, "Closing to make room for a security fix."); err != nil {
				t.Fatal(err)
			}
			if err := f.Say(n, "ada", "OWNER", "ok, the fix first"); err != nil {
				t.Fatal(err)
			}
		}, due: true, settled: due},
		{name: "settled before", st: func(st *state.State) { st.Onboard = "merged" }, settled: "merged"},
		{name: "quiet says what nothing uses", cfg: func(c *config.Config) { c.Onboarding = "quiet" }, settled: "quiet",
			log: "onboarding is quiet, so there is no tidy-up PR; nothing in the project uses left-pad"},
		{name: "off", cfg: func(c *config.Config) { c.Onboarding = "off" }, settled: "off"},
		{name: "commit mode opens no PRs", cfg: func(c *config.Config) { c.Deliver.Mode = "commit" }, settled: "commit"},
		{name: "not proven on this base", st: func(st *state.State) {
			st.Onboard = due
			st.Fail(core.TidyID, "npm test failed", "base1", time.Now())
		}, settled: due},
		{name: "nothing to tidy and a tend.yml already", cfg: func(c *config.Config) { c.Ignore = []config.Ignore{{Name: "left-pad"}} },
			more: map[string]string{"tend.yml": "cooldown: 3d\n"}, settled: "clean"},
		{name: "nothing to tidy and a tend.yml not yet committed", cfg: func(c *config.Config) { c.Ignore = []config.Ignore{{Name: "left-pad"}} },
			local: "cooldown: 3d\n", settled: "clean"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv, root := onboardRepo(t, tt.more)
			var errs bytes.Buffer
			s := &session{e: Env{Stderr: &errs, Getenv: func(string) string { return "" }, Now: time.Now}, cfg: config.Default(), st: state.New(), root: root}
			if tt.local != "" {
				s.root = t.TempDir()
				if err := os.WriteFile(filepath.Join(s.root, "tend.yml"), []byte(tt.local), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tt.cfg != nil {
				tt.cfg(s.cfg)
			}
			if tt.st != nil {
				tt.st(s.st)
			}
			if !tt.noForge {
				f := deliver.NewFake("acme", "shop", "main")
				if tt.forge != nil {
					tt.forge(t, f)
				}
				s.forge = f
			}
			ch, err := s.onboarding(ctx, inv, &plan.Plan{}, "base1")
			if err != nil {
				t.Fatal(err)
			}
			if (ch != nil) != tt.due || s.st.Onboard != tt.settled {
				t.Errorf("due = %v, settled %q; want %v, %q", ch != nil, s.st.Onboard, tt.due, tt.settled)
			}
			if ch != nil && (ch.Kind != core.Tidy || len(ch.Cleanup.Unused) != 1) {
				t.Errorf("tidy-up = %+v", ch)
			}
			if !strings.Contains(errs.String(), tt.log) {
				t.Errorf("logged %q, want %q", errs.String(), tt.log)
			}
		})
	}
}

func openPR(t *testing.T, f *deliver.FakeForge, branch, body string) int {
	t.Helper()
	pr, err := f.CreatePR(context.Background(), core.PRInput{Branch: branch, Base: "main", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	return pr.Number
}

// The tidy-up goes into the queue after security work and ahead of the rest.
func TestOnboardQueue(t *testing.T) {
	inv, root := onboardRepo(t, nil)
	s := &session{e: Env{Stderr: &bytes.Buffer{}, Getenv: func(string) string { return "" }, Now: time.Now}, cfg: config.Default(), st: state.New(), root: root}
	p := &plan.Plan{Queue: []core.Change{
		{ID: "npm/a@1.0.1", Kind: core.Security},
		{ID: "npm/b@2.0.0", Kind: core.Deprecation},
	}}
	if err := s.onboard(context.Background(), inv, p, "base1"); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, ch := range p.Queue {
		ids = append(ids, ch.ID)
	}
	if want := []string{"npm/a@1.0.1", core.TidyID, "npm/b@2.0.0"}; !slices.Equal(ids, want) {
		t.Errorf("queue %q, want %q", ids, want)
	}
}

// An open first PR tend no longer means to carry is closed with why; one
// still due, or any other PR, is not.
func TestUntidy(t *testing.T) {
	tidy := core.Change{ID: core.TidyID, Kind: core.Tidy}
	tests := []struct {
		name    string
		onboard string
		id      string
		queue   []core.Change
		want    string
	}{
		{"onboarding turned off", "off", core.TidyID, nil, "tend.yml sets onboarding: off now"},
		{"onboarding turned quiet", "quiet", core.TidyID, nil, "tend.yml sets onboarding: quiet now"},
		{"commit mode", "commit", core.TidyID, nil, "tend.yml delivers by commit now"},
		{"nothing left to tidy", "clean", core.TidyID, nil, "nothing is left to tidy up"},
		{"still due", due, core.TidyID, []core.Change{tidy}, ""},
		{"due, not proven on this base", due, core.TidyID, nil, ""},
		{"queued after all", "clean", core.TidyID, []core.Change{tidy}, ""},
		{"another PR", "off", "npm/a@1.0.0", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &session{st: state.New()}
			s.st.Onboard = tt.onboard
			if got := s.untidy(&plan.Plan{Queue: tt.queue}, tt.id); got != tt.want {
				t.Errorf("untidy = %q, want %q", got, tt.want)
			}
		})
	}
}
