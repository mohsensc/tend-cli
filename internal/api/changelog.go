package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/fetch"
)

// Changelogs reads the changelog of each provider that has one instead of a
// spec. An entry that names an endpoint the code calls, or a field it reads
// from the API's responses, in words that say it is going away, is a
// deprecation of that endpoint or field with the entry as evidence. Every
// entry is read each time, old ones included: news about a path the code
// still calls has not gone stale.
//
// With a model, an entry that warns of something but names no path or field
// the plain way, "the Things API retires in March", is given to the model to
// read. What it read is kept in Dir, so each entry is sent once.
//
// A changelog that cannot be read is a note; the other providers' changes
// stand.
type Changelogs struct {
	Cat   []Provider
	Fetch core.Fetcher
	Model core.Model
	Dir   string

	notes []string
}

var _ core.Source = (*Changelogs)(nil)

func (s *Changelogs) Name() string { return "changelog" }

// Notes lists the changelogs the last Changes could not read.
func (s *Changelogs) Notes() []string { return slices.Clone(s.notes) }

var warns = regexp.MustCompile(`(?i)deprecat|sunset|retir|remov|end[- ]of[- ]life|discontinu|no longer (supported|available)|shut(ting)? down`)

// mayWarn is looser than warns: it picks the entries worth giving to a
// model, which tells the rest apart.
var mayWarn = regexp.MustCompile(`(?i)deprecat|sunset|retir|remov|end[- ]of|discontinu|no longer|going away|will stop|stop(s|ped)? (support|return|accept)|\bdrop|shut`)

var isoDay = regexp.MustCompile(`\b(20\d\d-\d\d-\d\d)\b`)

type entry struct {
	Title, Text string
	At          time.Time
}

func (s *Changelogs) Changes(ctx context.Context, inv *core.Inventory) ([]core.Change, error) {
	s.notes = nil
	var res []core.Change
	for _, pu := range inv.Providers {
		i := slices.IndexFunc(s.Cat, func(p Provider) bool { return p.Name == pu.Provider })
		if i < 0 || s.Cat[i].Changelog == "" || s.Cat[i].Spec != "" || len(pu.URLs) == 0 {
			continue
		}
		p := s.Cat[i]
		entries, err := s.read(ctx, p.Changelog)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			s.notes = append(s.notes, fmt.Sprintf("%s: changelog unavailable: %v", p.Name, err))
			continue
		}
		paths := calledPaths(pu.URLs)
		news, about := fieldNews(inv.Index, p, pu.URLs, paths, entries)
		for _, path := range paths {
			for _, e := range entries {
				if warned(e, path, about) {
					res = append(res, changelogChange(p, path, pu.URLs, e))
					break
				}
			}
		}
		res = append(res, news...)
		if s.Model != nil {
			have := map[string]bool{}
			for _, c := range res {
				have[c.ID] = true
			}
			res = append(res, s.asked(ctx, inv.Index, p, pu.URLs, paths, entries, have)...)
		}
	}
	return res, nil
}

// askMax is how many entries one provider may send to the model in a run.
const askMax = 10

const readSystem = `You read one entry of an API's changelog. List what it says is deprecated, removed, retired or going away: endpoints by path, and response fields by name. Leave out anything it adds, changes compatibly, or recommends moving to.
Reply with JSON only, in this shape:
{"items": [{"method": "GET or empty", "path": "/v1/things or empty", "field": "a field name or empty", "sunset": "YYYY-MM-DD or empty"}]}
Reply {"items": []} when it names nothing going away.`

type item struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Field  string `json:"field"`
	Sunset string `json:"sunset"`
}

