package eco

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/fetch"
)

func TestGemsDetect(t *testing.T) {
	root := copyTestdata(t, "rubygems/project")
	write(t, root, map[string]string{"vendor/bundle/Gemfile": "", "node_modules/x/Gemfile": ""})
	got := manifestsIn(t, rubygems{}, root)
	want := []core.Manifest{
		{Ecosystem: "rubygems", Path: "Gemfile", Lock: "Gemfile.lock", Tool: "bundle"},
		{Ecosystem: "rubygems", Path: "gem/Gemfile", Lock: "gem/Gemfile.lock", Tool: "bundle"},
		{Ecosystem: "rubygems", Path: "gem/mygem.gemspec", Lock: "gem/Gemfile.lock", Tool: "bundle"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Detect = %+v\nwant %+v", got, want)
	}
}

func TestGemsDeps(t *testing.T) {
	root := copyTestdata(t, "rubygems/project")
	for _, tt := range []struct{ name, manifest string }{
		{"gemfile", "Gemfile"},
		{"gemspec", "gem/mygem.gemspec"},
		{"gemspec-gemfile", "gem/Gemfile"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			goldenCheck(t, "rubygems/deps/"+tt.name+".golden.json", jsonOf(t, depsIn(t, rubygems{}, root, tt.manifest)))
		})
	}
}

func TestGemsDepsShapes(t *testing.T) {
	root := copyTestdata(t, "rubygems/project")
	byName := map[string]core.Dep{}
	for _, d := range depsIn(t, rubygems{}, root, "Gemfile") {
		if d.Direct {
			byName[d.Name] = d
		}
	}
	tests := []struct {
		name string
		req  string
		dev  bool
		line int
		ver  string
	}{
		{"rails", "~> 7.1.3", false, 5, "7.1.3"},
		{"stripe", "~> 10.0, >= 10.1", false, 7, "10.5.0"},
		{"puma", "", false, 8, "6.4.2"},
		{"nokogiri", "~> 1.16", false, 11, "1.16.2"},
		{"rspec-rails", "~> 6.1", true, 16, "6.1.1"},
		{"pry", "", true, 17, "0.14.2"},
		{"webmock", "3.19.1", true, 21, "3.19.1"},
		{"rubocop", "~> 1.60", true, 24, "1.60.2"},
		{"faraday", "~> 2.9", false, 25, "2.9.0"},
		{"capybara", "", true, 26, "3.40.0"},
		{"simplecov", "", false, 33, "0.22.0"},
		{"tzinfo-data", "", false, 13, "1.2024.1"},
	}
	for _, tt := range tests {
		d, ok := byName[tt.name]
		if !ok {
			t.Errorf("%s missing", tt.name)
			continue
		}
		if d.Req != tt.req || d.Dev != tt.dev || d.Line != tt.line || d.Version != tt.ver {
			t.Errorf("%s = req %q dev %v line %d version %q; want %q %v %d %q", tt.name, d.Req, d.Dev, d.Line, d.Version, tt.req, tt.dev, tt.line, tt.ver)
		}
	}
	for _, skipped := range []string{"my_engine", "private_thing", "mono_a"} {
		if _, ok := byName[skipped]; ok {
			t.Errorf("%s comes from git or a path and must not be listed", skipped)
		}
	}
}

