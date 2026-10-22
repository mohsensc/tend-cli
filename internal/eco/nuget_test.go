package eco

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/fetch"
)

func TestNugetDetect(t *testing.T) {
	root := copyTestdata(t, "nuget/solution")
	write(t, root, map[string]string{"src/obj/x.csproj": "<Project/>", "bin/y.csproj": "<Project/>"})
	got := manifestsIn(t, nuget{}, root)
	want := []core.Manifest{
		{Ecosystem: "nuget", Path: "src/App.Tests/App.Tests.csproj", Tool: "dotnet"},
		{Ecosystem: "nuget", Path: "src/App/App.csproj", Lock: "src/App/packages.lock.json", Tool: "dotnet"},
		{Ecosystem: "nuget", Path: "src/Directory.Packages.props", Tool: "dotnet"},
		{Ecosystem: "nuget", Path: "src/Legacy/legacy.csproj", Tool: "dotnet"},
		{Ecosystem: "nuget", Path: "src/Old/Old.fsproj", Tool: "dotnet"},
		{Ecosystem: "nuget", Path: "src/Old/packages.config", Tool: "dotnet"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Detect = %+v\nwant %+v", got, want)
	}
}

func TestNugetDeps(t *testing.T) {
	root := copyTestdata(t, "nuget/solution")
	for _, tt := range []struct{ name, manifest string }{
		{"app", "src/App/App.csproj"},
		{"tests", "src/App.Tests/App.Tests.csproj"},
		{"central", "src/Directory.Packages.props"},
		{"legacy", "src/Legacy/legacy.csproj"},
		{"packages-config", "src/Old/packages.config"},
		{"fsproj", "src/Old/Old.fsproj"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			goldenCheck(t, "nuget/deps/"+tt.name+".golden.json", jsonOf(t, depsIn(t, nuget{}, root, tt.manifest)))
		})
	}
}

func TestNugetDepsSkipsProperties(t *testing.T) {
	root := copyTestdata(t, "nuget/solution")
	for _, m := range []string{"src/App/App.csproj", "src/Directory.Packages.props"} {
		for _, d := range depsIn(t, nuget{}, root, m) {
			if strings.Contains(d.Req, "$(") || strings.Contains(d.Name, "blobs") || strings.Contains(d.Name, "property") {
				t.Errorf("%s lists %+v", m, d)
			}
		}
	}
}

func TestNugetRefs(t *testing.T) {
	src := []byte("<Project>\n  <ItemGroup>\n    <PackageReference Include=\"A\" Version=\"1\"/>\n    <PackageReference Include='B'>\n      <Version> 2.0 </Version>\n      <PrivateAssets>ALL</PrivateAssets>\n      <IncludeAssets>runtime</IncludeAssets>\n    </PackageReference>\n    <PackageReference Update=\"C\" Version=\"3\" />\n    <PackageReference include=\"D\" version=\"4\" privateassets=\"all\"></PackageReference>\n  </ItemGroup>\n</Project>\n")
	refs, err := nugetRefs(src, "PackageReference")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range refs {
		got = append(got, fmt.Sprintf("%s %s line=%d dev=%v %q", r.id, r.version, r.line, r.dev, src[r.start:r.end]))
	}
	want := []string{
		`A 1 line=3 dev=false "<PackageReference Include=\"A\" Version=\"1\"/>"`,
		"B 2.0 line=4 dev=true \"<PackageReference Include='B'>\\n      <Version> 2.0 </Version>\\n      <PrivateAssets>ALL</PrivateAssets>\\n      <IncludeAssets>runtime</IncludeAssets>\\n    </PackageReference>\"",
		` 3 line=9 dev=false "<PackageReference Update=\"C\" Version=\"3\" />"`,
		`D 4 line=10 dev=true "<PackageReference include=\"D\" version=\"4\" privateassets=\"all\"></PackageReference>"`,
	}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Errorf("ref %d = %v\nwant %s", i, got, want[i])
			break
		}
	}
	if _, err := nugetRefs([]byte("<Project><ItemGroup></Project>"), "PackageReference"); err == nil {
		t.Error("read malformed xml")
	}
}