// asked turns what the model read in the warning entries into changes for
// the paths the code calls and the fields it reads.
func (s *Changelogs) asked(ctx context.Context, idx core.ScanIndex, p Provider, urls []core.URLUse, paths []string, entries []entry, have map[string]bool) []core.Change {
	files := callers(urls)
	var res []core.Change
	add := func(c core.Change, it item) {
		c.Evidence[0].Title += " (as " + s.Model.Name() + " read it)"
		if t, err := time.Parse(time.DateOnly, it.Sunset); err == nil {
			c.Sunset = t
		}
		if c.ID = core.ChangeID(&c); !have[c.ID] {
			have[c.ID] = true
			res = append(res, c)
		}
	}
	sent := 0
	for _, e := range entries {
		if sent >= askMax {
			break
		}
		if !mayWarn.MatchString(e.Title + "\n" + e.Text) {
			continue
		}
		items, fresh, err := s.ask(ctx, e)
		if fresh {
			sent++
		}
		if err != nil {
			continue
		}
		for _, it := range items {
			path := under(it.Path, paths)
			switch {
			case it.Field != "" && idx != nil:
				sites := idx.Fields(it.Field, files)
				if len(sites) == 0 {
					continue
				}
				add(core.Change{
					Kind:      core.Deprecation,
					API:       &core.APIChange{Provider: p.Name, Path: path, Field: it.Field, What: "response field deprecated"},
					Evidence:  []core.Evidence{{Source: "changelog", Title: e.Title, Text: core.Clip(strings.TrimSpace(e.Text), 1200), URL: p.Changelog}},
					Published: e.At,
					Sunset:    sunset(e),
					Sites:     sites,
				}, it)
			case it.Field == "" && path != "":
				add(changelogChange(p, path, urls, e), it)
			}
		}
	}
	return res
}

// under is the called path an item names: the same path, or one below a
// path the entry retires as a whole. Placeholders end a path, as they do
// in calledPaths.
func under(item string, paths []string) string {
	if i := strings.IndexAny(item, "{:"); i >= 0 {
		item = item[:i]
	}
	item = strings.TrimSuffix(item, "/")
	if item == "" {
		return ""
	}
	for _, p := range paths {
		if p == item || strings.HasPrefix(p, item+"/") {
			return p
		}
	}
	return ""
}

// ask asks the model what an entry says is going away, or reads what it
// said before. fresh reports whether the model was asked this time.
func (s *Changelogs) ask(ctx context.Context, e entry) (items []item, fresh bool, err error) {
	sum := sha256.Sum256([]byte(e.Title + "\x00" + e.Text))
	file := filepath.Join(s.Dir, "changelog", hex.EncodeToString(sum[:8])+".json")
	if b, err := os.ReadFile(file); err == nil && json.Unmarshal(b, &items) == nil {
		return items, false, nil
	}
	reply, err := s.Model.Complete(ctx, readSystem, core.Clip(e.Title+"\n\n"+e.Text, 6000))
	if err != nil {
		return nil, true, err
	}
	var out struct {
		Items []item `json:"items"`
	}
	start, end := strings.IndexByte(reply, '{'), strings.LastIndexByte(reply, '}')
	if start < 0 || end < start {
		return nil, true, fmt.Errorf("changelog: no JSON in the reply")
	}
	if err := json.Unmarshal([]byte(reply[start:end+1]), &out); err != nil {
		return nil, true, fmt.Errorf("changelog: %w", err)
	}
	if s.Dir != "" {
		b, _ := json.Marshal(out.Items)
		if os.MkdirAll(filepath.Dir(file), 0o755) == nil {
			_ = os.WriteFile(file, b, 0o644)
		}
	}
	return out.Items, true, nil
}

