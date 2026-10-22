package scan

import (
	"regexp"
	"strings"
)

var csLang = &scanner{
	lang:     "csharp",
	exts:     []string{".cs"},
	syn:      csSyntax,
	seps:     []string{"?.", "."},
	sep:      ".",
	imports:  csImports,
	implicit: csImplicit,
}

var csFramework = map[string]bool{
	"Activator": true, "ArgumentException": true, "ArgumentNullException": true, "Array": true,
	"CancellationTokenSource": true, "Console": true, "Convert": true, "DateTime": true,
	"DateTimeOffset": true, "Directory": true, "Encoding": true, "Enumerable": true,
	"Environment": true, "Exception": true, "File": true, "Guid": true, "HttpClient": true,
	"HttpRequestMessage": true, "InvalidOperationException": true, "JsonSerializer": true,
	"Math": true, "Path": true, "Regex": true, "Stopwatch": true, "String": true,
	"StringBuilder": true, "Task": true, "Thread": true, "TimeSpan": true, "Uri": true,
}

var (
	csUsingRe = regexp.MustCompile(`(?m)^[ \t]*(?:global[ \t]+)?using[ \t]+(static[ \t]+)?(?:(\w+)[ \t]*=[ \t]*)?([\w.]+)[ \t]*;`)
	csDeclRe  = regexp.MustCompile(`\b(?:class|struct|interface|enum|record)[ \t]+(\w+)`)
)

// csImports reads `using A.B;`, `using X = A.B.C;`, `using static A.B;` and
// their global forms. A namespace using roots qualified names at its first
// segment and opens the namespace for csImplicit.
func csImports(f *file) {
	code := f.code
	for _, m := range csUsingRe.FindAllSubmatchIndex(code, -1) {
		ns := string(code[m[6]:m[7]])
		switch {
		case m[4] >= 0:
			alias := string(code[m[4]:m[5]])
			f.addImport(m[6], ns, alias, nil)
			f.bind(alias, 0, false, false, ns)
		case m[2] >= 0:
			f.addImport(m[6], ns, "", nil)
		default:
			first, _, _ := strings.Cut(ns, ".")
			f.addImport(m[6], ns, "", nil)
			f.bind(first, 0, false, false, first)
			if first != "System" && first != "Microsoft" {
				f.open = append(f.open, ns)
			}
		}
		f.blank(m[0], m[1])
	}
	for _, m := range csDeclRe.FindAllSubmatchIndex(code, -1) {
		f.decl[string(code[m[2]:m[3]])] = true
	}
}

// csImplicit attributes `new C(` and `C.m` to the namespaces the file opens,
// for a type C the file does not declare. Which namespace C lives in needs
// type information, so every open namespace gets the site. System and
// Microsoft are never opened, and the framework types nearly every file
// touches are never attributed.
func csImplicit(f *file, at int, name string) []string {
	if len(f.open) == 0 || !isUpper(name[0]) || f.decl[name] || csFramework[name] {
		return nil
	}
	end := identEnd(f.code, at)
	made := wordBefore(f.code, at) == "new" && next(f.code, end) == '('
	if !made && next(f.code, end) != '.' {
		return nil
	}
	res := make([]string, len(f.open))
	for k, ns := range f.open {
		res[k] = ns + "." + name
	}
	return res
}
