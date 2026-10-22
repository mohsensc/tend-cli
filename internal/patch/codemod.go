package patch

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
)

// tree is the worktree as the codemods see it: files read once, written through.
type tree struct {
	wt    string
	files map[string][]byte
	idx   core.ScanIndex
}

// read returns the file's bytes, nil when it does not exist.
func (t *tree) read(f string) []byte {
	if b, ok := t.files[f]; ok {
		return b
	}
	b, err := os.ReadFile(filepath.Join(t.wt, filepath.FromSlash(f)))
	if err != nil {
		b = nil
	}
	if t.files == nil {
		t.files = map[string][]byte{}
	}
	t.files[f] = b
	return b
}

func (t *tree) write(f string, b []byte) error {
	p := filepath.Join(t.wt, filepath.FromSlash(f))
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, b, fi.Mode().Perm()); err != nil {
		return err
	}
	t.files[f] = b
	return nil
}

// index returns an index of the worktree as it stands after the bump. Edits never
// add or remove lines, so its line numbers hold for the rest of the patch.
func (t *tree) index() (core.ScanIndex, error) {
	if t.idx != nil {
		return t.idx, nil
	}
	idx, err := rescan(t.wt)
	if err != nil {
		return nil, fmt.Errorf("patch: scan: %w", err)
	}
	t.idx = idx
	return idx, nil
}

type span struct {
	start, end int
	text       string
}

// edit is one rewrite: all of its spans land or none do.
type edit struct {
	file  string
	line  int
	rule  string
	spans []span
}

// codemod applies cm to the worktree and returns one Applied line per rule that matched.
func codemod(t *tree, cm *core.Codemod, sites []core.Site) ([]string, error) {
	files := siteFiles(sites)
	var edits []edit
	imp, err := imports(t, cm.Imports)
	if err != nil {
		return nil, err
	}
	edits = append(edits, imp...)
	edits = append(edits, renames(t, cm.Renames, sites)...)
	edits = append(edits, paths(t, cm.Paths, files)...)
	fld, err := fields(t, cm.Fields, files)
	if err != nil {
		return nil, err
	}
	edits = append(edits, fld...)
	edits = append(edits, text(t, cm.Text, files)...)

	kept := disjoint(edits)
	if err := splice(t, kept); err != nil {
		return nil, fmt.Errorf("patch: %w", err)
	}
	return report(kept), nil
}

// imports rewrites import specifiers equal to an old path or below it.
func imports(t *tree, m map[string]string) ([]edit, error) {
	if len(m) == 0 {
		return nil, nil
	}
	idx, err := t.index()
	if err != nil {
		return nil, err
	}
	var res []edit
	for _, old := range keys(m) {
		repl := m[old]
		rule := fmt.Sprintf("rewrote import %s -> %s", old, repl)
		for _, imp := range idx.Imports(old) {
			if !under(imp.Package, old) || under(imp.Package, repl) {
				continue
			}
			src := t.read(imp.File)
			start, end, ok := lineSpan(src, imp.Line)
			if !ok {
				continue
			}
			hits := find(src[start:end], imp.Package, isPath)
			if len(hits) == 0 {
				continue
			}
			at := start + hits[0]
			res = append(res, edit{imp.File, imp.Line, rule, []span{{at, at + len(old), repl}}})
		}
	}
	return res, nil
}

func under(pkg, root string) bool {
	return pkg == root || strings.HasPrefix(pkg, root+"/")
}

// renames rewrites member chains whose tail after the root binding matches an old chain.
// The longest old chain wins when several match.
func renames(t *tree, m map[string]string, sites []core.Site) []edit {
	if len(m) == 0 {
		return nil
	}
	olds := keys(m)
	slices.SortStableFunc(olds, func(a, b string) int {
		return strings.Count(b, ".") - strings.Count(a, ".")
	})
	var res []edit
	for _, s := range sites {
		if len(s.Chain) < 2 {
			continue
		}
		tail := s.Chain[1:]
		for _, old := range olds {
			segs := strings.Split(old, ".")
			if len(segs) > len(tail) || !slices.Equal(tail[len(tail)-len(segs):], segs) {
				continue
			}
			if e, ok := rename(t, s, len(s.Chain)-len(segs), strings.Split(m[old], ".")); ok {
				e.rule = fmt.Sprintf("renamed %s -> %s", old, m[old])
				res = append(res, e)
			}
			break
		}
	}
	return res
}

