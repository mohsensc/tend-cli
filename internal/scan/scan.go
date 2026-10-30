// Package scan finds where code touches its dependencies and the APIs it calls.
package scan

import (
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
)

type Options struct {
	Root  string
	Skip  []string // extra dir names to skip
	Files []string // the files to consider, when already listed; nil = git ls-files
}

// Go is read with go/parser. Every other language goes through a small lexer
// that blanks comments and strings, so a name inside either is never taken
// for code, and keeps the strings aside for URL and field lookups.
type lang interface {
	Lang() string
	Match(path string) bool
	scan(path string, src []byte) (*file, error)
}

var langs = []lang{goLang{}, jsLang, pyLang, javaLang, rubyLang, phpLang, csLang, rustLang}

// FileScan is what one file says about its dependencies. Lines and columns
// are 1-based; columns count bytes, so a tab is one column.
type FileScan struct {
	File    string
	Lang    string
	Imports []core.Import
	Sites   []core.Site
	URLs    []core.URLUse
}

// file is one scanned source file: the result plus what the index needs to
// answer later questions about it.
type file struct {
	FileScan
	src   []byte
	code  []byte
	lits  []lit
	nl    []int
	roots map[string][]root
	found []found
	imps  []imp
	open  []string        // packages whose names are in scope unqualified: go dot imports, c# usings
	decl  map[string]bool // names the file declares itself, never attributed through open
}

// root is a name a chain can start at: an import, or a local made by a
// constructor call on one (client = new Stripe(key)), which holds to the end
// of the file, or in Go to the end of its block. quals are the qualified
// names it stands for; a site's are those plus its members, which is what
// Uses matches.
type root struct {
	quals  []string
	at     int  // offset from which the name is bound
	end    int  // offset where its scope ends, 0 for the end of the file
	member bool // bound as this.x, self.x, $this->x or @x
	alias  bool // a local made from a call on an import
}

type found struct {
	core.Site
	off   int
	quals []string
}

type imp struct {
	core.Import
	off   int
	quals []string // every name the import brings in, for Index.Imports
}

func newFile(p, lang string, src []byte, syn *syntax) *file {
	code, lits, ok := strip(src, syn)
	if !ok {
		lang += "?"
	}
	return &file{
		FileScan: FileScan{File: p, Lang: lang},
		src:      src,
		code:     code,
		lits:     lits,
		nl:       newlines(src),
		roots:    map[string][]root{},
		decl:     map[string]bool{},
	}
}

func (f *file) pos(off int) (line, col int) { return pos(f.nl, off) }

// line returns line n as written, without its newline.
func (f *file) line(n int) string {
	if n < 1 || n > len(f.nl)+1 {
		return ""
	}
	start, end := 0, len(f.src)
	if n > 1 {
		start = f.nl[n-2] + 1
	}
	if n <= len(f.nl) {
		end = f.nl[n-1]
	}
	return string(f.src[start:end])
}

// text is a line for display: trimmed, at most 200 bytes.
func (f *file) text(n int) string {
	return core.Clip(strings.TrimSpace(strings.TrimPrefix(f.line(n), string(bom))), 200)
}

func (f *file) bind(name string, at int, member, alias bool, quals ...string) {
	f.roots[name] = append(f.roots[name], root{quals: quals, at: at, member: member, alias: alias})
}

func (f *file) bindScoped(name string, at, end int, quals ...string) {
	f.roots[name] = append(f.roots[name], root{quals: quals, at: at, end: end, alias: true})
}

// lookup finds the binding of name in force at off: the latest one before
// it whose scope has not ended.
func (f *file) lookup(name string, off int, member bool) *root {
	var res *root
	rs := f.roots[name]
	for k := range rs {
		r := &rs[k]
		if r.member == member && r.at <= off && (r.end == 0 || off < r.end) && (res == nil || r.at >= res.at) {
			res = r
		}
	}
	return res
}