var (
	ticked    = regexp.MustCompile("`([A-Za-z_][\\w.]*)`")
	snakeCase = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b`)
	pathLike  = regexp.MustCompile(`/[\w/{}.:-]*`)
)

// fieldNews finds the entries that say a field is going away, where the code
// reads that field in a file that calls the API. A field is a name in
// backticks or written snake_case, which prose never is. about holds the
// sentences that were about a field, so the path they name is not taken
// for the thing going away.
func fieldNews(idx core.ScanIndex, p Provider, urls []core.URLUse, paths []string, entries []entry) (res []core.Change, about map[string]bool) {
	about = map[string]bool{}
	if idx == nil {
		return nil, about
	}
	files := callers(urls)
	done := map[string]bool{}
	for _, e := range entries {
		for _, sen := range sentence.FindAllString(e.Title+"\n"+e.Text, -1) {
			if !warns.MatchString(sen) {
				continue
			}
			for _, f := range named(sen) {
				if instead.MatchString(sen[:strings.Index(sen, f)]) {
					continue
				}
				sites := idx.Fields(f, files)
				if len(sites) == 0 {
					continue
				}
				about[sen] = true
				if done[f] {
					continue
				}
				done[f] = true
				c := core.Change{
					Kind:      core.Deprecation,
					API:       &core.APIChange{Provider: p.Name, Path: pathIn(sen, paths), Field: f, What: "response field deprecated"},
					Evidence:  []core.Evidence{{Source: "changelog", Title: e.Title, Text: core.Clip(strings.TrimSpace(e.Text), 1200), URL: p.Changelog}},
					Published: e.At,
					Sunset:    sunset(e),
					Sites:     sites,
				}
				c.ID = core.ChangeID(&c)
				res = append(res, c)
			}
		}
	}
	return res, about
}

// callers are the files the URL literals sit in.
func callers(urls []core.URLUse) []string {
	var res []string
	for _, u := range urls {
		if !slices.Contains(res, u.File) {
			res = append(res, u.File)
		}
	}
	return res
}

// named are the field names a sentence writes, leaving out its paths.
func named(sen string) []string {
	var res []string
	for _, m := range ticked.FindAllStringSubmatch(sen, -1) {
		f := m[1][strings.LastIndexByte(m[1], '.')+1:]
		if f != "" && !slices.Contains(res, f) {
			res = append(res, f)
		}
	}
	for _, f := range snakeCase.FindAllString(pathLike.ReplaceAllString(sen, " "), -1) {
		if !slices.Contains(res, f) {
			res = append(res, f)
		}
	}
	return res
}

// pathIn is the called path a sentence names, "" when it names none.
func pathIn(sen string, paths []string) string {
	for _, p := range paths {
		if strings.Contains(sen, p) {
			return p
		}
	}
	return ""
}

// sunset is the first date an entry gives after its own.
func sunset(e entry) time.Time {
	for _, m := range isoDay.FindAllString(e.Text, -1) {
		if t, err := time.Parse(time.DateOnly, m); err == nil && t.After(e.At) {
			return t
		}
	}
	return time.Time{}
}

var sentence = regexp.MustCompile(`[^.!?\n]+`)

// warned reports whether an entry says path is going away: one sentence names
// it and a word like deprecated or removed, and does not name it as the thing
// to move to ("use /v2/items instead") or only as where a field lives.
func warned(e entry, path string, about map[string]bool) bool {
	for _, s := range sentence.FindAllString(e.Title+"\n"+e.Text, -1) {
		i := strings.Index(s, path)
		if i < 0 || !warns.MatchString(s) || about[s] {
			continue
		}
		if !instead.MatchString(s[:i]) {
			return true
		}
	}
	return false
}

var instead = regexp.MustCompile(`(?i)\b(use|try|switch(ing)? to|migrat(e|ing) to|mov(e|ing) to|in favou?r of|replaced by|instead of)\s+\S*\s*$`)

// calledPaths are the literal paths the code calls, cut where a value is
// put together: "/v2/items/%s" is called as "/v2/items".
func calledPaths(urls []core.URLUse) []string {
	var res []string
	for _, u := range urls {
		p := u.URL
		if i := strings.IndexAny(p, "?#%{$"); i >= 0 {
			p = p[:i]
		}
		if _, rest, ok := strings.Cut(p, "://"); ok {
			_, p, _ = strings.Cut(rest, "/")
			p = "/" + p
		}
		p = strings.TrimSuffix(p, "/")
		if strings.Count(p, "/") >= 2 && !slices.Contains(res, p) {
			res = append(res, p)
		}
	}
	slices.Sort(res)
	return res
}

func changelogChange(p Provider, path string, urls []core.URLUse, e entry) core.Change {
	var sites []core.Site
	for _, u := range urls {
		if strings.Contains(u.URL, path) {
			sites = append(sites, core.Site{File: u.File, Line: u.Line, Text: strings.TrimSpace(u.Method + " " + u.URL)})
		}
	}
	c := core.Change{
		Kind:      core.Deprecation,
		API:       &core.APIChange{Provider: p.Name, Path: path, What: "operation deprecated"},
		Evidence:  []core.Evidence{{Source: "changelog", Title: e.Title, Text: core.Clip(strings.TrimSpace(e.Text), 1200), URL: p.Changelog}},
		Published: e.At,
		Sunset:    sunset(e),
		Sites:     sites,
	}
	c.ID = core.ChangeID(&c)
	return c
}

// read fetches a changelog and splits it into entries: the items of an RSS
// or Atom feed, or the sections of an HTML page under its headings.
func (s *Changelogs) read(ctx context.Context, u string) ([]entry, error) {
	got, err := s.Fetch.Get(ctx, u, fetch.TTL(specTTL))
	if err != nil {
		return nil, err
	}
	if got.Status != 200 {
		return nil, fmt.Errorf("%s: HTTP %d", u, got.Status)
	}
	if res, ok := feed(got.Body); ok {
		return res, nil
	}
	return sections(string(got.Body)), nil
}

func feed(b []byte) ([]entry, bool) {
	var doc struct {
		XMLName xml.Name
		Items   []struct {
			Title string `xml:"title"`
			Desc  string `xml:"description"`
			Date  string `xml:"pubDate"`
		} `xml:"channel>item"`
		Entries []struct {
			Title   string `xml:"title"`
			Summary string `xml:"summary"`
			Content string `xml:"content"`
			Updated string `xml:"updated"`
		} `xml:"entry"`
	}
	if xml.Unmarshal(b, &doc) != nil || doc.XMLName.Local != "rss" && doc.XMLName.Local != "feed" {
		return nil, false
	}
	var res []entry
	for _, it := range doc.Items {
		at, err := time.Parse(time.RFC1123Z, it.Date)
		if err != nil {
			at, _ = time.Parse(time.RFC1123, it.Date) // "GMT" in place of "+0000"
		}
		res = append(res, entry{it.Title, text(it.Desc), at})
	}
	for _, e := range doc.Entries {
		at, _ := time.Parse(time.RFC3339, e.Updated)
		res = append(res, entry{e.Title, text(e.Summary + "\n" + e.Content), at})
	}
	return res, true
}

var (
	heading = regexp.MustCompile(`(?is)<h[1-4][^>]*>(.*?)</h[1-4]>`)
	tag     = regexp.MustCompile(`(?s)<[^>]+>`)
	htmlTag = regexp.MustCompile(`(?i)</?(?:a|b|br|blockquote|code|div|em|h[1-6]|hr|i|img|li|ol|p|pre|span|strong|table|td|th|tr|ul)\b[^>]*>`)
	blanks  = regexp.MustCompile(`\n\s*\n+`)
)

// sections splits an HTML changelog under its headings.
func sections(page string) []entry {
	locs := heading.FindAllStringSubmatchIndex(page, -1)
	var res []entry
	for i, l := range locs {
		end := len(page)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		e := entry{Title: text(page[l[2]:l[3]]), Text: text(page[l[1]:end])}
		if m := isoDay.FindString(e.Title); m != "" {
			e.At, _ = time.Parse(time.DateOnly, m)
		}
		res = append(res, e)
	}
	return res
}

// text is the plain text of an HTML fragment. Tags go before entities are
// decoded, so a path written /v1/items/&lt;id&gt; keeps its <id>. A feed that
// escaped its HTML twice has no tags until it is decoded once.
func text(s string) string {
	if !tag.MatchString(s) {
		if un := html.UnescapeString(s); htmlTag.MatchString(un) {
			s = un
		}
	}
	s = html.UnescapeString(tag.ReplaceAllString(s, "\n"))
	return strings.TrimSpace(blanks.ReplaceAllString(s, "\n"))
}
