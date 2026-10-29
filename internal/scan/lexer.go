package scan

import (
	"bytes"
	"sort"
	"strings"
	"unicode/utf8"
)

// lit is a string literal. Text is its content as written, escapes and
// interpolations included, without the delimiters. Off is the offset of the
// first content byte.
type lit struct {
	Off  int
	Line int
	Col  int
	Text string
}

// syntax is how a language writes comments and strings. The flags at the end
// switch on the few constructs that do not fit a table.
type syntax struct {
	line   []string // line comment openers
	block  bool     // /* */ comments
	nest   bool     // block comments nest
	quotes string   // quotes that open strings with backslash escapes
	multi  bool     // those strings may span lines
	raw    string   // quotes that open strings without escapes, which may span lines
	triple string   // quotes that also open triple-quoted strings

	template bool // `...${expr}...`
	regex    bool // /re/ where an operand is expected
	ruby     bool // %w[] %q() literals, heredocs, =begin/=end
	php      bool // heredoc and nowdoc, text outside <?php ?>
	csharp   bool // @"" verbatim and """ raw strings
	rust     bool // r#""# raw strings, char literals next to lifetimes
}

var (
	goSyntax   = &syntax{line: []string{"//"}, block: true, quotes: `"'`, raw: "`"}
	jsSyntax   = &syntax{line: []string{"//"}, block: true, quotes: `"'`, template: true, regex: true}
	pySyntax   = &syntax{line: []string{"#"}, quotes: `"'`, triple: `"'`}
	javaSyntax = &syntax{line: []string{"//"}, block: true, quotes: `"'`, triple: `"`}
	rubySyntax = &syntax{line: []string{"#"}, quotes: "\"'`", multi: true, regex: true, ruby: true}
	phpSyntax  = &syntax{line: []string{"//", "#"}, block: true, quotes: "\"'`", multi: true, php: true}
	csSyntax   = &syntax{line: []string{"//"}, block: true, quotes: `"'`, csharp: true}
	rustSyntax = &syntax{line: []string{"//"}, block: true, nest: true, quotes: `"`, multi: true, rust: true}
)

var bom = []byte("\xef\xbb\xbf")

type lexer struct {
	*syntax
	src  []byte
	code []byte
	lits []lit
	i    int
	prev byte // last code byte that was not a space
	docs []heredoc
	bad  bool
}

type heredoc struct {
	id     string
	indent bool
}

// strip returns a copy of src with comments and string contents blanked, and
// the strings it blanked. Newlines are kept, so lines and columns still line
// up with src. ok is false when a comment or string runs off the end of the
// file; the rest of the file is then left as code.
func strip(src []byte, syn *syntax) (code []byte, lits []lit, ok bool) {
	l := &lexer{syntax: syn, src: src, code: bytes.Clone(src)}
	if bytes.HasPrefix(src, bom) {
		l.blank(0, len(bom))
		l.i = len(bom)
	}
	if l.php {
		l.html()
	}
	l.run(false)
	sort.Slice(l.lits, func(a, b int) bool { return l.lits[a].Off < l.lits[b].Off })
	nl := newlines(src)
	for k := range l.lits {
		l.lits[k].Line, l.lits[k].Col = pos(nl, l.lits[k].Off)
	}
	return l.code, l.lits, !l.bad
}

// run lexes code. Inside a template expression it stops at the brace that
// closes the expression.
func (l *lexer) run(expr bool) {
	depth := 0
	for l.i < len(l.src) {
		c := l.src[l.i]
		switch {
		case c == '\n':
			l.i++
			if len(l.docs) > 0 {
				l.heredocs()
			}
			continue
		case c == ' ' || c == '\t' || c == '\r':
			l.i++
			continue
		case l.comment():
			continue
		case l.str():
			l.prev = '"'
			continue
		case c == '{':
			depth++
		case c == '}':
			if expr && depth == 0 {
				return
			}
			depth--
		}
		l.prev = c
		l.i++
	}
}

func (l *lexer) comment() bool {
	switch {
	case l.php && l.at("?>"):
		l.html()
	case l.php && l.at("#["):
		return false
	case l.lineComment():
		end := l.eol(l.i)
		if l.php {
			if k := bytes.Index(l.src[l.i:end], []byte("?>")); k >= 0 {
				end = l.i + k
			}
		}
		l.blank(l.i, end)
		l.i = end
	case l.block && l.at("/*"):
		l.blockComment()
	case l.ruby && l.at("=begin") && (l.i == 0 || l.src[l.i-1] == '\n'):
		k := bytes.Index(l.src[l.i:], []byte("\n=end"))
		if k < 0 {
			l.fail()
			break
		}
		end := l.eol(l.i + k + 1)
		l.blank(l.i, end)
		l.i = end
	default:
		return false
	}
	return true
}