func TestNugetBump(t *testing.T) {
	bumpGolden(t, nuget{}, "nuget/solution", []bumpCase{
		{"attribute", "src/App/App.csproj", "newtonsoft.json", true, "13.0.3", ""},
		{"element", "src/App/App.csproj", "serilog", true, "4.0.0", ""},
		{"prerelease-to-stable", "src/App/App.csproj", "stylecop.analyzers", true, "1.2.0", ""},
		{"range", "src/App/App.csproj", "dapper", true, "3.1.0", ""},
		{"range-inside", "src/App/App.csproj", "dapper", true, "2.1.35", ""},
		{"floating", "src/App/App.csproj", "polly", true, "9.0.0", ""},
		{"floating-inside", "src/App/App.csproj", "polly", true, "8.3.0", ""},
		{"central-from-project", "src/App/App.csproj", "humanizer", true, "3.0.0", ""},
		{"central-from-props", "src/Directory.Packages.props", "moq", true, "4.20.72", ""},
		{"swap", "src/App/App.csproj", "newtonsoft.json", true, "8.0.0", "System.Text.Json"},
		{"swap-element", "src/App/App.csproj", "serilog", true, "1.0.0", "Serilog.Next"},
		{"no-lock", "src/Old/Old.fsproj", "fsharp.core", true, "9.0.100", ""},
	})
}

// TestNugetBumpBytes checks that nothing but the version moves: not the BOM,
// the CRLF line ends, nor the namespace.
func TestNugetBumpBytes(t *testing.T) {
	hideTools(t)
	root := copyTestdata(t, "nuget/solution")
	p := filepath.Join(root, "src/Legacy/legacy.csproj")
	before, _ := os.ReadFile(p)
	for _, tt := range []struct{ dep, to, old, new string }{
		{"log4net", "2.0.17", `Version="2.0.15"`, `Version="2.0.17"`},
		{"nunit", "4.1.0", `<Version>3.13.3</Version>`, `<Version>4.1.0</Version>`},
	} {
		d := depIn(t, nuget{}, root, "src/Legacy/legacy.csproj", tt.dep, true)
		if _, err := (nuget{}).Bump(context.Background(), root, d, tt.to, ""); err != nil {
			t.Fatal(err)
		}
		want := strings.Replace(string(before), tt.old, tt.new, 1)
		before = []byte(want)
		got, _ := os.ReadFile(p)
		if string(got) != want {
			t.Fatalf("%s: file differs from the single replacement\n%q", tt.dep, got)
		}
	}
	if !strings.HasPrefix(string(before), "\xef\xbb\xbf<?xml") || !strings.Contains(string(before), "\r\n") {
		t.Fatal("fixture lost its BOM or CRLF")
	}
}

func TestNugetBumpErrors(t *testing.T) {
	hideTools(t)
	root := copyTestdata(t, "nuget/solution")
	ctx := context.Background()
	hum := depIn(t, nuget{}, root, "src/App/App.csproj", "humanizer.core", false)
	if _, err := (nuget{}).Bump(ctx, root, hum, "2.15.0", ""); err == nil {
		t.Error("bumped a transitive package")
	}
	nlog := depIn(t, nuget{}, root, "src/Old/packages.config", "nlog", true)
	if _, err := (nuget{}).Bump(ctx, root, nlog, "5.0.0", ""); err == nil {
		t.Error("bumped packages.config")
	}
	moq := depIn(t, nuget{}, root, "src/Directory.Packages.props", "moq", true)
	if _, err := (nuget{}).Bump(ctx, root, moq, "5.0.0", "Other"); err == nil {
		t.Error("swapped a centrally versioned package")
	}
	xunit := depIn(t, nuget{}, root, "src/App.Tests/App.Tests.csproj", "xunit", true)
	xunit.Line = 99
	if _, err := (nuget{}).Bump(ctx, root, xunit, "3.0.0", ""); err == nil {
		t.Error("bumped a package from the wrong line")
	}
	write(t, root, map[string]string{"src/Old/Old.fsproj": `<Project><ItemGroup><PackageReference Include="FSharp.Core" Version="8.0.*-beta*" /></ItemGroup></Project>`})
	odd := depIn(t, nuget{}, root, "src/Old/Old.fsproj", "fsharp.core", true)
	if _, err := (nuget{}).Bump(ctx, root, odd, "9.0.0", ""); err == nil {
		t.Error("rewrote a version it cannot read")
	}
}

func TestNugetBumpCommands(t *testing.T) {
	logged := fakeTool(t, "dotnet")
	ctx := context.Background()
	root := copyTestdata(t, "nuget/solution")
	json := depIn(t, nuget{}, root, "src/App/App.csproj", "newtonsoft.json", true)
	res, err := (nuget{}).Bump(ctx, root, json, "13.0.3", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Locked || !reflect.DeepEqual(res.Files, []string{"src/App/App.csproj", "src/App/packages.lock.json"}) {
		t.Errorf("bump = %+v", res)
	}
	hum := depIn(t, nuget{}, root, "src/App/App.csproj", "humanizer", true)
	if res, err = (nuget{}).Bump(ctx, root, hum, "3.0.0", ""); err != nil {
		t.Fatal(err)
	}
	if !res.Locked || !reflect.DeepEqual(res.Files, []string{"src/Directory.Packages.props", "src/App/packages.lock.json"}) {
		t.Errorf("central bump = %+v", res)
	}
	fs := depIn(t, nuget{}, root, "src/Old/Old.fsproj", "fsharp.core", true)
	if res, err = (nuget{}).Bump(ctx, root, fs, "9.0.100", ""); err != nil || res.Locked || res.Note != "no packages.lock.json" {
		t.Errorf("project without a lock = %+v, %v", res, err)
	}
	want := []string{
		"./src/App restore App.csproj --force-evaluate",
		"./src/App restore App.csproj --force-evaluate",
	}
	if got := logged(root); !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %q\nwant %q", got, want)
	}
}

