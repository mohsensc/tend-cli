package eco

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/fetch"
)

func TestActionsDetect(t *testing.T) {
	root := copyTestdata(t, "actions/repo")
	write(t, root, map[string]string{"docs/ci.yml": "on: push", ".github/dependabot.yml": "version: 2"})
	got := manifestsIn(t, actions{}, root)
	var paths []string
	for _, m := range got {
		if m.Ecosystem != "actions" || m.Tool != "actions" || m.Lock != "" {
			t.Errorf("manifest = %+v", m)
		}
		paths = append(paths, m.Path)
	}
	want := []string{".github/workflows/ci.yml", ".github/workflows/release.yaml", "action.yml"}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("Detect = %v\nwant %v", paths, want)
	}
}

func TestActionsDeps(t *testing.T) {
	root := copyTestdata(t, "actions/repo")
	for _, tt := range []struct{ name, manifest string }{
		{"workflow", ".github/workflows/ci.yml"},
		{"release", ".github/workflows/release.yaml"},
		{"composite", "action.yml"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			goldenCheck(t, "actions/deps/"+tt.name+".golden.json", jsonOf(t, depsIn(t, actions{}, root, tt.manifest)))
		})
	}
}

func TestActionsDepsShapes(t *testing.T) {
	root := copyTestdata(t, "actions/repo")
	got := map[string]string{}
	for _, d := range depsIn(t, actions{}, root, ".github/workflows/ci.yml") {
		got[d.Name] = fmt.Sprintf("line=%d version=%s req=%s pin=%s", d.Line, d.Version, d.Req, d.Pin)
	}
	want := map[string]string{
		"actions/checkout":         "line=8 version=v2 req=v2 pin=",
		"actions/setup-node":       "line=9 version=v4.1.0 req=v4.1.0 pin=",
		"actions/cache":            "line=12 version=v4.0.2 req=0c45773b623bea8c8e75f6c82b208c3cf94ea4f9 pin=0c45773b623bea8c8e75f6c82b208c3cf94ea4f9",
		"docker/build-push-action": "line=13 version=v4 req=3b5e8027fcad23fda98b2e3ac259d8d67585f671 pin=3b5e8027fcad23fda98b2e3ac259d8d67585f671",
		"owner/tool":               "line=14 version=main req=main pin=",
		"actions/upload-artifact":  "line=19 version=a8a3f3ad30e3422c9c7b888a15615d96a6e6e6a2 req=a8a3f3ad30e3422c9c7b888a15615d96a6e6e6a2 pin=a8a3f3ad30e3422c9c7b888a15615d96a6e6e6a2",
		"acme/workflows":           "line=21 version=v1.2 req=v1.2 pin=",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deps = %v\nwant %v", got, want)
	}
}

func TestSplitUses(t *testing.T) {
	tests := []struct {
		in             string
		repo, sub, ref string
		ok             bool
	}{
		{"actions/checkout@v4", "actions/checkout", "", "v4", true},
		{"github/codeql-action/init@v3", "github/codeql-action", "/init", "v3", true},
		{"o/r/.github/workflows/x.yml@main", "o/r", "/.github/workflows/x.yml", "main", true},
		{"o/r@refs/heads/dev", "o/r", "", "refs/heads/dev", true},
		{"./local@v1", "", "", "", false},
		{"./.github/workflows/x.yml", "", "", "", false},
		{"docker://alpine:3.20", "", "", "", false},
		{"actions", "", "", "", false},
		{"actions@v1", "", "", "", false},
	}
	for _, tt := range tests {
		repo, sub, ref, ok := splitUses(tt.in)
		if repo != tt.repo || sub != tt.sub || ref != tt.ref || ok != tt.ok {
			t.Errorf("splitUses(%q) = %q %q %q %v", tt.in, repo, sub, ref, ok)
		}
	}
}

const (
	shaCache = "0c45773b623bea8c8e75f6c82b208c3cf94ea4f9"
	shaBuild = "3b5e8027fcad23fda98b2e3ac259d8d67585f671"
	shaNewC  = "1bd1e32a3bdc45362d1e726936510720a7c30a57"
	shaNewB  = "4f58ea79222b3b9dc2c8bbdd6debcef730109a75"
	shaNewU  = "ea165f8d65b6e75b540449e92b4886f43607fa02"
)

