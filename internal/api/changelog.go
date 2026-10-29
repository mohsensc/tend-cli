package api

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
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

// warns are the words that say whatever a sentence names is going away. Removal
// is not among them, since "remove" says as often that something can be removed
// or that a limit was: removed has to apply to the path itself.
var warns = regexp.MustCompile(`(?i)deprecat|sunset|retir|end[- ]of[- ]life|discontinu|no longer (supported|available)|shut(ting)? down`)

var (
	removedAfter  = regexp.MustCompile(`(?i)^[^.;,]{0,80}?\b(?:is|are|was|were|be|being|been|gets?)\s+(?:\w+\s+)?removed\b`)
	removedBefore = regexp.MustCompile("(?i)\\b(?:removal of|removing|remove|removed)\\s+(?:the\\s+)?(?:(?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\\s+)?`?$")
)

// mayWarn is looser than warns: it picks the entries worth giving to a
// model, which tells the rest apart.
var mayWarn = regexp.MustCompile(`(?i)deprecat|sunset|retir|remov|end[- ]of|discontinu|no longer|going away|will stop|stop(s|ped)? (support|return|accept)|\bdrop|shut`)

var isoDay = regexp.MustCompile(`\b(20\d\d-\d\d-\d\d)\b`)

// goes are the words of a sentence that dates a removal, not a deprecation.
var goes = regexp.MustCompile(`(?i)remov|sunset|retir|shut|end[- ]of[- ]life|discontinu|no longer|going away|turn(ed|s)? off`)

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
		if len(entries) == 0 {
			s.notes = append(s.notes, fmt.Sprintf("%s: changelog %s has no entries tend could read", p.Name, p.Changelog))
			continue
		}
		paths := calledPaths(pu.URLs)
		news, about := fieldNews(inv.Index, p, pu.URLs, paths, entries)
		for _, path := range paths {
			for _, e := range entries {
				if warned(e, path, callsTo(pu.URLs, path), about) {
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
			below := under(it.Path, paths)
			switch {
			case it.Field != "" && idx != nil:
				sites := idx.Fields(it.Field, files)
				if len(sites) == 0 {
					continue
				}
				path := ""
				if len(below) > 0 {
					path = below[0]
				}
				add(core.Change{
					Kind:      core.Deprecation,
					API:       &core.APIChange{Provider: p.Name, Path: path, Field: it.Field, What: "response field deprecated"},
					Evidence:  []core.Evidence{{Source: "changelog", Title: e.Title, Text: core.Clip(strings.TrimSpace(e.Text), 1200), URL: p.Changelog}},
					Published: e.At,
					Sunset:    sunset(e),
					Sites:     sites,
				}, it)
			case it.Field == "":
				for _, path := range below {
					add(changelogChange(p, path, urls, e), it)
				}
			}
		}
	}
	return res
}

// under are the called paths an item names: the same path, the exact one
// first, or every one below a path the entry retires as a whole. Placeholders
// end a path, as they do in calledPaths.
func under(item string, paths []string) []string {
	if i := strings.IndexAny(item, "{:<"); i >= 0 {
		item = item[:i]
	}
	item = strings.TrimSuffix(item, "/")
	if item == "" {
		return nil
	}
	var res []string
	for _, p := range paths {
		switch {
		case p == item:
			res = slices.Insert(res, 0, p)
		case strings.HasPrefix(p, item+"/"):
			res = append(res, p)
		}
	}
	return res
}

// ask asks the model what an entry says is going away, or reads what it
// said before. fresh reports whether the model was asked this time.
func (s *Changelogs) ask(ctx context.Context, e entry) (items []item, fresh bool, err error) {
	sum := sha256.Sum256([]byte(e.Title + "\x00" + e.Text))
	file := ""
	if s.Dir != "" {
		file = filepath.Join(s.Dir, "changelog", hex.EncodeToString(sum[:8])+".json")
		if b, err := os.ReadFile(file); err == nil && json.Unmarshal(b, &items) == nil {
			return items, false, nil
		}
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
		err = errors.New("changelog: no JSON in the reply")
	} else if err = json.Unmarshal([]byte(reply[start:end+1]), &out); err != nil {
		err = fmt.Errorf("changelog: %w", err)
	}
	if err != nil {
		out.Items = nil
	}
	if file != "" {
		b, _ := json.Marshal(out.Items)
		if os.MkdirAll(filepath.Dir(file), 0o755) == nil {
			_ = os.WriteFile(file, b, 0o644)
		}
	}
	return out.Items, true, err
}