func TestNugetVersion(t *testing.T) {
	tests := []struct {
		req, to, want string
		ok            bool
	}{
		{"13.0.1", "13.0.3", "13.0.3", true},
		{"1.0.0-beta.2", "1.0.0", "1.0.0", true},
		{"1.0", "2.0.0", "2.0.0", true},
		{"[1.0.0]", "1.0.5", "[1.0.5]", true},
		{"[2.0.0,3.0.0)", "3.1.0", "[3.1.0,4.0.0)", true},
		{"[2.0.0, 3.0.0)", "3.1.0", "[3.1.0, 4.0.0)", true},
		{"[2.0.0,3.0.0)", "2.5.0", "[2.0.0,3.0.0)", true},
		{"[2.0.0,3.0.0)", "3.0.0", "[3.0.0,4.0.0)", true},
		{"[2.0.0,3.0.0]", "3.0.0", "[2.0.0,3.0.0]", true},
		{"(2.0.0,3.0.0)", "2.0.0", "[2.0.0,3.0.0)", true},
		{"[2.0.0,)", "5.0.0", "[2.0.0,)", true},
		{"[2.0.0,)", "1.0.0", "[1.0.0,)", true},
		{"(,3.0.0)", "4.0.0", "(,5.0.0)", true},
		{"8.*", "9.0.0", "9.*", true},
		{"8.*", "8.3.0", "8.*", true},
		{"8.1.*", "8.2.0", "8.2.*", true},
		{"1.0.0-*", "2.0.0", "", false},
		{"$(V)", "2.0.0", "", false},
		{"1.0", "not-a-version", "", false},
	}
	for _, tt := range tests {
		got, ok := nugetVersion(tt.req, tt.to)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("nugetVersion(%q, %q) = %q, %v; want %q, %v", tt.req, tt.to, got, ok, tt.want, tt.ok)
		}
	}
}

