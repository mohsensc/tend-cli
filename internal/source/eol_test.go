package source

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
)

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func rel(name, codename, released, eol string, lts, isEol bool) map[string]any {
	maintained := !isEol
	r := map[string]any{
		"name": name, "codename": nilIfEmpty(codename), "releaseDate": released,
		"isLts": lts, "isEol": isEol, "isMaintained": maintained,
	}
	if eol != "" {
		r["eolFrom"] = eol
	}
	return r
}

func product1(releases ...map[string]any) *core.Response {
	return ok(map[string]any{"result": map[string]any{"releases": releases}})
}

func eolFake(products map[string]*core.Response) *fake {
	m := map[string]*core.Response{}
	for k, v := range products {
		m["GET "+eolURL+k] = v
	}
	return routes(m)
}

func TestSplitTag(t *testing.T) {
	tests := []struct{ tag, head, suffix string }{
		{"16-alpine", "16", "-alpine"},
		{"3.9-slim", "3.9", "-slim"},
		{"22.04", "22.04", ""},
		{"16.20.2-alpine3.18", "16.20.2", "-alpine3.18"},
		{"11", "11", ""},
		{"bookworm-slim", "bookworm", "-slim"},
		{"jammy", "jammy", ""},
		{"latest", "latest", ""},
		{"8.0.", "8.0", "."},
		{"", "", ""},
		{"-x", "", "-x"},
	}
	for _, tc := range tests {
		head, suffix := splitTag(tc.tag)
		if head != tc.head || suffix != tc.suffix {
			t.Errorf("splitTag(%q) = %q, %q; want %q, %q", tc.tag, head, suffix, tc.head, tc.suffix)
		}
	}
}

func TestFindCycle(t *testing.T) {
	py := []eolRelease{{Name: "3.15"}, {Name: "3.14"}, {Name: "3.9"}, {Name: "3.1"}}
	dn := []eolRelease{{Name: "10"}, {Name: "9"}, {Name: "8"}}
	pg := []eolRelease{{Name: "18"}, {Name: "11"}, {Name: "9.6"}}
	ub := []eolRelease{{Name: "24.04", Codename: "Noble Numbat"}, {Name: "22.04", Codename: "Jammy Jellyfish"}}
	tests := []struct {
		name  string
		rels  []eolRelease
		head  string
		cycle string
		extra []string
	}{
		{"two components", py, "3.9", "3.9", []string{}},
		{"patch is dropped", py, "3.9.18", "3.9", []string{"18"}},
		{"3.1 is not 3.14", py, "3.1", "3.1", []string{}},
		{"one component only", py, "3", "", nil},
		{"unknown minor", py, "3.99", "", nil},
		{"dotnet 8.0 is 8", dn, "8.0", "8", []string{"0"}},
		{"dotnet 8.0.100", dn, "8.0.100", "8", []string{"0", "100"}},
		{"postgres 11", pg, "11", "11", []string{}},
		{"postgres 11.4 is 11", pg, "11.4", "11", []string{"4"}},
		{"postgres 9.6", pg, "9.6", "9.6", []string{}},
		{"postgres 9 is unknown", pg, "9", "", nil},
		{"ubuntu number", ub, "22.04", "22.04", []string{}},
		{"ubuntu codename", ub, "jammy", "22.04", nil},
		{"ubuntu codename first word only", ub, "noble", "24.04", nil},
		{"codename is not a prefix match", ub, "jam", "", nil},
		{"alpha tag", py, "latest", "", nil},
		{"empty", py, "", "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, extra, ok := findCycle(tc.rels, tc.head)
			if ok != (tc.cycle != "") || r.Name != tc.cycle {
				t.Fatalf("cycle = %q, %v; want %q", r.Name, ok, tc.cycle)
			}
			if ok && !reflect.DeepEqual(extra, tc.extra) {
				t.Errorf("extra = %#v, want %#v", extra, tc.extra)
			}
		})
	}
}