func (f *file) addImport(off int, pkg, binding string, names []string, quals ...string) {
	if len(quals) == 0 {
		quals = []string{pkg}
	}
	line, _ := f.pos(off)
	im := core.Import{File: f.File, Line: line, Package: pkg, Binding: binding, Names: names}
	f.imps = append(f.imps, imp{im, off, quals})
}

func (f *file) addSite(off int, chain []string, call bool, quals []string) {
	line, col := f.pos(off)
	s := core.Site{File: f.File, Line: line, Col: col, Chain: chain, Call: call, Text: f.text(line)}
	f.found = append(f.found, found{s, off, quals})
}

// finish orders what the scanners found and fills in the public result.
func (f *file) finish() {
	sort.SliceStable(f.found, func(a, b int) bool { return f.found[a].off < f.found[b].off })
	f.found = slices.CompactFunc(f.found, func(a, b found) bool { return a.off == b.off })
	sort.SliceStable(f.imps, func(a, b int) bool { return f.imps[a].off < f.imps[b].off })
	f.Sites, f.Imports = nil, nil
	for _, s := range f.found {
		f.Sites = append(f.Sites, s.Site)
	}
	for _, im := range f.imps {
		f.Imports = append(f.Imports, im.Import)
	}
	f.URLs = f.endpoints()
}

// litAt returns the literal whose content starts at off.
func (f *file) litAt(off int) (lit, bool) {
	k := sort.Search(len(f.lits), func(k int) bool { return f.lits[k].Off >= off })
	if k < len(f.lits) && f.lits[k].Off == off {
		return f.lits[k], true
	}
	return lit{}, false
}

// litAfter returns the literal opened by the quote at off, and the offset
// just past its closing quote.
func (f *file) litAfter(off int) (lit, int, bool) {
	if off >= len(f.code) || strings.IndexByte("\"'`", f.code[off]) < 0 {
		return lit{}, 0, false
	}
	l, ok := f.litAt(off + 1)
	return l, off + 1 + len(l.Text) + 1, ok
}

// blank removes a statement from the code, keeping its newlines.
func (f *file) blank(a, b int) {
	for k := a; k < b && k < len(f.code); k++ {
		if f.code[k] != '\n' {
			f.code[k] = ' '
		}
	}
}

// scanner is a lexer-level scanner. The languages differ in their syntax, their
// member separators and how they import; the walk over the code is shared.
type scanner struct {
	lang     string
	exts     []string
	syn      *syntax
	seps     []string // member separators, longest first
	sep      string   // joins a qualified name and its members; "" keeps the import path as is
	index    bool     // a['b'] is a member access
	decorate bool     // @a and @a.b at the start of a line are sites
	ivar     bool     // @a is an instance variable, a member of self
	imports  func(f *file)
	implicit func(f *file, at int, name string) []string // roots that need no import
}

func (s *scanner) Lang() string { return s.lang }

func (s *scanner) Match(p string) bool { return slices.Contains(s.exts, strings.ToLower(path.Ext(p))) }

func (s *scanner) scan(p string, src []byte) (*file, error) {
	f := newFile(p, s.lang, src, s.syn)
	s.imports(f)
	s.aliases(f)
	s.walk(f)
	f.finish()
	return f, nil
}

// walk records a site for every chain in the code that starts at a root.
func (s *scanner) walk(f *file) {
	code := f.code
	for i := 0; i < len(code); {
		if !isIdentStart(code[i]) || i > 0 && isIdent(code[i-1]) {
			i++
			continue
		}
		end := identEnd(code, i)
		quals, alias := s.resolve(f, i, end)
		if quals != nil {
			chain, stop := s.chain(f, i)
			call := next(code, stop) == '('
			if len(chain) > 1 || call || s.decorated(code, i) {
				if !alias {
					quals = qualify(quals, s.sep, chain[1:])
				}
				f.addSite(i, chain, call, quals)
			}
		}
		i = end
	}
}