// seedTags runs Meta against a canned tag listing so Bump knows the commits.
func seedTags(t *testing.T, repo string, tags map[string]string) {
	t.Helper()
	var items []string
	for name, sha := range tags {
		items = append(items, fmt.Sprintf(`{"name": %q, "commit": {"sha": %q}}`, name, sha))
	}
	sort.Strings(items)
	f := replayCanned(t, canned{url: "https://api.github.com/repos/" + repo + "/tags?per_page=100", body: "[" + strings.Join(items, ",") + "]"})
	if _, err := (actions{}).Meta(context.Background(), f, repo); err != nil {
		t.Fatal(err)
	}
}

func TestActionsBump(t *testing.T) {
	seedTags(t, "actions/cache", map[string]string{"v4": shaNewC, "v4.2": shaNewC, "v4.2.0": shaNewC, "v4.0.2": shaCache})
	seedTags(t, "docker/build-push-action", map[string]string{"v6": shaNewB, "v6.1.0": shaNewB, "v6.1": shaNewB, "v4": shaBuild})
	seedTags(t, "actions/upload-artifact", map[string]string{"v4.6.0": shaNewU, "v4": shaNewU})
	bumpGolden(t, actions{}, "actions/repo", []bumpCase{
		{"major-ref", ".github/workflows/ci.yml", "actions/checkout", true, "v4.2.2", ""},
		{"full-ref", ".github/workflows/ci.yml", "actions/setup-node", true, "v5.0.0", ""},
		{"sha-with-comment", ".github/workflows/ci.yml", "actions/cache", true, "v4.2.0", ""},
		{"sha-quoted-major-comment", ".github/workflows/ci.yml", "docker/build-push-action", true, "v6", ""},
		{"sha-without-comment", ".github/workflows/ci.yml", "actions/upload-artifact", true, "v4.6.0", ""},
		{"reusable-workflow", ".github/workflows/ci.yml", "acme/workflows", true, "v2.1.0", ""},
		{"composite-action", "action.yml", "actions/checkout", true, "v4.2.2", ""},
		{"swap", ".github/workflows/ci.yml", "actions/checkout", true, "v4.2.2", "acme/checkout"},
		{"already-there", ".github/workflows/release.yaml", "actions/checkout", true, "v4.2.2", ""},
	})
}

