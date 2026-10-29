package source

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/eco"
	"github.com/mohsensc/tend-cli/internal/ver"
)

const (
	notesMax     = 8
	textMax      = 1200
	releasePages = 5
)

var changelogs = []string{"CHANGELOG.md", "CHANGES.md", "HISTORY.md", "CHANGELOG.rst"}

// Notes returns the release notes of the versions in (from, to], newest first.
// It reads GitHub releases and falls back to the changelog file in the repo.
func Notes(ctx context.Context, f core.Fetcher, m *core.Meta, from, to string) ([]core.Evidence, error) {
	owner, repo, ok := ParseRepo(m.Repo)
	if !ok {
		return nil, core.ErrNotSupported
	}
	hi, ok := ver.Parse(to)
	if !ok {
		return nil, nil
	}
	lo, known := ver.Parse(from)
	span := func(v ver.V) bool {
		return ver.Compare(v, lo) > 0 && ver.Compare(v, hi) <= 0
	}
	reached := func(v ver.V) bool { return known && ver.Compare(v, lo) <= 0 }
	res, err := releaseNotes(ctx, f, owner, repo, m.Name, hi.Pre != "", span, reached)
	if len(res) > 0 {
		return res, nil
	}
	res, err2 := changelogNotes(ctx, f, owner, repo, span)
	if len(res) > 0 {
		return res, nil
	}
	if err == nil {
		err = err2
	}
	return nil, err
}

type ghRelease struct {
	Tag        string `json:"tag_name"`
	Name       string `json:"name"`
	Body       string `json:"body"`
	URL        string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

func releaseNotes(ctx context.Context, f core.Fetcher, owner, repo, pkg string, pre bool, span, reached func(ver.V) bool) ([]core.Evidence, error) {
	rels, err := releases(ctx, f, owner, repo, pkg, reached)
	if err != nil {
		return nil, err
	}
	type hit struct {
		v ver.V
		r ghRelease
	}
	var hits []hit
	for _, r := range rels {
		v, ok := tagVersion(r.Tag, pkg)
		if !ok || r.Draft || !span(v) {
			continue
		}
		if (r.Prerelease || v.Pre != "") && !pre {
			continue
		}
		hits = append(hits, hit{v, r})
	}
	slices.SortFunc(hits, func(a, b hit) int { return ver.Compare(b.v, a.v) })
	vs := make([]ver.V, len(hits))
	for i, h := range hits {
		vs[i] = h.v
	}
	var res []core.Evidence
	for _, i := range pickNotes(vs) {
		h := hits[i]
		title := h.r.Tag
		if h.r.Name != "" && h.r.Name != h.r.Tag {
			title += " " + h.r.Name
		}
		res = append(res, core.Evidence{Source: "releases", Title: title, Text: core.Clip(strings.TrimSpace(h.r.Body), textMax), URL: h.r.URL})
	}
	return res, nil
}

// releases lists a repo's releases, newest first, until one of pkg's
// releases has reached the version in use, or releasePages pages are read.
// A later page that fails leaves the ones read.
func releases(ctx context.Context, f core.Fetcher, owner, repo, pkg string, reached func(ver.V) bool) ([]ghRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=100", owner, repo)
	res, err := eco.GitHubPages(ctx, f, url, releasePages, func(list []ghRelease) bool {
		return slices.ContainsFunc(list, func(r ghRelease) bool {
			v, ok := tagVersion(r.Tag, pkg)
			return ok && reached(v)
		})
	}, ghOpts()...)
	if err != nil && len(res) == 0 {
		return nil, fmt.Errorf("notes: %s/%s releases: %w", owner, repo, err)
	}
	return res, nil
}

// tagVersion reads "v1.2.3" and monorepo tags like "pkg@1.2.3". A tag that
// names another package of the repo is not ours.
func tagVersion(tag, pkg string) (ver.V, bool) {
	if i := strings.LastIndexByte(tag, '@'); i > 0 {
		if tag[:i] != pkg {
			return ver.V{}, false
		}
		tag = tag[i+1:]
	}
	return ver.Parse(tag)
}

func changelogNotes(ctx context.Context, f core.Fetcher, owner, repo string, span func(ver.V) bool) ([]core.Evidence, error) {
	var first error
	for _, name := range changelogs {
		u := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/HEAD/%s", owner, repo, name)
		resp, err := f.Get(ctx, u)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if resp.Status != 200 {
			continue
		}
		secs := sections(string(resp.Body))
		var hits []section
		for _, s := range secs {
			if span(s.v) && s.body != "" {
				hits = append(hits, s)
			}
		}
		if len(hits) == 0 {
			continue
		}
		slices.SortFunc(hits, func(a, b section) int { return ver.Compare(b.v, a.v) })
		vs := make([]ver.V, len(hits))
		for i, h := range hits {
			vs[i] = h.v
		}
		var res []core.Evidence
		for _, i := range pickNotes(vs) {
			s := hits[i]
			res = append(res, core.Evidence{
				Source: "changelog",
				Title:  s.title,
				Text:   core.Clip(s.body, textMax),
				URL:    fmt.Sprintf("https://github.com/%s/%s/blob/HEAD/%s", owner, repo, name),
			})
		}
		return res, nil
	}
	return nil, first
}

// majorNotes caps how many first releases of a major the notes add past
// the newest notesMax.
const majorNotes = 4

// pickNotes picks, from versions newest first, which releases' notes to
// quote: the newest notesMax, and the first release of each major the range
// crosses, where a rewrite such as openai 1.0.0 says what breaks, however
// many releases came after it. It returns indexes, newest first.
func pickNotes(vs []ver.V) []int {
	keep := map[int]bool{}
	for i := range min(len(vs), notesMax) {
		keep[i] = true
	}
	first := map[int]int{}
	for i, v := range vs {
		first[v.Major()] = i
	}
	if len(first) < 2 {
		return slices.Sorted(maps.Keys(keep))
	}
	added := 0
	for i := len(vs) - 1; i >= 0 && added < majorNotes; i-- {
		if first[vs[i].Major()] == i && !keep[i] {
			keep[i] = true
			added++
		}
	}
	return slices.Sorted(maps.Keys(keep))
}

type section struct {
	v     ver.V
	title string
	body  string
}

type heading struct {
	line  int
	level int
	text  string
	skip  int // lines the heading takes after its first
}

var (
	atxRe     = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	headVerRe = regexp.MustCompile(`(?:^|[^\w.])v?(\d+(?:\.\d+)+(?:-[0-9A-Za-z.]+)?)`)
	linkTxtRe = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	refDefRe  = regexp.MustCompile(`^\[[^\]]+\]:\s`)
)

// sections cuts a changelog into the parts that sit under a heading with a version.
// A part ends at the next version heading or the next heading of the same or higher rank.
func sections(text string) []section {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	heads := headings(lines)
	var res []section
	for i, h := range heads {
		m := headVerRe.FindStringSubmatch(h.text)
		if m == nil {
			continue
		}
		v, ok := ver.Parse(m[1])
		if !ok {
			continue
		}
		end := len(lines)
		for _, n := range heads[i+1:] {
			if n.level <= h.level || headVerRe.MatchString(n.text) {
				end = n.line
				break
			}
		}
		res = append(res, section{v: v, title: linkTxtRe.ReplaceAllString(h.text, "$1"), body: body(lines[h.line+1+h.skip : end])})
	}
	return res
}

func headings(lines []string) []heading {
	var res []heading
	var under []byte
	fence := "" // the marker that closes the code block we are in
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if fence != "" {
			if strings.HasPrefix(t, fence) {
				fence = ""
			}
			continue
		}
		// A bare run of tildes is an rst underline, not an opening fence.
		if strings.HasPrefix(t, "```") {
			fence = "```"
			continue
		}
		if strings.HasPrefix(t, "~~~") && strings.Trim(t, "~") != "" {
			fence = "~~~"
			continue
		}
		if m := atxRe.FindStringSubmatch(l); m != nil {
			res = append(res, heading{line: i, level: len(m[1]), text: m[2]})
			continue
		}
		if t == "" || i+1 >= len(lines) || len(res) > 0 && res[len(res)-1].line+res[len(res)-1].skip == i {
			continue
		}
		next := strings.TrimSpace(lines[i+1])
		if underline(next) && len(next) >= len(t) && !underline(t) {
			c := next[0]
			at := slices.Index(under, c)
			if at < 0 {
				under = append(under, c)
				at = len(under) - 1
			}
			res = append(res, heading{line: i, level: at + 1, text: t, skip: 1})
		}
	}
	return res
}

