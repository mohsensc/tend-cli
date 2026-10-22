package plan

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/api"
	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/state"
)

var now = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

func ago(days int) time.Time { return now.Add(-time.Duration(days) * 24 * time.Hour) }

func rel(v string, days int) core.Release { return core.Release{Version: v, Time: ago(days)} }

func names(rs []core.Release) *core.Meta { return &core.Meta{Releases: rs} }

func TestCandidate(t *testing.T) {
	cool := 72 * time.Hour
	tests := []struct {
		name     string
		dep      core.Dep
		meta     *core.Meta
		maxMajor int
		want     string
	}{
		{
			name: "latest stable",
			dep:  core.Dep{Ecosystem: "npm", Version: "1.0.0"},
			meta: names([]core.Release{rel("1.0.0", 100), rel("1.1.0", 50), rel("1.2.0", 10)}),
			want: "1.2.0",
		},
		{
			name: "skips pre-release",
			dep:  core.Dep{Ecosystem: "npm", Version: "1.0.0"},
			meta: names([]core.Release{rel("1.0.0", 100), rel("1.1.0", 50), {Version: "2.0.0-rc.1", Time: ago(10)}, {Version: "1.2.0", Pre: true, Time: ago(10)}}),
			want: "1.1.0",
		},
		{
			name: "pre-release of the same major allowed from a pre-release",
			dep:  core.Dep{Ecosystem: "npm", Version: "2.0.0-beta.1"},
			meta: names([]core.Release{rel("2.0.0-beta.1", 100), rel("2.0.0-rc.1", 20), rel("3.0.0-alpha.1", 10)}),
			want: "2.0.0-rc.1",
		},
		{
			name: "skips yanked and deprecated",
			dep:  core.Dep{Ecosystem: "pip", Version: "1.0.0"},
			meta: names([]core.Release{rel("1.0.0", 100), rel("1.0.1", 50), {Version: "1.0.2", Yanked: true, Time: ago(40)}, {Version: "1.0.3", Deprecated: "broken", Time: ago(30)}}),
			want: "1.0.1",
		},
		{
			name: "cooldown prefers an older release",
			dep:  core.Dep{Ecosystem: "npm", Version: "1.0.0"},
			meta: names([]core.Release{rel("1.0.0", 100), rel("1.1.0", 5), rel("1.2.0", 1)}),
			want: "1.1.0",
		},
		{
			name: "every newer release is cooling down",
			dep:  core.Dep{Ecosystem: "npm", Version: "1.0.0"},
			meta: names([]core.Release{rel("1.0.0", 100), rel("1.0.1", 2), rel("1.0.2", 1)}),
			want: "1.0.2",
		},
		{
			name: "no timestamp is not cooling down",
			dep:  core.Dep{Ecosystem: "docker", Version: "16-alpine"},
			meta: names([]core.Release{{Version: "16-alpine"}, {Version: "18-alpine"}}),
			want: "18-alpine",
		},
		{
			name: "registry latest caps",
			dep:  core.Dep{Ecosystem: "npm", Version: "1.0.0"},
			meta: &core.Meta{Latest: "1.1.0", Releases: []core.Release{rel("1.0.0", 100), rel("1.1.0", 50), rel("2.0.0", 20)}},
			want: "1.1.0",
		},
		{
			name: "docker keeps the tag shape",
			dep:  core.Dep{Ecosystem: "docker", Version: "16-alpine"},
			meta: names([]core.Release{{Version: "16-alpine"}, {Version: "22"}, {Version: "22-slim"}, {Version: "22.11.0-alpine"}, {Version: "22-alpine"}, {Version: "24-alpine3.20"}}),
			want: "22-alpine",
		},
		{
			name: "actions keeps major-only refs",
			dep:  core.Dep{Ecosystem: "actions", Version: "v2"},
			meta: names([]core.Release{{Version: "v2"}, {Version: "v4.2.2"}, {Version: "v4"}, {Version: "v5"}, {Version: "v5.0.0"}}),
			want: "v5",
		},
		{
			name: "actions keeps full refs",
			dep:  core.Dep{Ecosystem: "actions", Version: "v4.1.0"},
			meta: names([]core.Release{{Version: "v4.1.0"}, {Version: "v4.2.2"}, {Version: "v5"}}),
			want: "v4.2.2",
		},
		{
			name:     "major cap",
			dep:      core.Dep{Ecosystem: "npm", Version: "18.0.0"},
			meta:     names([]core.Release{rel("18.0.0", 100), rel("18.3.0", 50), rel("19.0.0", 20)}),
			maxMajor: 18,
			want:     "18.3.0",
		},
		{
			name: "requirement when nothing is locked",
			dep:  core.Dep{Ecosystem: "pip", Req: ">=1.0,<2"},
			meta: names([]core.Release{rel("1.0", 100), rel("1.5", 50)}),
			want: "1.5",
		},
		{
			name: "nothing newer",
			dep:  core.Dep{Ecosystem: "npm", Version: "1.2.0"},
			meta: names([]core.Release{rel("1.0.0", 100), rel("1.2.0", 50)}),
		},
		{
			name: "unparsable current",
			dep:  core.Dep{Ecosystem: "docker", Version: "latest"},
			meta: names([]core.Release{{Version: "22"}}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			maxMajor := tt.maxMajor
			if maxMajor == 0 {
				maxMajor = -1
			}
			got, ok := candidate(tt.dep, tt.meta, now, cool, maxMajor)
			if ok != (tt.want != "") || got.Version != tt.want {
				t.Errorf("candidate = %q, %v; want %q", got.Version, ok, tt.want)
			}
		})
	}
}

func TestFixFor(t *testing.T) {
	m := names([]core.Release{
		rel("1.0.0", 300), {Version: "1.0.1", Yanked: true}, rel("2.0.0-rc.1", 100), rel("2.0.0", 50), rel("2.0.1", 10),
		{Version: "4.18.0", Deprecated: "Bad release. Please use lodash@4.17.21 instead."}, rel("4.18.1", 5),
	})
	tests := []struct{ to, want string }{
		{"1.0.1", "2.0.0"},
		{"2.0.0", "2.0.0"},
		{"1.0.0", "1.0.0"},
		{"5.0.0", "5.0.0"},
		{"4.18.0", "4.18.1"},
	}
	for _, tt := range tests {
		if got, _ := fixFor(m, tt.to); got.Version != tt.want {
			t.Errorf("fixFor(%s) = %s, want %s", tt.to, got.Version, tt.want)
		}
	}
	if got, ok := fixFor(nil, "1.2.3"); ok || got.Version != "1.2.3" {
		t.Errorf("fixFor without meta = %v, %v", got, ok)
	}
}

func npmDep(name, version, manifest string) core.Dep {
	return core.Dep{Ecosystem: "npm", Name: name, Version: version, Direct: true, Manifest: core.Manifest{Ecosystem: "npm", Path: manifest}}
}

