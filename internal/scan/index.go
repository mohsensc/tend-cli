package scan

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/gitx"
)

// maxSize keeps generated bundles and data dumps out of the index.
const maxSize = 2 << 20

// Index answers questions about the scanned files. It implements core.ScanIndex.
type Index struct {
	files []*file
	uses  map[key][]ref // sites by every prefix of their qualified names
	imps  map[key][]ref // imports likewise
	notes []string
}

type key struct {
	lang, name string
	fold       bool // name is lower-cased
}

type ref struct{ file, n int }

var (
	_ core.ScanIndex   = (*Index)(nil)
	_ core.TestReacher = (*Index)(nil)
)

// Build scans the source files under o.Root: what git tracks or would track,
// or every file when git is not there. Files it cannot use are skipped with a
// note, never an error.
func Build(o Options) (*Index, error) {
	files, err := list(o)
	if err != nil {
		return nil, err
	}
	files = slices.Compact(slices.Sorted(slices.Values(files)))
	x := &Index{uses: map[key][]ref{}, imps: map[key][]ref{}}
	for _, p := range files {
		if f := x.load(o.Root, p); f != nil {
			x.add(f)
		}
	}
	return x, nil
}

// With is the index after the files in changed were edited, added or
// deleted under root: those are read again and the rest are kept as they are.
func (x *Index) With(root string, changed []string) *Index {
	redo := map[string]bool{}
	for _, p := range changed {
		if pick(p) != nil && !under(p, skipAlways) {
			redo[p] = true
		}
	}
	if len(redo) == 0 {
		return x
	}
	res := &Index{uses: map[key][]ref{}, imps: map[key][]ref{}}
	for _, n := range x.notes {
		// "a.go: skipped, binary", or "a.go:7:2: expected ..." from the Go parser
		if p, _, _ := strings.Cut(n, ":"); !redo[p] {
			res.notes = append(res.notes, n)
		}
	}
	old := map[string]*file{}
	var paths []string
	for _, f := range x.files {
		old[f.File] = f
		paths = append(paths, f.File)
	}
	for p := range redo {
		paths = append(paths, p)
	}
	for _, p := range slices.Compact(slices.Sorted(slices.Values(paths))) {
		f := old[p]
		if redo[p] {
			f = res.load(root, p)
		}
		if f != nil {
			res.add(f)
		}
	}
	return res
}

// load scans one file, or returns nil when it is not source tend can read,
// with a note saying why when the reason is worth one.
func (x *Index) load(root, p string) *file {
	l := pick(p)
	if l == nil {
		return nil
	}
	src, note := read(filepath.Join(root, filepath.FromSlash(p)))
	if note != "" {
		x.notes = append(x.notes, p+": "+note)
	}
	if src == nil {
		return nil
	}
	f, err := l.scan(p, src)
	switch {
	case err != nil:
		x.notes = append(x.notes, err.Error())
	case strings.HasSuffix(f.Lang, "?"):
		x.notes = append(x.notes, p+": unterminated string or comment; read the rest without strings")
	}
	return f
}

// Lang names the language a file is read as, "" for one tend does not read.
func Lang(p string) string {
	if l := pick(p); l != nil {
		return l.Lang()
	}
	return ""
}

func pick(p string) lang {
	for _, l := range langs {
		if l.Match(p) {
			return l
		}
	}
	return nil
}

// read returns the file's bytes, or nil and why it was left out. A file that
// is gone or not a regular file is left out without a word.
func read(p string) ([]byte, string) {
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, ""
	}
	if fi.Size() > maxSize {
		return nil, "skipped, larger than 2 MB"
	}
	src, err := os.ReadFile(p)
	if err != nil {
		return nil, err.Error()
	}
	if bytes.IndexByte(src[:min(len(src), 8000)], 0) >= 0 {
		return nil, "skipped, binary"
	}
	return src, ""
}

var (
	// never scanned: other people's code, and test fixtures, which the go
	// tool skips for the same reason
	skipAlways = []string{"node_modules", "vendor", ".git", "testdata", ".yarn"}
	// also skipped when there is no git to say what is ignored
	skipWalk = []string{"target", "dist", "build", ".venv", "venv", "__pycache__", "bin", "obj"}
)

// list is what Build scans: the files git lists, or o.Files when the caller
// already listed them, or every file when there is no git, less what skip
// and skipAlways name.
func list(o Options) ([]string, error) {
	skip := append(slices.Clone(o.Skip), skipAlways...)
	files := o.Files
	if files == nil {
		var err error
		if files, err = gitx.Files(context.Background(), o.Root); err != nil {
			if files, err = walkFiles(o.Root, append(skip, skipWalk...)); err != nil {
				return nil, err
			}
		}
	}
	var res []string
	for _, f := range files {
		if !under(f, skip) {
			res = append(res, f)
		}
	}
	return res, nil
}

