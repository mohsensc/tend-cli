package vet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
)

type status int

const (
	none     status = iota // nothing past the import to check
	resolved               // the symbol exists in the new version
	missing                // it does not
	unknown                // could not tell
)

// symbols: does every call into the changed packages exist in the new
// version? The calls the patch rewrote, and the ones it left alone, since the
// package moved under them too. A call tend cannot look into is flagged only
// when the patch wrote it.
func symbols(ctx context.Context, p *pass) ([]core.Finding, string) {
	ch := p.in.Change
	if len(ch.Deps) == 0 {
		return nil, na
	}
	if p.in.Index == nil {
		return nil, "no scan index"
	}
	r := &resolver{p: p, js: map[string]*jsPkg{}, added: map[string]map[int]string{}, docs: map[string]docResult{}}
	var res []core.Finding
	var where []string
	seen := map[string]bool{}
	checked, ok, untouched := 0, 0, 0
	for _, d := range targets(ch) {
		ec := p.in.Ecos[d.Ecosystem]
		if ec == nil {
			continue
		}
		names := ec.Imports(d)
		for _, s := range p.in.Index.Uses(names) {
			key := fmt.Sprintf("%s:%d:%d", s.File, s.Line, s.Col)
			if seen[key] || len(s.Chain) == 0 {
				continue
			}
			seen[key] = true
			st, why, in := r.resolve(ctx, d, names, s)
			touched := r.touched(s)
			if st == none || st == unknown && !touched {
				continue
			}
			checked++
			if !touched {
				untouched++
			}
			if in != "" && !slices.Contains(where, in) {
				where = append(where, in)
			}
			call := "`" + strings.Join(s.Chain, ".") + "`"
			switch st {
			case resolved:
				ok++
				if touched {
					r.declare(names, s)
				}
			case missing:
				res = append(res, finding("symbols", s.File, s.Line, "flagged", "%s: %s", call, why))
			case unknown:
				res = append(res, finding("symbols", s.File, s.Line, "flagged", "%s not verified: %s", call, why))
			}
		}
	}
	if checked == 0 {
		return nil, na
	}
	noun := "changed calls"
	if untouched > 0 {
		noun = "calls"
	}
	p.note = fmt.Sprintf("%d/%d %s resolve", ok, checked, noun)
	if len(where) > 0 {
		p.note += " " + strings.Join(where, ", ")
	}
	sortFindings(res)
	return res, ""
}

// targets are the deps as they are after the change: a swap checks the new package.
func targets(ch *core.Change) []core.Dep {
	var res []core.Dep
	for _, d := range ch.Deps {
		if ch.ToName != "" {
			d.Name, d.Version = ch.ToName, ch.To
		}
		res = append(res, d)
	}
	return res
}

type resolver struct {
	p     *pass
	js    map[string]*jsPkg
	added map[string]map[int]string
	docs  map[string]docResult // go doc answers by package and symbol
}

type docResult struct {
	st  status
	why string
}

func (r *resolver) touched(s core.Site) bool {
	a, ok := r.added[s.File]
	if !ok {
		a = r.p.added(s.File)
		r.added[s.File] = a
	}
	_, ok = a[s.Line]
	return ok
}

// resolve looks the site's call up in the new version and says where it looked.
func (r *resolver) resolve(ctx context.Context, d core.Dep, names []string, s core.Site) (st status, why, in string) {
	switch d.Ecosystem {
	case core.Npm:
		return r.npm(names, s)
	case core.Go:
		st, why = r.golang(ctx, s)
		return st, why, "(go doc)"
	case core.Pip:
		return r.pip(names, s)
	}
	if len(s.Chain) < 2 {
		return none, "", ""
	}
	return unknown, "symbols are not checked for " + d.Ecosystem, ""
}

// importOf finds the import the site's root binds to and returns the package
// and the chain as names inside that package.
func (r *resolver) importOf(names []string, s core.Site) (string, []string, bool) {
	for _, n := range names {
		for _, imp := range r.p.in.Index.Imports(n) {
			if imp.File != s.File {
				continue
			}
			if imp.Binding == s.Chain[0] {
				return imp.Package, s.Chain[1:], true
			}
			if slices.Contains(imp.Names, s.Chain[0]) {
				return imp.Package, s.Chain, true
			}
		}
	}
	return "", nil, false
}

