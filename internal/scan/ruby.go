package scan

import "regexp"

// rubyLang roots chains at constants rather than imports: a gem is required
// by one name and used through another (aws-sdk-s3 is Aws::S3), so every
// constant chain is a site and Uses matches the constant names the rubygems
// ecosystem gives for a gem.
var rubyLang = &scanner{
	lang:    "ruby",
	exts:    []string{".rb", ".rake"},
	syn:     rubySyntax,
	seps:    []string{"::", "&.", "."},
	sep:     "::",
	ivar:    true,
	imports: rubyImports,
	implicit: func(f *file, at int, name string) []string {
		if isUpper(name[0]) {
			return []string{name}
		}
		return nil
	},
}

var rubyRequireRe = regexp.MustCompile(`\brequire(?:[ \t]*\([ \t]*|[ \t]+)['"]`)

// rubyImports reads require 'x'. require_relative is the project's own.
func rubyImports(f *file) {
	for _, m := range rubyRequireRe.FindAllIndex(f.code, -1) {
		if l, end, ok := f.litAfter(m[1] - 1); ok {
			f.addImport(l.Off, l.Text, "", nil)
			f.blank(m[0], end)
		}
	}
}