// resolve says what the name at i stands for, if anything.
func (s *scanner) resolve(f *file, i, end int) (quals []string, alias bool) {
	code := f.code
	member := false
	if p := sepBefore(code, i, s.seps); p >= 0 {
		if !receiver(code, p) {
			return nil, false
		}
		member = true
	} else if s.ivar && i > 0 && code[i-1] == '@' {
		member = true
	}
	if _, ok := f.roots[string(code[i:end])]; !ok && (member || s.implicit == nil) {
		return nil, false
	}
	name := string(code[i:end])
	if r := f.lookup(name, i, member); r != nil {
		return r.quals, r.alias
	}
	if member || s.implicit == nil {
		return nil, false
	}
	return s.implicit(f, i, name), false
}

// chain reads the member chain that starts with the name at i and returns
// its segments and the offset just past it.
func (s *scanner) chain(f *file, i int) ([]string, int) {
	end := identEnd(f.code, i)
	res := []string{string(f.code[i:end])}
	for {
		seg, k := s.member(f, skipSpace(f.code, end))
		if k < 0 {
			return res, end
		}
		res = append(res, seg)
		end = k
	}
}

// member reads one member access at j: a separator and a name, or ['name'].
func (s *scanner) member(f *file, j int) (string, int) {
	code := f.code
	for _, sep := range s.seps {
		if !hasAt(code, j, sep) {
			continue
		}
		k := skipSpace(code, j+len(sep))
		if k < len(code) && isIdentStart(code[k]) {
			e := identEnd(code, k)
			return string(code[k:e]), e
		}
		return "", -1
	}
	if !s.index || j >= len(code) || code[j] != '[' {
		return "", -1
	}
	l, end, ok := f.litAfter(skipSpace(code, j+1))
	if !ok || !isName(l.Text) {
		return "", -1
	}
	if e := skipSpace(code, end); e < len(code) && code[e] == ']' {
		return l.Text, e + 1
	}
	return "", -1
}

func (s *scanner) decorated(code []byte, i int) bool {
	if !s.decorate || i == 0 || code[i-1] != '@' {
		return false
	}
	for k := i - 2; k >= 0 && code[k] != '\n'; k-- {
		if code[k] != ' ' && code[k] != '\t' {
			return false
		}
	}
	return true
}

var aliasRe = regexp.MustCompile(`(this\.|self\.|\$this->|@)?([A-Za-z_$][\w$]*)(?:\s*:\s*[\w$.<>\[\]?, ]+?)?\s*=`)

// aliases binds local names assigned from a call on an import:
// client = OpenAI(), $c = new Client(), this.s3 = new S3Client({}).
func (s *scanner) aliases(f *file) {
	code := f.code
	for _, m := range aliasRe.FindAllSubmatchIndex(code, -1) {
		start, eq := m[0], m[1]
		if eq < len(code) && strings.IndexByte("=>~", code[eq]) >= 0 {
			continue
		}
		if start > 0 && (isIdent(code[start-1]) || m[2] < 0 && code[start-1] == '.') {
			continue
		}
		from := skipWords(code, skipSpace(code, eq), "await", "yield")
		k := skipWords(code, from, "new")
		isNew := k != from
		if k < len(code) && code[k] == '\\' {
			k++
		}
		if k >= len(code) || !isIdentStart(code[k]) {
			continue
		}
		name := string(code[k:identEnd(code, k)])
		var quals []string
		if r := f.lookup(name, k, false); r != nil {
			if r.alias {
				continue
			}
			quals = r.quals
		} else if s.implicit != nil {
			quals = s.implicit(f, k, name)
		}
		if quals == nil {
			continue
		}
		chain, stop := s.chain(f, k)
		if next(code, stop) != '(' && chain[len(chain)-1] != "new" || !constructor(chain, isNew) {
			continue
		}
		f.bind(string(code[m[4]:m[5]]), k, m[2] >= 0, true, qualify(quals, s.sep, chain[1:])...)
	}
}

// constructor tells a call that makes a client from one that calls the API:
// new X(), X(), stripe(key), boto3.client('s3'), Foo.new, axios.create() make
// one; charge = Charge.create(...) holds a result and is no alias.
func constructor(chain []string, isNew bool) bool {
	last := chain[len(chain)-1]
	low := strings.ToLower(last)
	switch {
	case isNew, len(chain) == 1, isUpper(last[0]), low == "new":
		return true
	case strings.Contains(low, "client"), strings.Contains(low, "connect"), low == "resource", low == "session":
		return true
	}
	return low == "create" && len(chain) == 2 && !isUpper(chain[0][0])
}