type jsPkg struct {
	label   string // name@version
	text    string // every .d.ts file and the entry file
	exports map[string]bool
	members map[string]status // member answers by "name.m"
	decls   map[string]string // the line declaring "name.m", when found
}

var (
	jsDecl  = regexp.MustCompile(`\bexport\s+(?:declare\s+)?(?:default\s+)?(?:abstract\s+)?(?:async\s+)?(?:const|let|var|function\*?|class|interface|type|enum|namespace|module)\s+([A-Za-z_$][\w$]*)`)
	jsList  = regexp.MustCompile(`\bexport\s+(?:type\s+)?\{([^}]*)\}`)
	cjsProp = regexp.MustCompile(`\b(?:module\.)?exports\.([A-Za-z_$][\w$]*)\s*=`)
	cjsObj  = regexp.MustCompile(`\bmodule\.exports\s*=\s*\{`)
	jsKey   = regexp.MustCompile(`^\s*(?:async\s+|get\s+|set\s+|\*\s*)?['"]?([A-Za-z_$][\w$]*)`)
)

func (r *resolver) npm(names []string, s core.Site) (status, string, string) {
	pkg, chain, ok := r.importOf(names, s)
	if !ok {
		// The call goes through a value the code made, such as an app a
		// factory returned, whose members no export declares.
		return none, "", ""
	}
	if len(chain) == 0 {
		return none, "", ""
	}
	name := npmName(pkg)
	js := r.jsPackage(s.File, name)
	if js == nil {
		return unknown, name + " is not installed", ""
	}
	in := "in " + js.label + " typings"
	if !js.exports[chain[0]] {
		return missing, fmt.Sprintf("`%s` is not exported by %s", chain[0], js.label), in
	}
	if len(chain) == 1 {
		return resolved, "", in
	}
	switch js.member(chain[0], chain[1]) {
	case missing:
		return missing, fmt.Sprintf("`%s` has no member `%s` in %s", chain[0], chain[1], js.label), in
	case unknown:
		return unknown, fmt.Sprintf("no declaration of `%s` to look into in %s", chain[0], js.label), in
	}
	if len(chain) > 2 {
		return unknown, fmt.Sprintf("checked as far as `%s.%s`", chain[0], chain[1]), in
	}
	return resolved, "", in
}

// declare keeps the line of the new typings that declares a call the patch
// wrote, for the question check to show.
func (r *resolver) declare(names []string, s core.Site) {
	pkg, chain, ok := r.importOf(names, s)
	if !ok || len(chain) != 2 {
		return
	}
	js := r.jsPackage(s.File, npmName(pkg))
	if js == nil {
		return
	}
	js.member(chain[0], chain[1])
	decl := js.decls[chain[0]+"."+chain[1]]
	if decl == "" {
		return
	}
	line := fmt.Sprintf("`%s` in %s: %s", strings.Join(chain, "."), js.label, decl)
	if r.p.decls == nil {
		r.p.decls = map[string][]string{}
	}
	if !slices.Contains(r.p.decls[s.File], line) {
		r.p.decls[s.File] = append(r.p.decls[s.File], line)
	}
}

