package scan

import (
	"bytes"
	"regexp"
	"strings"
)

var jsLang = &scanner{
	lang:     "js",
	exts:     []string{".js", ".mjs", ".cjs", ".jsx", ".ts", ".tsx", ".mts", ".cts"},
	syn:      jsSyntax,
	seps:     []string{"?.", "."},
	index:    true,
	decorate: true,
	imports:  jsImports,
}

var (
	jsImportRe  = regexp.MustCompile(`\bimport\b`)
	jsExportRe  = regexp.MustCompile(`\bexport\s+(?:type\s+)?(?:\*(?:\s+as\s+[\w$]+)?|\{[^}]*\})\s*from\s*['"]`)
	jsRequireRe = regexp.MustCompile(`(?:\b(?:const|let|var|import)\s+([\w$]+|\{[^}]*\})\s*=\s*)?(?:await\s+)?\b(?:require|import)\s*\(\s*['"` + "`]")
)

// jsImports reads static imports, re-exports, require() and dynamic import(),
// binding default and namespace imports, named imports and destructured
// requires as roots of the package.
func jsImports(f *file) {
	code := f.code
	for _, m := range jsImportRe.FindAllIndex(code, -1) {
		if m[0] == 0 || code[m[0]-1] != '.' {
			jsStatic(f, m[0], m[1])
		}
	}
	for _, m := range jsExportRe.FindAllIndex(code, -1) {
		if l, end, ok := f.litAfter(m[1] - 1); ok && jsExternal(l.Text) {
			f.addImport(l.Off, l.Text, "", nil)
			f.blank(m[0], end)
		}
	}
	for _, m := range jsRequireRe.FindAllSubmatchIndex(code, -1) {
		if m[0] > 0 && code[m[0]-1] == '.' {
			continue
		}
		l, end, ok := f.litAfter(m[1] - 1)
		if !ok || !jsExternal(l.Text) {
			continue
		}
		var binding string
		var names []string
		if m[2] >= 0 {
			if decl := string(code[m[2]:m[3]]); decl[0] == '{' {
				names = jsNames(decl[1 : len(decl)-1])
			} else {
				binding = decl
			}
		}
		jsBind(f, l, binding, names)
		f.blank(m[0], end)
	}
}

// jsStatic reads one import declaration starting at the keyword.
func jsStatic(f *file, start, j int) {
	c := &cursor{b: f.code, i: j}
	if p := c.peek(); p == '(' || p == '.' {
		return
	}
	if c.word() == "type" {
		// import type X from, unless type is itself the default import
		save := c.take()
		if c.peek() == ',' || c.word() == "from" {
			c.i -= len(save)
		}
	}
	var def, ns string
	var names []string
	if w := c.word(); w != "" && w != "from" {
		def = c.take()
		c.eat(",")
	}
	switch {
	case c.eat("*"):
		if c.word() == "as" {
			c.take()
			ns = c.take()
		}
	case c.peek() == '{':
		e := bytes.IndexByte(c.b[c.i:], '}')
		if e < 0 {
			return
		}
		names = jsNames(string(c.b[c.i+1 : c.i+e]))
		c.i += e + 1
	}
	if def != "" || ns != "" || names != nil {
		if c.word() != "from" {
			return
		}
		c.take()
	}
	l, end, ok := f.litAfter(c.at())
	if !ok || !jsExternal(l.Text) {
		return
	}
	if def == "" {
		def = ns
	} else if ns != "" {
		names = append(names, ns)
	}
	jsBind(f, l, def, names)
	f.blank(start, end)
}

func jsBind(f *file, l lit, binding string, names []string) {
	f.addImport(l.Off, l.Text, binding, names)
	if binding != "" {
		f.bind(binding, 0, false, false, l.Text)
	}
	for _, n := range names {
		f.bind(n, 0, false, false, l.Text)
	}
}

// jsNames returns the local names of `a, b as c, type d` or of a
// destructuring `a, b: c, ...rest`.
func jsNames(s string) []string {
	var res []string
	for _, part := range strings.Split(s, ",") {
		part, _, _ = strings.Cut(part, "=")
		fs := strings.Fields(strings.ReplaceAll(part, ":", " as "))
		if len(fs) > 1 && fs[0] == "type" {
			fs = fs[1:]
		}
		if len(fs) == 0 {
			continue
		}
		if name := fs[len(fs)-1]; isName(name) {
			res = append(res, name)
		}
	}
	return res
}

// jsExternal tells a package specifier from a relative path or a computed one.
func jsExternal(spec string) bool {
	return spec != "" && spec[0] != '.' && spec[0] != '/' && !strings.Contains(spec, "${")
}

// jsPackage maps a specifier to its package: @scope/pkg/sub -> @scope/pkg, pkg/sub -> pkg.
func jsPackage(spec string) string {
	n := 1
	if strings.HasPrefix(spec, "@") {
		n = 2
	}
	parts := strings.SplitN(spec, "/", n+1)
	if len(parts) <= n {
		return spec
	}
	return strings.Join(parts[:n], "/")
}

// cursor reads tokens out of blanked code, skipping spaces before each.
type cursor struct {
	b []byte
	i int
}

func (c *cursor) at() int {
	c.i = skipSpace(c.b, c.i)
	return c.i
}

func (c *cursor) peek() byte {
	if c.at() < len(c.b) {
		return c.b[c.i]
	}
	return 0
}

// word returns the identifier at the cursor without moving past it.
func (c *cursor) word() string {
	i := c.at()
	if i >= len(c.b) || !isIdentStart(c.b[i]) {
		return ""
	}
	return string(c.b[i:identEnd(c.b, i)])
}

func (c *cursor) take() string {
	w := c.word()
	c.i += len(w)
	return w
}

func (c *cursor) eat(s string) bool {
	if hasAt(c.b, c.at(), s) {
		c.i += len(s)
		return true
	}
	return false
}
