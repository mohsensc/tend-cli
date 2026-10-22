package source

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

const (
	relURL = "GET https://api.github.com/repos/o/r/releases?per_page=100"
	rawURL = "GET https://raw.githubusercontent.com/o/r/HEAD/"
)

var meta = &core.Meta{Name: "pkg", Repo: "https://github.com/o/r"}

func release(tag, name, body string) map[string]any {
	return map[string]any{"tag_name": tag, "name": name, "body": body, "html_url": "https://github.com/o/r/releases/tag/" + tag, "draft": false, "prerelease": false}
}

func tags(ev []core.Evidence) []string {
	var res []string
	for _, e := range ev {
		res = append(res, e.Title)
	}
	return res
}

func TestNotesReleases(t *testing.T) {
	draft := release("v5.5.0", "", "draft")
	draft["draft"] = true
	flagged := release("v5.6.0", "", "flagged")
	flagged["prerelease"] = true
	f := routes(map[string]*core.Response{relURL: ok([]any{
		release("v6.3.0", "", "too new"),
		release("v6.2.0", "Fancy name", "last one"),
		release("pkg@6.1.5", "", "monorepo, ours"),
		release("other@6.1.9", "", "monorepo, not ours"),
		release("v6.1.0-rc.1", "", "rc"),
		flagged,
		draft,
		release("v6.0.0", "v6.0.0", strings.Repeat("x", 3000)),
		release("v5.1.0", "", "  padded\n\n"),
		release("v5.0.0", "", "the one we run"),
		release("v4.0.0", "", "old"),
		release("nightly", "", "no version"),
	})})
	got, err := Notes(context.Background(), f, meta, "5.0.0", "6.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"v6.2.0 Fancy name", "pkg@6.1.5", "v6.0.0", "v5.1.0"}; !reflect.DeepEqual(tags(got), want) {
		t.Fatalf("titles = %q, want %q", tags(got), want)
	}
	if e := got[0]; e.Source != "releases" || e.Text != "last one" || e.URL != "https://github.com/o/r/releases/tag/v6.2.0" {
		t.Errorf("evidence = %+v", e)
	}
	if len(got[2].Text) != 1200 {
		t.Errorf("body is %d bytes, want 1200", len(got[2].Text))
	}
	if got[3].Text != "padded" {
		t.Errorf("body = %q", got[3].Text)
	}
}

func TestNotesReleaseCap(t *testing.T) {
	var all []any
	for i := range 20 {
		all = append(all, release(fmt.Sprintf("v1.%d.0", i), "", "n"))
	}
	f := routes(map[string]*core.Response{relURL: ok(all)})
	got, err := Notes(context.Background(), f, meta, "1.0.0", "1.19.0")
	if err != nil || len(got) != 8 || got[0].Title != "v1.19.0" || got[7].Title != "v1.12.0" {
		t.Fatalf("got %v, %v; want the newest 8", tags(got), err)
	}
}