// npmName is the package of an import specifier: "pkg/sub" -> "pkg", "@s/p/x" -> "@s/p".
func npmName(spec string) string {
	parts := strings.Split(spec, "/")
	if strings.HasPrefix(spec, "@") && len(parts) > 1 {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

// jsPackage finds name in the node_modules nearest the file, as node would.
func (r *resolver) jsPackage(file, name string) *jsPkg {
	root := filepath.Clean(r.p.in.Dir)
	dir := filepath.Dir(filepath.Join(root, filepath.FromSlash(file)))
	for {
		cand := filepath.Join(dir, "node_modules", filepath.FromSlash(name))
		if js, ok := r.js[cand]; ok {
			return js
		}
		if fi, err := os.Stat(cand); err == nil && fi.IsDir() {
			js := loadJS(cand, name)
			r.js[cand] = js
			return js
		}
		if dir == root || !strings.HasPrefix(dir, root) {
			return nil
		}
		dir = filepath.Dir(dir)
	}
}

const (
	jsMax   = 16 << 20
	declMax = 300
)

func loadJS(dir, name string) *jsPkg {
	var meta struct{ Version, Main string }
	label := name
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil && json.Unmarshal(b, &meta) == nil && meta.Version != "" {
		label += "@" + meta.Version
	}
	var text strings.Builder
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || text.Len() > jsMax {
			return filepath.SkipDir
		}
		if d.IsDir() && d.Name() == "node_modules" && p != dir {
			return filepath.SkipDir
		}
		n := d.Name()
		if !d.IsDir() && (strings.HasSuffix(n, ".d.ts") || strings.HasSuffix(n, ".d.mts") || strings.HasSuffix(n, ".d.cts")) {
			if b, err := os.ReadFile(p); err == nil {
				text.Write(b)
				text.WriteByte('\n')
			}
		}
		return nil
	})
	if err != nil {
		return nil
	}
	entry := meta.Main
	if entry == "" {
		entry = "index.js"
	}
	for _, e := range []string{entry, entry + ".js", path.Join(entry, "index.js")} {
		if b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(e))); err == nil {
			text.Write(b)
			break
		}
	}
	js := &jsPkg{label: label, text: text.String(), exports: map[string]bool{}}
	for _, m := range jsDecl.FindAllStringSubmatch(js.text, -1) {
		js.exports[m[1]] = true
	}
	for _, m := range cjsProp.FindAllStringSubmatch(js.text, -1) {
		js.exports[m[1]] = true
	}
	for _, m := range jsList.FindAllStringSubmatch(js.text, -1) {
		for _, item := range strings.Split(m[1], ",") {
			f := strings.Fields(item)
			if len(f) > 0 {
				js.exports[f[len(f)-1]] = true
			}
		}
	}
	for _, loc := range cjsObj.FindAllStringIndex(js.text, -1) {
		for _, k := range topKeys(braceBlock(js.text, loc[1]-1)) {
			js.exports[k] = true
		}
	}
	return js
}

// member looks for m inside the declarations of name and inside the type name
// is declared with. Any hit wins, since a package often has both an empty
// runtime object and the typed declaration. unknown means there was no
// declaration body to look in. Answers are kept, since every call site asks
// again and each look scans all of the package's typings.
func (js *jsPkg) member(name, m string) status {
	k := name + "." + m
	if st, ok := js.members[k]; ok {
		return st
	}
	st, decl := js.lookup(name, m)
	if js.members == nil {
		js.members, js.decls = map[string]status{}, map[string]string{}
	}
	js.members[k], js.decls[k] = st, decl
	return st
}

func (js *jsPkg) lookup(name, m string) (status, string) {
	q := regexp.QuoteMeta(name)
	heads := []*regexp.Regexp{
		regexp.MustCompile(`\b(?:namespace|module|class|interface|enum)\s+` + q + `\b[^{;]*\{`),
		regexp.MustCompile(`\b(?:const|let|var)\s+` + q + `\s*(?::\s*)?=?\s*\{`),
		regexp.MustCompile(`\bexports\.` + q + `\s*=\s*\{`),
		regexp.MustCompile(`(?:^|[\s,{])` + q + `\s*:\s*\{`),
	}
	res, decl := js.lookIn(heads, m)
	if res == resolved {
		return res, decl
	}
	typed := regexp.MustCompile(`\b` + q + `\s*\??\s*:\s*(?:[A-Za-z_$][\w$]*\.)*([A-Za-z_$][\w$]*)\s*[;,\n]`)
	if t := typed.FindStringSubmatch(js.text); t != nil {
		head := regexp.MustCompile(`\b(?:class|interface)\s+` + regexp.QuoteMeta(t[1]) + `\b[^{;]*\{`)
		if st, decl := js.lookIn([]*regexp.Regexp{head}, m); st != unknown {
			return st, decl
		}
	}
	return res, ""
}