func qualify(quals []string, sep string, rest []string) []string {
	if sep == "" || len(rest) == 0 {
		return quals
	}
	tail := sep + strings.Join(rest, sep)
	res := make([]string, len(quals))
	for k, q := range quals {
		res[k] = q + tail
	}
	return res
}

// sepBefore returns the offset of the member separator right before the name
// at i, -1 if there is none. A spread (...x) is not a member access, nor is a
// leading backslash, which makes a PHP name fully qualified.
func sepBefore(code []byte, i int, seps []string) int {
	j := i
	for j > 0 && isSpace(code[j-1]) {
		j--
	}
	for _, s := range seps {
		p := j - len(s)
		if p < 0 || string(code[p:j]) != s {
			continue
		}
		if s == "." && p > 0 && code[p-1] == '.' || s == `\` && (p == 0 || !isIdent(code[p-1])) {
			return -1
		}
		return p
	}
	return -1
}

// receiver tells whether the separator at p follows this, self or $this.
func receiver(code []byte, p int) bool {
	for _, w := range []string{"this", "self", "$this"} {
		k := p - len(w)
		if k >= 0 && string(code[k:p]) == w && (k == 0 || !isIdent(code[k-1])) {
			return true
		}
	}
	return false
}

func identEnd(code []byte, i int) int {
	for i < len(code) && isIdent(code[i]) {
		i++
	}
	return i
}

func skipSpace(code []byte, i int) int {
	for i < len(code) && isSpace(code[i]) {
		i++
	}
	return i
}

// skipWords skips any of the keywords, in any order, and the space after each.
func skipWords(code []byte, i int, words ...string) int {
	for again := true; again; {
		again = false
		for _, w := range words {
			e := i + len(w)
			if hasAt(code, i, w) && e < len(code) && isSpace(code[e]) {
				i, again = skipSpace(code, e), true
			}
		}
	}
	return i
}

// next returns the first byte at or after i that is not a space, 0 at the end.
func next(code []byte, i int) byte {
	if i = skipSpace(code, i); i < len(code) {
		return code[i]
	}
	return 0
}

// wordBefore returns the identifier that ends right before i, spaces skipped.
func wordBefore(code []byte, i int) string {
	j := i
	for j > 0 && isSpace(code[j-1]) {
		j--
	}
	k := j
	for k > 0 && isIdent(code[k-1]) {
		k--
	}
	return string(code[k:j])
}

func hasAt(code []byte, j int, s string) bool {
	return j >= 0 && len(code)-j >= len(s) && string(code[j:j+len(s)]) == s
}

func isName(s string) bool {
	if s == "" || !isIdentStart(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isIdent(s[i]) {
			return false
		}
	}
	return true
}

func isUpper(c byte) bool { return 'A' <= c && c <= 'Z' }

// testDirs and the name patterns below are where each language keeps tests
// by convention.
var testDirs = map[string]bool{"test": true, "tests": true, "__tests__": true, "spec": true, "specs": true, "testdata": true}

var testName = regexp.MustCompile(`(_test\.go|\.(test|spec)(-d)?\.[cm]?[jt]sx?|^tests?\.([cm]?[jt]sx?|py)|^test_.*\.py|_test\.py|^conftest\.py|_(spec|test)\.rb|Tests?\.(java|kt|cs|php)|IT\.java)$`)

// IsTest reports whether a repo-relative path is test code by its language's
// conventions: a _test.go file, a .test.js file, anything under tests/, and so on.
func IsTest(p string) bool {
	for _, dir := range strings.Split(path.Dir(p), "/") {
		if testDirs[dir] || strings.HasSuffix(dir, ".Tests") {
			return true
		}
	}
	return testName.MatchString(path.Base(p))
}