func TestSuccessor(t *testing.T) {
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		rels []map[string]any
		want string
	}{
		{"newest LTS over newer non-LTS", []map[string]any{
			rel("26", "", "2026-05-05", "2029-04-30", false, false),
			rel("25", "", "2025-10-15", "2026-06-01", false, true),
			rel("24", "", "2025-05-06", "2028-04-30", true, false),
			rel("22", "", "2024-04-24", "2027-04-30", true, false),
		}, "24"},
		{"latest maintained without LTS", []map[string]any{
			rel("18", "", "2025-09-25", "2030-11-14", false, false),
			rel("17", "", "2024-09-26", "2029-11-08", false, false),
			rel("13", "", "2020-09-24", "2025-11-13", false, true),
		}, "18"},
		{"order of the API does not matter", []map[string]any{
			rel("22", "", "2024-04-24", "2027-04-30", true, false),
			rel("24", "", "2025-05-06", "2028-04-30", true, false),
		}, "24"},
		{"numeric order, not text", []map[string]any{
			rel("9", "", "2020-01-01", "2030-01-01", false, false),
			rel("10", "", "2021-01-01", "2030-01-01", false, false),
		}, "10"},
		{"not released yet", []map[string]any{
			rel("27", "", "2027-03-01", "2030-01-01", true, false),
			rel("26", "", "2026-03-01", "2030-01-01", false, false),
		}, "26"},
		{"LTS that is dead does not count", []map[string]any{
			rel("20", "", "2023-01-01", "2024-01-01", true, true),
			rel("21", "", "2024-01-01", "2030-01-01", false, false),
		}, "21"},
		{"nothing alive", []map[string]any{rel("1", "", "2020-01-01", "2021-01-01", false, true)}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rels, err := (&EOL{Fetch: eolFake(map[string]*core.Response{"nodejs": product1(tc.rels...)})}).releases(context.Background(), "nodejs")
			if err != nil {
				t.Fatal(err)
			}
			got, ok := successor(rels, now)
			if got.Name != tc.want || ok != (tc.want != "") {
				t.Errorf("successor = %q, %v; want %q", got.Name, ok, tc.want)
			}
		})
	}
}

func TestRetag(t *testing.T) {
	noble := eolRelease{Name: "24.04", Codename: "Noble Numbat"}
	tests := []struct {
		to    eolRelease
		head  string
		extra []string
		want  string
	}{
		{eolRelease{Name: "24"}, "16", []string{}, "24"},
		{eolRelease{Name: "3.15"}, "3.9", []string{}, "3.15"},
		{eolRelease{Name: "3.15"}, "3.9.18", []string{"18"}, "3.15"},
		{eolRelease{Name: "10"}, "8.0", []string{"0"}, "10.0"},
		{eolRelease{Name: "10"}, "8.0.0", []string{"0", "0"}, "10.0.0"},
		{eolRelease{Name: "10"}, "8.0.100", []string{"0", "100"}, "10"},
		{noble, "22.04", []string{}, "24.04"},
		{noble, "jammy", nil, "noble"},
		{eolRelease{Name: "13"}, "bullseye", nil, "13"},
	}
	for _, tc := range tests {
		if got := retag(tc.to, tc.head, tc.extra); got != tc.want {
			t.Errorf("retag(%q, %q, %v) = %q, want %q", tc.to.Name, tc.head, tc.extra, got, tc.want)
		}
	}
}

