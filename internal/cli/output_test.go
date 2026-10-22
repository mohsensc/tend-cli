package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/deliver"
	"github.com/mohsensc/tend-cli/internal/plan"
	"github.com/mohsensc/tend-cli/internal/state"
)

func TestCamel(t *testing.T) {
	tests := []struct{ in, want string }{
		{"ID", "id"}, {"ToName", "toName"}, {"API", "api"}, {"URL", "url"},
		{"NodeID", "nodeID"}, {"Kind", "kind"}, {"repo", "repo"}, {"", ""},
	}
	for _, tt := range tests {
		if got := camel(tt.in); got != tt.want {
			t.Errorf("camel(%q) = %q", tt.in, got)
		}
	}
}

func TestSubjectWithoutDeps(t *testing.T) {
	if got := subject(&core.Change{ID: "npm/@types/node@"}); got != "npm/@types/node" {
		t.Errorf("subject = %q", got)
	}
}

// check shows the tidy-up as what it would do, not as a dep and a target.
func TestTidyLine(t *testing.T) {
	d := core.Dep{Ecosystem: core.Npm, Name: "left-pad", Version: "1.3.0"}
	open := &core.PR{Number: 1, Body: `<!-- tend id="tidy-up" base="abc" rev="1" -->`}
	tests := []struct {
		c    core.Cleanup
		open *core.PR
		want string
	}{
		{core.Cleanup{Unused: []core.Dep{d, d, d}, Moves: []core.Change{{}}, Kept: []core.Aside{{}}}, nil, "remove 3 unused · move off 1 deprecated · 1 to look at"},
		{core.Cleanup{Left: []core.Aside{{}, {}}}, open, "2 to look at · open PR #1"},
		{core.Cleanup{}, nil, "nothing to tidy; adds tend.yml"},
	}
	for _, tt := range tests {
		ch := core.Change{ID: core.TidyID, Kind: core.Tidy, Deps: []core.Dep{d}, Cleanup: &tt.c}
		if got := subject(&ch) + "|" + target(&ch) + "|" + detail(&ch, plan.OpenID(tt.open, nil), tt.open); got != "first PR||"+tt.want {
			t.Errorf("line = %q, want %q", got, tt.want)
		}
	}
}

// target shows the version a dep is at the way the planner reads it: a
// requirement without a lockfile shows its lowest version, not its range.
func TestTargetReadsVersionsLikeThePlanner(t *testing.T) {
	tests := []struct {
		name string
		deps []core.Dep
		want string
	}{
		{"locked", []core.Dep{{Version: "1.2.3", Req: "^1.2.0"}}, "1.2.3 → 2.0.0"},
		{"caret", []core.Dep{{Req: "^1.2.0"}}, "1.2.0 → 2.0.0"},
		{"range", []core.Dep{{Req: ">=1.4.0, <2"}}, "1.4.0 → 2.0.0"},
		{"lowest of several", []core.Dep{{Req: "~1.10.0"}, {Version: "1.9.0"}}, "1.9.0 → 2.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := &core.Change{Deps: tt.deps, To: "2.0.0"}
			if got := target(ch); got != tt.want {
				t.Errorf("target = %q, want %q", got, tt.want)
			}
			if got, want := plan.Lowest(ch.Deps), strings.Fields(tt.want)[0]; got != want {
				t.Errorf("plan.Lowest = %q, want %q", got, want)
			}
		})
	}
}

var day = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

func sample() *plan.Plan {
	pay := core.Dep{Ecosystem: "npm", Name: "@fixture/pay", Version: "1.4.0", Direct: true, Manifest: core.Manifest{Path: "package.json"}}
	node := core.Dep{Ecosystem: "docker", Name: "library/node", Version: "16-alpine", Direct: true, Line: 1, Manifest: core.Manifest{Path: "Dockerfile"}}
	crypto := core.Dep{Ecosystem: "go", Name: "example.com/fixture/crypto", Version: "v1.0.0", Direct: true}
	util := core.Dep{Ecosystem: "npm", Name: "@fixture/util", Version: "1.0.0", Direct: true}
	return &plan.Plan{
		Queue: []core.Change{
			{
				ID: "go/example.com/fixture/crypto@v1.0.1", Kind: core.Security, Severity: "high", Level: "patch",
				Deps: []core.Dep{crypto}, To: "v1.0.1",
				Evidence: []core.Evidence{{Source: "osv", Title: "GO-2026-0001 Signatures can be forged", URL: "https://osv.dev/vulnerability/GO-2026-0001"}},
				Sites:    []core.Site{{File: "main.go", Line: 9, Chain: []string{"crypto", "Sign"}, Call: true}},
			},
			{
				ID: "api/fixture/GET /v1/orders#total_cents", Kind: core.Breaking,
				API:   &core.APIChange{Provider: "fixture", Method: "GET", Path: "/v1/orders", Field: "total_cents", What: "response field removed"},
				Sites: []core.Site{{File: "app/orders.py", Line: 12, Chain: []string{"total_cents"}}},
			},
			{
				ID: "npm/@fixture/pay@2.0.0", Kind: core.Deprecation, Level: "major", Deps: []core.Dep{pay}, To: "2.0.0",
				Sites: []core.Site{{File: "src/billing.js", Line: 4, Chain: []string{"pay", "charges", "create"}}, {File: "src/billing.js", Line: 9, Chain: []string{"pay", "charges", "retrieve"}}},
			},
			{
				ID: "docker/library/node@22-alpine", Kind: core.Deprecation, Deps: []core.Dep{node}, To: "22-alpine",
				Sunset:   time.Date(2023, 9, 11, 0, 0, 0, 0, time.UTC),
				Evidence: []core.Evidence{{Source: "eol", URL: "https://endoflife.date/nodejs"}},
				Sites:    []core.Site{{File: "Dockerfile", Line: 1, Text: "FROM node:16-alpine"}},
			},
			{ID: "npm/moment@", Kind: core.Deprecation, Deps: []core.Dep{{Ecosystem: "npm", Name: "moment", Version: "2.29.4"}}, Options: []core.Option{{Title: "dayjs"}, {Title: "luxon"}}},
		},
		Dropped: []plan.Drop{
			{Change: core.Change{ID: "npm/@fixture/util@1.0.1", Kind: core.Update, Level: "patch", Deps: []core.Dep{util}, To: "1.0.1"}, Reason: "noise", Detail: "patch update that touches nothing; updates.patch is off"},
			{Change: core.Change{ID: "npm/left-pad@1.3.0", Kind: core.Update, Level: "minor", Deps: []core.Dep{{Ecosystem: "npm", Name: "left-pad", Version: "1.1.0"}}, To: "1.3.0"}, Reason: "noise", Detail: "minor update that touches nothing; updates.minor is off"},
			{Change: core.Change{ID: "npm/react@20.0.0", Kind: core.Breaking, Level: "major", Deps: []core.Dep{{Ecosystem: "npm", Name: "react", Version: "19.2.0"}}, To: "20.0.0", Published: day}, Reason: "cooldown", Detail: "released 2026-10-21, cooldown is 3d"},
		},
		Untracked: []plan.Untracked{
			{What: "vendor/lib", Why: "a git submodule, pinned to a commit; tend does not move submodules"},
			{What: "api.one.example", Why: "no spec", Key: "apis", Fix: "{name: one.example, hosts: [api.one.example], spec: <OpenAPI spec URL>}"},
			{What: "api.two.example", Why: "no spec", Key: "apis", Fix: "{name: two.example, hosts: [api.two.example], spec: <OpenAPI spec URL>}"},
		},
		Notes: []string{"npm: broken: registry returned 503"},
		Open:  &core.PR{Number: 12, Title: "Bump example.com/fixture/crypto to v1.0.1", Body: "<!-- tend id=\"go/example.com/fixture/crypto@v1.0.1\" base=\"abc\" rev=\"1\" -->"},
	}
}