func under(rel string, dirs []string) bool {
	for _, part := range strings.Split(path.Dir(rel), "/") {
		if slices.Contains(dirs, part) {
			return true
		}
	}
	return false
}

func walkFiles(root string, skip []string) ([]string, error) {
	var res []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && (slices.Contains(skip, d.Name()) || core.NestedRepo(p)) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		res = append(res, filepath.ToSlash(rel))
		return nil
	})
	return res, err
}

func (x *Index) add(f *file) {
	n := len(x.files)
	x.files = append(x.files, f)
	lang := strings.TrimSuffix(f.Lang, "?")
	for k, s := range f.found {
		for _, q := range s.quals {
			for _, kk := range keys(lang, q) {
				x.uses[kk] = appendRef(x.uses[kk], ref{n, k})
			}
		}
	}
	for k, im := range f.imps {
		for _, q := range im.quals {
			for _, kk := range keys(lang, q) {
				x.imps[kk] = appendRef(x.imps[kk], ref{n, k})
			}
		}
	}
}

func appendRef(refs []ref, r ref) []ref {
	if len(refs) > 0 && refs[len(refs)-1] == r {
		return refs
	}
	return append(refs, r)
}

var langNames = []string{"go", "js", "python", "java", "ruby", "php", "csharp", "rust"}

