package scan

import (
	"regexp"
	"strings"
)

var phpLang = &scanner{
	lang:    "php",
	exts:    []string{".php"},
	syn:     phpSyntax,
	seps:    []string{"?->", "->", "::", `\`},
	sep:     `\`,
	imports: phpImports,
	implicit: func(f *file, at int, name string) []string {
		// \A\B\C is fully qualified and needs no use statement
		if at > 0 && f.code[at-1] == '\\' {
			return []string{name}
		}
		return nil
	},
}

var phpUseRe = regexp.MustCompile(`(?m)^[ \t]*use[ \t]+(?:(?:function|const)[ \t]+)?([^;(]+);`)

// phpImports reads `use A\B\C;`, `use A\B\C as D;`, `use function A\b;` and
// grouped `use A\{B, C as D};`. One import per name.
func phpImports(f *file) {
	code := f.code
	for _, m := range phpUseRe.FindAllSubmatchIndex(code, -1) {
		body := string(code[m[2]:m[3]])
		prefix, items := "", body
		if open := strings.IndexByte(body, '{'); open >= 0 {
			prefix = strings.TrimSpace(body[:open])
			items = strings.TrimSuffix(strings.TrimSpace(body[open+1:]), "}")
		}
		for _, item := range strings.Split(items, ",") {
			fs := strings.Fields(item)
			if len(fs) == 0 {
				continue
			}
			if fs[0] == "function" || fs[0] == "const" {
				fs = fs[1:]
			}
			if len(fs) != 1 && (len(fs) != 3 || fs[1] != "as") {
				continue
			}
			name := strings.TrimPrefix(prefix+fs[0], `\`)
			binding := name[strings.LastIndexByte(name, '\\')+1:]
			if len(fs) == 3 {
				binding = fs[2]
			}
			f.addImport(m[2], name, binding, nil)
			f.bind(binding, 0, false, false, name)
		}
		f.blank(m[0], m[1])
	}
}