func TestProduct(t *testing.T) {
	tests := []struct{ eco, name, want string }{
		{core.Docker, "library/node", "nodejs"},
		{core.Docker, "node", "nodejs"},
		{core.Docker, "docker.io/library/node", "nodejs"},
		{core.Docker, "library/postgres", "postgresql"},
		{core.Docker, "library/golang", "go"},
		{core.Docker, "library/openjdk", "eclipse-temurin"},
		{core.Docker, "library/eclipse-temurin", "eclipse-temurin"},
		{core.Docker, "library/rockylinux", "rocky-linux"},
		{core.Docker, "mcr.microsoft.com/dotnet/sdk", "dotnet"},
		{core.Docker, "mcr.microsoft.com/dotnet/aspnet", "dotnet"},
		{core.Docker, "mcr.microsoft.com/mssql/server", ""},
		{core.Docker, "bitnami/postgresql", ""},
		{core.Docker, "someone/node", ""},
		{core.Docker, "ghcr.io/o/node", ""},
		{core.Npm, "node", ""},
		{core.Toolchain, "nodejs", "nodejs"},
		{core.Toolchain, "python", "python"},
	}
	for _, tc := range tests {
		if got := product(dep(tc.eco, tc.name, "1")); got != tc.want {
			t.Errorf("product(%s %s) = %q, want %q", tc.eco, tc.name, got, tc.want)
		}
	}
}

