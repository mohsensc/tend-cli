package scan

import (
	"regexp"
	"strings"
)

// rustLang roots chains at use bindings and at any crate path a::b, since a
// crate is in scope without a use. A method call on a value, x.m(), is only
// attributed when x was made by a constructor such as Client::new().
var rustLang = &scanner{
	lang:    "rust",
	exts:    []string{".rs"},
	syn:     rustSyntax,
	seps:    []string{"::", "."},
	sep:     "::",
	imports: rustImports,
	implicit: func(f *file, at int, name string) []string {
		end := identEnd(f.code, at)
		if !hasAt(f.code, skipSpace(f.code, end), "::") || rustLocal[name] {
			return nil
		}
		return []string{name}
	},
}

var rustLocal = map[string]bool{"crate": true, "self": true, "super": true, "Self": true}

var (
	rustUseRe   = regexp.MustCompile(`\buse\s+([^;]+);`)
	rustCrateRe = regexp.MustCompile(`\bextern[ \t]+crate[ \t]+(\w+)(?:[ \t]+as[ \t]+(\w+))?[ \t]*;`)
)

// rustImports reads use trees, `use a::b::{c, d as e, f::*};`, and
// `extern crate a as b;`. Paths into the crate itself are left out.
func rustImports(f *file) {
	code := f.code
	for _, m := range rustUseRe.FindAllSubmatchIndex(code, -1) {
		tree := strings.Join(strings.Fields(string(code[m[2]:m[3]])), " ")
		useTree("", strings.TrimPrefix(tree, "::"), func(p, local string) {
			first, _, _ := strings.Cut(p, "::")
			if rustLocal[first] {
				return
			}
			if local == "*" || local == "_" {
				f.addImport(m[2], p, "", nil)
				return
			}
			f.addImport(m[2], p, local, nil)
			f.bind(local, 0, false, false, p)
		})
		f.blank(m[0], m[1])
	}
	for _, m := range rustCrateRe.FindAllSubmatchIndex(code, -1) {
		name := string(code[m[2]:m[3]])
		local := name
		if m[4] >= 0 {
			local = string(code[m[4]:m[5]])
		}
		f.addImport(m[2], name, local, nil)
		f.bind(local, 0, false, false, name)
		f.blank(m[0], m[1])
	}
}

// useTree walks a use tree and calls add with each full path and the local
// name it binds ("*" for a glob).
func useTree(prefix, s string, add func(p, local string)) {
	s = strings.TrimSpace(s)
	if open := strings.IndexByte(s, '{'); open >= 0 && strings.HasSuffix(s, "}") {
		head := strings.TrimSuffix(strings.TrimSpace(s[:open]), "::")
		p := join(prefix, head)
		for _, part := range splitTop(s[open+1 : len(s)-1]) {
			useTree(p, part, add)
		}
		return
	}
	p, local := s, ""
	if fs := strings.Fields(s); len(fs) == 3 && fs[1] == "as" {
		p, local = fs[0], fs[2]
	}
	if p == "" {
		return
	}
	full := join(prefix, p)
	if p == "self" {
		full = prefix
	}
	if local == "" {
		local = full
		if i := strings.LastIndex(full, "::"); i >= 0 {
			local = full[i+2:]
		}
	}
	add(full, local)
}

func join(prefix, p string) string {
	switch {
	case prefix == "":
		return p
	case p == "":
		return prefix
	}
	return prefix + "::" + p
}

// splitTop splits at the commas outside braces.
func splitTop(s string) []string {
	var res []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
		case ',':
			if depth == 0 {
				res = append(res, s[start:i])
				start = i + 1
			}
		}
	}
	return append(res, s[start:])
}