func TestNugetMeta(t *testing.T) {
	ctx := context.Background()
	t.Run("recorded deprecated package", func(t *testing.T) {
		m, err := (nuget{}).Meta(ctx, fetch.Replay(recordedDir("nuget")), "Microsoft.Azure.Storage.Blob")
		if err != nil {
			t.Fatal(err)
		}
		if m.Name != "microsoft.azure.storage.blob" || m.Replacement != "azure.storage.blobs" || !strings.Contains(m.Deprecated, "obsolete") || len(m.Releases) < 3 {
			t.Fatalf("meta = %+v", m)
		}
		for _, r := range m.Releases {
			if r.Time.IsZero() || r.Deprecated == "" || r.Yanked {
				t.Errorf("release = %+v", r)
			}
		}
	})
	t.Run("pages, unlisted and reasons", func(t *testing.T) {
		const base = "https://api.nuget.org/v3/registration5-gz-semver2/acme.lib/"
		f := replayCanned(t,
			canned{url: base + "index.json", body: `{"count": 1, "items": [{"@id": "` + base + `page/0.9.0/2.0.0-beta1.json", "count": 4, "lower": "0.9.0", "upper": "2.0.0-beta1"}]}`},
			canned{url: base + "page/0.9.0/2.0.0-beta1.json", body: `{"@id": "x", "items": [
				{"catalogEntry": {"version": "0.9.0", "published": "1900-01-01T00:00:00+00:00", "listed": true}},
				{"catalogEntry": {"version": "1.0.0", "published": "2023-05-01T10:00:00.5+00:00", "listed": true, "projectUrl": "https://acme.example/docs",
					"deprecation": {"reasons": ["CriticalBugs", "Other"], "alternatePackage": {"id": "Better.Lib", "range": "*"}}}},
				{"catalogEntry": {"version": "1.1.0", "published": "2023-06-01T10:00:00+00:00", "listed": false}},
				{"catalogEntry": {"version": "2.0.0-beta1", "published": "2024-01-01T10:00:00+00:00"}},
				{"catalogEntry": {"version": "2.0.1-dependabot-02442", "published": "2024-02-01T10:00:00+00:00"}}]}`})
		m, err := (nuget{}).Meta(ctx, f, "Acme.Lib")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range m.Releases {
			got = append(got, fmt.Sprintf("%s pre=%v yanked=%v dep=%q", r.Version, r.Pre, r.Yanked, r.Deprecated))
		}
		want := []string{
			`0.9.0 pre=false yanked=true dep=""`,
			`1.0.0 pre=false yanked=false dep="CriticalBugs, Other"`,
			`1.1.0 pre=false yanked=true dep=""`,
			`2.0.0-beta1 pre=true yanked=false dep=""`,
			`2.0.1-dependabot-02442 pre=true yanked=false dep=""`,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("releases = %q\nwant %q", got, want)
		}
		if m.Deprecated != "CriticalBugs, Other" || m.Replacement != "better.lib" || m.Repo != "" {
			t.Errorf("meta = %+v", m)
		}
	})
	t.Run("forge project url becomes the repo", func(t *testing.T) {
		f := replayCanned(t, canned{url: "https://api.nuget.org/v3/registration5-gz-semver2/acme.web/index.json", body: `{"items": [{"items": [
			{"catalogEntry": {"version": "1.0.0", "published": "2023-05-01T10:00:00+00:00", "projectUrl": "https://github.com/acme/web"}}]}]}`})
		m, err := (nuget{}).Meta(ctx, f, "Acme.Web")
		if err != nil || m.Repo != "https://github.com/acme/web" || m.Deprecated != "" || m.Replacement != "" {
			t.Errorf("meta = %+v, %v", m, err)
		}
	})
	t.Run("deprecation without text", func(t *testing.T) {
		d := &nugetDeprecated{}
		if d.text() != "deprecated" || (*nugetDeprecated)(nil).text() != "" {
			t.Error("deprecation text")
		}
	})
	t.Run("not found", func(t *testing.T) {
		f := replayCanned(t, canned{url: "https://api.nuget.org/v3/registration5-gz-semver2/acme.nope/index.json", status: 404, body: "<Error/>"})
		if _, err := (nuget{}).Meta(ctx, f, "Acme.Nope"); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("missing page", func(t *testing.T) {
		const base = "https://api.nuget.org/v3/registration5-gz-semver2/acme.gone/"
		f := replayCanned(t,
			canned{url: base + "index.json", body: `{"items": [{"@id": "` + base + `page.json"}]}`},
			canned{url: base + "page.json", status: 404, body: ""})
		if _, err := (nuget{}).Meta(ctx, f, "Acme.Gone"); err == nil {
			t.Error("expected an error for the missing page")
		}
	})
}

func TestNugetMetaRecord(t *testing.T) {
	wantRecord(t)
	if _, err := (nuget{}).Meta(context.Background(), liveClient(t, "nuget"), "Microsoft.Azure.Storage.Blob"); err != nil {
		t.Fatal(err)
	}
}

func TestNugetImports(t *testing.T) {
	root := copyTestdata(t, "nuget/solution")
	depsIn(t, nuget{}, root, "src/App/App.csproj")
	tests := map[string]string{
		"newtonsoft.json":           "Newtonsoft.Json",
		"stylecop.analyzers":        "StyleCop.Analyzers",
		"zzz.never.seen":            "zzz.never.seen",
		"microsoft.build.tasks.git": "Microsoft.Build.Tasks.Git",
	}
	for name, want := range tests {
		got := (nuget{}).Imports(core.Dep{Name: name})
		if !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("Imports(%s) = %v, want [%s]", name, got, want)
		}
	}
}

func TestNugetCommands(t *testing.T) {
	tests := []struct {
		path string
		want core.Commands
	}{
		{"App.csproj", core.Commands{Build: []string{"dotnet build --nologo"}, Test: []string{"dotnet test --nologo"}}},
		{"src/App/App.csproj", core.Commands{Build: []string{"cd src/App && dotnet build --nologo"}, Test: []string{"cd src/App && dotnet test --nologo"}}},
	}
	for _, tt := range tests {
		got := (nuget{}).Commands(t.TempDir(), core.Manifest{Path: tt.path})
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Commands(%s) = %+v, want %+v", tt.path, got, tt.want)
		}
	}
}

func TestTestProject(t *testing.T) {
	tests := map[string]bool{
		"src/App.Tests/App.Tests.csproj": true,
		"Foo.UnitTests.csproj":           true,
		"FooTest.fsproj":                 true,
		"Foo.Test.Helpers.csproj":        true,
		"Tests.Foo.csproj":               true,
		"src/App/App.csproj":             false,
		"Latest.csproj":                  false,
		"Contests.Api.csproj":            false,
		"app.tests.csproj":               true,
		"Directory.Packages.props":       false,
	}
	for p, want := range tests {
		if got := testProject(p); got != want {
			t.Errorf("testProject(%s) = %v, want %v", p, got, want)
		}
	}
}
