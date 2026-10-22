package scan

import (
	"regexp"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
)

var (
	fullURLRe = regexp.MustCompile(`^https?://[^\s"'<>]+$`)
	pathURLRe = regexp.MustCompile(`^/[A-Za-z][A-Za-z0-9_.{}~-]*(/[^\s"']*)?$`)

	methodKeyRe   = regexp.MustCompile(`(?i)\bmethod['"]?\s*(?:=>|:|=)\s*:?['"]?(get|post|put|patch|delete|head|options)\b`)
	methodConstRe = regexp.MustCompile(`(?i)method(?:\.|::)?(get|post|put|patch|delete|head|options)\b`)
	methodLitRe   = regexp.MustCompile("[\"'`](GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)[\"'`]")
	verbCallRe    = regexp.MustCompile(`(?i)(?:\.|->|::)(get|post|put|patch|delete|head|options)(?:async|asjsonasync|form)?(?:\s*\(|\.new\b)`)
	fetchRe       = regexp.MustCompile(`\bfetch\s*\(`)
)

// endpoints returns the URL literals of the file: full URLs, and paths such
// as "/v1/orders" or `${base}/v1/orders`. A path takes the file's most
// API-looking host literal as its host until Endpoints knows better.
func (f *file) endpoints() []core.URLUse {
	var res []core.URLUse
	var cols []int
	host := ""
	for _, l := range f.lits {
		u, h, ok := endpoint(l.Text)
		if !ok {
			continue
		}
		if h != "" && (host == "" || strings.HasPrefix(h, "api.") && !strings.HasPrefix(host, "api.")) {
			host = h
		}
		res = append(res, core.URLUse{File: f.File, Line: l.Line, URL: u, Host: h})
		cols = append(cols, l.Col)
	}
	lines := map[int]bool{}
	for _, u := range res {
		lines[u.Line] = true
	}
	for k := range res {
		if res[k].Host == "" {
			res[k].Host = host
		}
		res[k].Method = f.method(res[k].Line, cols[k], lines)
	}
	return res
}

// endpoint reads a literal as a full URL and its host, or as a path with no
// host. A computed host or a leading base leaves just the path.
func endpoint(s string) (u, host string, ok bool) {
	if fullURLRe.MatchString(s) {
		rest := s[strings.Index(s, "://")+3:]
		end := strings.IndexAny(rest, "/?#")
		if end < 0 {
			end = len(rest)
		}
		if host = hostOf(rest[:end]); host != "" {
			return s, host, true
		}
		// a computed host such as https://${region}.example.com: keep the path
		s = rest[end:]
	}
	s = trimPlaceholder(s)
	if pathURLRe.MatchString(s) {
		return s, "", true
	}
	return "", "", false
}

func hostOf(s string) string {
	if i := strings.LastIndexByte(s, '@'); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	s = strings.ToLower(s)
	if s == "" || strings.ContainsAny(s, "{}$%<>") || !strings.Contains(s, ".") && s != "localhost" {
		return ""
	}
	return s
}

// trimPlaceholder drops a leading base such as ${base}, {base}, #{base} or %s.
func trimPlaceholder(s string) string {
	switch {
	case strings.HasPrefix(s, "${"), strings.HasPrefix(s, "#{"), strings.HasPrefix(s, "{"):
		if i := strings.IndexByte(s, '}'); i >= 0 {
			return s[i+1:]
		}
	case strings.HasPrefix(s, "%s"), strings.HasPrefix(s, "%v"):
		return s[2:]
	}
	return s
}

// method infers the HTTP method of the URL literal at line n, column col:
// an explicit method on the line or the next three (until the next URL
// literal), else the verb of the call the literal is passed to, on its line
// or opened at the end of the line before. A bare fetch() is a GET.
func (f *file) method(n, col int, urls map[int]bool) string {
	ln := f.line(n)
	if m := explicit(ln); m != "" {
		return m
	}
	if m := verbCall(ln[:min(max(col-1, 0), len(ln))], false); m != "" {
		return m
	}
	if m := verbCall(strings.TrimRight(f.line(n-1), " \t\r"), true); m != "" {
		return m
	}
	for k := n + 1; k <= n+3 && !urls[k]; k++ {
		if m := explicit(f.line(k)); m != "" {
			return m
		}
	}
	if fetchRe.MatchString(ln) {
		return "GET"
	}
	return ""
}

func explicit(s string) string {
	for _, re := range []*regexp.Regexp{methodKeyRe, methodConstRe, methodLitRe} {
		if m := re.FindStringSubmatch(s); m != nil {
			return strings.ToUpper(m[1])
		}
	}
	return ""
}

// verbCall returns the verb of the last call like .get( or ->post( in s; with
// atEnd only a call that ends s counts.
func verbCall(s string, atEnd bool) string {
	ms := verbCallRe.FindAllStringSubmatchIndex(s, -1)
	if len(ms) == 0 {
		return ""
	}
	m := ms[len(ms)-1]
	if atEnd && m[1] != len(s) {
		return ""
	}
	return strings.ToUpper(s[m[2]:m[3]])
}