func (l *lexer) lineComment() bool {
	for _, s := range l.line {
		if l.at(s) {
			return true
		}
	}
	return false
}

func (l *lexer) blockComment() {
	depth := 0
	for j := l.i; j < len(l.src); {
		switch {
		case (depth == 0 || l.nest) && l.has(j, "/*"):
			depth++
			j += 2
		case l.has(j, "*/"):
			depth--
			j += 2
			if depth == 0 {
				l.blank(l.i, j)
				l.i = j
				return
			}
		default:
			j++
		}
	}
	l.fail()
}

// html blanks the text outside <?php ... ?>, starting at l.i.
func (l *lexer) html() {
	k := bytes.Index(l.src[l.i:], []byte("<?"))
	if k < 0 {
		l.blank(l.i, len(l.src))
		l.i = len(l.src)
		return
	}
	end := l.i + k + 2
	switch {
	case l.has(end, "php"):
		end += 3
	case l.has(end, "="):
		end++
	}
	l.blank(l.i, end)
	l.i = end
}

func (l *lexer) str() bool {
	c := l.src[l.i]
	switch {
	case l.template && c == '`':
		l.tmpl()
	case l.rust && (c == 'r' || c == 'b') && (l.i == 0 || !isIdent(l.src[l.i-1])):
		return l.rustRaw()
	case l.rust && c == '\'':
		return l.char()
	case l.csharp && (c == '@' || c == '$'):
		return l.verbatim()
	case l.csharp && l.at(`"""`):
		l.rawQuotes()
	case strings.IndexByte(l.triple, c) >= 0 && l.has(l.i+1, string([]byte{c, c})):
		l.closed(l.i+3, string([]byte{c, c, c}), true)
	case strings.IndexByte(l.raw, c) >= 0:
		l.closed(l.i+1, string(c), false)
	case strings.IndexByte(l.quotes, c) >= 0:
		return l.quoted(c)
	case l.regex && c == '/' && l.regexAllowed():
		return l.re()
	case l.ruby && c == '%':
		return l.percent()
	case l.ruby && l.at("<<"):
		return l.heredoc()
	case l.php && l.at("<<<"):
		return l.nowdoc()
	default:
		return false
	}
	return true
}

// quoted reads a string with backslash escapes. A quote that does not close
// on its line in a language without multi-line strings is not a string: it is
// an apostrophe in JSX text or the like, and lexing goes on after it.
func (l *lexer) quoted(q byte) bool {
	for j := l.i + 1; j < len(l.src); j++ {
		switch l.src[j] {
		case '\\':
			j++
		case q:
			l.add(l.i+1, j)
			l.i = j + 1
			return true
		case '\n':
			if !l.multi {
				return false
			}
		}
	}
	if !l.multi {
		return false
	}
	l.fail()
	return true
}

// closed reads a string whose content starts at from and ends at end.
func (l *lexer) closed(from int, end string, esc bool) {
	k := l.find(from, end, esc)
	if k < 0 {
		l.fail()
		return
	}
	l.add(from, k)
	l.i = k + len(end)
}

func (l *lexer) tmpl() {
	start := l.i + 1
	seg := start
	for j := start; j < len(l.src); {
		switch {
		case l.src[j] == '\\':
			j += 2
		case l.src[j] == '`':
			l.blank(seg, j)
			l.lits = append(l.lits, lit{Off: start, Text: string(l.src[start:j])})
			l.i = j + 1
			return
		case l.has(j, "${"):
			l.blank(seg, j)
			l.i = j + 2
			l.run(true)
			j = l.i + 1
			seg = j
		default:
			j++
		}
	}
	l.fail()
}

var (
	regexAfter = map[string]bool{
		"return": true, "typeof": true, "instanceof": true, "in": true, "of": true, "new": true, "delete": true,
		"void": true, "throw": true, "case": true, "do": true, "else": true, "yield": true, "await": true,
	}
	rubyRegexAfter = map[string]bool{
		"if": true, "elsif": true, "unless": true, "when": true, "and": true, "or": true, "not": true, "while": true, "until": true,
	}
)

func (l *lexer) regexAllowed() bool {
	switch {
	case l.prev == 0 || strings.IndexByte("(,=:[!&|?;{~>*%^", l.prev) >= 0:
		return true
	case l.prev == '+' || l.prev == '-':
		j := l.back(l.i)
		return j < 1 || l.code[j-1] != l.prev
	case !isIdent(l.prev):
		return false
	}
	end := l.back(l.i) + 1
	start := end
	for start > 0 && isIdent(l.code[start-1]) {
		start--
	}
	if start > 0 && l.code[start-1] == '.' {
		return false
	}
	word := l.code[start:end]
	return regexAfter[string(word)] || l.ruby && rubyRegexAfter[string(word)]
}