// rename rewrites the chain segments from index k on with repl.
func rename(t *tree, s core.Site, k int, repl []string) (edit, bool) {
	src := t.read(s.File)
	segs, seps, ok := locate(src, s.Line, s.Col, s.Chain)
	if !ok || slices.Contains(repl, "") {
		return edit{}, false
	}
	e := edit{file: s.File, line: s.Line}
	if len(repl) == len(s.Chain)-k {
		for i, r := range repl {
			sp := segs[k+i]
			if string(src[sp.start:sp.end]) != r {
				e.spans = append(e.spans, span{sp.start, sp.end, r})
			}
		}
		return e, len(e.spans) > 0
	}
	// Different lengths: only a run joined by one plain separator on one line
	// can be rewritten; joining lines would move every site below it.
	sep := seps[k]
	for _, x := range seps[k:] {
		if x != sep || x == "[" {
			return edit{}, false
		}
	}
	start, end := segs[k].start, segs[len(segs)-1].end
	if bytes.IndexByte(src[start:end], '\n') >= 0 {
		return edit{}, false
	}
	e.spans = []span{{start, end, strings.Join(repl, sep)}}
	return e, true
}

// locate finds chain in src starting at line, nearest to col, and returns the byte span
// of every segment and the separator in front of each ("" for the root).
// Chains may continue over following lines.
func locate(src []byte, line, col int, chain []string) ([]span, []string, bool) {
	start, end, ok := lineSpan(src, line)
	if !ok {
		return nil, nil, false
	}
	for _, i := range nearest(find(src[start:end], chain[0], isWord), col) {
		if segs, seps, ok := walk(src, start+i, chain); ok {
			return segs, seps, true
		}
	}
	return nil, nil, false
}

func walk(src []byte, at int, chain []string) ([]span, []string, bool) {
	segs := []span{{at, at + len(chain[0]), ""}}
	seps := []string{""}
	pos := at + len(chain[0])
	for _, seg := range chain[1:] {
		pos = skipSpace(src, pos)
		sep := ""
		for _, s := range []string{"?.", ".", "::", "->", "["} {
			if bytes.HasPrefix(src[pos:], []byte(s)) {
				sep = s
				break
			}
		}
		if sep == "" {
			return nil, nil, false
		}
		pos = skipSpace(src, pos+len(sep))
		if sep == "[" {
			if pos >= len(src) || !strings.ContainsRune("'\"`", rune(src[pos])) {
				return nil, nil, false
			}
			q := src[pos]
			pos++
			if !bytes.HasPrefix(src[pos:], []byte(seg+string(q))) {
				return nil, nil, false
			}
			segs = append(segs, span{pos, pos + len(seg), ""})
			pos = skipSpace(src, pos+len(seg)+1)
			if pos >= len(src) || src[pos] != ']' {
				return nil, nil, false
			}
			pos++
		} else {
			if !bytes.HasPrefix(src[pos:], []byte(seg)) || (pos+len(seg) < len(src) && isWord(src[pos+len(seg)])) {
				return nil, nil, false
			}
			segs = append(segs, span{pos, pos + len(seg), ""})
			pos += len(seg)
		}
		seps = append(seps, sep)
	}
	return segs, seps, true
}

// paths rewrites URL literals whose path matches an old template.
func paths(t *tree, m map[string]string, files []string) []edit {
	if len(m) == 0 {
		return nil
	}
	olds := keys(m)
	var res []edit
	for _, f := range files {
		eachLine(t.read(f), func(n int, ln []byte, start int) {
			for _, lit := range literals(ln) {
				from, to := urlPath(ln[lit.start:lit.end])
				if from == to {
					continue
				}
				at := start + lit.start + from
				p := string(ln[lit.start+from : lit.start+to])
				for _, old := range olds {
					if repl, ok := fill(old, m[old], p); ok {
						rule := fmt.Sprintf("rewrote path %s -> %s", old, m[old])
						res = append(res, edit{f, n, rule, []span{{at, at + len(p), repl}}})
						break
					}
				}
			}
		})
	}
	return res
}

// literals returns the content spans of quoted strings on one line.
func literals(ln []byte) []span {
	var res []span
	for i := 0; i < len(ln); i++ {
		q := ln[i]
		if q != '"' && q != '\'' && q != '`' {
			continue
		}
		j := i + 1
		for j < len(ln) && ln[j] != q {
			if ln[j] == '\\' {
				j++
			}
			j++
		}
		if j >= len(ln) {
			break
		}
		res = append(res, span{i + 1, j, ""})
		i = j
	}
	return res
}