func TestMerge(t *testing.T) {
	a := npmDep("a", "1.0.0", "package.json")
	a2 := npmDep("a", "1.0.0", "web/package.json")
	osv := core.Evidence{Source: "osv", Title: "GHSA-aaaa-bbbb-cccc prototype pollution", URL: "https://osv.dev/vulnerability/GHSA-aaaa-bbbb-cccc"}
	other := core.Evidence{Source: "osv", Title: "CVE-2026-1234 second issue", URL: "https://osv.dev/vulnerability/CVE-2026-1234"}
	notes := core.Evidence{Source: "releases", Title: "1.0.2", URL: "https://github.com/x/a/releases/1.0.2"}

	got := merge([]core.Change{
		{Kind: core.Update, Level: "patch", Deps: []core.Dep{a}, To: "1.0.2", Evidence: []core.Evidence{notes}},
		{Kind: core.Security, Severity: "moderate", Deps: []core.Dep{a}, To: "1.0.1", Evidence: []core.Evidence{osv}, Published: ago(3)},
		{Kind: core.Security, Severity: "high", Deps: []core.Dep{a2}, To: "1.0.2", Evidence: []core.Evidence{osv, other}, Published: ago(5)},
		{Kind: core.Update, Level: "minor", Deps: []core.Dep{a}, To: "1.1.0"},
		{API: &core.APIChange{Provider: "p", Method: "GET", Path: "/x"}, Kind: core.Deprecation, Evidence: []core.Evidence{{URL: "u1"}}},
		{API: &core.APIChange{Provider: "p", Method: "GET", Path: "/x"}, Kind: core.Breaking, Evidence: []core.Evidence{{URL: "u1"}, {URL: "u2"}}},
	})
	if len(got) != 3 {
		t.Fatalf("got %d changes: %+v", len(got), got)
	}
	sec := got[0]
	if sec.Kind != core.Security || sec.To != "1.0.2" || sec.Severity != "high" || !sec.Published.Equal(ago(5)) {
		t.Errorf("security = %+v", sec)
	}
	if len(sec.Deps) != 2 || sec.Deps[0].Manifest.Path != "package.json" || sec.Deps[1].Manifest.Path != "web/package.json" {
		t.Errorf("deps = %+v", sec.Deps)
	}
	var titles []string
	for _, ev := range sec.Evidence {
		titles = append(titles, ev.Source+":"+strings.Fields(ev.Title)[0])
	}
	if want := []string{"releases:1.0.2", "osv:GHSA-aaaa-bbbb-cccc", "osv:CVE-2026-1234"}; !slices.Equal(titles, want) {
		t.Errorf("evidence = %v, want %v", titles, want)
	}
	if got[1].To != "1.1.0" || got[1].Kind != core.Update {
		t.Errorf("minor update = %+v", got[1])
	}
	if got[2].Kind != core.Breaking || len(got[2].Evidence) != 2 {
		t.Errorf("api = %+v", got[2])
	}
}

type fakeEco struct {
	name    string
	metas   map[string]*core.Meta
	errs    map[string]error
	imports map[string][]string

	mu    sync.Mutex
	calls []string
}

func (f *fakeEco) Name() string                                   { return f.name }
func (f *fakeEco) Detect(string) ([]core.Manifest, error)         { return nil, nil }
func (f *fakeEco) Deps(string, core.Manifest) ([]core.Dep, error) { return nil, nil }
func (f *fakeEco) Commands(string, core.Manifest) core.Commands   { return core.Commands{} }

// Imports is the package's own name, as for npm, except for docker and
// actions, which code does not import.
func (f *fakeEco) Traits() core.Traits { return core.Traits{} }

func (f *fakeEco) Imports(d core.Dep) []string {
	if f.name == core.Docker || f.name == core.Actions {
		return nil
	}
	if res, ok := f.imports[d.Name]; ok {
		return res
	}
	return []string{d.Name}
}
func (f *fakeEco) Bump(context.Context, string, core.Dep, string, string) (*core.Bump, error) {
	return nil, core.ErrNotSupported
}

func (f *fakeEco) Meta(ctx context.Context, _ core.Fetcher, name string) (*core.Meta, error) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.mu.Unlock()
	if err := f.errs[name]; err != nil {
		return nil, err
	}
	m, ok := f.metas[name]
	if !ok {
		return nil, fmt.Errorf("%s: not found", name)
	}
	return m, nil
}

type fakeIndex struct{ uses map[string][]core.Site }

func (x fakeIndex) Uses(imports []string) []core.Site {
	var res []core.Site
	for _, i := range imports {
		res = append(res, x.uses[i]...)
	}
	return res
}
func (fakeIndex) Fields(string, []string) []core.Site { return nil }
func (fakeIndex) Endpoints([]string) []core.URLUse    { return nil }
func (fakeIndex) URLs() []core.URLUse                 { return nil }
func (fakeIndex) Imports(string) []core.Import        { return nil }
func (fakeIndex) Files() []string                     { return nil }

type urlIndex struct {
	fakeIndex
	urls []core.URLUse
}

func (x urlIndex) URLs() []core.URLUse { return x.urls }

func TestDiscoverSkipsIgnoredAndRegistryHosts(t *testing.T) {
	inv := &core.Inventory{Index: urlIndex{urls: []core.URLUse{
		{File: "feed.go", Line: 3, Host: "api.feeds.dev", URL: "https://api.feeds.dev/v1/query"},
		{File: "items.go", Line: 9, Host: "api.items.dev", URL: "https://api.items.dev/v1/items"},
		{File: "pkg.go", Line: 4, Host: "api.nuget.org", URL: "https://api.nuget.org/v3/index.json"},
		{File: "pkg.go", Line: 5, Host: "crates.io", URL: "https://crates.io/api/v1/crates/serde"},
	}}}
	cfg := config.Default()
	cfg.Ignore = []config.Ignore{{Name: "api.feeds.dev", Ecosystem: "api"}}
	st := state.New()
	_, got := Discover(context.Background(), inv, cfg, st, nowhere{}, now)
	if len(got) != 1 || got[0].What != "api.items.dev" {
		t.Errorf("untracked = %+v", got)
	}
	for _, h := range []string{"api.feeds.dev", "api.nuget.org", "crates.io"} {
		if _, ok := st.Found[h]; ok {
			t.Errorf("%s was looked up", h)
		}
	}
}

// nowhere answers every request with a 404, so nothing in these tests reaches the network.
type nowhere struct{}

func (nowhere) Get(context.Context, string, ...core.FetchOpt) (*core.Response, error) {
	return &core.Response{Status: 404}, nil
}

func (nowhere) Post(context.Context, string, []byte, ...core.FetchOpt) (*core.Response, error) {
	return &core.Response{Status: 404}, nil
}

type fakeSource struct {
	name string
	chs  []core.Change
	err  error
}

func (s fakeSource) Name() string { return s.name }
func (s fakeSource) Changes(context.Context, *core.Inventory) ([]core.Change, error) {
	return s.chs, s.err
}

