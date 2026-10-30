package api

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
)

const rss = `<?xml version="1.0"?>
<rss version="2.0"><channel><title>Acme API changes</title>
<item><title>New filters for /v2/items</title><description>&lt;p&gt;You can now filter /v2/items by tag.&lt;/p&gt;</description><pubDate>Mon, 01 Sep 2026 10:00:00 +0000</pubDate></item>
<item><title>Retiring /v1/things</title><description>&lt;p&gt;/v1/things is deprecated and will be removed on 2027-01-31. Use /v2/items.&lt;/p&gt;</description><pubDate>Tue, 15 Sep 2026 10:00:00 +0000</pubDate></item>
</channel></rss>`

const atom = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom"><title>Changes</title>
<entry><title>Sunsetting the orders endpoint</title><updated>2026-08-01T00:00:00Z</updated><summary>GET /v1/orders is deprecated; it shuts down on 2026-12-01.</summary></entry>
</feed>`

const page = `<html><body><h1>Changelog</h1>
<h2>2026-07-01</h2><p>/v3/report gets a <b>format</b> option.</p>
<h2>2026-06-01</h2><p>/v3/legacy-report is no longer supported after 2026-09-30.</p>
</body></html>`

func TestChangelogs(t *testing.T) {
	cat := []Provider{
		{Name: "acme", Hosts: []string{"api.acme.test"}, Changelog: "https://acme.test/changes.rss"},
		{Name: "orders", Hosts: []string{"orders.test"}, Changelog: "https://orders.test/atom.xml"},
		{Name: "reports", Hosts: []string{"reports.test"}, Changelog: "https://reports.test/changelog.html"},
		{Name: "withspec", Hosts: []string{"spec.test"}, Spec: "https://spec.test/openapi.json", Changelog: "https://spec.test/changes"},
	}
	inv := &core.Inventory{Providers: []core.ProviderUse{
		{Provider: "acme", URLs: []core.URLUse{
			{File: "src/a.js", Line: 3, URL: "https://api.acme.test/v1/things?limit=5"},
			{File: "src/a.js", Line: 9, URL: "https://api.acme.test/v2/items"},
		}},
		{Provider: "orders", URLs: []core.URLUse{{File: "app.py", Line: 7, Method: "GET", URL: "https://orders.test/v1/orders/%s"}}},
		{Provider: "reports", URLs: []core.URLUse{{File: "r.go", Line: 2, URL: "https://reports.test/v3/report"}, {File: "r.go", Line: 4, URL: "https://reports.test/v3/legacy-report"}}},
		{Provider: "withspec", URLs: []core.URLUse{{File: "s.go", Line: 1, URL: "https://spec.test/v1/x"}}},
	}}
	f := fetcher{
		"https://acme.test/changes.rss":       rss,
		"https://orders.test/atom.xml":        atom,
		"https://reports.test/changelog.html": page,
	}
	got, err := (&Changelogs{Cat: cat, Fetch: f}).Changes(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ id, sunset, site, title string }
	var have []row
	for _, c := range got {
		have = append(have, row{c.ID, c.Sunset.Format(time.DateOnly), c.Sites[0].File, c.Evidence[0].Title})
	}
	want := []row{
		{"api/acme//v1/things", "2027-01-31", "src/a.js", "Retiring /v1/things"},
		{"api/orders//v1/orders", "2026-12-01", "app.py", "Sunsetting the orders endpoint"},
		{"api/reports//v3/legacy-report", "2026-09-30", "r.go", "2026-06-01"},
	}
	if len(have) != len(want) {
		t.Fatalf("changes = %+v", have)
	}
	for i := range want {
		if have[i] != want[i] {
			t.Errorf("change %d = %+v, want %+v", i, have[i], want[i])
		}
	}
	if got[0].Kind != core.Deprecation || got[0].Evidence[0].Source != "changelog" || got[0].Evidence[0].Text != "/v1/things is deprecated and will be removed on 2027-01-31. Use /v2/items." {
		t.Errorf("change = %+v", got[0])
	}
}

func TestChangelogFields(t *testing.T) {
	feed := `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom"><title>Changes</title>
