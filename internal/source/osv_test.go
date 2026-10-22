package source

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
)

type osvHits map[string][]string // "name@version" -> ids

// osvServer answers querybatch from hits and vulns from details.
func osvServer(hits osvHits, details map[string]any) *fake {
	return &fake{h: func(method, url string, body []byte) *core.Response {
		if method == "POST" {
			var req struct {
				Queries []osvQuery `json:"queries"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				return status(400)
			}
			var results []map[string]any
			for _, q := range req.Queries {
				var vulns []map[string]string
				for _, id := range hits[q.Package.Name+"@"+q.Version] {
					vulns = append(vulns, map[string]string{"id": id})
				}
				results = append(results, map[string]any{"vulns": vulns})
			}
			return ok(map[string]any{"results": results})
		}
		d, found := details[strings.TrimPrefix(url, osvVuln)]
		if !found {
			return status(404)
		}
		return ok(d)
	}}
}

func affected(name, eco string, events ...map[string]string) map[string]any {
	return map[string]any{
		"package": map[string]string{"name": name, "ecosystem": eco},
		"ranges":  []any{map[string]any{"type": "SEMVER", "events": events}},
	}
}

func fixed(v string) map[string]string      { return map[string]string{"fixed": v} }
func introduced(v string) map[string]string { return map[string]string{"introduced": v} }

func TestOSVQueries(t *testing.T) {
	inv := &core.Inventory{Deps: []core.Dep{
		dep(core.Npm, "a", "1.0.0"),
		dep(core.Npm, "a", "1.0.0"), // same package in a second manifest
		dep(core.Go, "example.com/b", "v1.2.3"),
		dep(core.Go, "example.com/c", "v2.0.0+incompatible"),
		dep(core.Gradle, "org.x:y", "2.0"),
		dep(core.Maven, "org.x:z", "2.1"),
		dep(core.Pip, "pkg", "3.0"),
		dep(core.Cargo, "crate", "0.1.0"),
		dep(core.Gems, "gem", "1.1"),
		dep(core.Composer, "v/p", "5.0.0"),
		dep(core.Nuget, "nu", "1.0.0"),
		dep(core.Actions, "o/full", "v4.1.0"),
		dep(core.Actions, "o/major", "v4"),
		dep(core.Actions, "o/sha", "11bd71901bbe5b1630ceea73d27597364c9af683"),
		dep(core.Docker, "library/node", "16-alpine"),
		dep(core.Npm, "unversioned", ""),
	}}
	f := osvServer(nil, nil)
	if _, err := (&OSV{Fetch: f}).Changes(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("%d calls, want 1", len(f.calls))
	}
	var got struct {
		Queries []osvQuery `json:"queries"`
	}
	if err := json.Unmarshal(f.calls[0].Body, &got); err != nil {
		t.Fatal(err)
	}
	var have []string
	for _, q := range got.Queries {
		have = append(have, fmt.Sprintf("%s %s %s", q.Package.Ecosystem, q.Package.Name, q.Version))
	}
	want := []string{
		"npm a 1.0.0",
		"Go example.com/b 1.2.3",
		"Go example.com/c 2.0.0",
		"Maven org.x:y 2.0",
		"Maven org.x:z 2.1",
		"PyPI pkg 3.0",
		"crates.io crate 0.1.0",
		"RubyGems gem 1.1",
		"Packagist v/p 5.0.0",
		"NuGet nu 1.0.0",
		"GitHub Actions o/full 4.1.0",
	}
	if !reflect.DeepEqual(have, want) {
		t.Errorf("queries:\n%s\nwant:\n%s", strings.Join(have, "\n"), strings.Join(want, "\n"))
	}
}

func TestOSVBatches(t *testing.T) {
	inv := &core.Inventory{}
	for i := range 1203 {
		inv.Deps = append(inv.Deps, dep(core.Npm, fmt.Sprintf("pkg-%d", i), "1.0.0"))
	}
	f := osvServer(nil, nil)
	if _, err := (&OSV{Fetch: f}).Changes(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	var sizes []int
	for _, c := range f.calls {
		var req struct {
			Queries []osvQuery `json:"queries"`
		}
		_ = json.Unmarshal(c.Body, &req)
		sizes = append(sizes, len(req.Queries))
		if c.URL != "https://api.osv.dev/v1/querybatch" {
			t.Errorf("posted to %s", c.URL)
		}
		if !c.Opts.Idempotent {
			t.Error("a batch lookup should be marked safe to retry")
		}
	}
	if want := []int{500, 500, 203}; !reflect.DeepEqual(sizes, want) {
		t.Errorf("batch sizes = %v, want %v", sizes, want)
	}
}

func TestOSVPaging(t *testing.T) {
	var tokens []string
	f := &fake{h: func(method, url string, body []byte) *core.Response {
		if method == "GET" {
			id := strings.TrimPrefix(url, osvVuln)
			return ok(map[string]any{"id": id, "summary": "s " + id, "affected": []any{affected("a", "npm", introduced("0"), fixed("1.1."+id[len(id)-1:]))}})
		}
		var req struct {
			Queries []osvQuery `json:"queries"`
		}
		_ = json.Unmarshal(body, &req)
		if len(req.Queries) != 1 {
			return status(400)
		}
		tok := req.Queries[0].PageToken
		tokens = append(tokens, tok)
		switch tok {
		case "":
			return text(`{"results":[{"vulns":[{"id":"V1"},{"id":"V2"}],"next_page_token":"p2"}]}`)
		case "p2":
			return text(`{"results":[{"vulns":[{"id":"V3"}],"next_page_token":"p3"}]}`)
		}
		return text(`{"results":[{"vulns":[{"id":"V4"}]}]}`)
	}}
	inv := &core.Inventory{Deps: []core.Dep{dep(core.Npm, "a", "1.0.0")}}
	got, err := (&OSV{Fetch: f}).Changes(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"", "p2", "p3"}; !reflect.DeepEqual(tokens, want) {
		t.Errorf("page tokens = %v, want %v", tokens, want)
	}
	var tos []string
	for _, c := range got {
		tos = append(tos, c.To+":"+fmt.Sprint(len(c.Evidence)))
	}
	if want := []string{"1.1.1:1", "1.1.2:1", "1.1.3:1", "1.1.4:1"}; !reflect.DeepEqual(tos, want) {
		t.Errorf("changes = %v, want %v", tos, want)
	}
}

func TestOSVChanges(t *testing.T) {
	long := strings.Repeat("é", 900) // 1800 bytes
	hits := osvHits{
		"a@1.2.0":             {"A1", "A2", "A3", "GONE", "W"},
		"example.com/m@1.4.0": {"G1"},
		"left@1.0.0":          {"L1"},
		"some-pkg@1.0.0":      {"P1", "P2"},
	}
	details := map[string]any{
		"A1": map[string]any{
			"id": "A1", "summary": "first", "details": long, "published": "2026-03-02T10:00:00Z",
			"database_specific": map[string]any{"severity": "MODERATE"},
			"affected":          []any{affected("a", "npm", introduced("0"), fixed("1.2.5"))},
		},
		"A2": map[string]any{
			"id": "A2", "summary": "second", "published": "2026-03-01T10:00:00Z",
			"database_specific": map[string]any{"severity": "CRITICAL"},
			"affected":          []any{affected("a", "npm", introduced("1.0.0"), fixed("1.2.5"))},
		},
		"A3": map[string]any{
			"id": "A3", "summary": "third", "published": "2026-04-01T10:00:00Z",
			"severity": []any{map[string]string{"type": "CVSS_V3", "score": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:H"}},
			"affected": []any{affected("a", "npm", introduced("0"), fixed("1.3.0"))},
		},
		"W": map[string]any{
			"id": "W", "summary": "retracted", "withdrawn": "2026-05-01T00:00:00Z",
			"affected": []any{affected("a", "npm", introduced("0"), fixed("9.9.9"))},
		},
		"G1": map[string]any{
			"id": "GO-1", "summary": "go", "published": "2026-01-01T00:00:00Z",
			"affected": []any{affected("example.com/m", "Go", introduced("0"), fixed("1.4.2"))},
		},
		"L1": map[string]any{
			"id": "L1", "summary": "unfixed", "published": "2026-01-01T00:00:00Z",
			"database_specific": map[string]any{"severity": "HIGH"},
			"affected":          []any{affected("left", "npm", introduced("0"))},
		},
		"P1": map[string]any{
			"id": "P1", "summary": "py", "published": "2026-01-01T00:00:00Z",
			"affected": []any{affected("Some_Pkg", "PyPI", introduced("0"), fixed("1.0.1"))},
		},
		"P2": map[string]any{
			"id": "P2", "summary": "other package", "published": "2026-01-01T00:00:00Z",
			"affected": []any{affected("some-pkg-other", "PyPI", introduced("0"), fixed("1.0.1"))},
		},
	}
	inv := &core.Inventory{Deps: []core.Dep{
		dep(core.Npm, "a", "1.2.0"),
		dep(core.Go, "example.com/m", "v1.4.0"),
		dep(core.Npm, "left", "1.0.0"),
		dep(core.Pip, "some-pkg", "1.0.0"),
	}}
	f := osvServer(hits, details)
	got, err := (&OSV{Fetch: f}).Changes(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 {
		t.Fatalf("%d changes, want 6: %+v", len(got), got)
	}

	merged := got[0]
	if merged.ID != "npm/a@1.2.5" || merged.Kind != core.Security || merged.To != "1.2.5" || merged.Level != "patch" {
		t.Errorf("merged = %+v", merged)
	}
	if merged.Severity != "critical" {
		t.Errorf("severity = %q, want the highest of the merged advisories", merged.Severity)
	}
	if want := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC); !merged.Published.Equal(want) {
		t.Errorf("published = %v, want the earliest, %v", merged.Published, want)
	}
	if len(merged.Evidence) != 2 {
		t.Fatalf("evidence = %+v, want both advisories", merged.Evidence)
	}
	ev := merged.Evidence[0]
	if ev.Source != "osv" || ev.Title != "A1 first" || ev.URL != "https://osv.dev/vulnerability/A1" {
		t.Errorf("evidence = %+v", ev)
	}
	if len(ev.Text) > 1200 || len(ev.Text) < 1190 || !strings.HasPrefix(ev.Text, "éé") {
		t.Errorf("text is %d bytes, want it cut near 1200 on a rune boundary", len(ev.Text))
	}

	next := got[1]
	if next.ID != "npm/a@1.3.0" || next.Severity != "high" || len(next.Evidence) != 1 {
		t.Errorf("second change = %+v", next)
	}
	if next.Level != "minor" {
		t.Errorf("level = %q, want minor", next.Level)
	}

	goChange := got[2]
	if goChange.To != "v1.4.2" || goChange.ID != "go/example.com/m@v1.4.2" || goChange.Severity != "" {
		t.Errorf("go change = %+v", goChange)
	}

	py, orphan := got[4], got[5]
	if py.To != "1.0.1" || orphan.To != "" {
		t.Errorf("pip changes = %+v and %+v; want the fix found through a differently spelled name, and an advisory that names only another package kept as unfixed", py, orphan)
	}

	nofix := got[3]
	if nofix.To != "" || nofix.Severity != "high" || nofix.Kind != core.Security || len(nofix.Evidence) != 1 {
		t.Errorf("unfixed change = %+v", nofix)
	}

	if len(f.urls("GET")) != 9 {
		t.Errorf("details fetched %d times, want once per distinct advisory: %v", len(f.urls("GET")), f.urls("GET"))
	}
	for _, c := range f.calls {
		if c.Method == "GET" && c.Opts.TTL != 24*time.Hour {
			t.Errorf("%s cached for %v, want 24h", c.URL, c.Opts.TTL)
		}
	}
}

func TestOSVFailures(t *testing.T) {
	inv := &core.Inventory{Deps: []core.Dep{dep(core.Npm, "a", "1.0.0")}}
	tests := []struct {
		name string
		h    func(method, url string, body []byte) *core.Response
	}{
		{"batch status", func(method, url string, body []byte) *core.Response { return status(500) }},
		{"batch garbage", func(method, url string, body []byte) *core.Response { return text("<html>") }},
		{"short results", func(method, url string, body []byte) *core.Response { return text(`{"results":[]}`) }},
		{"detail status", func(method, url string, body []byte) *core.Response {
			if method == "POST" {
				return text(`{"results":[{"vulns":[{"id":"X"}]}]}`)
			}
			return status(503)
		}},
		{"detail garbage", func(method, url string, body []byte) *core.Response {
			if method == "POST" {
				return text(`{"results":[{"vulns":[{"id":"X"}]}]}`)
			}
			return text("nope")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := (&OSV{Fetch: &fake{h: tc.h}}).Changes(context.Background(), inv); err == nil {
				t.Fatal("no error")
			}
		})
	}
}

func TestOSVMissingDetail(t *testing.T) {
	f := osvServer(osvHits{"a@1.0.0": {"X"}}, nil)
	got, err := (&OSV{Fetch: f}).Changes(context.Background(), &core.Inventory{Deps: []core.Dep{dep(core.Npm, "a", "1.0.0")}})
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want a 404 advisory skipped", got, err)
	}
}

func TestSmallestFixed(t *testing.T) {
	tests := []struct {
		cur   string
		fixes []string
		want  string
	}{
		{"1.2.0", []string{"1.2.5", "1.3.1", "1.1.0"}, "1.2.5"},
		{"1.2.0", []string{"1.3.1", "1.2.5"}, "1.2.5"},
		{"1.2.0", []string{"1.1.0", "0.9"}, ""},
		{"1.2.0", []string{"1.2.0"}, ""},
		{"1.2.0", nil, ""},
		{"1.2.0", []string{"garbage", "2.0.0"}, "2.0.0"},
		{"1.2.0", []string{"1.2.1-rc.1", "1.2.1"}, "1.2.1-rc.1"},
		{"4.17.15", []string{"4.17.12", "4.17.21"}, "4.17.21"},
		{"bad", []string{"1.0.0"}, ""},
	}
	for _, tc := range tests {
		if got := smallestFixed(tc.cur, tc.fixes); got != tc.want {
			t.Errorf("smallestFixed(%q, %v) = %q, want %q", tc.cur, tc.fixes, got, tc.want)
		}
	}
}

func TestOSVFixes(t *testing.T) {
	e := &osvEntry{}
	err := json.Unmarshal([]byte(`{"affected": [
		{"package": {"name": "a", "ecosystem": "npm"}, "ranges": [
			{"type": "SEMVER", "events": [{"introduced": "0"}, {"fixed": "1.0.1"}]},
			{"type": "ECOSYSTEM", "events": [{"introduced": "2.0.0"}, {"fixed": "v2.0.4"}]},
			{"type": "GIT", "events": [{"introduced": "0"}, {"fixed": "deadbeef"}]}]},
		{"package": {"name": "A", "ecosystem": "npm"}, "ranges": [
			{"type": "SEMVER", "events": [{"fixed": "3.0.0"}]}]},
		{"package": {"name": "a", "ecosystem": "PyPI"}, "ranges": [
			{"type": "SEMVER", "events": [{"fixed": "4.0.0"}]}]},
		{"package": {"name": "b", "ecosystem": "npm"}, "ranges": [
			{"type": "SEMVER", "events": [{"fixed": "5.0.0"}]}]},
		{"package": {"name": "a", "ecosystem": "npm"}, "versions": ["1.0.0"]}
	]}`), e)
	if err != nil {
		t.Fatal(err)
	}
	got := e.fixes(dep(core.Npm, "a", "1.0.0"), osvQuery{Package: osvPkg{Name: "a", Ecosystem: "npm"}})
	if want := []string{"1.0.1", "2.0.4", "3.0.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("fixes = %v, want %v", got, want)
	}
}

func TestOSVSeverity(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{"github critical", `{"database_specific": {"severity": "CRITICAL"}}`, "critical"},
		{"github moderate", `{"database_specific": {"severity": "MODERATE"}}`, "moderate"},
		{"medium is moderate", `{"database_specific": {"severity": "medium"}}`, "moderate"},
		{"low", `{"database_specific": {"severity": "LOW"}}`, "low"},
		{"database wins over vector", `{"database_specific": {"severity": "LOW"}, "severity": [{"type": "CVSS_V3", "score": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"}]}`, "low"},
		{"junk database severity falls to vector", `{"database_specific": {"severity": "UNKNOWN"}, "severity": [{"type": "CVSS_V3", "score": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"}]}`, "critical"},
		{"non string database severity", `{"database_specific": {"severity": 7}}`, ""},
		{"vector 9.8", `{"severity": [{"type": "CVSS_V3", "score": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"}]}`, "critical"},
		{"vector 7.5", `{"severity": [{"type": "CVSS_V3", "score": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:H"}]}`, "high"},
		{"vector 5.3", `{"severity": [{"type": "CVSS_V3", "score": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:L/I:N/A:N"}]}`, "moderate"},
		{"vector 1.8", `{"severity": [{"type": "CVSS_V3", "score": "CVSS:3.1/AV:L/AC:H/PR:H/UI:R/S:U/C:L/I:N/A:N"}]}`, "low"},
		{"bare number", `{"severity": [{"type": "CVSS_V3", "score": "8.8"}]}`, "high"},
		{"v4 vector alone", `{"severity": [{"type": "CVSS_V4", "score": "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:N/SI:N/SA:N"}]}`, ""},
		{"v4 then v3", `{"severity": [{"type": "CVSS_V4", "score": "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:N/SI:N/SA:N"}, {"type": "CVSS_V3", "score": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:H"}]}`, "high"},
		{"nothing", `{}`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var e osvEntry
			if err := json.Unmarshal([]byte(tc.json), &e); err != nil {
				t.Fatal(err)
			}
			if got := e.band(); got != tc.want {
				t.Errorf("band = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCVSS3(t *testing.T) {
	tests := []struct {
		vec  string
		want float64
		ok   bool
	}{
		{"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H", 9.8, true},
		{"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:H/A:H", 9.1, true},
		{"CVSS:3.1/AV:N/AC:H/PR:N/UI:N/S:U/C:H/I:H/A:H", 8.1, true},
		{"CVSS:3.1/AV:N/AC:L/PR:N/UI:R/S:C/C:L/I:L/A:N", 6.1, true},
		{"CVSS:3.1/AV:N/AC:L/PR:L/UI:N/S:C/C:H/I:H/A:H", 9.9, true},
		{"CVSS:3.0/AV:L/AC:L/PR:H/UI:N/S:U/C:H/I:H/A:H", 6.7, true},
		{"CVSS:3.1/AV:P/AC:H/PR:H/UI:R/S:U/C:N/I:N/A:N", 0, true},
		{"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H", 0, false},
		{"CVSS:3.1/AV:X/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H", 0, false},
		{"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:Z/C:H/I:H/A:H", 0, false},
		{"CVSS:3.1/AV:N/AC:L/PR:Q/UI:N/S:U/C:H/I:H/A:H", 0, false},
		{"CVSS:4.0/AV:N", 0, false},
		{"AV:N/AC:L", 0, false},
	}
	for _, tc := range tests {
		got, ok := cvss3(tc.vec)
		if ok != tc.ok || got != tc.want {
			t.Errorf("cvss3(%q) = %v, %v; want %v, %v", tc.vec, got, ok, tc.want, tc.ok)
		}
	}
}

func TestOSVReplay(t *testing.T) {
	got, err := (&OSV{Fetch: replay()}).Changes(context.Background(), osvInventory())
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		ID, Severity string
		Evidence     int
	}
	var have []row
	for _, c := range got {
		have = append(have, row{c.ID, c.Severity, len(c.Evidence)})
	}
	want := []row{
		{"npm/minimist@1.2.3", "moderate", 1},
		{"npm/minimist@1.2.6", "critical", 1},
		// GHSA-w73w-5m7g-f7qc and GO-2020-0017 are one issue, listed under both ids.
		{"go/github.com/dgrijalva/jwt-go@", "high", 1},
	}
	if !reflect.DeepEqual(have, want) {
		t.Fatalf("changes = %+v, want %+v", have, want)
	}
	jwt := got[2]
	if jwt.Deps[0].Version != "v3.2.0+incompatible" || jwt.To != "" {
		t.Errorf("jwt-go change = %+v", jwt)
	}
	if jwt.Evidence[0].Title != "GHSA-w73w-5m7g-f7qc Authorization bypass in github.com/dgrijalva/jwt-go" {
		t.Errorf("title = %q", jwt.Evidence[0].Title)
	}
}

// A version tend.yml points at an npm package is asked about as that package,
// and the change it gets is still about the line in the file.
func TestOSVUpstream(t *testing.T) {
	pin := core.Dep{Ecosystem: core.Custom, Name: "esbuild", Version: "0.19.2", Direct: true}
	up := func(d core.Dep) (core.Dep, bool) {
		if d.Ecosystem != core.Custom {
			return d, false
		}
		d.Ecosystem = core.Npm
		return d, true
	}
	f := osvServer(osvHits{"esbuild@0.19.2": {"GHSA-67mh-4wv8-2f99"}}, map[string]any{
		"GHSA-67mh-4wv8-2f99": map[string]any{
			"id": "GHSA-67mh-4wv8-2f99", "summary": "dev server reads any file",
			"affected": []any{affected("esbuild", "npm", introduced("0"), fixed("0.25.0"))},
		},
	})
	got, err := (&OSV{Fetch: f, Upstream: up}).Changes(context.Background(), &core.Inventory{Deps: []core.Dep{pin}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].To != "0.25.0" || got[0].Deps[0].Ecosystem != core.Custom {
		t.Fatalf("changes = %+v", got)
	}
}

// A tend.yml entry named apart from the package it follows still gets the
// package's fix.
func TestOSVUpstreamRenamed(t *testing.T) {
	pin := core.Dep{Ecosystem: core.Custom, Name: "bundler", Version: "0.19.2", Direct: true}
	up := func(d core.Dep) (core.Dep, bool) {
		if d.Ecosystem != core.Custom {
			return d, false
		}
		d.Ecosystem, d.Name = core.Npm, "esbuild"
		return d, true
	}
	f := osvServer(osvHits{"esbuild@0.19.2": {"GHSA-67mh-4wv8-2f99"}}, map[string]any{
		"GHSA-67mh-4wv8-2f99": map[string]any{
			"id": "GHSA-67mh-4wv8-2f99", "summary": "dev server reads any file",
			"affected": []any{affected("esbuild", "npm", introduced("0"), fixed("0.25.0"))},
		},
	})
	got, err := (&OSV{Fetch: f, Upstream: up}).Changes(context.Background(), &core.Inventory{Deps: []core.Dep{pin}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].To != "0.25.0" || got[0].Deps[0].Name != "bundler" {
		t.Fatalf("changes = %+v", got)
	}
}