// bounds separates the parts of a qualified name, per language. A name
// matches a qualified name equal to it or starting with it and a boundary.
var bounds = map[string]string{
	"go":     "/",
	"python": ".",
	"java":   ".",
	"ruby":   "::",
	"php":    `\`,
	"csharp": ".",
	"rust":   "::",
}

// caseless are the languages whose names are written in CamelCase while the
// package names that lead to them come in lower case: stripe.net is Stripe,
// guzzlehttp/guzzle is GuzzleHttp, activerecord is ActiveRecord. PHP
// namespaces are case insensitive anyway.
var caseless = map[string]bool{"php": true, "csharp": true, "ruby": true}

// keys lists the keys a qualified name is found by: itself and its prefixes
// at the language's boundaries, and the same in lower case where case does
// not count. A JavaScript specifier is found by itself and its package and
// nothing shorter, since npm names are exact; a Go path is not found by a
// prefix that a major version suffix makes another module.
func keys(lang, q string) []key {
	var names []string
	switch lang {
	case "js":
		names = append(names, q)
		if p := jsPackage(q); p != q {
			names = append(names, p)
		}
	default:
		sep := bounds[lang]
		for i := 0; ; {
			k := strings.Index(q[i:], sep)
			if k < 0 {
				break
			}
			p, rest := q[:i+k], q[i+k+len(sep):]
			major, _, _ := strings.Cut(rest, "/")
			if p != "" && (lang != "go" || !isMajor(major)) {
				names = append(names, p)
			}
			i += k + len(sep)
		}
		names = append(names, q)
	}
	var res []key
	for _, n := range names {
		res = append(res, key{lang: lang, name: n})
		if caseless[lang] {
			res = append(res, key{lang: lang, name: strings.ToLower(n), fold: true})
		}
	}
	return res
}

// query lists the keys a name asked for is looked up by. In the caseless
// languages a CamelCase or namespaced name ignores case, while a plain lower
// case name is an npm or PyPI style package name and must match exactly, so
// that "stripe" finds `require 'stripe'` but not Ruby's Stripe constant. In
// Rust, crate a-b is a_b.
func query(n string) []key {
	var res []key
	for _, lang := range langNames {
		switch {
		case lang == "rust":
			res = append(res, key{lang: lang, name: strings.ReplaceAll(n, "-", "_")})
		case caseless[lang] && (n != strings.ToLower(n) || strings.Contains(n, bounds[lang])):
			res = append(res, key{lang: lang, name: strings.ToLower(n), fold: true})
		default:
			res = append(res, key{lang: lang, name: n})
		}
	}
	return res
}

func (x *Index) lookup(m map[key][]ref, names []string) []ref {
	seen := map[ref]bool{}
	var res []ref
	for _, n := range names {
		if n == "" {
			continue
		}
		for _, k := range query(n) {
			for _, r := range m[k] {
				if !seen[r] {
					seen[r] = true
					res = append(res, r)
				}
			}
		}
	}
	sort.Slice(res, func(a, b int) bool {
		if res[a].file != res[b].file {
			return res[a].file < res[b].file
		}
		return res[a].n < res[b].n
	})
	return res
}

// Uses returns the sites rooted at any of the names, sorted by file, line
// and column. A name matches an import or constant equal to it or below it:
// "github.com/stripe/stripe-go/v79" matches ".../v79/charge", "google.cloud"
// matches "google.cloud.storage", "@aws-sdk/client-s3" matches
// "@aws-sdk/client-s3/dist", but "stripe" never matches "stripe-mock".
// Names carry no ecosystem, so one that npm and PyPI share matches in both.
func (x *Index) Uses(names []string) []core.Site {
	var res []core.Site
	for _, r := range x.lookup(x.uses, names) {
		s := x.files[r.file].found[r.n].Site
		s.Chain = slices.Clone(s.Chain)
		res = append(res, s)
	}
	return res
}

// Imports returns the imports of pkg, or of anything below it, by the rules of Uses.
func (x *Index) Imports(pkg string) []core.Import {
	var res []core.Import
	for _, r := range x.lookup(x.imps, []string{pkg}) {
		im := x.files[r.file].imps[r.n].Import
		im.Names = slices.Clone(im.Names)
		res = append(res, im)
	}
	return res
}

// Fields returns where name is used as a field: a member (.name, ?.name,
// ->name, ::name), a key (name: in an object literal or at the start of a
// line, name= as a keyword argument), a Go struct tag, or a string literal
// equal to name, which covers "name": and ['name'].
func (x *Index) Fields(name string, files []string) []core.Site {
	if name == "" {
		return nil
	}
	var want map[string]bool
	if files != nil {
		want = map[string]bool{}
		for _, f := range files {
			want[f] = true
		}
	}
	var res []core.Site
	for _, f := range x.files {
		if want == nil || want[f.File] {
			res = append(res, f.fields(name)...)
		}
	}
	return res
}

// Endpoints returns the URL literals whose host is one of hosts, and the
// path literals of files that mention one of hosts anywhere. A path takes
// the host mentioned first in its file.
func (x *Index) Endpoints(hosts []string) []core.URLUse {
	want := map[string]bool{}
	for _, h := range hosts {
		want[strings.ToLower(h)] = true
	}
	var res []core.URLUse
	for _, f := range x.files {
		host, looked := "", false
		for _, u := range f.URLs {
			if !strings.HasPrefix(u.URL, "/") {
				if want[u.Host] {
					res = append(res, u)
				}
				continue
			}
			if !looked {
				host, looked = f.mention(hosts), true
			}
			if host != "" {
				u.Host = host
				res = append(res, u)
			}
		}
	}
	return res
}

// URLs lists every URL literal of the scanned files, in file order. A path
// literal carries the host its file mentions first, if any.
func (x *Index) URLs() []core.URLUse {
	var res []core.URLUse
	for _, f := range x.files {
		res = append(res, f.URLs...)
	}
	return res
}

func (x *Index) Files() []string {
	var res []string
	for _, f := range x.files {
		res = append(res, f.File)
	}
	return res
}

// Reached reports whether a test reaches the file p: a JavaScript or
// TypeScript test names a relative path that resolves to it, a Python test
// imports the module it is, or a Go test sits in its package's directory.
// In the other languages a test named for p counts, as FooTest.java,
// FooTests.cs, foo_spec.rb or test_foo.rb is for Foo: the name less its test
// marker must be p's base name. A Rust file with a #[test] or #[cfg(test)]
// of its own is reached by it, and a crate's src/ by the tests/ and
// benches/ next to it.
func (x *Index) Reached(p string) bool {
	return slices.ContainsFunc(x.files, func(f *file) bool {
		return reaches(f, p) || f.File == p && f.Lang == "rust" && (bytes.Contains(f.code, []byte("#[test]")) || bytes.Contains(f.code, []byte("#[cfg(test)]")))
	})
}

// Reaches reports whether the test file test reaches the file p, as Reached
// reads it.
func (x *Index) Reaches(test, p string) bool {
	return slices.ContainsFunc(x.files, func(f *file) bool { return f.File == test && reaches(f, p) })
}

func reaches(f *file, p string) bool {
	if f.File == p || !IsTest(f.File) {
		return false
	}
	stem := strings.TrimSuffix(p, path.Ext(p))
	switch strings.TrimSuffix(f.Lang, "?") {
	case "go":
		return path.Dir(f.File) == path.Dir(p)
	case "js":
		for _, l := range f.lits {
			if !strings.HasPrefix(l.Text, ".") {
				continue
			}
			r := path.Join(path.Dir(f.File), l.Text)
			if r == p || r == stem || path.Join(r, "index") == stem {
				return true
			}
		}
	case "python":
		for _, im := range f.imps {
			for _, q := range im.quals {
				m := strings.ReplaceAll(q, ".", "/")
				if stem == m || strings.HasSuffix(stem, "/"+m) || stem == m+"/__init__" || strings.HasSuffix(stem, "/"+m+"/__init__") {
					return true
				}
			}
		}
	case "rust":
		dir := path.Dir(f.File)
		if b := path.Base(dir); b != "tests" && b != "benches" || path.Ext(p) != ".rs" {
			return false
		}
		src := path.Join(path.Dir(dir), "src") + "/"
		return strings.HasPrefix(p, strings.TrimPrefix(src, "./"))
	default:
		return slices.ContainsFunc(subjects(f.File), func(s string) bool { return strings.EqualFold(s, path.Base(stem)) })
	}
	return false
}

var (
	testSuffixes = []string{"Tests", "Test", "IT", "Spec", "_test", "_spec"}
	testPrefixes = []string{"test_", "Test"}
)

func subjects(p string) []string {
	name := strings.TrimSuffix(path.Base(p), path.Ext(p))
	var res []string
	for _, a := range testSuffixes {
		if s, ok := strings.CutSuffix(name, a); ok && s != "" {
			res = append(res, s)
			break
		}
	}
	for _, a := range testPrefixes {
		if s, ok := strings.CutPrefix(name, a); ok && s != "" {
			res = append(res, s)
			break
		}
	}
	return res
}

// Notes lists the files left out or only partly read, and why.
func (x *Index) Notes() []string { return x.notes }

// mention returns the host of hosts that appears first in the file, "" if none does.
func (f *file) mention(hosts []string) string {
	res, at := "", len(f.src)
	for _, h := range hosts {
		if k := bytes.Index(f.src, []byte(strings.ToLower(h))); h != "" && k >= 0 && k < at {
			res, at = strings.ToLower(h), k
		}
	}
	return res
}

var fieldSeps = []string{"?.", "?->", "->", "::", "."}

func (f *file) fields(name string) []core.Site {
	var offs []int
	code := f.code
	for i := 0; ; {
		k := bytes.Index(code[i:], []byte(name))
		if k < 0 {
			break
		}
		at, end := i+k, i+k+len(name)
		i = at + 1
		if at > 0 && isIdent(code[at-1]) || end < len(code) && isIdent(code[end]) {
			continue
		}
		if fieldAt(code, at, end) {
			offs = append(offs, at)
		}
	}
	for _, l := range f.lits {
		if l.Text == name {
			offs = append(offs, l.Off)
		} else if k := tagField(l.Text, name); k >= 0 && strings.TrimSuffix(f.Lang, "?") == "go" {
			offs = append(offs, l.Off+k)
		}
	}
	sort.Ints(offs)
	var res []core.Site
	for _, off := range slices.Compact(offs) {
		line, col := f.pos(off)
		res = append(res, core.Site{
			File:  f.File,
			Line:  line,
			Col:   col,
			Chain: []string{name},
			Call:  next(code, off+len(name)) == '(',
			Text:  f.text(line),
		})
	}
	return res
}

// fieldAt tells whether the name at code[at:end] is a member or a key.
func fieldAt(code []byte, at, end int) bool {
	if sepBefore(code, at, fieldSeps) >= 0 {
		return true
	}
	j := skipSpace(code, end)
	if j >= len(code) {
		return false
	}
	prev := byte(0)
	for k := at - 1; k >= 0; k-- {
		if !isSpace(code[k]) {
			prev = code[k]
			break
		}
	}
	switch code[j] {
	case ':':
		return !hasAt(code, j, "::") && (lineStart(code, at) || strings.IndexByte("{,(;", prev) >= 0)
	case '=':
		return !hasAt(code, j, "==") && !hasAt(code, j, "=>") && (prev == '(' || prev == ',')
	}
	return false
}

// lineStart tells whether only spaces come before at on its line.
func lineStart(code []byte, at int) bool {
	for k := at - 1; k >= 0 && code[k] != '\n'; k-- {
		if code[k] != ' ' && code[k] != '\t' {
			return false
		}
	}
	return true
}

// tagField returns where name sits in a Go struct tag such as
// `json:"name,omitempty" db:"name"`, -1 if it does not.
func tagField(tag, name string) int {
	for _, q := range []string{`:"` + name + `"`, `:"` + name + `,`} {
		if k := strings.Index(tag, q); k >= 0 {
			return k + 2
		}
	}
	return -1
}