<entry><title>Order totals</title><updated>2026-08-01T00:00:00Z</updated><summary>The ` + "`total_cents`" + ` field of /v1/orders is deprecated and goes away on 2027-03-01; read ` + "`total.amount`" + ` instead. The unused_flag field is deprecated too.</summary></entry>
</feed>`
	cat := []Provider{{Name: "orders", Hosts: []string{"orders.test"}, Changelog: "https://orders.test/atom.xml"}}
	inv := &core.Inventory{
		Providers: []core.ProviderUse{{Provider: "orders", URLs: []core.URLUse{{File: "app.py", Line: 7, Method: "GET", URL: "https://orders.test/v1/orders"}}}},
		Index: &index{fields: []core.Site{
			field("app.py", 9, "total_cents"),
			field("app.py", 10, "amount"),
			field("other.py", 2, "unused_flag"),
		}},
	}
	got, err := (&Changelogs{Cat: cat, Fetch: fetcher{"https://orders.test/atom.xml": feed}}).Changes(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range got {
		ids = append(ids, c.ID)
	}
	if len(got) != 1 || ids[0] != "api/orders//v1/orders#total_cents" {
		t.Fatalf("changes = %q; only the field the API's callers read, and not its replacement", ids)
	}
	c := got[0]
	if c.API.What != "response field deprecated" || c.Sunset.Format(time.DateOnly) != "2027-03-01" || len(c.Sites) != 1 || c.Sites[0].Line != 9 {
		t.Errorf("change = %+v", c)
	}
}

// reader answers the changelog question with the reply for the entry title
// the prompt starts with.
type reader struct {
	replies map[string]string
	calls   int
}

func (r *reader) Name() string { return "fake" }

func (r *reader) Complete(_ context.Context, _, prompt string) (string, error) {
	r.calls++
	for title, reply := range r.replies {
		if strings.HasPrefix(prompt, title) {
			return reply, nil
		}
	}
	return `{"items": []}`, nil
}

func TestChangelogsReadByModel(t *testing.T) {
	feed := `<?xml version="1.0"?>
<rss version="2.0"><channel><title>Acme</title>
<item><title>The Things API retires</title><description>We are retiring the whole Things API at the end of March 2027. Move to Items.</description><pubDate>Tue, 15 Sep 2026 10:00:00 +0000</pubDate></item>
<item><title>Leaner orders</title><description>Orders no longer return their legacy totals after October.</description><pubDate>Mon, 01 Sep 2026 10:00:00 +0000</pubDate></item>
<item><title>Faster search</title><description>Search is faster now.</description><pubDate>Sun, 31 Aug 2026 10:00:00 +0000</pubDate></item>
</channel></rss>`
	cat := []Provider{{Name: "acme", Hosts: []string{"api.acme.test"}, Changelog: "https://acme.test/changes.rss"}}
	inv := &core.Inventory{
		Providers: []core.ProviderUse{{Provider: "acme", URLs: []core.URLUse{
			{File: "src/a.js", Line: 3, URL: "https://api.acme.test/v1/things/list"},
			{File: "src/b.js", Line: 5, URL: "https://api.acme.test/v1/things/search"},
			{File: "src/a.js", Line: 9, URL: "https://api.acme.test/v1/orders"},
		}}},
		Index: &index{fields: []core.Site{field("src/a.js", 12, "legacy_total")}},
	}
	m := &reader{replies: map[string]string{
		"The Things API retires": "Here you go:\n" + `{"items": [{"method": "", "path": "/v1/things", "field": "", "sunset": "2027-03-31"}]}`,
		"Leaner orders":          `{"items": [{"path": "/v1/orders", "field": "legacy_total"}]}`,
	}}
	s := &Changelogs{Cat: cat, Fetch: fetcher{"https://acme.test/changes.rss": feed}, Model: m, Dir: t.TempDir()}
	got, err := s.Changes(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range got {
		ids = append(ids, c.ID+" "+c.Sunset.Format(time.DateOnly))
	}
	want := []string{"api/acme//v1/things/list 2027-03-31", "api/acme//v1/things/search 2027-03-31", "api/acme//v1/orders#legacy_total 0001-01-01"}
	if !slices.Equal(ids, want) {
		t.Fatalf("changes = %q, want %q", ids, want)
	}
	if title := got[0].Evidence[0].Title; title != "The Things API retires (as fake read it)" {
		t.Errorf("evidence title = %q", title)
	}
	if s := got[1].Sites; len(s) != 1 || s[0].File != "src/b.js" {
		t.Errorf("each called path has its own sites: %+v", s)
	}
	if m.calls != 2 {
		t.Errorf("the model was asked %d times; only entries that warn go to it", m.calls)
	}
	if _, err := s.Changes(context.Background(), inv); err != nil || m.calls != 2 {
		t.Errorf("a second run asked the model again (%d calls, %v); what it read is kept", m.calls, err)
	}
}

func TestFeedDates(t *testing.T) {
	rss := `<rss><channel>