func TestEOLChanges(t *testing.T) {
	f := eolFake(map[string]*core.Response{
		"nodejs": product1(
			rel("26", "", "2026-05-05", "2029-04-30", false, false),
			rel("24", "", "2025-05-06", "2028-04-30", true, false),
			rel("16", "", "2021-04-20", "2023-09-11", false, true),
		),
		"ubuntu": product1(
			rel("26.04", "Resolute Raccoon", "2026-04-23", "2031-05-29", true, false),
			rel("22.04", "Jammy Jellyfish", "2022-04-21", "2027-04-01", true, false),
			rel("20.04", "Focal Fossa", "2020-04-23", "2025-05-29", true, true),
		),
		"dotnet": product1(
			rel("10", "", "2025-11-11", "2028-11-14", true, false),
			rel("7", "", "2022-11-08", "2024-05-14", false, true),
		),
		"python": product1(
			rel("3.14", "", "2025-10-07", "2030-10-31", false, false),
		),
		"mariadb": {Status: 404},
	})
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("# base\nFROM node:16-alpine AS build\nFROM node:24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	node := dep(core.Docker, "library/node", "16-alpine")
	node.Manifest = core.Manifest{Ecosystem: core.Docker, Path: "Dockerfile"}
	node.Line = 2
	fine := dep(core.Docker, "library/node", "24")
	fine.Manifest, fine.Line = node.Manifest, 3
	inv := &core.Inventory{Root: root, Deps: []core.Dep{
		node,
		fine,
		dep(core.Docker, "library/ubuntu", "focal-20240101"),
		dep(core.Docker, "library/ubuntu", "20.04"),
		dep(core.Docker, "mcr.microsoft.com/dotnet/sdk", "7.0"),
		dep(core.Docker, "library/python", "3.14-slim"),
		dep(core.Docker, "library/mariadb", "10.6"),
		dep(core.Docker, "library/nginx", "latest"),
		dep(core.Npm, "node", "16.0.0"),
	}}
	f.h = wrap(f.h, "GET "+eolURL+"nginx", status(404))
	s := &EOL{Fetch: f, Now: func() time.Time { return time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC) }}
	got, err := s.Changes(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ ID, Title string }
	var have []row
	for _, c := range got {
		have = append(have, row{c.ID, c.Evidence[0].Title})
		if c.Kind != core.Deprecation || len(c.Deps) != 1 || c.Evidence[0].Source != "eol" {
			t.Errorf("change = %+v", c)
		}
	}
	want := []row{
		{"docker/library/node@24-alpine", "nodejs 16 end of life 2023-09-11"},
		{"docker/library/ubuntu@resolute", "ubuntu 20.04 end of life 2025-05-29"},
		{"docker/library/ubuntu@26.04", "ubuntu 20.04 end of life 2025-05-29"},
		{"docker/mcr.microsoft.com/dotnet/sdk@10.0", "dotnet 7 end of life 2024-05-14"},
	}
	if !reflect.DeepEqual(have, want) {
		t.Fatalf("changes:\n%+v\nwant:\n%+v", have, want)
	}

	c := got[0]
	if c.To != "24-alpine" || !c.Sunset.Equal(time.Date(2023, 9, 11, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("node change = %+v", c)
	}
	if c.Evidence[0].URL != "https://endoflife.date/nodejs" || c.Evidence[0].Text != "nodejs 16 reached end of life on 2023-09-11." {
		t.Errorf("evidence = %+v", c.Evidence[0])
	}
	if want := []core.Site{{File: "Dockerfile", Line: 2, Text: "FROM node:16-alpine AS build"}}; !reflect.DeepEqual(c.Sites, want) {
		t.Errorf("sites = %+v, want %+v", c.Sites, want)
	}
	if got[1].Sites[0].Text != "" || got[1].Sites[0].File != "manifest" {
		t.Errorf("site without a readable file = %+v", got[1].Sites)
	}

	var fetched []string
	for _, c := range f.calls {
		fetched = append(fetched, c.URL[len(eolURL):])
		if c.Opts.TTL != 24*time.Hour {
			t.Errorf("%s cached for %v, want 24h", c.URL, c.Opts.TTL)
		}
	}
	slices.Sort(fetched)
	if want := []string{"dotnet", "mariadb", "nginx", "nodejs", "python", "ubuntu"}; !reflect.DeepEqual(fetched, want) {
		t.Errorf("fetched %v, want one request per product: %v", fetched, want)
	}
}

func wrap(h func(method, url string, body []byte) *core.Response, key string, res *core.Response) func(string, string, []byte) *core.Response {
	return func(method, url string, body []byte) *core.Response {
		if method+" "+url == key {
			return res
		}
		return h(method, url, body)
	}
}

func TestEOLNoSuccessor(t *testing.T) {
	f := eolFake(map[string]*core.Response{"nodejs": product1(rel("16", "", "2021-04-20", "2023-09-11", false, true))})
	inv := &core.Inventory{Deps: []core.Dep{dep(core.Docker, "library/node", "16")}}
	got, err := (&EOL{Fetch: f}).Changes(context.Background(), inv)
	if err != nil || len(got) != 1 || got[0].To != "" || got[0].ID != "docker/library/node@" {
		t.Fatalf("got %+v, %v; want a decision-only change", got, err)
	}
}

func TestEOLLooseShapes(t *testing.T) {
	body := `{"result": {"releases": [
		{"name": "5", "isEol": true, "isMaintained": false, "eolFrom": true, "releaseDate": null, "codename": null},
		{"name": "6", "isLts": false, "isEol": false, "isMaintained": null, "eolFrom": null, "releaseDate": "2020-01-01"}
	]}}`
	f := eolFake(map[string]*core.Response{"nodejs": text(body)})
	inv := &core.Inventory{Deps: []core.Dep{dep(core.Docker, "library/node", "5"), dep(core.Docker, "library/node", "6")}}
	got, err := (&EOL{Fetch: f}).Changes(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v; an unknown maintained flag must not read as dead", got)
	}
	if got[0].Evidence[0].Title != "nodejs 5 end of life" || !got[0].Sunset.IsZero() || got[0].To != "6" {
		t.Errorf("change = %+v", got[0])
	}
}

func TestEOLFailures(t *testing.T) {
	inv := &core.Inventory{Deps: []core.Dep{dep(core.Docker, "library/node", "16")}}
	for name, r := range map[string]*core.Response{"500": status(500), "garbage": text("<html>")} {
		t.Run(name, func(t *testing.T) {
			f := eolFake(map[string]*core.Response{"nodejs": r})
			if _, err := (&EOL{Fetch: f}).Changes(context.Background(), inv); err == nil {
				t.Fatal("no error")
			}
		})
	}
}

func TestEOLReplay(t *testing.T) {
	s := &EOL{Fetch: replay(), Now: func() time.Time { return recordedNow }}
	got, err := s.Changes(context.Background(), eolInventory())
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		ID     string
		Sunset string
	}
	var have []row
	for _, c := range got {
		have = append(have, row{c.ID, c.Sunset.Format(time.DateOnly)})
	}
	// A pinned runtime moves the way an image tag does, to the newest LTS,
	// and a pin to a patch release stays one.
	want := []row{
		{"docker/library/node@24-alpine", "2023-09-11"},
		{"docker/library/postgres@18", "2023-11-09"},
		{"docker/library/debian@trixie-slim", "2026-08-31"},
		{"toolchain/nodejs@24.22.0", "2023-09-11"},
		{"toolchain/nodejs@24", "2026-04-30"},
	}
	if !reflect.DeepEqual(have, want) {
		t.Fatalf("changes:\n%+v\nwant:\n%+v", have, want)
	}
}