// urlPath returns the offsets of the path in a URL literal: after scheme and host,
// before query and fragment. from == to when the literal is not a URL or a path.
func urlPath(lit []byte) (int, int) {
	from := 0
	if i := bytes.Index(lit, []byte("://")); i > 0 && bytes.HasPrefix(lit, []byte("http")) {
		j := bytes.IndexByte(lit[i+3:], '/')
		if j < 0 {
			return 0, 0
		}
		from = i + 3 + j
	}
	if from >= len(lit) || lit[from] != '/' {
		return 0, 0
	}
	to := len(lit)
	if i := bytes.IndexAny(lit[from:], "?#"); i >= 0 {
		to = from + i
	}
	return from, to
}

// fill matches path p against template old and renders template repl with the same
// parameters: "/v1/orders/{id}" matches "/v1/orders/42" and yields "/v2/orders/42".
func fill(old, repl, p string) (string, bool) {
	ts, ps := strings.Split(old, "/"), strings.Split(p, "/")
	if len(ts) != len(ps) {
		return "", false
	}
	vals := map[string]string{}
	for i, seg := range ts {
		if isParam(seg) {
			if ps[i] == "" {
				return "", false
			}
			vals[seg] = ps[i]
			continue
		}
		if seg != ps[i] {
			return "", false
		}
	}
	out := strings.Split(repl, "/")
	for i, seg := range out {
		if !isParam(seg) {
			continue
		}
		v, ok := vals[seg]
		if !ok {
			return "", false
		}
		out[i] = v
	}
	res := strings.Join(out, "/")
	return res, res != p
}

func isParam(seg string) bool {
	return len(seg) > 2 && seg[0] == '{' && seg[len(seg)-1] == '}'
}

// fields renames keys, attributes and string literals of a field in the site files.
// Dropping a field is not mechanical and is left to the model.
func fields(t *tree, m map[string]string, files []string) ([]edit, error) {
	if len(m) == 0 || len(files) == 0 {
		return nil, nil
	}
	idx, err := t.index()
	if err != nil {
		return nil, err
	}
	var res []edit
	for _, old := range keys(m) {
		repl := m[old]
		if repl == "" {
			continue
		}
		rule := fmt.Sprintf("renamed field %s -> %s", old, repl)
		for _, s := range idx.Fields(old, files) {
			src := t.read(s.File)
			start, end, ok := lineSpan(src, s.Line)
			if !ok {
				continue
			}
			hits := nearest(find(src[start:end], old, isWord), s.Col)
			if len(hits) == 0 {
				continue
			}
			at := start + hits[0]
			res = append(res, edit{s.File, s.Line, rule, []span{{at, at + len(old), repl}}})
		}
	}
	return res, nil
}

// text substitutes literal text line by line in the site files.
func text(t *tree, m map[string]string, files []string) []edit {
	var res []edit
	for _, old := range keys(m) {
		repl := m[old]
		if old == "" || strings.Contains(old, "\n") || strings.Contains(repl, "\n") {
			continue
		}
		rule := fmt.Sprintf("replaced %s -> %s", old, repl)
		for _, f := range files {
			eachLine(t.read(f), func(n int, ln []byte, start int) {
				// Already rewritten: the replacement contains the old text.
				if strings.Contains(repl, old) && bytes.Contains(ln, []byte(repl)) {
					return
				}
				for i := 0; ; {
					j := bytes.Index(ln[i:], []byte(old))
					if j < 0 {
						return
					}
					at := start + i + j
					res = append(res, edit{f, n, rule, []span{{at, at + len(old), repl}}})
					i += j + len(old)
				}
			})
		}
	}
	return res
}

// disjoint keeps edits in order, dropping any that overlaps an edit already kept.
func disjoint(edits []edit) []edit {
	taken := map[string][]span{}
	var res []edit
	for _, e := range edits {
		if overlaps(taken[e.file], e.spans) {
			continue
		}
		taken[e.file] = append(taken[e.file], e.spans...)
		res = append(res, e)
	}
	return res
}

func overlaps(have, spans []span) bool {
	for _, a := range have {
		for _, b := range spans {
			if a.start < b.end && b.start < a.end {
				return true
			}
		}
	}
	return false
}

