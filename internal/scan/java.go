package scan

import (
	"regexp"
	"strings"
)

// javaLang reads Java and Kotlin, which import alike.
var javaLang = &scanner{
	lang:     "java",
	exts:     []string{".java", ".kt", ".kts"},
	syn:      javaSyntax,
	seps:     []string{"?.", "::", "."},
	sep:      ".",
	decorate: true,
	imports:  javaImports,
}

var javaImportRe = regexp.MustCompile(`(?m)^[ \t]*import[ \t]+(static[ \t]+)?([\w.]+?)(\.\*)?(?:[ \t]+as[ \t]+(\w+))?[ \t]*;?[ \t]*\r?$`)

// javaImports reads `import a.b.C;`, `import a.b.*;`, `import static a.b.C.m;`
// and Kotlin's `import a.b.C as D`. A wildcard binds nothing: which names it
// brings in is not knowable from the file.
func javaImports(f *file) {
	code := f.code
	for _, m := range javaImportRe.FindAllSubmatchIndex(code, -1) {
		name := string(code[m[4]:m[5]])
		if m[6] >= 0 {
			f.addImport(m[4], name+".*", "", nil)
			f.blank(m[0], m[1])
			continue
		}
		binding := name[strings.LastIndexByte(name, '.')+1:]
		if m[8] >= 0 {
			binding = string(code[m[8]:m[9]])
		}
		f.addImport(m[4], name, binding, nil)
		f.bind(binding, 0, false, false, name)
		f.blank(m[0], m[1])
	}
}