func TestActionsBumpSwapPinned(t *testing.T) {
	root := copyTestdata(t, "actions/repo")
	seedTags(t, "actions/cache", map[string]string{"v5.0.0": shaNewC, "v4.0.2": shaCache})
	seedTags(t, "acme/cache", map[string]string{"v5.0.0": shaNewU})
	cache := depIn(t, actions{}, root, ".github/workflows/ci.yml", "actions/cache", true)
	if _, err := (actions{}).Bump(context.Background(), root, cache, "v5.0.0", "acme/cache"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "uses: acme/cache@" + shaNewU + " # v5.0.0"; !strings.Contains(string(b), want) {
		t.Errorf("swap wrote\n%s\nwant a line with %q", b, want)
	}
}

func TestActionsBumpErrors(t *testing.T) {
	root := copyTestdata(t, "actions/repo")
	ctx := context.Background()
	branch := depIn(t, actions{}, root, ".github/workflows/ci.yml", "owner/tool", true)
	if _, err := (actions{}).Bump(ctx, root, branch, "v2.0.0", ""); err == nil || !strings.Contains(err.Error(), "not a version") {
		t.Errorf("bumped a branch ref: %v", err)
	}
	cache := depIn(t, actions{}, root, ".github/workflows/ci.yml", "actions/cache", true)
	actionsShas.Delete("actions/cache")
	if _, err := (actions{}).Bump(ctx, root, cache, "v4.2.0", ""); err == nil || !strings.Contains(err.Error(), "Meta") {
		t.Errorf("pinned without a tag listing: %v", err)
	}
	seedTags(t, "actions/cache", map[string]string{"v4.0.2": shaCache})
	if _, err := (actions{}).Bump(ctx, root, cache, "v9.9.9", ""); err == nil || !strings.Contains(err.Error(), "not in the listing") {
		t.Errorf("pinned to a tag nobody has: %v", err)
	}
	checkout := depIn(t, actions{}, root, ".github/workflows/ci.yml", "actions/checkout", true)
	checkout.Line = 1
	if _, err := (actions{}).Bump(ctx, root, checkout, "v4", ""); err == nil {
		t.Error("bumped a line that has no uses")
	}
	checkout.Line = 12
	if _, err := (actions{}).Bump(ctx, root, checkout, "v4", ""); err == nil {
		t.Error("bumped a line that uses another action")
	}
	checkout.Line = 500
	if _, err := (actions{}).Bump(ctx, root, checkout, "v4", ""); err == nil {
		t.Error("bumped a line past the end")
	}
}

func TestWithTagComment(t *testing.T) {
	tests := []struct{ in, want string }{
		{"- uses: a/b@abc # v1.0.0", "- uses: a/b@abc # v2.0.0"},
		{"- uses: a/b@abc   #v1", "- uses: a/b@abc   #v2.0.0"},
		{"- uses: a/b@abc", "- uses: a/b@abc # v2.0.0"},
		{"- uses: a/b@abc # pinned", "- uses: a/b@abc # pinned # v2.0.0"},
	}
	for _, tt := range tests {
		if got := withTagComment(tt.in, "v2.0.0"); got != tt.want {
			t.Errorf("withTagComment(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func actionFile(using string) string {
	return base64.StdEncoding.EncodeToString([]byte("name: x\nruns:\n  using: " + using + "\n  main: index.js\n"))
}

func TestActionsMeta(t *testing.T) {
	ctx := context.Background()
	t.Run("recorded", func(t *testing.T) {
		m, err := (actions{}).Meta(ctx, fetch.Replay(recordedDir("actions")), "actions/checkout")
		if err != nil {
			t.Fatal(err)
		}
		if m.Repo != "https://github.com/actions/checkout" || m.Deprecated != "" || m.Latest == "" || len(m.Releases) < 4 {
			t.Fatalf("meta = %+v", m)
		}
		got := map[string]string{}
		for _, r := range m.Releases {
			got[r.Version] = r.Deprecated
			if r.Version == m.Latest && r.Pre {
				t.Errorf("latest %s is a pre-release", r.Version)
			}
		}
		want := map[string]string{
			"v1.2.0": "",
			"v2":     "runs on node12, removed from GitHub runners",
			"v2.8.0": "runs on node12, removed from GitHub runners",
			"v3":     "runs on node16, removed from GitHub runners",
			"v4.4.0": "runs on node20, removed from GitHub runners",
			"v4":     "runs on node20, removed from GitHub runners",
			"v5.1.0": "",
			"v7":     "",
		}
		for v, dep := range want {
			if d, ok := got[v]; !ok || d != dep {
				t.Errorf("%s: deprecated %q (listed %v), want %q", v, d, ok, dep)
			}
		}
		if m.Latest != "v7.0.1" {
			t.Errorf("latest = %q", m.Latest)
		}
		for _, r := range m.Releases {
			if r.Version == "v6-beta" && !r.Pre {
				t.Error("v6-beta must be a pre-release")
			}
		}
	})
	t.Run("pages, runtimes, archive and prereleases", func(t *testing.T) {
		const api = "https://api.github.com/repos/acme/tool"
		f := replayCanned(t,
			canned{url: api + "/tags?per_page=100", header: map[string][]string{"Link": {`<` + api + `/tags?per_page=100&page=2>; rel="next", <` + api + `/tags?per_page=100&page=2>; rel="last"`}},
				body: `[{"name": "v3", "commit": {"sha": "c3"}}, {"name": "v3.1.0", "commit": {"sha": "c3"}}, {"name": "nightly", "commit": {"sha": "cn"}}, {"name": "v2.9.0", "commit": {"sha": "c29"}}]`},
			canned{url: api + "/tags?per_page=100&page=2", body: `[{"name": "v2", "commit": {"sha": "c29"}}, {"name": "v1.0.0", "commit": {"sha": "c1"}}, {"name": "v4.0.0-rc.1", "commit": {"sha": "c4"}}]`},
			canned{url: api + "/releases?per_page=100", body: `[
				{"tag_name": "v4.0.0-rc.1", "published_at": "2024-03-01T00:00:00Z", "prerelease": true},
				{"tag_name": "v3.1.0", "published_at": "2024-02-01T00:00:00Z", "prerelease": false}]`},
			canned{url: api, body: `{"full_name": "acme/tool", "archived": true}`},
			canned{url: api + "/contents/action.yml?ref=v3.1.0", body: `{"encoding": "base64", "content": "` + actionFile("node20") + `"}`},
			canned{url: api + "/contents/action.yml?ref=v2.9.0", status: 404, body: `{"message": "Not Found"}`},
			canned{url: api + "/contents/action.yaml?ref=v2.9.0", body: `{"encoding": "base64", "content": "` + actionFile("node16") + `"}`},
			canned{url: api + "/contents/action.yml?ref=v1.0.0", status: 404, body: `{}`},
			canned{url: api + "/contents/action.yaml?ref=v1.0.0", status: 404, body: `{}`})
		m, err := (actions{}).Meta(ctx, f, "acme/tool")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range m.Releases {
			got = append(got, fmt.Sprintf("%s pre=%v at=%s dep=%q", r.Version, r.Pre, r.Time.Format("2006-01-02"), r.Deprecated))
		}
		want := []string{
			`v1.0.0 pre=false at=0001-01-01 dep=""`,
			`v2 pre=false at=0001-01-01 dep="runs on node16, removed from GitHub runners"`,
			`v2.9.0 pre=false at=0001-01-01 dep="runs on node16, removed from GitHub runners"`,
			`v3 pre=false at=0001-01-01 dep="runs on node20, removed from GitHub runners"`,
			`v3.1.0 pre=false at=2024-02-01 dep="runs on node20, removed from GitHub runners"`,
			`v4.0.0-rc.1 pre=true at=2024-03-01 dep=""`,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("releases:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		if m.Latest != "v3.1.0" || m.Deprecated != "repository archived" || m.Repo != "https://github.com/acme/tool" {
			t.Errorf("meta = %+v", m)
		}
	})
	t.Run("current runtime is fine", func(t *testing.T) {
		const api = "https://api.github.com/repos/acme/modern"
		f := replayCanned(t,
			canned{url: api + "/tags?per_page=100", body: `[{"name": "v5", "commit": {"sha": "a"}}, {"name": "v5.0.0", "commit": {"sha": "a"}}]`},
			canned{url: api + "/contents/action.yml?ref=v5.0.0", body: `{"content": "` + actionFile("node24") + `"}`})
		m, err := (actions{}).Meta(ctx, f, "acme/modern")
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range m.Releases {
			if r.Deprecated != "" {
				t.Errorf("%s: %q", r.Version, r.Deprecated)
			}
		}
		if m.Latest != "v5.0.0" {
			t.Errorf("latest = %q", m.Latest)
		}
	})
	t.Run("composite and docker actions are not deprecated", func(t *testing.T) {
		const api = "https://api.github.com/repos/acme/comp"
		f := replayCanned(t,
			canned{url: api + "/tags?per_page=100", body: `[{"name": "v1", "commit": {"sha": "a"}}]`},
			canned{url: api + "/contents/action.yml?ref=v1", body: `{"content": "` + actionFile("composite") + `"}`})
		m, err := (actions{}).Meta(ctx, f, "acme/comp")
		if err != nil || m.Releases[0].Deprecated != "" {
			t.Errorf("meta = %+v, %v", m, err)
		}
	})
	t.Run("not found", func(t *testing.T) {
		f := replayCanned(t, canned{url: "https://api.github.com/repos/acme/nope/tags?per_page=100", status: 404, body: `{"message": "Not Found"}`})
		if _, err := (actions{}).Meta(ctx, f, "acme/nope"); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("rate limited", func(t *testing.T) {
		f := replayCanned(t, canned{url: "https://api.github.com/repos/acme/busy/tags?per_page=100", status: http.StatusForbidden, body: `{"message": "rate limit exceeded"}`})
		if _, err := (actions{}).Meta(ctx, f, "acme/busy"); err == nil || !strings.Contains(err.Error(), "403") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestActionsMetaRecord(t *testing.T) {
	wantRecord(t)
	if _, err := (actions{}).Meta(context.Background(), liveClient(t, "actions"), "actions/checkout"); err != nil {
		t.Fatal(err)
	}
}

func TestActionsPin(t *testing.T) {
	seedTags(t, "pin/test", map[string]string{"v1": "s1", "v1.2": "s1", "v1.2.3": "s1", "v1.2.4": "s3", "latest": "s2", "v2": "s2", "v2.0.1": "s2"})
	tests := []struct{ tag, sha, name string }{
		{"v1", "s1", "v1.2.3"},
		{"v1.2.4", "s3", "v1.2.4"},
		{"v2", "s2", "v2.0.1"},
	}
	for _, tt := range tests {
		sha, name, err := actionPin("pin/test", tt.tag)
		if err != nil || sha != tt.sha || name != tt.name {
			t.Errorf("actionPin(%s) = %q %q %v, want %q %q", tt.tag, sha, name, err, tt.sha, tt.name)
		}
	}
}

func TestRetiredCommands(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"double quotes", `echo "::set-output name=sha::abc123"`, `echo "sha=abc123" >> "$GITHUB_OUTPUT"`},
		{"in a run line", `        run: echo "::set-output name=sha::$(git rev-parse HEAD)"`, `        run: echo "sha=$(git rev-parse HEAD)" >> "$GITHUB_OUTPUT"`},
		{"nested quotes", `echo "::set-output name=files::$(ls "a b" | tr '\n' ' ')"`, `echo "files=$(ls "a b" | tr '\n' ' ')" >> "$GITHUB_OUTPUT"`},
		{"expression", `echo "::set-output name=tag::${{ github.ref_name }}"`, `echo "tag=${{ github.ref_name }}" >> "$GITHUB_OUTPUT"`},
		{"dotted dashed name", `echo "::set-output name=my-out.put_1::v"`, `echo "my-out.put_1=v" >> "$GITHUB_OUTPUT"`},
		{"single quotes", `echo '::set-output name=version::1.2.3'`, `echo 'version=1.2.3' >> "$GITHUB_OUTPUT"`},
		{"no quotes", `echo ::set-output name=version::1.2.3`, `echo "version=1.2.3" >> "$GITHUB_OUTPUT"`},
		{"trailing space", `echo "::set-output name=a::b"  `, `echo "a=b" >> "$GITHUB_OUTPUT"`},
		{"empty value", `echo "::set-output name=a::"`, `echo "a=" >> "$GITHUB_OUTPUT"`},
		{"save-state", `echo "::save-state name=pid::$$"`, `echo "pid=$$" >> "$GITHUB_STATE"`},
		{"save-state single quotes", `echo '::save-state name=pid::1'`, `echo 'pid=1' >> "$GITHUB_STATE"`},
		{"save-state bare", `echo ::save-state name=pid::1`, `echo "pid=1" >> "$GITHUB_STATE"`},
		{"other commands are left alone", `echo "::warning::careful"`, ""},
		{"already migrated", `echo "sha=abc" >> "$GITHUB_OUTPUT"`, ""},
		{"not an echo", `printf "::set-output name=a::b"`, ""},
		{"command after it", `echo "::set-output name=a::b" && true`, ""},
		{"plain text", `make build`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := RetiredCommands([]string{tt.in})
			if tt.want == "" {
				if c != nil {
					t.Fatalf("codemod for a line that needs none: %q", c.Text)
				}
				return
			}
			if c == nil || len(c.Text) != 1 || len(c.Renames)+len(c.Imports)+len(c.Paths)+len(c.Fields) != 0 {
				t.Fatalf("codemod = %+v", c)
			}
			got := tt.in
			for old, now := range c.Text {
				got = strings.Replace(got, old, now, 1)
			}
			if got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
			if RetiredCommands([]string{got}) != nil {
				t.Errorf("not idempotent: %s", got)
			}
		})
	}
}

func TestActionsNothingToRun(t *testing.T) {
	if got := (actions{}).Commands("", core.Manifest{}); !reflect.DeepEqual(got, core.Commands{}) {
		t.Errorf("Commands = %+v", got)
	}
	if got := (actions{}).Imports(core.Dep{Name: "actions/checkout"}); got != nil {
		t.Errorf("Imports = %v", got)
	}
}

func TestActionsBumpKeepsOtherLines(t *testing.T) {
	seedTags(t, "actions/cache", map[string]string{"v4.2.0": shaNewC})
	root := copyTestdata(t, "actions/repo")
	p := filepath.Join(root, ".github/workflows/ci.yml")
	before, _ := os.ReadFile(p)
	d := depIn(t, actions{}, root, ".github/workflows/ci.yml", "actions/cache", true)
	if _, err := (actions{}).Bump(context.Background(), root, d, "v4.2.0", ""); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(p)
	want := strings.Replace(string(before), "cache@"+shaCache+" # v4.0.2", "cache@"+shaNewC+" # v4.2.0", 1)
	if string(after) != want {
		t.Errorf("file changed beyond the one line:\n%s", after)
	}
}