func TestNotesReleasePages(t *testing.T) {
	page := func(from, to int, next string) *core.Response {
		var rels []any
		for i := to; i >= from; i-- {
			rels = append(rels, release(fmt.Sprintf("v1.%d.0", i), "", "n"))
		}
		res := ok(rels)
		if next != "" {
			res.Header = map[string][]string{"Link": {`<` + next + `>; rel="next", <https://api.github.com/x?page=9>; rel="last"`}}
		}
		return res
	}
	const p2 = "https://api.github.com/repositories/1/releases?per_page=100&page=2"
	const p3 = "https://api.github.com/repositories/1/releases?per_page=100&page=3"
	tests := []struct {
		name     string
		pages    map[string]*core.Response
		from, to string
		want     []string
	}{
		{"past the first page", map[string]*core.Response{
			relURL:      page(200, 299, p2),
			"GET " + p2: page(100, 199, p3),
			"GET " + p3: page(0, 99, ""),
		}, "1.149.0", "1.150.0", []string{"v1.150.0"}},
		{"a later page fails", map[string]*core.Response{
			relURL:      page(200, 299, p2),
			"GET " + p2: status(502),
		}, "1.249.0", "1.250.0", []string{"v1.250.0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := routes(tt.pages)
			got, err := Notes(context.Background(), f, meta, tt.from, tt.to)
			if err != nil || !reflect.DeepEqual(tags(got), tt.want) {
				t.Fatalf("got %q, %v; want %q", tags(got), err, tt.want)
			}
		})
	}
	var asked int
	f := &fake{h: func(method, url string, body []byte) *core.Response {
		asked++
		return page(asked, asked, fmt.Sprintf("https://api.github.com/x?page=%d", asked+1))
	}}
	if _, err := Notes(context.Background(), f, meta, "1.0.0", "1.100.0"); err != nil || asked != releasePages {
		t.Errorf("asked for %d pages, %v; want at most %d", asked, err, releasePages)
	}

	// The page holding the version in use is the last one read.
	asked = 0
	f = &fake{h: func(method, url string, body []byte) *core.Response {
		asked++
		return page(100*(3-asked), 100*(3-asked)+99, fmt.Sprintf("https://api.github.com/x?page=%d", asked+1))
	}}
	got, err := Notes(context.Background(), f, meta, "1.150.0", "1.151.0")
	if err != nil || asked != 2 || !reflect.DeepEqual(tags(got), []string{"v1.151.0"}) {
		t.Errorf("asked for %d pages, got %q, %v; want 2 pages and v1.151.0", asked, tags(got), err)
	}
}

func TestNotesPrereleaseTarget(t *testing.T) {
	rc := release("v7.0.0-rc.1", "", "rc one")
	rc["prerelease"] = true
	f := routes(map[string]*core.Response{relURL: ok([]any{rc, release("v6.9.0", "", "stable")})})
	got, err := Notes(context.Background(), f, meta, "6.0.0", "7.0.0-rc.1")
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v, %v; want the rc and the stable release", tags(got), err)
	}
}

func TestNotesUnknownFrom(t *testing.T) {
	f := routes(map[string]*core.Response{relURL: ok([]any{release("v1.0.0", "", "a"), release("v2.0.0", "", "b")})})
	got, err := Notes(context.Background(), f, meta, "", "2.0.0")
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v, %v", tags(got), err)
	}
}

func TestNotesRepoSpellings(t *testing.T) {
	for _, repo := range []string{"https://github.com/o/r", "git+https://github.com/o/r.git", "git@github.com:o/r.git", "https://github.com/o/r/tree/main"} {
		f := routes(map[string]*core.Response{relURL: ok([]any{release("v1.1.0", "", "x")})})
		got, err := Notes(context.Background(), f, &core.Meta{Repo: repo}, "1.0.0", "1.1.0")
		if err != nil || len(got) != 1 {
			t.Errorf("%s: got %v, %v", repo, tags(got), err)
		}
	}
}