// splice writes every file once, applying its spans from the end so offsets hold.
func splice(t *tree, edits []edit) error {
	byFile := map[string][]span{}
	for _, e := range edits {
		byFile[e.file] = append(byFile[e.file], e.spans...)
	}
	for _, f := range keys(byFile) {
		spans := byFile[f]
		slices.SortFunc(spans, func(a, b span) int { return b.start - a.start })
		src := slices.Clone(t.read(f))
		for _, s := range spans {
			src = slices.Concat(src[:s.start], []byte(s.text), src[s.end:])
		}
		if err := t.write(f, src); err != nil {
			return err
		}
	}
	return nil
}

// report renders one line per rule: "renamed a.b -> a.c at src/x.js:4, :9, src/y.js:2".
func report(edits []edit) []string {
	type pos struct {
		file string
		line int
	}
	var rules []string
	at := map[string][]pos{}
	for _, e := range edits {
		if _, ok := at[e.rule]; !ok {
			rules = append(rules, e.rule)
		}
		at[e.rule] = append(at[e.rule], pos{e.file, e.line})
	}
	var res []string
	for _, r := range rules {
		ps := at[r]
		slices.SortFunc(ps, func(a, b pos) int {
			if c := strings.Compare(a.file, b.file); c != 0 {
				return c
			}
			return a.line - b.line
		})
		ps = slices.Compact(ps)
		var locs []string
		for i, p := range ps {
			if i > 0 && ps[i-1].file == p.file {
				locs = append(locs, fmt.Sprintf(":%d", p.line))
				continue
			}
			locs = append(locs, fmt.Sprintf("%s:%d", p.file, p.line))
		}
		res = append(res, r+" at "+strings.Join(locs, ", "))
	}
	return res
}

// lineSpan returns the byte range of the 1-based line n, without its newline.
func lineSpan(src []byte, n int) (int, int, bool) {
	if n < 1 {
		return 0, 0, false
	}
	start := 0
	for i := 1; i < n; i++ {
		j := bytes.IndexByte(src[start:], '\n')
		if j < 0 {
			return 0, 0, false
		}
		start += j + 1
	}
	if start == len(src) && n > 1 {
		return 0, 0, false
	}
	end := len(src)
	if j := bytes.IndexByte(src[start:], '\n'); j >= 0 {
		end = start + j
	}
	return start, end, true
}

// eachLine calls fn with every 1-based line number, the line without its newline and its offset.
func eachLine(src []byte, fn func(n int, ln []byte, start int)) {
	for n, start := 1, 0; start < len(src); n++ {
		end := len(src)
		if j := bytes.IndexByte(src[start:], '\n'); j >= 0 {
			end = start + j
		}
		fn(n, src[start:end], start)
		start = end + 1
	}
}

// find returns the offsets of tok in ln that are not glued to neighbors for which in is true.
func find(ln []byte, tok string, in func(byte) bool) []int {
	var res []int
	for i := 0; tok != ""; {
		j := bytes.Index(ln[i:], []byte(tok))
		if j < 0 {
			break
		}
		at := i + j
		end := at + len(tok)
		if (at == 0 || !in(ln[at-1])) && (end == len(ln) || !in(ln[end])) {
			res = append(res, at)
		}
		i = at + 1
	}
	return res
}

// nearest orders 0-based offsets by distance to the 1-based column col.
func nearest(offs []int, col int) []int {
	dist := func(i int) int {
		d := i + 1 - col
		if d < 0 {
			return -d
		}
		return d
	}
	slices.SortStableFunc(offs, func(a, b int) int { return dist(a) - dist(b) })
	return offs
}

func skipSpace(src []byte, i int) int {
	for i < len(src) && strings.ContainsRune(" \t\r\n", rune(src[i])) {
		i++
	}
	return i
}

func isWord(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isPath(c byte) bool {
	return isWord(c) || strings.IndexByte("/.-@~", c) >= 0
}

func siteFiles(sites []core.Site) []string {
	var res []string
	for _, s := range sites {
		res = append(res, s.File)
	}
	return uniq(res)
}

func uniq(s []string) []string {
	res := slices.Clone(s)
	res = slices.DeleteFunc(res, func(x string) bool { return x == "" })
	slices.Sort(res)
	return slices.Compact(res)
}

func keys[V any](m map[string]V) []string {
	res := make([]string, 0, len(m))
	for k := range m {
		res = append(res, k)
	}
	slices.Sort(res)
	return res
}