func (l *lexer) back(j int) int {
	j--
	for j >= 0 && isSpace(l.code[j]) {
		j--
	}
	return j
}

func (l *lexer) re() bool {
	class := false
	for j := l.i + 1; j < len(l.src); j++ {
		switch c := l.src[j]; {
		case c == '\\':
			j++
		case c == '\n':
			return false
		case c == '[':
			class = true
		case c == ']':
			class = false
		case c == '/' && !class:
			j++
			for j < len(l.src) && isIdent(l.src[j]) {
				j++
			}
			l.blank(l.i, j)
			l.i = j
			return true
		}
	}
	return false
}

func (l *lexer) rustRaw() bool {
	j := l.i
	if l.src[j] == 'b' {
		j++
	}
	if !l.has(j, "r") {
		return false
	}
	j++
	h := j
	for j < len(l.src) && l.src[j] == '#' {
		j++
	}
	if !l.has(j, `"`) {
		return false
	}
	l.closed(j+1, `"`+strings.Repeat("#", j-h), false)
	return true
}

// char tells a char literal 'a' or '\n' from a lifetime 'a.
func (l *lexer) char() bool {
	j := l.i + 1
	if j >= len(l.src) || l.src[j] == '\'' || l.src[j] == '\n' {
		return false
	}
	var k int
	if l.src[j] == '\\' {
		// the escaped byte, then up to \u{10FFFF}
		end := min(j+12, len(l.src))
		n := -1
		if j+2 <= end {
			n = bytes.IndexByte(l.src[j+2:end], '\'')
		}
		if n < 0 {
			return false
		}
		k = j + 2 + n
	} else {
		_, size := utf8.DecodeRune(l.src[j:])
		k = j + size
	}
	if k >= len(l.src) || l.src[k] != '\'' {
		return false
	}
	l.add(j, k)
	l.i = k + 1
	return true
}

// verbatim reads @"..." and $@"...", where "" is a quote and backslashes are
// plain. A lone $ prefix is a regular string and is left to quoted.
func (l *lexer) verbatim() bool {
	j, at := l.i, false
	for j < len(l.src) && j < l.i+2 && (l.src[j] == '@' || l.src[j] == '$') {
		at = at || l.src[j] == '@'
		j++
	}
	if !at || !l.has(j, `"`) {
		return false
	}
	for k := j + 1; k < len(l.src); k++ {
		if l.src[k] != '"' {
			continue
		}
		if l.has(k, `""`) {
			k++
			continue
		}
		l.add(j+1, k)
		l.i = k + 1
		return true
	}
	l.fail()
	return true
}

// rawQuotes reads a C# raw string: three or more quotes, closed by as many.
func (l *lexer) rawQuotes() {
	j := l.i
	for j < len(l.src) && l.src[j] == '"' {
		j++
	}
	l.closed(j, string(l.src[l.i:j]), false)
}

var pairs = map[byte]byte{'(': ')', '[': ']', '{': '}', '<': '>'}

// percent reads %w[], %q(), %Q{}, %i<>, %r||, %() and friends. A % after an
// operand is the modulo operator, unless it is %w[ style with a space before.
func (l *lexer) percent() bool {
	j := l.i + 1
	letter := j < len(l.src) && strings.IndexByte("qQwWiIrsx", l.src[j]) >= 0
	if letter {
		j++
	}
	if j >= len(l.src) {
		return false
	}
	open := l.src[j]
	if isIdent(open) || isSpace(open) || open == '=' {
		return false
	}
	operand := isIdent(l.prev) || strings.IndexByte(`)]}"`, l.prev) >= 0
	spaced := l.i > 0 && l.src[l.i-1] == ' '
	if operand && (!letter || !spaced) {
		return false
	}
	end, paired := pairs[open]
	if !paired {
		end = open
	}
	depth := 0
	for k := j + 1; k < len(l.src); k++ {
		switch c := l.src[k]; {
		case c == '\\':
			k++
		case paired && c == open:
			depth++
		case c == end && depth > 0:
			depth--
		case c == end:
			l.add(j+1, k)
			l.i = k + 1
			return true
		}
	}
	l.fail()
	return true
}