func TestNotesChangelogFallback(t *testing.T) {
	f := routes(map[string]*core.Response{
		relURL:                  ok([]any{}),
		rawURL + "CHANGELOG.md": status(404),
		rawURL + "CHANGES.md":   text("## [1.2.0] - 2024-01-02\n- added a thing\n\n## [1.1.0] - 2024-01-01\n- fixed it\n\n## [1.0.0]\n- first\n"),
	})
	got, err := Notes(context.Background(), f, meta, "1.0.0", "1.2.0")
	if err != nil {
		t.Fatal(err)
	}
	want := []core.Evidence{
		{Source: "changelog", Title: "[1.2.0] - 2024-01-02", Text: "- added a thing", URL: "https://github.com/o/r/blob/HEAD/CHANGES.md"},
		{Source: "changelog", Title: "[1.1.0] - 2024-01-01", Text: "- fixed it", URL: "https://github.com/o/r/blob/HEAD/CHANGES.md"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestNotesChangelogFileOrder(t *testing.T) {
	f := routes(map[string]*core.Response{
		relURL:                   status(404),
		rawURL + "CHANGELOG.md":  text("# Changelog\n\nnothing versioned here\n"),
		rawURL + "CHANGES.md":    status(404),
		rawURL + "HISTORY.md":    text("1.1.0\n=====\n\nrst-style heading in a md file\n"),
		rawURL + "CHANGELOG.rst": text("never reached"),
	})
	got, err := Notes(context.Background(), f, meta, "1.0.0", "1.1.0")
	if err != nil || len(got) != 1 || got[0].URL != "https://github.com/o/r/blob/HEAD/HISTORY.md" {
		t.Fatalf("got %+v, %v; want the first file that has the versions", got, err)
	}
	for _, c := range f.calls {
		if strings.HasSuffix(c.URL, ".rst") {
			t.Error("read CHANGELOG.rst after HISTORY.md had the answer")
		}
	}
}

func TestNotesNothingFound(t *testing.T) {
	f := &fake{h: func(method, url string, body []byte) *core.Response {
		if strings.Contains(url, "/releases") {
			return ok([]any{})
		}
		return status(404)
	}}
	got, err := Notes(context.Background(), f, meta, "1.0.0", "1.1.0")
	if got != nil || err != nil {
		t.Fatalf("got %v, %v; want quiet nothing", got, err)
	}
}

func TestNotesErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := Notes(ctx, routes(nil), &core.Meta{Repo: "https://gitlab.com/o/r"}, "1.0.0", "1.1.0"); !errors.Is(err, core.ErrNotSupported) {
		t.Errorf("non-GitHub repo: err = %v", err)
	}
	if _, err := Notes(ctx, routes(nil), &core.Meta{}, "1.0.0", "1.1.0"); !errors.Is(err, core.ErrNotSupported) {
		t.Errorf("no repo: err = %v", err)
	}
	if got, err := Notes(ctx, routes(nil), meta, "1.0.0", "bogus"); got != nil || err != nil {
		t.Errorf("unparsable target: %v, %v", got, err)
	}
	limited := &fake{h: func(method, url string, body []byte) *core.Response {
		if strings.Contains(url, "/releases") {
			return status(403)
		}
		return status(404)
	}}
	if _, err := Notes(ctx, limited, meta, "1.0.0", "1.1.0"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("rate limit with no changelog: err = %v, want it surfaced", err)
	}
	limited.h = func(method, url string, body []byte) *core.Response {
		if strings.Contains(url, "/releases") {
			return status(403)
		}
		return text("## 1.1.0\n- ok\n")
	}
	if got, err := Notes(ctx, limited, meta, "1.0.0", "1.1.0"); err != nil || len(got) != 1 {
		t.Errorf("rate limit with a changelog: %v, %v", got, err)
	}
}

func TestSections(t *testing.T) {
	tests := []struct {
		name string
		text string
		want map[string]string // title -> body
	}{
		{"keep a changelog", "# Changelog\n\n## [Unreleased]\n- wip\n\n## [1.2.3] - 2020-01-01\n### Added\n- a\n\n### Fixed\n- b\n\n## [1.2.2] - 2019-12-31\n- c\n\n[1.2.3]: https://example.com/1.2.3\n[1.2.2]: https://example.com/1.2.2\n",
			map[string]string{"[1.2.3] - 2020-01-01": "### Added\n- a\n\n### Fixed\n- b", "[1.2.2] - 2019-12-31": "- c"}},
		{"v prefix", "## v2.0.0\nbreaking stuff\n## v1.9.0\nfine\n",
			map[string]string{"v2.0.0": "breaking stuff", "v1.9.0": "fine"}},
		{"bare version with date", "## 2.0.0 (2020-05-05)\n- x\n", map[string]string{"2.0.0 (2020-05-05)": "- x"}},
		{"h3 versions under h2 groups", "## Releases\n### 3.1.0\n- a\n### 3.0.0\n- b\n## Other\ntext\n",
			map[string]string{"3.1.0": "- a", "3.0.0": "- b"}},
		{"title with link", "## [4.0.0](https://example.com/compare/v3...v4) (2021-01-01)\n- z\n",
			map[string]string{"4.0.0 (2021-01-01)": "- z"}},
		{"h1 versions", "# 5.0.0\n- a\n# 4.0.0\n- b\n", map[string]string{"5.0.0": "- a", "4.0.0": "- b"}},
		{"setext", "6.1.0\n=====\n\n- a\n\n6.0.0\n-----\n\n- b\n", map[string]string{"6.1.0": "- a", "6.0.0": "- b"}},
		{"rst", "Changelog\n=========\n\n7.0.0 (2022-02-02)\n------------------\n\n- a\n\nBug fixes\n~~~~~~~~~\n\n- b\n\n6.9.0 (2022-01-01)\n------------------\n\n- c\n",
			map[string]string{"7.0.0 (2022-02-02)": "- a\n\nBug fixes\n~~~~~~~~~\n\n- b", "6.9.0 (2022-01-01)": "- c"}},
		{"code fence hides headings", "## 8.0.0\n```sh\n# 1.0.0 is not a heading\n## nor is this\n```\ndone\n", map[string]string{"8.0.0": "```sh\n# 1.0.0 is not a heading\n## nor is this\n```\ndone"}},
		{"tilde fence", "## 8.1.0\n~~~sh\n# 1.0.0 nope\n~~~\nok\n## 8.0.0\n- a\n", map[string]string{"8.1.0": "~~~sh\n# 1.0.0 nope\n~~~\nok", "8.0.0": "- a"}},
		{"two part version", "## 9.1\n- a\n", map[string]string{"9.1": "- a"}},
		{"prerelease", "## 10.0.0-rc.1\n- a\n", map[string]string{"10.0.0-rc.1": "- a"}},
		{"crlf", "## 11.0.0\r\n- a\r\n", map[string]string{"11.0.0": "- a"}},
		{"no versions", "## Intro\ntext\n## Usage\nmore\n", map[string]string{}},
		{"empty section is kept for the caller to drop", "## 12.0.0\n## 11.0.0\n- a\n", map[string]string{"12.0.0": "", "11.0.0": "- a"}},
		{"hash without space is text", "## 13.0.0\n#hashtag 1.2.3\n", map[string]string{"13.0.0": "#hashtag 1.2.3"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := map[string]string{}
			for _, s := range sections(tc.text) {
				got[s.title] = s.body
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("sections = %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestNotesReplayReleases(t *testing.T) {
	got, err := Notes(context.Background(), replay(), releasesMeta, releasesFrom, releasesTo)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"v6.2.0", "v6.1.0", "v6.0.0"}; !reflect.DeepEqual(tags(got), want) {
		t.Fatalf("titles = %v, want %v", tags(got), want)
	}
	if got[0].Source != "releases" || got[0].Text == "" || got[0].URL != "https://github.com/sindresorhus/p-limit/releases/tag/v6.2.0" {
		t.Errorf("evidence = %+v", got[0])
	}
}

func TestNotesReplayChangelog(t *testing.T) {
	got, err := Notes(context.Background(), replay(), changelogMeta, changelogFrom, changelogTo)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"v2.88.0 (2018/08/10)", "v2.87.0 (2018/05/21)", "v2.86.0 (2018/05/15)"}; !reflect.DeepEqual(tags(got), want) {
		t.Fatalf("titles = %v, want %v", tags(got), want)
	}
	e := got[0]
	if e.Source != "changelog" || !strings.HasPrefix(e.Text, "- [#2996]") || strings.Contains(e.Text, "v2.87.0") || e.URL != "https://github.com/request/request/blob/HEAD/CHANGELOG.md" {
		t.Errorf("evidence = %+v", e)
	}
}

func site(chain ...string) core.Site {
	return core.Site{File: "a.js", Line: 1, Chain: chain, Call: true}
}

func TestMentions(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		sites []core.Site
		want  []string // "token|paragraph"
	}{
		{"deprecated, last two segments", "- `charges.create` is deprecated, use `paymentIntents.create`", []core.Site{site("stripe", "charges", "create")},
			[]string{"charges.create|- `charges.create` is deprecated, use `paymentIntents.create`"}},
		{"removed, last segment", "- Removed `create`.", []core.Site{site("stripe", "charges", "create")}, []string{"create|- Removed `create`."}},
		{"renamed", "- `send` renamed to `deliver`", []core.Site{site("mail", "send")}, []string{"send|- `send` renamed to `deliver`"}},
		{"breaking", "BREAKING: `client.connect()` now returns a promise", []core.Site{site("client", "connect")}, []string{"client.connect|BREAKING: `client.connect()` now returns a promise"}},
		{"call parens with arguments", "- `retrieve(id)` has been removed", []core.Site{site("customers", "retrieve")}, []string{"retrieve|- `retrieve(id)` has been removed"}},
		{"leading dot", "- `.create` is gone: removed", []core.Site{site("charges", "create")}, []string{"create|- `.create` is gone: removed"}},
		{"blank line paragraphs", "Intro text.\n\nThe `list` option was deprecated\nand will go away.\n\nOther text `list`.", []core.Site{site("api", "list")},
			[]string{"list|The `list` option was deprecated\nand will go away."}},
		{"bullets split paragraphs", "- removed `a`\n- `b` is fine\n- deprecated `b`\n  with a continuation", []core.Site{site("x", "b")},
			[]string{"b|- deprecated `b`\n  with a continuation"}},
		{"numbered", "1. `old` removed", []core.Site{site("old")}, []string{"old|1. `old` removed"}},
		{"two paragraphs hit", "- `a` removed\n- `a` deprecated", []core.Site{site("a")}, []string{"a|- `a` removed", "a|- `a` deprecated"}},
		{"first token in the paragraph wins", "- `y` and `x` removed", []core.Site{site("x"), site("y")}, []string{"y|- `y` and `x` removed"}},
		{"case insensitive trigger", "- `a` DEPRECATED", []core.Site{site("a")}, []string{"a|- `a` DEPRECATED"}},

		{"no trigger word", "- `charges.create` now takes a currency", []core.Site{site("stripe", "charges", "create")}, nil},
		{"not backticked", "- charges.create is deprecated", []core.Site{site("stripe", "charges", "create")}, nil},
		{"trigger only inside the ticks", "- `removeListener` now takes options", []core.Site{site("ee", "removeListener")}, nil},
		{"token in another paragraph", "- `create` now takes a currency\n\nThings were removed elsewhere.", []core.Site{site("create")}, nil},
		{"longer name is not the name", "- `createCharge` removed", []core.Site{site("stripe", "create")}, nil},
		{"qualified more than the site", "- `stripe.charges.create` removed", []core.Site{site("stripe", "charges", "create")}, nil},
		{"site without a chain", "- `x` removed", []core.Site{{File: "a.js"}}, nil},
		{"no sites", "- `x` removed", nil, nil},
		{"empty text", "", []core.Site{site("x")}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := []core.Evidence{{Source: "releases", Title: "v2.0.0", Text: tc.text, URL: "https://example.com/v2"}}
			got := Mentions(in, tc.sites)
			var have []string
			for _, e := range got {
				tok := strings.TrimSuffix(strings.TrimPrefix(e.Title, "v2.0.0 (mentions "), ")")
				have = append(have, tok+"|"+e.Text)
				if !strings.HasPrefix(e.Title, "v2.0.0 (mentions ") || e.Source != "releases" || e.URL != "https://example.com/v2" {
					t.Errorf("evidence = %+v", e)
				}
			}
			if !reflect.DeepEqual(have, tc.want) {
				t.Errorf("got %q\nwant %q", have, tc.want)
			}
			if in[0].Title != "v2.0.0" || in[0].Text != tc.text {
				t.Errorf("input was modified: %+v", in[0])
			}
		})
	}
}
