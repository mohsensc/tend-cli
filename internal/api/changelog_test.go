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
	want := []string{"api/acme//v1/things/list 2027-03-31", "api/acme//v1/orders#legacy_total 0001-01-01"}
	if !slices.Equal(ids, want) {
		t.Fatalf("changes = %q, want %q", ids, want)
	}
	if title := got[0].Evidence[0].Title; title != "The Things API retires (as fake read it)" {
		t.Errorf("evidence title = %q", title)
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