// lookIn also returns the line that declares m, when it is found.
func (js *jsPkg) lookIn(heads []*regexp.Regexp, m string) (status, string) {
	member := regexp.MustCompile(`(?:^|[\s;{,(])(?:(?:readonly|static|public|async|get|set|declare|export|function|const|let|var|abstract)\s+)*` + regexp.QuoteMeta(m) + `\s*\??\s*[(:<=,;}]`)
	res := unknown
	for _, h := range heads {
		for _, loc := range h.FindAllStringIndex(js.text, -1) {
			block := braceBlock(js.text, loc[1]-1)
			if at := member.FindStringIndex(block); at != nil {
				start := strings.LastIndexByte(block[:at[1]], '\n') + 1
				line, _, _ := strings.Cut(block[start:], "\n")
				return resolved, core.Clip(strings.TrimSpace(line), declMax)
			}
			if strings.Contains(js.text[loc[0]:loc[1]], "extends") {
				continue
			}
			res = missing
		}
	}
	return res, ""
}

// braceBlock returns what is between the brace at open and its match.
func braceBlock(s string, open int) string {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[open+1 : i]
			}
		}
	}
	return s[open+1:]
}

// topKeys lists the keys of an object literal body.
func topKeys(body string) []string {
	var res []string
	depth, start := 0, 0
	for i := 0; i <= len(body); i++ {
		if i < len(body) {
			switch body[i] {
			case '{', '(', '[':
				depth++
				continue
			case '}', ')', ']':
				depth--
				continue
			case ',':
				if depth != 0 {
					continue
				}
			default:
				continue
			}
		}
		if m := jsKey.FindStringSubmatch(body[start:i]); m != nil {
			res = append(res, m[1])
		}
		start = i + 1
	}
	return res
}

var goDocTimeout = 20 * time.Second

func (r *resolver) golang(ctx context.Context, s core.Site) (status, string) {
	if len(s.Chain) < 2 {
		return none, ""
	}
	pkg := goImports(filepath.Join(r.p.in.Dir, filepath.FromSlash(s.File)))[s.Chain[0]]
	if pkg == "" {
		return unknown, "import not found"
	}
	if _, err := exec.LookPath("go"); err != nil {
		return unknown, "go not found"
	}
	sym := s.Chain[1]
	if len(s.Chain) > 2 {
		sym += "." + s.Chain[2]
	}
	k := pkg + " " + sym
	res, ok := r.docs[k]
	if !ok {
		res.st, res.why = goDoc(ctx, r.p.in.Dir, pkg, sym)
		r.docs[k] = res
	}
	if res.st == resolved && len(s.Chain) > 3 {
		return unknown, fmt.Sprintf("checked as far as `%s`", sym)
	}
	return res.st, res.why
}

// goDoc asks go doc whether pkg has sym, from the worktree's module. go.mod is
// read only: a change go doc made to it would be committed without being vetted.
func goDoc(ctx context.Context, dir, pkg, sym string) (status, string) {
	ctx, cancel := context.WithTimeout(ctx, goDocTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "doc", pkg, sym)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=readonly")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return resolved, ""
	}
	msg := strings.TrimSpace(string(out))
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if strings.Contains(msg, "no symbol") || strings.Contains(msg, "no method or field") {
		return missing, fmt.Sprintf("`%s` is not in %s", sym, pkg)
	}
	if msg == "" {
		msg = err.Error()
	}
	return unknown, strings.TrimPrefix(msg, "doc: ")
}

var goMajor = regexp.MustCompile(`^v[0-9]+$`)

// goImports maps the names a Go file binds its imports to onto import paths.
// Unaliased imports are bound to the package name, guessed from the path the
// way most modules name their packages.
func goImports(file string) map[string]string {
	res := map[string]string{}
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
	if err != nil {
		return res
	}
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		if imp.Name != nil {
			res[imp.Name.Name] = p
			continue
		}
		parts := strings.Split(p, "/")
		name := parts[len(parts)-1]
		if goMajor.MatchString(name) && len(parts) > 1 {
			name = parts[len(parts)-2]
		}
		if i := strings.Index(name, ".v"); i > 0 {
			name = name[:i]
		}
		name = strings.TrimSuffix(strings.TrimPrefix(name, "go-"), "-go")
		name = strings.ReplaceAll(name, "-", "")
		res[name] = p
	}
	return res
}