// underline reports whether s is a run of one punctuation mark, as under an rst title.
func underline(s string) bool {
	if len(s) < 3 || !strings.ContainsRune("=-~^\"*+#", rune(s[0])) {
		return false
	}
	return strings.Trim(s, s[:1]) == ""
}

func body(lines []string) string {
	end := len(lines)
	for end > 0 && (strings.TrimSpace(lines[end-1]) == "" || refDefRe.MatchString(lines[end-1])) {
		end--
	}
	return strings.TrimSpace(strings.Join(lines[:end], "\n"))
}

var (
	bulletRe  = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+`)
	tickRe    = regexp.MustCompile("`([^`\n]+)`")
	triggerRe = regexp.MustCompile(`(?i)\b(?:deprecat|break|remov|renam)`)
	callRe    = regexp.MustCompile(`\(.*\)$`)
)

// Mentions keeps the evidence that names one of the sites' symbols near a word
// like deprecated, breaking or removed.
func Mentions(ev []core.Evidence, sites []core.Site) []core.Evidence {
	want := map[string]bool{}
	for _, s := range sites {
		n := len(s.Chain)
		if n > 0 {
			want[s.Chain[n-1]] = true
		}
		if n > 1 {
			want[s.Chain[n-2]+"."+s.Chain[n-1]] = true
		}
	}
	var res []core.Evidence
	for _, e := range ev {
		for _, p := range paragraphs(e.Text) {
			if !triggerRe.MatchString(tickRe.ReplaceAllString(p, " ")) {
				continue
			}
			for _, m := range tickRe.FindAllStringSubmatch(p, -1) {
				tok := strings.TrimLeft(callRe.ReplaceAllString(strings.TrimSpace(m[1]), ""), ".")
				if !want[tok] {
					continue
				}
				out := e
				out.Title += " (mentions " + tok + ")"
				out.Text = p
				res = append(res, out)
				break
			}
		}
	}
	return res
}

// paragraphs splits at blank lines, and at every bullet.
func paragraphs(text string) []string {
	var res, cur []string
	flush := func() {
		if len(cur) > 0 {
			res = append(res, strings.Join(cur, "\n"))
		}
		cur = nil
	}
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimRight(l, " \t\r")
		if strings.TrimSpace(l) == "" {
			flush()
			continue
		}
		if bulletRe.MatchString(l) {
			flush()
		}
		cur = append(cur, l)
	}
	flush()
	return res
}