func TestWriteCheck(t *testing.T) {
	for _, tt := range []struct {
		name string
		o    opts
	}{{"check.golden", opts{}}, {"check-all.golden", opts{all: true}}, {"check-quiet.golden", opts{quiet: true}}} {
		var b bytes.Buffer
		s := &session{o: tt.o, st: state.New(), root: "/src/shop", forge: deliver.NewFake("acme", "shop", "main")}
		if err := writeCheck(&b, s, sample(), false); err != nil {
			t.Fatal(err)
		}
		golden(t, tt.name, b.Bytes())
	}
	var b bytes.Buffer
	if err := writeCheck(&b, &session{root: "/src/empty", st: state.New()}, &plan.Plan{}, true); err != nil {
		t.Fatal(err)
	}
	if b.String() != "tend: nothing to watch: no manifests or API calls found in /src/empty\n" {
		t.Errorf("empty = %q", b.String())
	}
}

func TestWriteCheckManyNotes(t *testing.T) {
	p := &plan.Plan{}
	for i := range 13 {
		p.Notes = append(p.Notes, fmt.Sprintf("note %d", i))
	}
	var b bytes.Buffer
	if err := writeCheck(&b, &session{st: state.New()}, p, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "  - note 9\n  and 3 more   (tend check --all)\n") || strings.Contains(b.String(), "note 10") {
		t.Errorf("notes:\n%s", b.String())
	}
}

func TestCheckJSON(t *testing.T) {
	var b bytes.Buffer
	if err := writeJSON(&b, checkJSON("acme/shop", "main", sample())); err != nil {
		t.Fatal(err)
	}
	golden(t, "check.json.golden", b.Bytes())

	b.Reset()
	if err := writeJSON(&b, checkJSON("acme/shop", "main", &plan.Plan{})); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"base\": \"main\",\n  \"dropped\": [],\n  \"notes\": [],\n  \"open\": null,\n  \"queue\": [],\n  \"repo\": \"acme/shop\",\n  \"untracked\": []\n}\n"
	if b.String() != want {
		t.Errorf("empty plan = %s", b.String())
	}
}

func TestJSONKeepsCodemodKeys(t *testing.T) {
	ch := core.Change{Options: []core.Option{{Codemod: &core.Codemod{Fields: map[string]string{"TotalCents": "Total"}}}}}
	var b bytes.Buffer
	if err := writeJSON(&b, ch); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b.Bytes(), []byte(`"TotalCents": "Total"`)) || !bytes.Contains(b.Bytes(), []byte(`"fields"`)) {
		t.Errorf("codemod keys changed: %s", b.String())
	}
}

func TestLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var runs atomic.Int32
	var waits []time.Duration
	wait := func(ctx context.Context, d time.Duration) bool {
		waits = append(waits, d)
		return ctx.Err() == nil
	}
	loop(ctx, time.Hour, func(d time.Duration) time.Duration { return d / 10 }, wait, func(rc context.Context) {
		if runs.Add(1) == 3 {
			cancel()
			if rc.Err() != nil {
				t.Error("the run in flight was cancelled")
			}
		}
	})
	if runs.Load() != 3 || len(waits) != 2 || waits[0] != 66*time.Minute {
		t.Errorf("runs %d, waits %v", runs.Load(), waits)
	}
	for range 100 {
		if j := jitter(time.Hour); j < 0 || j > 6*time.Minute {
			t.Fatalf("jitter %s", j)
		}
	}
}