func (r *resolver) pip(names []string, s core.Site) (status, string, string) {
	mod, chain, ok := r.importOf(names, s)
	if !ok {
		return none, "", ""
	}
	if len(chain) == 0 {
		return none, "", ""
	}
	src := r.pyModule(mod)
	if src == nil {
		return unknown, mod + " is not installed in .venv", ""
	}
	st, why := r.pyLookup(mod, src, chain)
	return st, why, "in " + mod + " as installed"
}

func (r *resolver) pyLookup(mod string, src []byte, chain []string) (status, string) {
	if !pyDefines(src, chain[0]) {
		if r.pyModule(mod+"."+chain[0]) != nil {
			if len(chain) > 1 {
				return unknown, fmt.Sprintf("checked as far as module `%s.%s`", mod, chain[0])
			}
			return resolved, ""
		}
		if bytes.Contains(src, []byte("import *")) {
			return unknown, fmt.Sprintf("%s re-exports with *", mod)
		}
		return missing, fmt.Sprintf("`%s` is not defined in %s", chain[0], mod)
	}
	if len(chain) == 1 {
		return resolved, ""
	}
	switch pyMember(src, chain[0], chain[1]) {
	case missing:
		return missing, fmt.Sprintf("`%s` has no member `%s` in %s", chain[0], chain[1], mod)
	case unknown:
		return unknown, fmt.Sprintf("`%s.%s` may be inherited or imported", chain[0], chain[1])
	}
	if len(chain) > 2 {
		return unknown, fmt.Sprintf("checked as far as `%s.%s`", chain[0], chain[1])
	}
	return resolved, ""
}

// pyModule reads a module's file from the project's virtualenv, nil when absent.
func (r *resolver) pyModule(mod string) []byte {
	parts := strings.Split(mod, ".")
	var sites []string
	for _, venv := range []string{".venv", "venv"} {
		base := filepath.Join(r.p.in.Dir, venv)
		found, _ := filepath.Glob(filepath.Join(base, "lib", "python*", "site-packages"))
		sites = append(sites, found...)
		sites = append(sites, filepath.Join(base, "Lib", "site-packages"))
	}
	for _, sp := range sites {
		dir := filepath.Join(append([]string{sp}, parts...)...)
		for _, p := range []string{filepath.Join(dir, "__init__.py"), dir + ".py"} {
			if b, err := os.ReadFile(p); err == nil {
				return b
			}
		}
	}
	return nil
}

func pyDefines(src []byte, name string) bool {
	q := regexp.QuoteMeta(name)
	re := regexp.MustCompile(`(?m)^(?:async\s+)?(?:def|class)\s+` + q + `\b|^` + q + `\s*(?::[^=\n]*)?=|^from\s+\S+\s+import\s+(?:\([^)]*|[^\n#]*)\b` + q + `\b|^import\s+[^\n#]*\bas\s+` + q + `\b`)
	return re.Match(src)
}

// pyMember looks for m in the body of class cls. A class with bases may
// inherit it, so a miss there is unknown rather than missing.
func pyMember(src []byte, cls, m string) status {
	head := regexp.MustCompile(`(?m)^class\s+` + regexp.QuoteMeta(cls) + `\b(\([^)]*\))?`)
	loc := head.FindSubmatchIndex(src)
	if loc == nil {
		return unknown
	}
	member := regexp.MustCompile(`^\s+(?:async\s+)?def\s+` + regexp.QuoteMeta(m) + `\b|^\s+` + regexp.QuoteMeta(m) + `\s*(?::[^=\n]*)?=`)
	lines := strings.Split(string(src[loc[1]:]), "\n")
	for _, l := range lines[1:] {
		if l != "" && l[0] != ' ' && l[0] != '\t' && l[0] != '#' {
			break
		}
		if member.MatchString(l) {
			return resolved
		}
	}
	if loc[2] >= 0 {
		if bases := strings.Trim(string(src[loc[2]:loc[3]]), "() "); bases != "" && bases != "object" {
			return unknown
		}
	}
	return missing
}