func TestGemfileParser(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string // name:reqs:groups:skip
	}{
		{"nested groups", "group :development do\n  group :test do\n    gem 'a'\n  end\n  gem 'b'\nend\ngem 'c'\n", []string{"a::development,test:false", "b::development:false", "c:::false"}},
		{"comment and parens", "gem('a', '~> 1.0') # note\n# gem 'b'\ngem 'c', '1.0', '>= 0.9' , require: false\n", []string{"a:~> 1.0::false", "c:1.0,>= 0.9::false"}},
		{"hash rockets", "gem 'a', :git => 'x'\ngem 'b', :group => :test\ngem 'c', 'groups' => [:test]\n", []string{"a:::true", "b::test:false", "c:::false"}},
		{"options only", "gem 'a', github: 'x/y', branch: 'main'\ngem 'b', path: '../b'\ngem 'c', source: 'https://gems.example'\n", []string{"a:::true", "b:::true", "c:::true"}},
		{"source block", "source 'https://gems.example' do\n  gem 'a'\nend\ngem 'b'\n", []string{"a:::true", "b:::false"}},
		{"conditionals", "if RUBY_VERSION > '3'\n  gem 'a'\nelse\n  gem 'b'\nend\nunless x\n gem 'c'\nend\ngem 'd'\n", []string{"a:::false", "b:::false", "c:::false", "d:::false"}},
		{"block with args", "platforms :ruby do\n  gem 'a'\nend\ninstall_if -> { true } do\n  gem 'b'\nend\ngem 'c'\n", []string{"a:::false", "b:::false", "c:::false"}},
		{"version-less string after name", "gem 'a', require: 'a/b'\n", []string{"a:::false"}},
		{"continuation", "gem 'a',\n    '~> 2.0',\n    group: :test\n", []string{"a:~> 2.0:test:false"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, e := range gemfile(strings.Split(tt.src, "\n")) {
				got = append(got, fmt.Sprintf("%s:%s:%s:%v", e.name, strings.Join(e.reqs(), ","), strings.Join(e.groups, ","), e.skip))
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestGemLockSpecs(t *testing.T) {
	root := copyTestdata(t, "rubygems/project")
	ls, err := readLocked(root, "Gemfile.lock")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, l := range ls {
		got[l.Name] = l.Version
	}
	if got["nokogiri"] != "1.16.2" || got["rack"] != "3.0.9" || got["tzinfo-data"] != "1.2024.1" {
		t.Errorf("specs = %v", got)
	}
	for _, name := range []string{"private_thing", "my_engine", "bundler"} {
		if _, ok := got[name]; ok {
			t.Errorf("%s is not a registry gem", name)
		}
	}
}

func TestGemsBump(t *testing.T) {
	bumpGolden(t, rubygems{}, "rubygems/project", []bumpCase{
		{"pessimistic", "Gemfile", "rails", true, "8.0.0", ""},
		{"floor", "Gemfile", "pg", true, "2.0.0", ""},
		{"two-requirements", "Gemfile", "stripe", true, "11.0.0", ""},
		{"no-requirement", "Gemfile", "puma", true, "7.0.0", ""},
		{"continued-statement", "Gemfile", "nokogiri", true, "2.0.0", ""},
		{"exact", "Gemfile", "webmock", true, "3.20.0", ""},
		{"inside-range", "Gemfile", "rubocop", true, "1.70.0", ""},
		{"transitive", "Gemfile", "rack", false, "3.1.0", ""},
		{"swap", "Gemfile", "faraday", true, "3.0.1", "faraday2"},
	})
}

func TestGemsBumpErrors(t *testing.T) {
	hideTools(t)
	root := copyTestdata(t, "rubygems/project")
	ctx := context.Background()
	thor := depIn(t, rubygems{}, root, "gem/mygem.gemspec", "thor", true)
	if _, err := (rubygems{}).Bump(ctx, root, thor, "2.0.0", ""); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("bumped a gemspec: %v", err)
	}
	stripe := depIn(t, rubygems{}, root, "Gemfile", "stripe", true)
	if _, err := (rubygems{}).Bump(ctx, root, stripe, "11.0.0", "stripe2"); err == nil {
		t.Error("swapped a gem that has two requirements")
	}
	stripe.Line = 1
	if _, err := (rubygems{}).Bump(ctx, root, stripe, "11.0.0", ""); err == nil {
		t.Error("bumped a gem from the wrong line")
	}
	rack := depIn(t, rubygems{}, root, "Gemfile", "rack", false)
	if _, err := (rubygems{}).Bump(ctx, root, rack, "3.1.0", "other"); err == nil {
		t.Error("swapped a transitive gem")
	}
}

func TestGemsBumpCommands(t *testing.T) {
	ctx := context.Background()
	root := copyTestdata(t, "rubygems/project")
	logged := lockingTool(t, "bundle", root, "Gemfile.lock", strings.NewReplacer(
		"    rails (7.1.3)", "    rails (8.0.0)",
		"    rack (3.0.9)", "    rack (3.1.0)",
		"    faraday (2.9.0)", "    faraday2 (3.0.1)",
	).Replace)
	rails := depIn(t, rubygems{}, root, "Gemfile", "rails", true)
	rack := depIn(t, rubygems{}, root, "Gemfile", "rack", false)
	faraday := depIn(t, rubygems{}, root, "Gemfile", "faraday", true)
	res, err := (rubygems{}).Bump(ctx, root, rails, "8.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Locked || !reflect.DeepEqual(res.Files, []string{"Gemfile", "Gemfile.lock"}) {
		t.Errorf("bump = %+v", res)
	}
	if _, err := (rubygems{}).Bump(ctx, root, rack, "3.1.0", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := (rubygems{}).Bump(ctx, root, faraday, "3.0.1", "faraday2"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		". lock --update rails --conservative", ". lock",
		". lock --update rack --conservative", ". lock",
		". lock", ". lock",
	}
	if got := logged(root); !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %q\nwant %q", got, want)
	}
	gemfile := treeSnapshot(t, root)["Gemfile"]
	for _, s := range []string{"'= ", `"= `, "rack"} {
		if strings.Contains(gemfile, s) {
			t.Errorf("the one-pass pin %q stayed in the Gemfile:\n%s", s, gemfile)
		}
	}
}

// TestGemsBumpPinPass reads the Gemfile the lock tool sees on its first pass.
func TestGemsBumpPinPass(t *testing.T) {
	tests := []struct {
		name, dep string
		direct    bool
		to, swap  string
		want      string
	}{
		{"pessimistic", "rails", true, "8.0.0", "", `gem 'rails', '= 8.0.0'`},
		{"two-requirements", "stripe", true, "11.0.0", "", `gem "stripe", "= 11.0.0", ">= 10.1"`},
		{"no-requirement", "puma", true, "7.0.0", "", `gem 'puma', '= 7.0.0'`},
		{"continued-statement", "nokogiri", true, "2.0.0", "", `gem 'nokogiri', '= 2.0.0',`},
		{"transitive", "rack", false, "3.1.0", "", `gem "rack", "= 3.1.0"`},
		{"swap", "faraday", true, "3.0.1", "faraday2", `gem 'faraday2', '= 3.0.1', groups:`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := copyTestdata(t, "rubygems/project")
			name := tt.dep
			if tt.swap != "" {
				name = tt.swap
			}
			seen := filepath.Join(t.TempDir(), "seen")
			relock := relockLine(t, root, "Gemfile.lock", func(s string) string {
				return regexp.MustCompile(`(?m)^    `+regexp.QuoteMeta(tt.dep)+` \(.*\)$`).ReplaceAllString(s, "    "+name+" ("+tt.to+")")
			})
			toolScript(t, "bundle", "[ -f "+seen+" ] || cat Gemfile > "+seen+"\n"+relock)
			d := depIn(t, rubygems{}, root, "Gemfile", tt.dep, tt.direct)
			if _, err := (rubygems{}).Bump(context.Background(), root, d, tt.to, tt.swap); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(seen)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), tt.want) {
				t.Errorf("first pass Gemfile has no %q:\n%s", tt.want, b)
			}
		})
	}
}