var (
	ticked    = regexp.MustCompile("`([A-Za-z_][\\w.]*)`")
	snakeCase = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b`)
	pathLike  = regexp.MustCompile(`/[\w/{}.:-]*`)
	pathToken = regexp.MustCompile(`/[\w/{}<>.:%~-]+`)
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
		for _, sen := range sentences(e.Title + "\n" + e.Text) {
			if !warns.MatchString(sen) {
				continue
			}
			for _, f := range named(sen) {
				if replacement(sen[:f.at]) || after.MatchString(sen[f.end:]) {
					continue
				}
				sites := idx.Fields(f.name, files)
				if len(sites) == 0 {
					continue
				}
				about[sen] = true
				if done[f.name] {
					continue
				}
				done[f.name] = true
				c := core.Change{
					Kind:      core.Deprecation,
					API:       &core.APIChange{Provider: p.Name, Path: pathIn(sen, paths, urls), Field: f.name, What: "response field deprecated"},
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

// mention is a field a sentence names, and where: at and end take in its
// backticks.
type mention struct {
	name    string
	at, end int
}

// named are the field names a sentence writes, leaving out its paths. A
// dotted name, `charge.amount`, is its last part.
func named(sen string) []mention {
	var res []mention
	have := func(f string) bool {
		return slices.ContainsFunc(res, func(m mention) bool { return m.name == f })
	}
	for _, m := range ticked.FindAllStringSubmatchIndex(sen, -1) {
		full := sen[m[2]:m[3]]
		f := full[strings.LastIndexByte(full, '.')+1:]
		if f != "" && !have(f) {
			res = append(res, mention{f, m[0], m[1]})
		}
	}
	blank := pathLike.ReplaceAllStringFunc(sen, func(p string) string { return strings.Repeat(" ", len(p)) })
	for _, m := range snakeCase.FindAllStringIndex(blank, -1) {
		if f := blank[m[0]:m[1]]; !have(f) {
			res = append(res, mention{f, m[0], m[1]})
		}
	}
	return res
}

// pathIn is the called path a sentence names, "" when it names none.
func pathIn(sen string, paths []string, urls []core.URLUse) string {
	for _, p := range paths {
		if len(mentions(sen, p, callsTo(urls, p))) > 0 {
			return p
		}
	}
	return ""
}

// sunset is the date an entry gives for the removal: the latest one after its
// own in a sentence about something going away, else the latest after its own
// anywhere. A removal comes after the deprecation it follows, and a date that
// has passed stays, since that is when it matters most.
func sunset(e entry) time.Time {
	var gone, latest time.Time
	for _, sen := range sentences(e.Text) {
		for _, m := range isoDay.FindAllString(sen, -1) {
			t, err := time.Parse(time.DateOnly, m)
			if err != nil || !t.After(e.At) {
				continue
			}
			if t.After(latest) {
				latest = t
			}
			if goes.MatchString(sen) && t.After(gone) {
				gone = t
			}
		}
	}
	if !gone.IsZero() {
		return gone
	}
	return latest
}

var (
	sentenceEnd = regexp.MustCompile(`[.!?]+(?:\s+|$)|\n`)
	abbrev      = regexp.MustCompile(`(?i)(?:^|[\s(])(?:e\.g|i\.e|etc|vs|cf|approx|incl)$`)
)

// sentences splits text where a sentence ends, so the dots in
// /api/files.upload, `charge.amount` and e.g. stay inside one.
func sentences(text string) []string {
	var res []string
	start := 0
	add := func(s string) {
		if strings.TrimSpace(s) != "" {
			res = append(res, s)
		}
	}
	for _, m := range sentenceEnd.FindAllStringIndex(text, -1) {
		if text[m[0]] == '.' && abbrev.MatchString(text[start:m[0]]) {
			continue
		}
		add(text[start:m[0]])
		start = m[1]
	}
	add(text[start:])
	return res
}

// warned reports whether an entry says path is going away: one sentence names
// it and a word like deprecated, or says it is removed, and does not name it
// as the thing to move to ("use /v2/items instead") or only as where a field
// lives. calls are the URLs the code calls path with, for a sentence that
// names an operation below it.
func warned(e entry, path string, calls []core.URLUse, about map[string]bool) bool {
	for _, s := range sentences(e.Title + "\n" + e.Text) {
		if about[s] {
			continue
		}
		for _, m := range mentions(s, path, calls) {
			if replacement(s[:m[0]]) {
				continue
			}
			if warns.MatchString(s) || removedAfter.MatchString(s[m[1]:]) || removedBefore.MatchString(s[:m[0]]) {
				return true
			}
		}
	}
	return false
}

// mentions are where a sentence names path, as [start, end) pairs. A path is
// a whole token: /v1/items, or /v1/items/<id> with one placeholder after it.
// A token that goes on past a placeholder, /v1/users/{id}/tokens, names that
// deeper operation, which only a call of the same shape makes.
func mentions(sen, path string, calls []core.URLUse) [][2]int {
	var res [][2]int
	for _, loc := range pathToken.FindAllStringIndex(sen, -1) {
		tok := strings.TrimRight(sen[loc[0]:loc[1]], ".:-~")
		end := loc[0] + len(tok)
		if rest, ok := strings.CutPrefix(tok, "//"); ok {
			i := strings.IndexByte(rest, '/')
			if i < 0 {
				continue
			}
			tok = rest[i:]
		}
		if len(tok) > 1 {
			tok = strings.TrimRight(tok, "/")
		}
		segs := strings.Split(tok, "/")
		cut := len(segs)
		for i, sg := range segs {
			if placeholder(sg) {
				cut = i
				break
			}
		}
		if strings.Join(segs[:cut], "/") != path {
			continue
		}
		if len(segs)-cut <= 1 || slices.ContainsFunc(calls, func(u core.URLUse) bool { return shaped(segs, u.URL) }) {
			res = append(res, [2]int{loc[0], end})
		}
	}
	return res
}

func placeholder(seg string) bool {
	return seg != "" && strings.ContainsAny(seg[:1], "{<:")
}

// shaped reports whether a called URL has the segments of a path a changelog
// writes, a placeholder on either side standing for any one segment.
func shaped(segs []string, lit string) bool {
	p := lit
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if _, rest, ok := strings.Cut(p, "://"); ok {
		_, p, _ = strings.Cut(rest, "/")
		p = "/" + p
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	called := strings.Split(p, "/")
	if len(called) != len(segs) {
		return false
	}
	for i, sg := range segs {
		c := called[i]
		if sg != c && !placeholder(sg) && !strings.ContainsAny(c, "%{$<:") {
			return false
		}
	}
	return true
}

// callsTo are the calls whose called path is path.
func callsTo(urls []core.URLUse, path string) []core.URLUse {
	var res []core.URLUse
	for _, u := range urls {
		if slices.Equal(calledPaths([]core.URLUse{u}), []string{path}) {
			res = append(res, u)
		}
	}
	return res
}

const filler = `(?:(?:the|a|an|new|newer|updated|replacement)\s+){0,2}(?:(?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s+)?` + "`?"

var (
	instead   = regexp.MustCompile(`(?i)\b(?:use|try|switch(?:ing)?(?: over)? to|migrat(?:e|ing) to|mov(?:e|ing) to|upgrad(?:e|ing) to|in favou?r of|replaced (?:by|with))\s+` + filler + `$`)
	insteadOf = regexp.MustCompile(`(?i)\binstead of\b[^;]*,\s*(?:please\s+)?(?:call|use|send|request|read)\s+` + filler + `$`)
	after     = regexp.MustCompile("^`?\\s*(?i:instead|in (?:its|their) place|takes (?:its|their) place|replaces)\\b")
)

// replacement reports whether the text before a path or field names it as the
// thing to move to: "use the new /v2/items", "in favor of `total`". Only its
// own clause counts, so "/v1/items is deprecated; use /v2/items" names both
// right. The words after "instead of" or "in place of" are the old thing.
func replacement(before string) bool {
	clause := before[strings.LastIndexAny(before, ";,")+1:]
	return instead.MatchString(clause) || insteadOf.MatchString(before)
}

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
	for _, u := range callsTo(urls, path) {
		sites = append(sites, core.Site{File: u.File, Line: u.Line, Text: cmp.Or(u.Text, strings.TrimSpace(u.Method+" "+u.URL))})
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
			Title     string `xml:"title"`
			Summary   string `xml:"summary"`
			Content   string `xml:"content"`
			Published string `xml:"published"`
			Updated   string `xml:"updated"`
		} `xml:"entry"`
	}
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.CharsetReader = charset
	if dec.Decode(&doc) != nil || doc.XMLName.Local != "rss" && doc.XMLName.Local != "feed" {
		return nil, false
	}
	var res []entry
	for _, it := range doc.Items {
		res = append(res, entry{it.Title, text(it.Desc), pubDate(it.Date)})
	}
	for _, e := range doc.Entries {
		at, err := time.Parse(time.RFC3339, strings.TrimSpace(e.Published))
		if err != nil {
			at, _ = time.Parse(time.RFC3339, strings.TrimSpace(e.Updated))
		}
		res = append(res, entry{e.Title, text(e.Summary + "\n" + e.Content), at})
	}
	return res, true
}

// rssDates are the RFC 822 dates feeds write, the day in one digit or two,
// the weekday there or not, the zone a name or an offset.
var rssDates = []string{
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"Mon, 2 Jan 2006 15:04:05 MST",
	"2 Jan 2006 15:04:05 -0700",
	"2 Jan 2006 15:04:05 MST",
	"Mon, 2 Jan 2006 15:04 -0700",
	"Mon, 2 Jan 2006 15:04 MST",
}

func pubDate(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range rssDates {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// charset reads the single-byte encodings feeds still declare, so a Latin-1
// feed is not taken for a page with no entries.
func charset(label string, r io.Reader) (io.Reader, error) {
	var high *[32]rune
	switch strings.ToLower(label) {
	case "iso-8859-1", "iso8859-1", "latin1", "latin-1", "us-ascii", "ascii":
	case "windows-1252", "cp1252":
		high = &cp1252
	default:
		return nil, fmt.Errorf("changelog: charset %s", label)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var out strings.Builder
	for _, c := range b {
		if high != nil && c >= 0x80 && c < 0xa0 && high[c-0x80] != 0 {
			out.WriteRune(high[c-0x80])
			continue
		}
		out.WriteRune(rune(c))
	}
	return strings.NewReader(out.String()), nil
}

var cp1252 = [32]rune{
	'€', 0, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0, 'Ž', 0,
	0, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0, 'ž', 'Ÿ',
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