func TestLineSite(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "compose.yml"), []byte("services:\n  db:\n    image:   postgres:11   \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := dep(core.Docker, "library/postgres", "11")
	d.Manifest.Path = "compose.yml"
	tests := []struct {
		line int
		want core.Site
	}{
		{3, core.Site{File: "compose.yml", Line: 3, Text: "image:   postgres:11"}},
		{99, core.Site{File: "compose.yml", Line: 99}},
		{0, core.Site{File: "compose.yml"}},
	}
	for _, tc := range tests {
		d.Line = tc.line
		if got := lineSite(root, d); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("line %d: %+v, want %+v", tc.line, got, tc.want)
		}
	}
	d.Manifest.Path, d.Line = "missing", 1
	if got := lineSite(root, d); !reflect.DeepEqual(got, core.Site{File: "missing", Line: 1}) {
		t.Errorf("missing file: %+v", got)
	}
}

func TestEOLMinimum(t *testing.T) {
	f := eolFake(map[string]*core.Response{
		"nodejs": product1(
			rel("26", "", "2026-04-21", "2029-04-30", false, false),
			rel("25", "", "2025-10-14", "2026-06-01", false, true),
			rel("24", "", "2025-05-06", "2028-04-30", true, false),
			rel("22", "", "2024-04-24", "2027-04-30", true, false),
			rel("16", "", "2021-04-20", "2023-09-11", true, true),
		),
		"python": product1(
			rel("3.11", "", "2022-10-24", "2027-10-31", false, false),
			rel("3.10", "", "2021-10-04", "2026-10-31", false, false),
			rel("3.8", "", "2019-10-14", "2024-10-07", false, true),
		),
		"go": product1(
			rel("1.27", "", "2026-08-11", "", false, false),
			rel("1.26", "", "2026-02-10", "", false, false),
			rel("1.24", "", "2025-02-11", "2026-02-10", false, true),
		),
		"eclipse-temurin": product1(
			rel("17", "", "2021-09-22", "2027-10-31", true, false),
			rel("16", "", "2021-07-30", "2021-09-30", false, true),
		),
	})
	min := func(name, version, req string) core.Dep {
		d := dep(core.Toolchain, name, version)
		d.Req, d.Min = req, true
		return d
	}
	inv := &core.Inventory{Deps: []core.Dep{
		min("nodejs", "16.14.0", ">=16.14.0"),
		min("python", "3.8", ">=3.8"),
		min("go", "1.24", "1.24"),
		min("java", "16", "16"),
		min("nodejs", "22", ">=22"),
	}}
	s := &EOL{Fetch: f, Now: func() time.Time { return time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC) }}
	got, err := s.Changes(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range got {
		ids = append(ids, c.ID)
	}
	want := []string{"toolchain/nodejs@22.0.0", "toolchain/python@3.11", "toolchain/go@1.26", "toolchain/java@17"}
	if !slices.Equal(ids, want) {
		t.Fatalf("changes = %q, want %q", ids, want)
	}
	if text := got[0].Evidence[0].Text; text != "nodejs 16 reached end of life on 2023-09-11. The oldest nodejs release still maintained is 22." {
		t.Errorf("evidence = %q", text)
	}
	if text := got[1].Evidence[0].Text; text != "python 3.8 reached end of life on 2024-10-07. python 3.10 reaches its own end of life on 2026-10-31, so the oldest release to move to is 3.11." {
		t.Errorf("evidence = %q", text)
	}
}
