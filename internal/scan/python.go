package scan

import (
	"regexp"
	"strings"
)

var pyLang = &scanner{
	lang:     "python",
	exts:     []string{".py"},
	syn:      pySyntax,
	seps:     []string{"."},
	sep:      ".",
	decorate: true,
	imports:  pyImports,
}

var (
	pyImportRe = regexp.MustCompile(`(?m)^[ \t]*import[ \t]+([^\n;]+)`)
	pyFromRe   = regexp.MustCompile(`(?m)^[ \t]*from[ \t]+([\w.]+)[ \t]+import[ \t]+(\([^)]*\)|[^\n;]+)`)
)

// pyImports reads `import a.b as c, d` and `from a.b import c as d, (e, f)`.
// Relative imports are the project's own and are left out; `from a import *`
// is recorded but binds nothing.
func pyImports(f *file) {
	code := f.code
	for _, m := range pyImportRe.FindAllSubmatchIndex(code, -1) {
		off := m[2]
		for _, item := range strings.Split(string(code[m[2]:m[3]]), ",") {
			fs := strings.Fields(item)
			switch {
			case len(fs) == 1:
				first, _, _ := strings.Cut(fs[0], ".")
				f.addImport(off, fs[0], first, nil)
				f.bind(first, 0, false, false, first)
			case len(fs) == 3 && fs[1] == "as":
				f.addImport(off, fs[0], fs[2], nil)
				f.bind(fs[2], 0, false, false, fs[0])
			}
		}
		f.blank(m[0], m[1])
	}
	for _, m := range pyFromRe.FindAllSubmatchIndex(code, -1) {
		mod := string(code[m[2]:m[3]])
		if strings.HasPrefix(mod, ".") {
			continue
		}
		list := strings.Trim(strings.TrimSpace(string(code[m[4]:m[5]])), "()")
		var names []string
		quals := []string{mod}
		for _, item := range strings.Split(list, ",") {
			fs := strings.Fields(item)
			if len(fs) == 0 || fs[0] == "*" {
				continue
			}
			local := fs[len(fs)-1]
			names = append(names, local)
			quals = append(quals, mod+"."+fs[0])
			f.bind(local, 0, false, false, mod+"."+fs[0])
		}
		f.addImport(m[2], mod, "", names, quals...)
		f.blank(m[0], m[1])
	}
}