func TestGemsMeta(t *testing.T) {
	ctx := context.Background()
	t.Run("recorded", func(t *testing.T) {
		m, err := (rubygems{}).Meta(ctx, fetch.Replay(recordedDir("rubygems")), "rack")
		if err != nil {
			t.Fatal(err)
		}
		if m.Name != "rack" || m.Repo != "https://github.com/rack/rack" || len(m.Releases) < 4 {
			t.Fatalf("meta = %+v", m)
		}
		first := m.Releases[0]
		if first.Time.IsZero() || first.Yanked {
			t.Errorf("first release = %+v", first)
		}
	})
	t.Run("pre, platform and yanked", func(t *testing.T) {
		webmock := depIn(t, rubygems{}, copyTestdata(t, "rubygems/project"), "Gemfile", "webmock", true)
		if webmock.Version != "3.19.1" {
			t.Fatalf("webmock = %+v", webmock)
		}
		f := replayCanned(t,
			canned{url: "https://rubygems.org/api/v1/versions/webmock.json", body: `[
				{"number": "3.20.0.rc1", "created_at": "2024-03-02T00:00:00.000Z", "prerelease": true, "platform": "ruby"},
				{"number": "3.19.0", "created_at": "2024-01-02T00:00:00.000Z", "prerelease": false, "platform": "ruby"},
				{"number": "3.19.0", "created_at": "2024-01-02T00:00:00.000Z", "prerelease": false, "platform": "java"},
				{"number": "3.18.1", "created_at": "2023-01-02T00:00:00.000Z", "prerelease": false, "platform": "ruby"}]`},
			canned{url: "https://rubygems.org/api/v1/gems/webmock.json", body: `{"source_code_uri": "", "homepage_uri": "https://github.com/bblimke/webmock"}`})
		m, err := (rubygems{}).Meta(ctx, f, "webmock")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range m.Releases {
			got = append(got, fmt.Sprintf("%s pre=%v yanked=%v", r.Version, r.Pre, r.Yanked))
		}
		want := []string{"3.18.1 pre=false yanked=false", "3.19.0 pre=false yanked=false", "3.19.1 pre=false yanked=true", "3.20.0.rc1 pre=true yanked=false"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("releases = %q\nwant %q", got, want)
		}
		if m.Repo != "https://github.com/bblimke/webmock" {
			t.Errorf("repo = %q", m.Repo)
		}
	})
	t.Run("platform-only gem is not yanked", func(t *testing.T) {
		gemLocked.add("native-only", "1.0.0")
		f := replayCanned(t,
			canned{url: "https://rubygems.org/api/v1/versions/native-only.json", body: `[{"number": "1.0.0", "created_at": "2024-01-02T00:00:00.000Z", "prerelease": false, "platform": "x86_64-linux"}]`},
			canned{url: "https://rubygems.org/api/v1/gems/native-only.json", status: 404, body: "{}"})
		m, err := (rubygems{}).Meta(ctx, f, "native-only")
		if err != nil {
			t.Fatal(err)
		}
		if len(m.Releases) != 0 || m.Repo != "" {
			t.Errorf("meta = %+v", m)
		}
	})
	t.Run("not found", func(t *testing.T) {
		f := replayCanned(t, canned{url: "https://rubygems.org/api/v1/versions/nope.json", status: 404, body: "This rubygem could not be found."})
		if _, err := (rubygems{}).Meta(ctx, f, "nope"); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestGemsMetaRecord(t *testing.T) {
	wantRecord(t)
	if _, err := (rubygems{}).Meta(context.Background(), liveClient(t, "rubygems"), "rack"); err != nil {
		t.Fatal(err)
	}
}

func TestGemsImports(t *testing.T) {
	tests := map[string]string{
		"stripe":               "Stripe",
		"nokogiri":             "Nokogiri",
		"aws-sdk-s3":           "Aws::S3",
		"aws-sdk":              "Aws",
		"active_support":       "ActiveSupport",
		"net-http":             "Net::Http",
		"google-cloud-storage": "Google::Cloud::Storage",
		"mysql2":               "Mysql2",
		"rails":                "Rails",
		"activerecord":         "ActiveRecord",
		"octokit":              "Octokit",
		"pg":                   "PG",
		"httparty":             "HTTParty",
		"rspec":                "RSpec",
		"rspec-rails":          "RSpec::Rails",
	}
	for name, want := range tests {
		got := (rubygems{}).Imports(core.Dep{Name: name})
		if !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("Imports(%s) = %v, want [%s]", name, got, want)
		}
	}
}

func TestGemsCommands(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"Gemfile": "", "spec/a_spec.rb": "", "svc/Gemfile": "", "svc/Rakefile": "", "lib/Gemfile": ""})
	tests := []struct {
		path string
		want core.Commands
	}{
		{"Gemfile", core.Commands{Install: []string{"bundle install"}, Test: []string{"bundle exec rspec"}}},
		{"svc/Gemfile", core.Commands{Install: []string{"cd svc && bundle install"}, Test: []string{"cd svc && bundle exec rake test"}}},
		{"lib/Gemfile", core.Commands{Install: []string{"cd lib && bundle install"}}},
	}
	for _, tt := range tests {
		got := (rubygems{}).Commands(root, core.Manifest{Path: tt.path})
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Commands(%s) = %+v, want %+v", tt.path, got, tt.want)
		}
	}
}