// heredoc reads a Ruby heredoc opener: <<~ID, <<-ID, <<ID, with ID optionally
// quoted. The body starts on the next line and is read by heredocs.
func (l *lexer) heredoc() bool {
	j := l.i + 2
	indent := l.has(j, "~") || l.has(j, "-")
	if indent {
		j++
	}
	var q byte
	if j < len(l.src) && strings.IndexByte("'\"`", l.src[j]) >= 0 {
		q = l.src[j]
		j++
	}
	k := j
	for k < len(l.src) && isIdent(l.src[k]) {
		k++
	}
	if k == j {
		return false
	}
	id := string(l.src[j:k])
	if q != 0 {
		if !l.has(k, string(q)) {
			return false
		}
		k++
	}
	if !indent && q == 0 {
		// a bare <<ID could be a shift; only an upper-case ID after a space,
		// paren, comma or = reads as a heredoc
		before := byte(' ')
		if l.i > 0 {
			before = l.src[l.i-1]
		}
		if id[0] < 'A' || id[0] > 'Z' || strings.IndexByte(" (,=", before) < 0 {
			return false
		}
	}
	l.docs = append(l.docs, heredoc{id, indent})
	l.blank(l.i, k)
	l.i = k
	return true
}

// heredocs reads the bodies of the heredocs opened on the previous line.
func (l *lexer) heredocs() {
	docs := l.docs
	l.docs = nil
	for n, d := range docs {
		if n > 0 {
			l.i++
		}
		start := l.i
		for {
			if l.i >= len(l.src) {
				l.fail()
				return
			}
			end := l.eol(l.i)
			line := strings.TrimRight(string(l.src[l.i:end]), "\r")
			if d.indent {
				line = strings.TrimLeft(line, " \t")
			}
			if line == d.id {
				l.add(start, max(start, l.i-1))
				l.blank(l.i, end)
				l.i = end
				break
			}
			l.i = end + 1
		}
	}
}

// nowdoc reads a PHP heredoc or nowdoc. The closing identifier may be
// indented and followed by more code on its line.
func (l *lexer) nowdoc() bool {
	j := l.i + 3
	for j < len(l.src) && (l.src[j] == ' ' || l.src[j] == '\t') {
		j++
	}
	var q byte
	if j < len(l.src) && (l.src[j] == '\'' || l.src[j] == '"') {
		q = l.src[j]
		j++
	}
	k := j
	for k < len(l.src) && isIdent(l.src[k]) {
		k++
	}
	if k == j {
		return false
	}
	id := l.src[j:k]
	if q != 0 {
		if !l.has(k, string(q)) {
			return false
		}
		k++
	}
	body := l.eol(k) + 1
	for s := body; s < len(l.src); {
		e := l.eol(s)
		t := bytes.TrimLeft(l.src[s:e], " \t")
		if bytes.HasPrefix(t, id) && (len(t) == len(id) || !isIdent(t[len(id)])) {
			stop := e - len(t) + len(id)
			l.add(body, max(body, s-1))
			l.blank(l.i, stop)
			l.i = stop
			return true
		}
		s = e + 1
	}
	l.fail()
	return true
}

func (l *lexer) add(a, b int) {
	l.lits = append(l.lits, lit{Off: a, Text: string(l.src[a:b])})
	l.blank(a, b)
}

func (l *lexer) blank(a, b int) {
	b = min(b, len(l.code))
	for k := a; k < b; k++ {
		if l.code[k] != '\n' {
			l.code[k] = ' '
		}
	}
}

func (l *lexer) fail() {
	l.bad = true
	l.i = len(l.src)
}

// find returns the offset of end at or after from, -1 if there is none. With
// esc a backslash hides the byte after it.
func (l *lexer) find(from int, end string, esc bool) int {
	for j := from; j < len(l.src); j++ {
		if esc && l.src[j] == '\\' {
			j++
			continue
		}
		if l.has(j, end) {
			return j
		}
	}
	return -1
}

func (l *lexer) at(s string) bool { return l.has(l.i, s) }

func (l *lexer) has(j int, s string) bool {
	return j >= 0 && len(l.src)-j >= len(s) && string(l.src[j:j+len(s)]) == s
}

func (l *lexer) eol(j int) int {
	if k := bytes.IndexByte(l.src[j:], '\n'); k >= 0 {
		return j + k
	}
	return len(l.src)
}

func isIdent(c byte) bool {
	return c == '_' || c == '$' || c >= 0x80 || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

func isIdentStart(c byte) bool { return isIdent(c) && (c < '0' || c > '9') }

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func newlines(src []byte) []int {
	var res []int
	for i := 0; ; {
		k := bytes.IndexByte(src[i:], '\n')
		if k < 0 {
			return res
		}
		res = append(res, i+k)
		i += k + 1
	}
}

// pos turns a byte offset into a 1-based line and a 1-based byte column.
func pos(nl []int, off int) (line, col int) {
	n := sort.SearchInts(nl, off)
	start := 0
	if n > 0 {
		start = nl[n-1] + 1
	}
	return n + 1, off - start + 1
}