<item><title>a</title><pubDate>Tue, 15 Sep 2026 10:00:00 +0000</pubDate></item>
<item><title>b</title><pubDate>Tue, 15 Sep 2026 10:00:00 GMT</pubDate></item>
</channel></rss>`
	got, ok := feed([]byte(rss))
	if !ok || len(got) != 2 {
		t.Fatalf("feed = %+v, %v", got, ok)
	}
	want := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	for _, e := range got {
		if !e.At.Equal(want) {
			t.Errorf("%s: at %v, want %v", e.Title, e.At, want)
		}
	}
}

func TestText(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"html", "<p>/v1/things is <b>deprecated</b>.</p>", "/v1/things is \ndeprecated\n."},
		{"escaped path", "<p>GET /v1/items/&lt;id&gt; is deprecated &amp; goes away.</p>", "GET /v1/items/<id> is deprecated & goes away."},
		{"escaped html", "&lt;p&gt;GET /v1/items/&amp;lt;id&amp;gt; is deprecated.&lt;/p&gt;", "GET /v1/items/<id> is deprecated."},
		{"plain text with an escaped path", "GET /v1/items/&lt;id&gt; is deprecated.", "GET /v1/items/<id> is deprecated."},
		{"blank lines", "<p>a</p>\n\n<p>b</p>", "a\nb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := text(tt.in); got != tt.want {
				t.Errorf("text(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestChangelogEscapedPaths(t *testing.T) {
	tests := []struct{ name, desc string }{
		{"html in the feed", "&lt;p&gt;GET /v1/items/&amp;lt;id&amp;gt; is deprecated and removed on 2027-02-01.&lt;/p&gt;"},
		{"html escaped twice", "&amp;lt;p&amp;gt;GET /v1/items/&amp;amp;lt;id&amp;amp;gt; is deprecated and removed on 2027-02-01.&amp;lt;/p&amp;gt;"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			feed := `<rss><channel><item><title>Items</title><description>` + tt.desc + `</description><pubDate>Tue, 15 Sep 2026 10:00:00 +0000</pubDate></item></channel></rss>`
			cat := []Provider{{Name: "acme", Hosts: []string{"api.acme.test"}, Changelog: "https://acme.test/changes.rss"}}
			inv := &core.Inventory{Providers: []core.ProviderUse{{Provider: "acme", URLs: []core.URLUse{
				{File: "a.js", Line: 3, URL: "https://api.acme.test/v1/items/%s"},
			}}}}
			got, err := (&Changelogs{Cat: cat, Fetch: fetcher{"https://acme.test/changes.rss": feed}}).Changes(context.Background(), inv)
			if err != nil || len(got) != 1 {
				t.Fatalf("got %+v, %v", got, err)
			}
			if c := got[0]; c.ID != "api/acme//v1/items" || c.Evidence[0].Text != "GET /v1/items/<id> is deprecated and removed on 2027-02-01." {
				t.Errorf("change = %s %q", c.ID, c.Evidence[0].Text)
			}
		})
	}
}

func TestChangelogsOneFails(t *testing.T) {
	cat := []Provider{
		{Name: "broken", Hosts: []string{"broken.test"}, Changelog: "https://broken.test/changes.rss"},
		{Name: "acme", Hosts: []string{"api.acme.test"}, Changelog: "https://acme.test/changes.rss"},
	}
	inv := &core.Inventory{Providers: []core.ProviderUse{
		{Provider: "broken", URLs: []core.URLUse{{File: "b.js", Line: 1, URL: "https://broken.test/v1/x"}}},
		{Provider: "acme", URLs: []core.URLUse{{File: "a.js", Line: 3, URL: "https://api.acme.test/v1/things"}}},
	}}
	s := &Changelogs{Cat: cat, Fetch: fetcher{"https://acme.test/changes.rss": rss}}
	got, err := s.Changes(context.Background(), inv)
	if err != nil || len(got) != 1 || got[0].ID != "api/acme//v1/things" {
		t.Fatalf("got %+v, %v; one changelog failing should leave the others' changes", got, err)
	}
	notes := s.Notes()
	if len(notes) != 1 || !strings.HasPrefix(notes[0], "broken: changelog unavailable: ") || !strings.Contains(notes[0], "404") {
		t.Errorf("notes = %q", notes)
	}
	s.Fetch = fetcher{"https://acme.test/changes.rss": rss, "https://broken.test/changes.rss": rss}
	if _, err := s.Changes(context.Background(), inv); err != nil || len(s.Notes()) != 0 {
		t.Errorf("notes from an earlier run were kept: %q, %v", s.Notes(), err)
	}
}

func TestWarned(t *testing.T) {
	tests := []struct {
		sentence, path string
		calls          []string
		want           bool
	}{
		{"/v1/things is deprecated and will be removed on 2027-01-31.", "/v1/things", nil, true},
		{"/v1/things and every endpoint under it will be removed in March.", "/v1/things", nil, true},
		{"Removal of /v1/things is planned for March.", "/v1/things", nil, true},
		{"We removed GET /v1/things.", "/v1/things", nil, true},
		{"GET /v1/items/<id> is deprecated.", "/v1/items", nil, true},
		{"See https://api.acme.test/v1/items, which is deprecated.", "/v1/items", nil, true},
		{"You can now remove labels with DELETE /v1/labels.", "/v1/labels", nil, false},
		{"Removed the 100-item limit on /v1/orders.", "/v1/orders", nil, false},
		{"We removed a bug where /v1/charges returned 500.", "/v1/charges", nil, false},
		{"/v1/users/{id}/legacy-tokens is deprecated.", "/v1/users", []string{"https://api.acme.test/v1/users/%s"}, false},
		{"/v1/users/{id}/legacy-tokens is deprecated.", "/v1/users", []string{"https://api.acme.test/v1/users/%s/legacy-tokens"}, true},
		{"/v2/items-archive is deprecated", "/v2/items", nil, false},
		{"/v3/legacy-report is no longer supported after 2026-09-30.", "/v3/report", nil, false},
		{"/api/files.upload is deprecated and will be removed.", "/api/files.upload", nil, true},
		{"The /v1/reports.json endpoint is deprecated.", "/v1/reports.json", nil, true},
		{"Deprecated, e.g. /v1/old is going away soon.", "/v1/old", nil, true},
		{"/v1/items is deprecated; use the new /v2/items endpoint.", "/v2/items", nil, false},
		{"/v1/items is deprecated; use the new /v2/items endpoint.", "/v1/items", nil, true},
		{"/v1/items is deprecated in favor of the new /v2/items.", "/v2/items", nil, false},
		{"Use /v2/items instead of /v1/items, which is deprecated.", "/v1/items", nil, true},
		{"Use /v2/items instead of /v1/items, which is deprecated.", "/v2/items", nil, false},
		{"Instead of /v1/items, which is deprecated, call /v2/items.", "/v1/items", nil, true},
		{"Instead of /v1/items, which is deprecated, call /v2/items.", "/v2/items", nil, false},
		{"Use of /v1/items is deprecated.", "/v1/items", nil, true},
		{"Calls that use the legacy /v1/items endpoint are deprecated.", "/v1/items", nil, true},
	}
	for _, tt := range tests {
		var calls []core.URLUse
		for _, u := range tt.calls {
			calls = append(calls, core.URLUse{File: "a.js", Line: 1, URL: u})
		}
		if got := warned(entry{Text: tt.sentence}, tt.path, calls, map[string]bool{}); got != tt.want {
			t.Errorf("warned(%q, %s) = %v, want %v", tt.sentence, tt.path, got, tt.want)
		}
	}
}

func TestFieldNewsPhrasing(t *testing.T) {
	tests := []struct {
		text string
		want []string
	}{
		{"The `charge.amount` field is deprecated.", []string{"amount"}},
		{"The `amount` field is deprecated.", []string{"amount"}},
		{"`total_cents` is deprecated; use `total` instead.", []string{"total_cents"}},
		{"Deprecated: use `total` instead of `total_cents`.", []string{"total_cents"}},
		{"`total_cents` is deprecated; use the new `total`.", []string{"total_cents"}},
		{"`total_cents` is deprecated in favor of the new `total`.", []string{"total_cents"}},
		{"Instead of `total_cents`, which is deprecated, read `total`.", []string{"total_cents"}},
		{"The field `total_cents` is deprecated, e.g. on orders, and `total` takes its place.", []string{"total_cents"}},
	}
	for _, tt := range tests {
		idx := &index{fields: []core.Site{field("app.py", 1, "total_cents"), field("app.py", 2, "total"), field("app.py", 3, "amount")}}
		urls := []core.URLUse{{File: "app.py", Line: 7, URL: "https://orders.test/v1/orders"}}
		got, _ := fieldNews(idx, Provider{Name: "orders"}, urls, calledPaths(urls), []entry{{Text: tt.text}})
		var have []string
		for _, c := range got {
			have = append(have, c.API.Field)
		}
		if !slices.Equal(have, tt.want) {
			t.Errorf("%q: fields %q, want %q", tt.text, have, tt.want)
		}
	}
}

func TestChangelogSites(t *testing.T) {
	feed := `<rss><channel><item><title>Items</title><description>/v2/items is deprecated.</description></item></channel></rss>`
	cat := []Provider{{Name: "acme", Hosts: []string{"api.acme.test"}, Changelog: "https://acme.test/changes.rss"}}
	inv := &core.Inventory{Providers: []core.ProviderUse{{Provider: "acme", URLs: []core.URLUse{
		{File: "a.js", Line: 3, URL: "https://api.acme.test/v2/items"},
		{File: "a.js", Line: 4, URL: "https://api.acme.test/v2/items/%s"},
		{File: "a.js", Line: 5, URL: "https://api.acme.test/v2/items-archive"},
	}}}}
	got, err := (&Changelogs{Cat: cat, Fetch: fetcher{"https://acme.test/changes.rss": feed}}).Changes(context.Background(), inv)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	var lines []int
	for _, s := range got[0].Sites {
		lines = append(lines, s.Line)
	}
	if !slices.Equal(lines, []int{3, 4}) {
		t.Errorf("sites = %v; a call to /v2/items-archive is not one to /v2/items", lines)
	}
}

func TestUnder(t *testing.T) {
	paths := []string{"/v1/things", "/v1/things/list", "/v1/things/search", "/v1/thingsx"}
	tests := map[string][]string{
		"/v1/things":         {"/v1/things", "/v1/things/list", "/v1/things/search"},
		"/v1/things/{id}":    {"/v1/things", "/v1/things/list", "/v1/things/search"},
		"/v1/things/search/": {"/v1/things/search"},
		"/v2":                nil,
		"":                   nil,
	}
	for item, want := range tests {
		if got := under(item, paths); !slices.Equal(got, want) {
			t.Errorf("under(%q) = %q, want %q", item, got, want)
		}
	}
}

func TestSunset(t *testing.T) {
	sep := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		text string
		at   time.Time
		want string
	}{
		{"Deprecated on 2024-01-05, removed 2027-03-01.", time.Time{}, "2027-03-01"},
		{"Deprecated on 2026-10-05. It is removed on 2027-03-01. Docs updated 2027-04-01.", sep, "2027-03-01"},
		{"Removed on 2025-06-01.", time.Time{}, "2025-06-01"},
		{"Deprecated. Migrate by 2027-01-01.", sep, "2027-01-01"},
		{"Deprecated on 2026-08-01.", sep, "0001-01-01"},
	}
	for _, tt := range tests {
		if got := sunset(entry{Text: tt.text, At: tt.at}).Format(time.DateOnly); got != tt.want {
			t.Errorf("sunset(%q) = %s, want %s", tt.text, got, tt.want)
		}
	}
}

func TestFeedFormats(t *testing.T) {
	latin := "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?>\n<rss><channel><item><title>Caf\xe9</title><pubDate>Mon, 6 Oct 2026 10:00:00 GMT</pubDate></item>" +
		"<item><title>b</title><pubDate>6 Oct 2026 10:00:00 +0200</pubDate></item></channel></rss>"
	got, ok := feed([]byte(latin))
	if !ok || len(got) != 2 || got[0].Title != "Café" {
		t.Fatalf("feed = %+v, %v", got, ok)
	}
	if want := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC); !got[0].At.Equal(want) || !got[1].At.Equal(want.Add(-2*time.Hour)) {
		t.Errorf("dates = %v, %v", got[0].At, got[1].At)
	}
	cp := "<?xml version=\"1.0\" encoding=\"windows-1252\"?><rss><channel><item><title>\x93a\x94</title></item></channel></rss>"
	if got, ok := feed([]byte(cp)); !ok || got[0].Title != "\u201ca\u201d" {
		t.Errorf("windows-1252 = %+v", got)
	}
	atom := `<feed xmlns="http://www.w3.org/2005/Atom"><entry><title>a</title><published>2026-08-01T00:00:00Z</published><updated>2026-09-01T00:00:00Z</updated></entry></feed>`
	if got, ok := feed([]byte(atom)); !ok || got[0].At.Month() != time.August {
		t.Errorf("atom = %+v", got)
	}
}

func TestChangelogNoEntries(t *testing.T) {
	cat := []Provider{{Name: "acme", Hosts: []string{"api.acme.test"}, Changelog: "https://acme.test/changes"}}
	inv := &core.Inventory{Providers: []core.ProviderUse{{Provider: "acme", URLs: []core.URLUse{{File: "a.js", Line: 3, URL: "https://api.acme.test/v1/things"}}}}}
	s := &Changelogs{Cat: cat, Fetch: fetcher{"https://acme.test/changes": "<html><body><p>nothing here</p></body></html>"}}
	if _, err := s.Changes(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	if n := s.Notes(); len(n) != 1 || n[0] != "acme: changelog https://acme.test/changes has no entries tend could read" {
		t.Errorf("notes = %q", n)
	}
}

func TestAskKeepsBadReplies(t *testing.T) {
	m := &reader{replies: map[string]string{"Odd": "I could not read that."}}
	s := &Changelogs{Model: m, Dir: t.TempDir()}
	e := entry{Title: "Odd", Text: "Something is deprecated."}
	if _, fresh, err := s.ask(context.Background(), e); err == nil || !fresh {
		t.Fatalf("a reply with no JSON should fail once: %v %v", fresh, err)
	}
	if items, fresh, err := s.ask(context.Background(), e); err != nil || fresh || len(items) != 0 || m.calls != 1 {
		t.Errorf("a bad reply should cost one call: %v %v %v %d", items, fresh, err, m.calls)
	}
}