func build(t *testing.T, cfg *config.Config, st *state.State, deps []core.Dep, ecos map[string]core.Ecosystem, srcs ...core.Source) *Plan {
	t.Helper()
	if cfg == nil {
		cfg = config.Default()
	}
	inv := &core.Inventory{Root: t.TempDir(), Deps: deps, Index: fakeIndex{uses: map[string][]core.Site{
		"pay":           {{File: "src/billing.js", Line: 3, Chain: []string{"pay", "charges", "create"}, Call: true}},
		"util":          {{File: "src/a.js", Line: 1}},
		"react":         {{File: "src/app.jsx", Line: 1}},
		"example.com/x": {{File: "main.go", Line: 5}},
	}}}
	p, err := Build(context.Background(), Input{Inv: inv, Cfg: cfg, State: st, Fetch: nowhere{}, Ecos: ecos, Sources: srcs, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func ids(chs []core.Change) []string {
	var res []string
	for _, ch := range chs {
		res = append(res, string(ch.Kind)+" "+ch.ID)
	}
	return res
}

func drops(ds []Drop) []string {
	var res []string
	for _, d := range ds {
		res = append(res, d.Reason+" "+d.Change.ID)
	}
	return res
}

func TestBuild(t *testing.T) {
	npm := &fakeEco{
		name:    "npm",
		imports: map[string][]string{"pay": {"pay"}, "util": {"util"}, "react": {"react"}},
		metas: map[string]*core.Meta{
			"pay": {Releases: []core.Release{
				{Version: "1.4.0", Time: ago(200), Deprecated: "use 2.x: charges.* was replaced by paymentIntents.*"},
				rel("2.0.0", 30),
			}},
			"util":         {Releases: []core.Release{rel("1.0.0", 90), rel("1.0.1", 20)}},
			"left":         {Releases: []core.Release{rel("1.0.0", 90)}, Deprecated: "no longer maintained", Replacement: "right"},
			"right":        {Releases: []core.Release{rel("3.1.0", 90)}},
			"deep":         {Releases: []core.Release{rel("0.9.0", 300), {Version: "0.9.1", Yanked: true}, rel("0.9.2", 50)}},
			"react":        {Releases: []core.Release{rel("18.3.0", 90), rel("19.0.0", 50)}},
			"@types/react": {Releases: []core.Release{rel("18.2.0", 90), rel("18.3.1", 60), rel("19.0.1", 40)}},
		},
		errs: map[string]error{"broken": errors.New("registry returned 503")},
	}
	deps := []core.Dep{
		npmDep("pay", "1.4.0", "package.json"),
		npmDep("util", "1.0.0", "package.json"),
		npmDep("left", "1.0.0", "package.json"),
		npmDep("broken", "1.0.0", "package.json"),
		npmDep("react", "18.3.0", "package.json"),
		npmDep("@types/react", "18.2.0", "package.json"),
		{Ecosystem: "npm", Name: "deep", Version: "0.9.0", Manifest: core.Manifest{Path: "package.json"}},
		{Ecosystem: "npm", Name: "unnamed", Version: "1.0.0", Manifest: core.Manifest{Path: "package.json"}},
	}
	osv := fakeSource{name: "osv", chs: []core.Change{{
		Kind: core.Security, Severity: "high", Deps: []core.Dep{deps[6]}, To: "0.9.1",
		Evidence: []core.Evidence{{Source: "osv", Title: "GHSA-1111-2222-3333 bad"}}, Published: ago(1),
	}}}
	down := fakeSource{name: "eol", err: errors.New("503 service unavailable")}
	st := state.New()
	st.Seen["npm/gone@1.0.0"] = state.Seen{Last: ago(181), Verdict: "noise"}
	st.Seen["npm/recent@1.0.0"] = state.Seen{Last: ago(30), Verdict: "noise"}
	p := build(t, nil, st, deps, map[string]core.Ecosystem{"npm": npm}, osv, down)
	if _, ok := st.Seen["npm/gone@1.0.0"]; ok {
		t.Error("a change unseen for half a year is still remembered")
	}
	if _, ok := st.Seen["npm/recent@1.0.0"]; !ok {
		t.Error("a recent record was forgotten")
	}

	wantQueue := []string{
		"security npm/deep@0.9.2",
		"deprecation npm/pay@2.0.0",
		"deprecation npm/left->right@3.1.0",
		"breaking npm/react@19.0.0",
	}
	if got := ids(p.Queue); !slices.Equal(got, wantQueue) {
		t.Errorf("queue = %q\nwant    %q", got, wantQueue)
	}
	wantDropped := []string{"noise npm/util@1.0.1", "noise npm/@types/react@18.3.1"}
	if got := drops(p.Dropped); !slices.Equal(got, wantDropped) {
		t.Errorf("dropped = %q\nwant      %q", got, wantDropped)
	}
	if slices.Contains(npm.calls, "unnamed") {
		t.Error("looked up a transitive dep no advisory names")
	}
	pay := p.Queue[1]
	if pay.Level != "major" || len(pay.Sites) != 1 || pay.Sites[0].File != "src/billing.js" || pay.Evidence[0].Source != "registry" {
		t.Errorf("pay = %+v", pay)
	}
	if sec := p.Queue[0]; sec.Level != "patch" || sec.Severity != "high" {
		t.Errorf("security = %+v", sec)
	}
	wantNotes := []string{
		"eol: unavailable, planned without it: 503 service unavailable",
		"npm: broken: registry returned 503",
	}
	if !slices.Equal(p.Notes, wantNotes) {
		t.Errorf("notes = %q", p.Notes)
	}
	if s := st.Seen["npm/util@1.0.1"]; s.Verdict != "noise" || !s.First.Equal(now) {
		t.Errorf("seen util = %+v", s)
	}
	if s := st.Seen["npm/pay@2.0.0"]; s.Verdict != "queued" {
		t.Errorf("seen pay = %+v", s)
	}
}

// A changelog that cannot be read is a note, and the plan goes on.
func TestBuildChangelogNotes(t *testing.T) {
	inv := &core.Inventory{Root: t.TempDir(), Index: fakeIndex{}, Providers: []core.ProviderUse{
		{Provider: "acme", URLs: []core.URLUse{{File: "a.js", Line: 1, URL: "https://api.acme.test/v1/x"}}},
	}}
	cl := &api.Changelogs{Cat: []api.Provider{{Name: "acme", Hosts: []string{"api.acme.test"}, Changelog: "https://acme.test/changes"}}, Fetch: nowhere{}}
	p, err := Build(context.Background(), Input{Inv: inv, Cfg: config.Default(), State: state.New(), Fetch: nowhere{}, Sources: []core.Source{cl}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"acme: changelog unavailable: https://acme.test/changes: HTTP 404"}; !slices.Equal(p.Notes, want) {
		t.Errorf("notes = %q, want %q", p.Notes, want)
	}
}

// A replacement two manifests swap to is looked up once, and release notes
// tend cannot read, from a repo outside GitHub, leave no note.
func TestBuildLookups(t *testing.T) {
	npm := &fakeEco{name: "npm", metas: map[string]*core.Meta{
		"left":  {Releases: []core.Release{rel("1.0.0", 90)}, Deprecated: "no longer maintained", Replacement: "right"},
		"right": {Releases: []core.Release{rel("3.1.0", 90)}},
		"util":  {Repo: "https://gitlab.com/o/util", Releases: []core.Release{rel("1.0.0", 90), rel("1.1.0", 20)}},
	}}
	deps := []core.Dep{
		npmDep("left", "1.0.0", "package.json"),
		npmDep("left", "1.0.0", "web/package.json"),
		npmDep("util", "1.0.0", "package.json"),
	}
	p := build(t, nil, state.New(), deps, map[string]core.Ecosystem{"npm": npm})
	if got := ids(p.Queue); !slices.Equal(got, []string{"deprecation npm/left->right@3.1.0"}) {
		t.Fatalf("queue = %q", got)
	}
	n := 0
	for _, c := range npm.calls {
		if c == "right" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("looked up the replacement %d times, want once", n)
	}
	if len(p.Notes) != 0 {
		t.Errorf("notes = %q", p.Notes)
	}
}

// notesOnly serves one GitHub releases page and nothing else.
type notesOnly struct{ body string }

func (n notesOnly) Get(_ context.Context, u string, _ ...core.FetchOpt) (*core.Response, error) {
	if strings.HasPrefix(u, "https://api.github.com/repos/acme/pay/releases") {
		return &core.Response{Status: 200, Body: []byte(n.body)}, nil
	}
	return &core.Response{Status: 404}, nil
}

func (notesOnly) Post(context.Context, string, []byte, ...core.FetchOpt) (*core.Response, error) {
	return &core.Response{Status: 404}, nil
}

// A minor release whose notes remove a call the code makes is breaking, and
// gets a PR with every updates flag off.
func TestBuildMinorBreaks(t *testing.T) {
	npm := &fakeEco{name: "npm", imports: map[string][]string{"pay": {"pay"}}, metas: map[string]*core.Meta{
		"pay": {Repo: "https://github.com/acme/pay", Releases: []core.Release{rel("1.4.0", 90), rel("1.5.0", 30)}},
	}}
	cfg := config.Default()
	cfg.Updates = config.Updates{}
	inv := &core.Inventory{Root: t.TempDir(), Deps: []core.Dep{npmDep("pay", "1.4.0", "package.json")}, Index: fakeIndex{uses: map[string][]core.Site{
		"pay": {{File: "src/billing.js", Line: 3, Chain: []string{"pay", "charges", "create"}, Call: true}},
	}}}
	notes := `[{"tag_name": "v1.5.0", "body": "Removed ` + "`charges.create`" + `; call paymentIntents.create.", "html_url": "https://github.com/acme/pay/releases/v1.5.0"}]`
	p, err := Build(context.Background(), Input{Inv: inv, Cfg: cfg, State: state.New(), Fetch: notesOnly{notes}, Ecos: map[string]core.Ecosystem{"npm": npm}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(p.Queue); !slices.Equal(got, []string{"breaking npm/pay@1.5.0"}) {
		t.Fatalf("queue = %q, dropped %q", got, drops(p.Dropped))
	}
	if ch := p.Queue[0]; ch.Level != "minor" {
		t.Errorf("level = %q", ch.Level)
	}
}

// A locked version the registry yanked is a deprecation in every ecosystem,
// whether the adapter says so in words or only flags it.
func TestBuildYanked(t *testing.T) {
	cargo := &fakeEco{name: "cargo", metas: map[string]*core.Meta{
		"serde": {Releases: []core.Release{rel("1.0.0", 300), {Version: "1.0.1", Time: ago(200), Yanked: true}, rel("1.0.2", 100)}},
	}}
	d := core.Dep{Ecosystem: "cargo", Name: "serde", Version: "1.0.1", Direct: true, Manifest: core.Manifest{Path: "Cargo.toml"}}
	p := build(t, nil, state.New(), []core.Dep{d}, map[string]core.Ecosystem{"cargo": cargo})
	if got := ids(p.Queue); !slices.Equal(got, []string{"deprecation cargo/serde@1.0.2"}) {
		t.Fatalf("queue = %q, dropped %q", got, drops(p.Dropped))
	}
	if ev := p.Queue[0].Evidence[0]; ev.Title != "serde 1.0.1 was yanked" {
		t.Errorf("evidence = %+v", ev)
	}
}

// answers serves fixed bodies by URL and 404 for the rest.
type answers map[string]string

func (a answers) Get(_ context.Context, u string, _ ...core.FetchOpt) (*core.Response, error) {
	if b, ok := a[u]; ok {
		return &core.Response{Status: 200, Body: []byte(b)}, nil
	}
	return &core.Response{Status: 404}, nil
}

func (answers) Post(context.Context, string, []byte, ...core.FetchOpt) (*core.Response, error) {
	return &core.Response{Status: 404}, nil
}

// A package whose source repository is archived is abandoned, whatever its
// registry says: a deprecation with a way to stay, cited from GitHub.
func TestBuildArchived(t *testing.T) {
	npm := &fakeEco{name: "npm", metas: map[string]*core.Meta{
		"dead": {Repo: "git+https://github.com/o/dead.git", Releases: []core.Release{rel("1.0.0", 900)}},
		"live": {Repo: "https://github.com/o/live", Releases: []core.Release{rel("1.0.0", 900)}},
	}}
	f := answers{
		"https://api.github.com/repos/o/dead": `{"full_name": "o/dead", "html_url": "https://github.com/o/dead", "archived": true, "pushed_at": "2021-02-03T00:00:00Z"}`,
		"https://api.github.com/repos/o/live": `{"full_name": "o/live", "html_url": "https://github.com/o/live", "archived": false}`,
	}
	inv := &core.Inventory{Root: t.TempDir(), Deps: []core.Dep{npmDep("dead", "1.0.0", "package.json"), npmDep("live", "1.0.0", "package.json")}}
	p, err := Build(context.Background(), Input{Inv: inv, Cfg: config.Default(), State: state.New(), Fetch: f, Ecos: map[string]core.Ecosystem{"npm": npm}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(p.Queue); !slices.Equal(got, []string{"deprecation npm/dead@"}) {
		t.Fatalf("queue = %q, dropped %q, notes %q", got, drops(p.Dropped), p.Notes)
	}
	ch := p.Queue[0]
	if ev := ch.Evidence[0]; ev.Source != "archived" || ev.Title != "o/dead is archived" || ev.URL != "https://github.com/o/dead" {
		t.Errorf("evidence = %+v", ev)
	}
	if len(ch.Options) != 1 || ch.Options[0].To != "1.0.0" {
		t.Errorf("options = %+v", ch.Options)
	}
}

func TestBuildDockerSites(t *testing.T) {
	docker := &fakeEco{name: "docker", metas: map[string]*core.Meta{
		"library/node": names([]core.Release{{Version: "16-alpine"}, {Version: "22-alpine"}}),
	}}
	d := core.Dep{Ecosystem: "docker", Name: "library/node", Version: "16-alpine", Direct: true, Line: 2, Manifest: core.Manifest{Path: "Dockerfile"}}
	cfg := config.Default()
	inv := &core.Inventory{Root: t.TempDir(), Deps: []core.Dep{d}}
	writeFile(t, inv.Root, "Dockerfile", "# syntax=docker/dockerfile:1\nFROM node:16-alpine AS build\n")
	p, err := Build(context.Background(), Input{Inv: inv, Cfg: cfg, State: state.New(), Fetch: nowhere{}, Ecos: map[string]core.Ecosystem{"docker": docker}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Queue) != 1 {
		t.Fatalf("queue = %q", ids(p.Queue))
	}
	s := p.Queue[0].Sites
	if len(s) != 1 || s[0].File != "Dockerfile" || s[0].Line != 2 || s[0].Text != "FROM node:16-alpine AS build" {
		t.Errorf("sites = %+v", s)
	}
}

// The go adapter lists every major of a module, /vN paths included, as one
// release line; a bump across majors is breaking and moves the imports itself.
func TestWorkflowCommands(t *testing.T) {
	actions := &fakeEco{name: "actions", metas: map[string]*core.Meta{
		"actions/checkout": names([]core.Release{{Version: "v2"}, {Version: "v5"}}),
		"actions/cache":    names([]core.Release{{Version: "v3"}, {Version: "v4"}}),
	}}
	wf := core.Manifest{Ecosystem: "actions", Path: ".github/workflows/ci.yml"}
	other := core.Manifest{Ecosystem: "actions", Path: ".github/workflows/release.yml"}
	inv := &core.Inventory{Root: t.TempDir(), Deps: []core.Dep{
		{Ecosystem: "actions", Name: "actions/checkout", Version: "v2", Direct: true, Line: 6, Manifest: wf},
		{Ecosystem: "actions", Name: "actions/cache", Version: "v3", Direct: true, Line: 3, Manifest: other},
	}}
	writeFile(t, inv.Root, wf.Path, "jobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - id: meta\n      - uses: actions/checkout@v2\n        run: echo \"::set-output name=sha::abc\"\n")
	writeFile(t, inv.Root, other.Path, "steps:\n  - run: make\n  - uses: actions/cache@v3\n")
	p, err := Build(context.Background(), Input{Inv: inv, Cfg: config.Default(), State: state.New(), Fetch: nowhere{}, Ecos: map[string]core.Ecosystem{"actions": actions}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range p.Queue {
		switch ch.ID {
		case "actions/actions/checkout@v5":
			if len(ch.Options) != 1 || !ch.Options[0].Safe || len(ch.Sites) != 2 || ch.Sites[1].Line != 7 {
				t.Fatalf("checkout = %+v", ch)
			}
			want := map[string]string{`echo "::set-output name=sha::abc"`: `echo "sha=abc" >> "$GITHUB_OUTPUT"`}
			if got := ch.Options[0].Codemod.Text; !maps.Equal(got, want) {
				t.Errorf("text = %q", got)
			}
		case "actions/actions/cache@v4":
			if len(ch.Options) != 0 {
				t.Errorf("cache got options for a workflow without retired commands: %+v", ch.Options)
			}
		default:
			t.Errorf("unexpected change %s", ch.ID)
		}
	}
}

func TestBuildGoMajor(t *testing.T) {
	gomod := &fakeEco{name: "go", imports: map[string][]string{"example.com/x": {"example.com/x"}}, metas: map[string]*core.Meta{
		"example.com/x": names([]core.Release{rel("v1.0.0", 300), rel("v1.1.0", 100), rel("v2.0.0", 200), rel("v3.1.0", 60)}),
	}}
	d := core.Dep{Ecosystem: "go", Name: "example.com/x", Version: "v1.0.0", Direct: true}
	p := build(t, nil, state.New(), []core.Dep{d}, map[string]core.Ecosystem{"go": gomod})
	if got := ids(p.Queue); !slices.Equal(got, []string{"breaking go/example.com/x@v3.1.0"}) {
		t.Fatalf("queue = %q, dropped %q", got, drops(p.Dropped))
	}
	if ch := p.Queue[0]; ch.Level != "major" || ch.ToName != "" {
		t.Errorf("change = %+v", ch)
	}
}

// An image at end of life and the registry's major bump to the same tag are one
// change, and the end of life is why it matters.
func TestMergeEndOfLife(t *testing.T) {
	pg := core.Dep{Ecosystem: core.Docker, Name: "library/postgres", Version: "11", Manifest: core.Manifest{Path: "compose.yml"}}
	eol := core.Evidence{Source: "eol", Title: "postgresql 11 end of life 2023-11-09", URL: "https://endoflife.date/postgresql"}
	for _, order := range [][2]int{{0, 1}, {1, 0}} {
		chs := []core.Change{
			{Kind: core.Breaking, Level: "major", Deps: []core.Dep{pg}, To: "18"},
			{Kind: core.Deprecation, Deps: []core.Dep{pg}, To: "18", Evidence: []core.Evidence{eol}, Sunset: ago(1000)},
		}
		got := merge([]core.Change{chs[order[0]], chs[order[1]]})
		if len(got) != 1 || got[0].Kind != core.Deprecation || got[0].Level != "major" || got[0].Sunset.IsZero() {
			t.Errorf("merged = %+v", got)
		}
	}
	api := merge([]core.Change{
		{Kind: core.Deprecation, API: &core.APIChange{Provider: "p", Method: "GET", Path: "/x"}},
		{Kind: core.Breaking, API: &core.APIChange{Provider: "p", Method: "GET", Path: "/x"}},
	})
	if len(api) != 1 || api[0].Kind != core.Breaking {
		t.Errorf("a removed operation lost to its deprecation: %+v", api)
	}
}

func TestMergeAPIField(t *testing.T) {
	removed := func(path, file string) core.Change {
		return core.Change{
			Kind:  core.Breaking,
			API:   &core.APIChange{Provider: "fixture", Method: "GET", Path: path, Field: "total_cents", What: "response field removed"},
			Sites: []core.Site{{File: file, Line: 3}},
		}
	}
	got := merge([]core.Change{
		removed("/v1/orders", "a.py"),
		removed("/v1/orders/{id}", "b.py"),
		{Kind: core.Deprecation, API: &core.APIChange{Provider: "fixture", Method: "GET", Path: "/v1/orders/{id}", Field: "total_cents", What: "response field deprecated"}},
	})
	if len(got) != 2 || got[0].API.Path != "/v1/orders" || len(got[0].Sites) != 2 {
		t.Errorf("merged = %+v", got)
	}
}

func TestGroups(t *testing.T) {
	npm := &fakeEco{name: "npm", metas: map[string]*core.Meta{
		"@aws-sdk/client-s3":  {Repo: "https://github.com/aws/aws-sdk-js-v3", Releases: []core.Release{rel("3.1.0", 90), rel("4.0.0", 10)}},
		"@aws-sdk/client-sqs": {Repo: "git+https://github.com/aws/aws-sdk-js-v3.git", Releases: []core.Release{rel("3.1.0", 90), rel("4.0.0", 10)}},
		"@aws-sdk/lib":        {Repo: "https://github.com/aws/aws-sdk-js-v3", Releases: []core.Release{rel("3.1.0", 90), rel("4.1.0", 10)}},
		"babel-core":          {Repo: "https://github.com/babel/babel", Releases: []core.Release{rel("6.0.0", 90), rel("7.0.0", 10)}},
		"babel-cli":           {Repo: "https://github.com/babel/babel", Releases: []core.Release{rel("6.0.0", 90), rel("7.0.0", 10)}},
		"solo":                {Releases: []core.Release{rel("1.0.0", 90), rel("2.0.0", 10)}},
	}}
	deps := []core.Dep{
		npmDep("@aws-sdk/client-s3", "3.1.0", "package.json"),
		npmDep("@aws-sdk/client-sqs", "3.1.0", "package.json"),
		npmDep("@aws-sdk/lib", "3.1.0", "package.json"),
		npmDep("babel-core", "6.0.0", "package.json"),
		npmDep("babel-cli", "6.0.0", "package.json"),
		npmDep("solo", "1.0.0", "package.json"),
	}
	cfg := config.Default()
	cfg.Groups = []config.Group{{Name: "babel", Match: []string{"babel-*"}}}
	cfg.Updates.Major = true
	p := build(t, cfg, state.New(), deps, map[string]core.Ecosystem{"npm": npm})
	want := []string{
		"update npm/@aws-sdk/lib@4.1.0",
		"update npm/group:aws-sdk/client@4.0.0",
		"update npm/group:babel@7.0.0",
		"update npm/solo@2.0.0",
	}
	got := ids(p.Queue)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("queue = %q\nwant    %q", got, want)
	}
	for _, ch := range p.Queue {
		if ch.ID == "npm/group:babel@7.0.0" && (len(ch.Deps) != 2 || ch.Deps[0].Name != "babel-cli") {
			t.Errorf("babel deps = %+v", ch.Deps)
		}
	}
}

// A bump takes along the packages that only resolve at its version, as
// @vitest/coverage-v8 pins vitest, so the lock tool can move them in one pass.
func TestCompanions(t *testing.T) {
	peer := func(v string, peers map[string]string) core.Release {
		r := rel(v, 90)
		r.Peers = peers
		return r
	}
	npm := &fakeEco{name: "npm", metas: map[string]*core.Meta{
		"vitest":              {Releases: []core.Release{rel("3.2.4", 90), rel("3.2.5", 10)}},
		"@vitest/coverage-v8": {Releases: []core.Release{peer("3.2.4", map[string]string{"vitest": "3.2.4"}), rel("3.2.5", 10)}},
		"@vitest/ui":          {Releases: []core.Release{rel("3.2.4", 90), rel("3.2.5", 10)}},
		"vite-plugin-test":    {Releases: []core.Release{peer("3.2.4", map[string]string{"vitest": "3.2.4"}), rel("3.2.5", 10)}},
		"@vitest/eslint":      {Releases: []core.Release{rel("1.1.0", 90), rel("1.2.0", 200)}},
		"@vitest/spy":         {Releases: []core.Release{rel("3.2.4", 90)}},
		"lodash":              {Releases: []core.Release{rel("3.2.4", 90), rel("3.2.5", 10)}},
	}}
	deps := []core.Dep{
		npmDep("vitest", "3.2.4", "package.json"),
		npmDep("@vitest/coverage-v8", "3.2.4", "package.json"),
		npmDep("@vitest/ui", "3.2.4", "web/package.json"),
		npmDep("vite-plugin-test", "3.2.4", "package.json"),
		npmDep("@vitest/eslint", "1.1.0", "package.json"),
		npmDep("@vitest/spy", "3.2.4", "package.json"),
		npmDep("lodash", "3.2.4", "package.json"),
	}
	cfg := config.Default()
	cfg.Updates.Patch = true
	p := build(t, cfg, state.New(), deps, map[string]core.Ecosystem{"npm": npm})
	want := []string{
		"update npm/@vitest/ui@3.2.5",
		"update npm/group:vitest@3.2.5",
		"update npm/lodash@3.2.5",
	}
	got := ids(p.Queue)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("queue = %q\nwant    %q", got, want)
	}
	for _, ch := range p.Queue {
		if ch.ID != "npm/group:vitest@3.2.5" {
			continue
		}
		// the coverage and plugin bumps of their own fold in; vitest stays first
		if names := core.Names(&ch); !slices.Equal(names, []string{"vitest", "@vitest/coverage-v8", "vite-plugin-test"}) {
			t.Errorf("names = %q", names)
		}
	}
}

// A security fix names the change it shares with a companion's own bump,
// whichever comes first in the list.
func TestCompanionsSecurityFirst(t *testing.T) {
	vitest, cov := npmDep("vitest", "3.2.4", "package.json"), npmDep("@vitest/coverage-v8", "3.2.4", "package.json")
	two := []core.Release{rel("3.2.4", 90), rel("3.2.5", 10)}
	b := &builder{in: Input{Inv: &core.Inventory{Deps: []core.Dep{vitest, cov}}}, meta: map[pkg]*core.Meta{
		{"npm", "vitest"}: {Releases: two}, {"npm", "@vitest/coverage-v8"}: {Releases: two},
	}}
	got := b.companions([]core.Change{
		{Kind: core.Update, Deps: []core.Dep{cov}, To: "3.2.5"},
		{Kind: core.Security, Severity: "high", Deps: []core.Dep{vitest}, To: "3.2.5"},
	})
	if len(got) != 1 {
		t.Fatalf("changes = %+v", got)
	}
	ch := got[0]
	if ch.Kind != core.Security || core.ChangeID(&ch) != "npm/group:vitest@3.2.5" || !slices.Equal(core.Names(&ch), []string{"vitest", "@vitest/coverage-v8"}) {
		t.Errorf("change = %s %s %q", ch.Kind, core.ChangeID(&ch), core.Names(&ch))
	}
}

func TestNoisePolicy(t *testing.T) {
	type tc struct {
		kind  core.Kind
		level string
		api   bool
	}
	cases := []tc{
		{core.Security, "patch", false}, {core.Security, "major", false},
		{core.Breaking, "major", false}, {core.Breaking, "minor", false}, {core.Breaking, "", true},
		{core.Update, "major", false},
		{core.Deprecation, "minor", false}, {core.Deprecation, "", true},
		{core.Update, "minor", false}, {core.Update, "patch", false},
	}
	flags := []string{"security", "deprecations", "major", "minor", "patch", "none"}
	keep := map[string][]string{
		"security":     {"security/patch", "security/major"},
		"deprecations": {"deprecation/minor", "deprecation/api"},
		"major":        {"update/major"},
		"minor":        {"update/minor"},
		"patch":        {"update/patch"},
		"none":         {},
	}
	for f := range keep {
		keep[f] = append(keep[f], "breaking/major", "breaking/minor", "breaking/api")
	}
	for _, flag := range flags {
		cfg := config.Default()
		cfg.Updates = config.Updates{
			Security: flag == "security", Deprecations: flag == "deprecations",
			Major: flag == "major", Minor: flag == "minor", Patch: flag == "patch",
		}
		b := &builder{in: Input{Cfg: cfg, State: state.New(), Now: now}}
		for _, c := range cases {
			ch := core.Change{Kind: c.kind, Level: c.level, Deps: []core.Dep{npmDep("x", "1.0.0", "package.json")}, To: "2.0.0"}
			key := string(c.kind) + "/" + c.level
			if c.api {
				ch.API, ch.Deps, key = &core.APIChange{Provider: "p"}, nil, string(c.kind)+"/api"
			}
			reason, _ := b.drop(&ch)
			want := slices.Contains(keep[flag], key)
			if (reason == "") != want {
				t.Errorf("updates.%s only: %s dropped as %q, want kept=%v", flag, key, reason, want)
			}
			if reason != "" && reason != "noise" {
				t.Errorf("updates.%s only: %s dropped as %q", flag, key, reason)
			}
		}
	}
}

func TestDrop(t *testing.T) {
	cfg := config.Default()
	cfg.Ignore = []config.Ignore{{Name: "pinned", Reason: "pinned on purpose"}, {Name: "stripe", Ecosystem: "api"}}
	st := state.New()
	st.Parked["npm/rej@2.0.0"] = state.Parked{PR: 4, Reason: "rejected", To: "2.0.0"}
	st.Parked["npm/pre@2.0.0"] = state.Parked{PR: 5, Reason: "preempted", To: "2.0.0"}
	st.Parked["api/stripe/GET /v1/x"] = state.Parked{PR: 6, Reason: "rejected"}
	st.Parked["api/fixture/GET /v1/orders"] = state.Parked{PR: 7, Reason: "rejected"}
	st.Seen["npm/flaky@3.0.0"] = state.Seen{Verdict: "unsupported", Detail: "npm test failed", Base: "abc"}
	st.Seen["npm/moved@3.0.0"] = state.Seen{Verdict: "unsupported", Detail: "npm test failed", Base: "old"}
	b := &builder{in: Input{Cfg: cfg, State: st, Now: now, Base: "abc"}}
	dep := func(name string) []core.Dep { return []core.Dep{npmDep(name, "1.0.0", "package.json")} }
	tests := []struct {
		name   string
		ch     core.Change
		reason string
	}{
		{"ignored", core.Change{Kind: core.Breaking, Level: "major", Deps: dep("pinned"), To: "2.0.0"}, "ignored"},
		{"ignored api", core.Change{Kind: core.Breaking, API: &core.APIChange{Provider: "stripe"}}, "ignored"},
		{"cooldown", core.Change{Kind: core.Breaking, Level: "major", Deps: dep("x"), To: "2.0.0", Published: ago(1)}, "cooldown"},
		{"security skips cooldown", core.Change{Kind: core.Security, Level: "patch", Deps: dep("x"), To: "1.0.1", Published: ago(1)}, ""},
		{"deprecation skips cooldown", core.Change{Kind: core.Deprecation, Level: "major", Deps: dep("x"), To: "2.0.0", Published: ago(1)}, ""},
		{"api changes skip cooldown", core.Change{Kind: core.Breaking, API: &core.APIChange{Provider: "p"}, Published: ago(0)}, ""},
		{"rejected same to", core.Change{ID: "npm/rej@2.0.0", Kind: core.Breaking, Level: "major", Deps: dep("rej"), To: "2.0.0"}, "parked"},
		{"rejected newer to", core.Change{ID: "npm/rej@2.1.0", Kind: core.Breaking, Level: "major", Deps: dep("rej"), To: "2.1.0"}, ""},
		{"preempted comes back", core.Change{ID: "npm/pre@2.0.0", Kind: core.Breaking, Level: "major", Deps: dep("pre"), To: "2.0.0"}, ""},
		{"rejected api", core.Change{ID: "api/fixture/GET /v1/orders", Kind: core.Deprecation, API: &core.APIChange{Provider: "fixture"}}, "parked"},
		{"proof failed on this base", core.Change{ID: "npm/flaky@3.0.0", Kind: core.Breaking, Level: "major", Deps: dep("flaky"), To: "3.0.0"}, "unsupported"},
		{"proof failed on an older base", core.Change{ID: "npm/moved@3.0.0", Kind: core.Breaking, Level: "major", Deps: dep("moved"), To: "3.0.0"}, ""},
		{"transitive maven", core.Change{Kind: core.Security, Deps: []core.Dep{{Ecosystem: "maven", Name: "g:a"}}, To: "1.2"}, "unsupported"},
		{"transitive npm", core.Change{Kind: core.Security, Deps: []core.Dep{{Ecosystem: "npm", Name: "deep"}}, To: "1.2.0"}, ""},
	}
	for _, tt := range tests {
		if reason, detail := b.drop(&tt.ch); reason != tt.reason {
			t.Errorf("%s: dropped as %q (%s), want %q", tt.name, reason, detail, tt.reason)
		}
	}
}

func TestRank(t *testing.T) {
	site := []core.Site{{File: "a"}}
	api := &core.APIChange{Provider: "p"}
	chs := []core.Change{
		{ID: "update", Kind: core.Update},
		{ID: "api-removed", Kind: core.Breaking, API: api},
		{ID: "deprecation-far", Kind: core.Deprecation, Sunset: now.Add(90 * 24 * time.Hour)},
		{ID: "deprecation-soon", Kind: core.Deprecation, Sunset: now.Add(10 * 24 * time.Hour)},
		{ID: "breaking-b", Kind: core.Breaking},
		{ID: "breaking-a", Kind: core.Breaking},
		{ID: "breaking-sites", Kind: core.Breaking, Sites: site},
		{ID: "breaking-old", Kind: core.Breaking, Published: ago(30)},
		{ID: "security-low", Kind: core.Security, Severity: "low"},
		{ID: "security-none", Kind: core.Security},
		{ID: "security-critical", Kind: core.Security, Severity: "critical"},
		{ID: "security-high-sunset", Kind: core.Security, Severity: "high", Sunset: now},
		{ID: "security-high", Kind: core.Security, Severity: "high"},
		{ID: "tidy-up", Kind: core.First},
	}
	Rank(chs, now)
	var got []string
	for _, ch := range chs {
		got = append(got, ch.ID)
	}
	want := []string{
		"tidy-up", "security-critical", "security-high-sunset", "security-high", "security-low", "security-none",
		"deprecation-soon", "api-removed", "deprecation-far",
		"breaking-sites", "breaking-old", "breaking-a", "breaking-b", "update",
	}
	if !slices.Equal(got, want) {
		t.Errorf("rank = %q\nwant   %q", got, want)
	}
}

func TestPick(t *testing.T) {
	sec := core.Change{ID: "npm/a@1.0.1", Kind: core.Security}
	sec2 := core.Change{ID: "npm/b@2.0.1", Kind: core.Security}
	brk := core.Change{ID: "npm/c@3.0.0", Kind: core.Breaking}
	brkNewer := core.Change{ID: "npm/c@3.1.0", Kind: core.Breaking}
	dep := core.Change{ID: "npm/d@1.0.0", Kind: core.Deprecation}
	first := core.Change{ID: core.FirstID, Kind: core.First}
	pr := func(n int, id string) *core.PR {
		return &core.PR{Number: n, Body: "body\n<!-- tend id=\"" + id + "\" base=\"abc\" rev=\"1\" -->\n"}
	}
	tests := []struct {
		name                string
		queue               []core.Change
		open                *core.PR
		pick, preempt, wait string
	}{
		{name: "nothing queued", queue: nil},
		{name: "nothing queued, PR open", queue: nil, open: pr(3, "npm/z@1.0.0"), wait: "#3"},
		{name: "no open PR", queue: []core.Change{brk, dep}, pick: "npm/c@3.0.0"},
		{name: "same change open", queue: []core.Change{brk, dep}, open: pr(4, "npm/d@1.0.0"), pick: "npm/d@1.0.0"},
		{name: "different change open", queue: []core.Change{brk}, open: pr(4, "npm/d@1.0.0"), wait: "#4"},
		{name: "security preempts", queue: []core.Change{sec, dep}, open: pr(4, "npm/d@1.0.0"), pick: "npm/a@1.0.1", preempt: "#4"},
		{name: "security preempts a vanished change", queue: []core.Change{sec}, open: pr(4, "npm/q@1.0.0"), pick: "npm/a@1.0.1", preempt: "#4"},
		{name: "security never preempts security", queue: []core.Change{sec, sec2}, open: pr(5, "npm/b@2.0.1"), pick: "npm/b@2.0.1"},
		{name: "newer target replaces", queue: []core.Change{brkNewer}, open: pr(6, "npm/c@3.0.0"), pick: "npm/c@3.1.0", preempt: "#6"},
		{name: "the first PR goes first", queue: []core.Change{first, brk}, pick: core.FirstID},
		{name: "the open first PR holds the rest back", queue: []core.Change{first, brk}, open: pr(1, core.FirstID), pick: core.FirstID},
		{name: "security does not preempt the first PR", queue: []core.Change{first, sec}, open: pr(1, core.FirstID), pick: core.FirstID},
		{name: "the first PR folds an open security fix in", queue: []core.Change{first, sec}, open: pr(4, "npm/a@1.0.1"), pick: core.FirstID, preempt: "#4"},
		{name: "the first PR folds any open PR in", queue: []core.Change{first, brk}, open: pr(4, "npm/z@1.0.0"), pick: core.FirstID, preempt: "#4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pick, preempt, wait := Pick(&Plan{Queue: tt.queue, Open: tt.open}, state.New())
			if got := idOf(pick); got != tt.pick {
				t.Errorf("pick = %q, want %q", got, tt.pick)
			}
			if got := num(preempt); got != tt.preempt {
				t.Errorf("preempt = %q, want %q", got, tt.preempt)
			}
			if got := num(wait); got != tt.wait {
				t.Errorf("wait = %q, want %q", got, tt.wait)
			}
		})
	}
}

func TestPickUsesState(t *testing.T) {
	st := state.New()
	st.Open = &state.OpenPR{ID: "npm/d@1.0.0", PR: 9}
	q := []core.Change{{ID: "npm/c@3.0.0", Kind: core.Breaking}, {ID: "npm/d@1.0.0", Kind: core.Deprecation}}
	pick, _, _ := Pick(&Plan{Queue: q, Open: &core.PR{Number: 9}}, st)
	if idOf(pick) != "npm/d@1.0.0" {
		t.Errorf("pick = %q", idOf(pick))
	}
}

func idOf(ch *core.Change) string {
	if ch == nil {
		return ""
	}
	return ch.ID
}

func num(pr *core.PR) string {
	if pr == nil {
		return ""
	}
	return fmt.Sprintf("#%d", pr.Number)
}

func TestParseCommand(t *testing.T) {
	at := func(m int) time.Time { return now.Add(time.Duration(m) * time.Minute) }
	tests := []struct {
		name string
		cs   []core.Comment
		want string
	}{
		{"none", nil, ""},
		{"owner apply", []core.Comment{{Role: "OWNER", Body: "tend apply 2", At: at(1)}}, "apply 2"},
		{"member skip", []core.Comment{{Role: "MEMBER", Body: "  tend skip\n", At: at(1)}}, "skip"},
		{"collaborator retry", []core.Comment{{Role: "COLLABORATOR", Body: "tend retry", At: at(1)}}, "retry"},
		{"contributor ignored", []core.Comment{{Role: "CONTRIBUTOR", Body: "tend skip", At: at(1)}}, ""},
		{"none role ignored", []core.Comment{{Role: "NONE", Body: "tend apply 1", At: at(1)}}, ""},
		{"must be the whole comment", []core.Comment{{Role: "OWNER", Body: "please tend skip", At: at(1)}}, ""},
		{"apply needs a number", []core.Comment{{Role: "OWNER", Body: "tend apply", At: at(1)}}, ""},
		{"apply 0 is not an option", []core.Comment{{Role: "OWNER", Body: "tend apply 0", At: at(1)}}, ""},
		{"unknown verb", []core.Comment{{Role: "OWNER", Body: "tend merge", At: at(1)}}, ""},
		{"a bot with write access is ignored", []core.Comment{{Author: "renovate[bot]", Role: "MEMBER", Body: "tend skip", At: at(1)}}, ""},
		{
			"a later bot comment does not outrank the maintainer",
			[]core.Comment{
				{Author: "ada", Role: "OWNER", Body: "tend retry", At: at(1)},
				{Author: "ci-bot[bot]", Role: "COLLABORATOR", Body: "tend skip", At: at(2)},
			},
			"retry",
		},
		{
			"newest wins",
			[]core.Comment{
				{Role: "OWNER", Body: "tend apply 1", At: at(1)},
				{Role: "MEMBER", Body: "tend apply 2", At: at(3)},
				{Role: "NONE", Body: "tend skip", At: at(4)},
				{Role: "OWNER", Body: "tend retry", At: at(2)},
			},
			"apply 2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, ok := ParseCommand(tt.cs)
			got := ""
			if ok {
				got = c.Verb
				if c.N > 0 {
					got += fmt.Sprint(" ", c.N)
				}
			}
			if got != tt.want {
				t.Errorf("command = %q, want %q", got, tt.want)
			}
		})
	}
}

func writeFile(t *testing.T, root, name, body string) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInventorySkipsFixtures(t *testing.T) {
	need(t, "git")
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.24\n\nrequire golang.org/x/text v0.3.0\n")
	writeFile(t, root, "main.go", "package main\n\nimport _ \"golang.org/x/text/language\"\n\nfunc main() {}\n")
	writeFile(t, root, "testdata/fixture/go.mod", "module example.com/fixture\n\ngo 1.24\n\nrequire golang.org/x/net v0.1.0\n")
	writeFile(t, root, "internal/x/testdata/package.json", `{"dependencies": {"left-pad": "1.0.0"}}`)
	writeFile(t, root, ".gitmodules", "[submodule \"proto\"]\n\tpath = third_party/proto\n\turl = https://github.com/acme/proto.git\n")
	inv, notes, err := Inventory(root, config.Default())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range inv.Manifests {
		got = append(got, m.Ecosystem+" "+m.Path)
	}
	if !slices.Equal(got, []string{"go go.mod", "toolchain go.mod"}) {
		t.Errorf("manifests = %q", got)
	}
	if len(notes) != 1 || notes[0].What != "third_party/proto" || !strings.Contains(notes[0].Why, "submodule") {
		t.Errorf("notes = %+v; a submodule is untracked", notes)
	}
	for _, f := range inv.Index.Files() {
		if strings.Contains(f, "testdata") {
			t.Errorf("scanned %s", f)
		}
	}
}

func need(t *testing.T, tool string) {
	t.Helper()
	if _, err := exec.LookPath(tool); err != nil {
		t.Skipf("%s not found in PATH", tool)
	}
}

func TestHint(t *testing.T) {
	tests := []struct {
		msg, old, now string
	}{
		{"1.x is deprecated, use 2.x: charges.* was replaced by paymentIntents.*", "charges", "paymentIntents"},
		{"`Client.connect` has been renamed to `Client.open`", "Client.connect", "Client.open"},
		{"Deprecated: use createClient() instead of connect().", "connect", "createClient"},
		{"please use `fetchAll` instead of `getAll`", "getAll", "fetchAll"},
		{"`v1.orders` -> `v2.orders`", "v1.orders", "v2.orders"},
		{"use 2.x instead of 1.x", "", ""},
		{"this version is no longer supported", "", ""},
		{"request has been deprecated, see https://github.com/request/request/issues/3142", "", ""},
	}
	for _, tt := range tests {
		old, now, ok := hint(tt.msg)
		if old != tt.old || now != tt.now || ok != (tt.old != "") {
			t.Errorf("hint(%q) = %q, %q, %v; want %q, %q", tt.msg, old, now, ok, tt.old, tt.now)
		}
	}
}

func TestDeprecationOptions(t *testing.T) {
	pay := npmDep("@fixture/pay", "1.4.0", "package.json")
	ch := core.Change{
		Kind: core.Deprecation, Deps: []core.Dep{pay}, To: "2.0.0",
		Evidence: []core.Evidence{deprecated(pay, "1.4.0", "1.x is deprecated, use 2.x: charges.* was replaced by paymentIntents.*")},
		Sites: []core.Site{
			{File: "src/billing.js", Line: 12, Chain: []string{"pay", "charges", "create"}, Call: true},
			{File: "src/billing.js", Line: 20, Chain: []string{"pay", "charges", "retrieve"}, Call: true},
			{File: "src/billing.js", Line: 30, Chain: []string{"pay", "customers", "list"}, Call: true},
		},
	}
	deprecationOptions(&ch)
	if len(ch.Options) != 2 {
		t.Fatalf("options = %+v", ch.Options)
	}
	rename, pin := ch.Options[0], ch.Options[1]
	want := map[string]string{"charges.create": "paymentIntents.create", "charges.retrieve": "paymentIntents.retrieve"}
	if rename.Safe || rename.To != "2.0.0" || rename.Codemod == nil || !maps.Equal(rename.Codemod.Renames, want) {
		t.Errorf("rename = %+v", rename)
	}
	if rename.Title != "Apply the rename hinted by the deprecation notice (charges.* -> paymentIntents.*)" {
		t.Errorf("rename title %q", rename.Title)
	}
	if pin.Safe || pin.To != "1.4.0" || pin.Title != "Pin to 1.4.0 and ignore" || !strings.Contains(pin.Detail, "  - name: \"@fixture/pay\"\n    versions: [\">=2.0.0\"]") {
		t.Errorf("pin = %+v", pin)
	}

	plain := core.Change{Kind: core.Deprecation, Deps: []core.Dep{pay}, To: "2.0.0", Evidence: []core.Evidence{deprecated(pay, "1.4.0", "no longer maintained")}}
	deprecationOptions(&plain)
	if len(plain.Options) != 1 || plain.Options[0].To != "1.4.0" {
		t.Errorf("without a hint: %+v", plain.Options)
	}
	eol := core.Change{Kind: core.Deprecation, Deps: []core.Dep{pay}, To: "2.0.0", Evidence: []core.Evidence{{Source: "eol"}}}
	if deprecationOptions(&eol); len(eol.Options) != 0 {
		t.Errorf("options without a registry notice: %+v", eol.Options)
	}
	whole := core.Change{Kind: core.Deprecation, Deps: []core.Dep{pay}, Evidence: []core.Evidence{deprecated(pay, "", "no longer maintained")}}
	deprecationOptions(&whole)
	if len(whole.Options) != 1 || whole.Options[0].To != "1.4.0" || strings.Contains(whole.Options[0].Detail, "versions:") {
		t.Errorf("a package deprecated as a whole: %+v", whole.Options)
	}
}

func TestStale(t *testing.T) {
	inv := &core.Inventory{Deps: []core.Dep{
		npmDep("a", "1.2.0", "package.json"),
		npmDep("b", "1.0.0", "package.json"),
		npmDep("c", "1.0.0", "package.json"),
	}}
	p := &Plan{Dropped: []Drop{
		{Change: core.Change{ID: "npm/c@2.0.0"}, Reason: "ignored", Detail: "pinned on purpose"},
		{Change: core.Change{ID: "npm/b@1.0.2"}, Reason: "cooldown", Detail: "released yesterday"},
	}}
	tests := []struct{ id, want string }{
		{"npm/a@1.2.0", "a is at 1.2.0 already"},
		{"npm/a@1.3.0", ""},
		{"npm/b@1.0.1", ""},
		{"npm/c@1.5.0", "tend.yml ignores it now (pinned on purpose)"},
		{"npm/gone@2.0.0", "gone is no longer a dependency"},
		{"npm/gone->other@2.0.0", "gone is no longer a dependency"},
		{"npm/a->other@2.0.0", ""},
		{"npm/group:aws@3.0.0", ""},
		{"api/stripe/GET /v1/x", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := Stale(p, inv, tt.id); got != tt.want {
			t.Errorf("Stale(%q) = %q, want %q", tt.id, got, tt.want)
		}
	}
}
