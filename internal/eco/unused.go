package eco

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
)

// pruner is an ecosystem whose dependencies tend can prove unused: its code
// is read exactly enough, and it knows what besides code may use a package.
type pruner interface {
	core.Remover
	// needs says why the project needs d although no code imports it, or
	// why tend cannot be sure it does not. Both are "" when nothing uses d.
	needs(u *usage, d core.Dep) (need, doubt string)
}

var (
	_ pruner = npm{}
	_ pruner = gomod{}
	_ pruner = python{}
)

// Unused sorts the direct dependencies no code imports into those nothing in
// the project uses, which the first PR removes, and those that only look
// unused, which it names and leaves alone, each with why. keep holds a
// dependency back from both, as an ignore entry in tend.yml does.
func Unused(inv *core.Inventory, keep func(core.Dep) bool) ([]core.Dep, []core.Aside, error) {
	if inv.Index == nil {
		return nil, nil, nil
	}
	files, err := Files(inv.Root)
	if err != nil {
		return nil, nil, err
	}
	u := &usage{root: inv.Root, idx: inv.Index, deps: inv.Deps, files: files, facts: map[string]*npmFacts{}}
	var gone []core.Dep
	var kept []core.Aside
	seen := map[string]bool{}
	for _, d := range inv.Deps {
		e := ByName(d.Ecosystem)
		p, ok := e.(pruner)
		at := d.Ecosystem + "/" + d.Name + " " + d.Manifest.Path
		if !ok || !d.Direct || seen[at] || keep(d) || u.imported(e.Imports(d)...) {
			continue
		}
		seen[at] = true
		switch need, doubt := p.needs(u, d); {
		case need != "":
		case doubt != "":
			kept = append(kept, core.Aside{Dep: d, Why: doubt})
		default:
			gone = append(gone, d)
		}
	}
	return gone, kept, nil
}

// usage is what a repository says about the packages it uses besides its
// imports: the text of every file that is neither documentation nor a
// lockfile, lowercased, with each line that declares a dependency blanked,
// so a declaration is never taken for a use.
type usage struct {
	root   string
	idx    core.ScanIndex
	deps   []core.Dep
	files  []string
	text   map[string][]byte // read on first use
	order  []string          // the files in text, sorted
	ts     *bool
	facts  map[string]*npmFacts           // by lockfile
	graphs map[string]map[string][]string // go mod graph, by go.mod
	pyCons map[string]bool                // requirements files read with -c
}

func (u *usage) imported(names ...string) bool {
	for _, n := range names {
		if n != "" && len(u.idx.Imports(n)) > 0 {
			return true
		}
	}
	return false
}

// declared reports whether a manifest names the package directly.
func (u *usage) declared(eco, name string) bool {
	return slices.ContainsFunc(u.deps, func(d core.Dep) bool { return d.Direct && d.Ecosystem == eco && d.Name == name })
}

// has reports whether the project depends on the package at all, directly
// or through another.
func (u *usage) has(eco, name string) bool {
	return slices.ContainsFunc(u.deps, func(d core.Dep) bool { return d.Ecosystem == eco && d.Name == name })
}

// mention returns where a file names one of words as a whole name, as
// file:line, or "" when none does.
func (u *usage) mention(words ...string) string {
	u.load()
	for _, f := range u.order {
		b := u.text[f]
		for _, w := range words {
			if i := named(b, strings.ToLower(w)); i >= 0 {
				return fmt.Sprintf("%s:%d", f, lineOf(b, i))
			}
		}
	}
	return ""
}

// built returns where code builds a name from prefix and a value, as
// `@fontsource/${font}` or 'gulp-' + name do, as file:line; "" when none does.
func (u *usage) built(prefix string) string {
	u.load()
	var forms [][]byte
	forms = append(forms, []byte("`"+prefix+"${"))
	for _, q := range []string{"'", `"`} {
		forms = append(forms, []byte(q+prefix+q+" +"), []byte(q+prefix+q+"+"))
	}
	for _, f := range u.order {
		b := u.text[f]
		for _, form := range forms {
			if i := bytes.Index(b, form); i >= 0 {
				return fmt.Sprintf("%s:%d", f, lineOf(b, i))
			}
		}
	}
	return ""
}

// maxText keeps generated bundles and data dumps out of what mention reads.
const maxText = 2 << 20

func (u *usage) load() {
	if u.text != nil {
		return
	}
	u.text = map[string][]byte{}
	decl := map[string][]int{}
	for _, d := range u.deps {
		if d.Line > 0 {
			decl[d.Manifest.Path] = append(decl[d.Manifest.Path], d.Line)
		}
	}
	for _, f := range u.files {
		md := markdown(f)
		if prose(f) && !md || lockfile(f) || foreign(f) {
			continue
		}
		b := readText(filepath.Join(u.root, filepath.FromSlash(f)))
		if nb := notebookCode(filepath.Join(u.root, filepath.FromSlash(f))); nb != nil {
			b = nb
		}
		if b == nil {
			continue
		}
		// package.json is blanked by entry, since a one-line file holds its
		// scripts on the line that declares the dependencies
		structured := path.Base(f) == "package.json" && blankEntries(b)
		b = bytes.ToLower(b)
		if md {
			mdImports(b)
		}
		for _, n := range decl[f] {
			if !structured {
				blankLine(b, n)
			}
		}
		u.text[f] = b
		u.order = append(u.order, f)
	}
	slices.Sort(u.order)
}

// markdown files are prose, except that docs sites such as Docusaurus and
// VitePress compile them with the imports they hold.
func markdown(f string) bool {
	ext := strings.ToLower(path.Ext(f))
	return ext == ".md" || ext == ".markdown"
}

// mdImports blanks every line of a markdown file but its JavaScript import
// lines outside code blocks, which a docs site compiles.
func mdImports(b []byte) {
	fenced := false
	for start := 0; start < len(b); {
		end := bytes.IndexByte(b[start:], '\n')
		if end < 0 {
			end = len(b)
		} else {
			end += start
		}
		line := bytes.TrimSpace(b[start:end])
		if bytes.HasPrefix(line, []byte("```")) || bytes.HasPrefix(line, []byte("~~~")) {
			fenced = !fenced
		}
		if fenced || !bytes.HasPrefix(line, []byte("import ")) || !bytes.ContainsAny(line, `'"`) {
			for i := start; i < end; i++ {
				b[i] = ' '
			}
		}
		start = end + 1
	}
}

// readText returns a file's bytes, or nil for one that is missing, not a
// regular file, too large, or binary.
func readText(p string) []byte {
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxText {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil || bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 {
		return nil
	}
	return b
}

// blankEntries blanks the key and requirement of every entry of the
// dependency blocks of package.json src, and reports whether it could read
// them.
func blankEntries(src []byte) bool {
	blocks, err := npmParse(src)
	if err != nil {
		return false
	}
	for _, bl := range blocks {
		for _, e := range bl.entries {
			for _, span := range [][2]int{{e.keyStart, e.keyEnd}, {e.valStart, e.valEnd}} {
				for i := span[0]; i < span[1]; i++ {
					src[i] = ' '
				}
			}
		}
	}
	return true
}

func blankLine(b []byte, n int) {
	start := 0
	for i := 1; i < n; i++ {
		k := bytes.IndexByte(b[start:], '\n')
		if k < 0 {
			return
		}
		start += k + 1
	}
	for i := start; i < len(b) && b[i] != '\n'; i++ {
		b[i] = ' '
	}
}

// foreign files are other people's code, or fixtures, which say nothing
// about what the project itself uses. .yarn holds Yarn's own release and
// its cache.
func foreign(f string) bool {
	for _, part := range strings.Split(path.Dir(f), "/") {
		switch part {
		case "node_modules", "vendor", ".git", "testdata", ".yarn":
			return true
		}
	}
	return false
}

var prosed = []string{"readme", "changelog", "changes", "history", "license", "licence", "notice", "authors", "contributing", "copying"}

// prose files talk about packages without using them.
func prose(f string) bool {
	base := strings.ToLower(path.Base(f))
	ext := path.Ext(base)
	switch ext {
	case ".md", ".markdown", ".rst", ".adoc":
		return true
	case "", ".txt":
		return slices.Contains(prosed, strings.TrimSuffix(base, ext))
	}
	return false
}

var lockNames = []string{
	"package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock", "bun.lockb",
	"go.sum", "go.work.sum", "poetry.lock", "uv.lock", "pipfile.lock", "pdm.lock", "cargo.lock",
	"gemfile.lock", "composer.lock", "packages.lock.json", ".terraform.lock.hcl", "chart.lock", "gradle.lockfile",
	// Yarn Plug'n'Play's maps, which name every installed package
	".pnp.cjs", ".pnp.js", ".pnp.loader.mjs",
}

func lockfile(f string) bool {
	base := strings.ToLower(path.Base(f))
	return slices.Contains(lockNames, base) || strings.HasSuffix(base, ".lock")
}

// named finds w in text as a whole name: not inside a longer one, though a
// path may lead to it (node_modules/.bin/tsc) and a subpath, a version or a
// member may follow (lodash/fp, lodash@4, yaml.safe_load). It returns where,
// or -1.
func named(text []byte, w string) int {
	if w == "" {
		return -1
	}
	for i := 0; ; {
		k := bytes.Index(text[i:], []byte(w))
		if k < 0 {
			return -1
		}
		at, end := i+k, i+k+len(w)
		if (at == 0 || !nameByte(text[at-1], true) && !scoped(text, at)) && (end == len(text) || !nameByte(text[end], false)) {
			return at
		}
		i = at + 1
	}
}

// scoped reports whether the name at text[at:] is the second half of a
// scoped package name, @acme/lodash, which is not lodash.
func scoped(text []byte, at int) bool {
	i := at
	for i > 0 && (text[i-1] == '/' || nameByte(text[i-1], true)) {
		i--
	}
	return i < at && text[i] == '@'
}

// nameByte reports whether c next to a match makes it part of a longer name.
// Before it, a dot or an @ does; after it, they start a member or a version.
func nameByte(c byte, before bool) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '-':
		return true
	case before:
		return c == '.' || c == '@'
	}
	return false
}

// configured returns a config file named for one of tools, the way
// .eslintrc.json names eslint, karma.conf.js karma and tox.ini tox; "" when
// there is none.
func (u *usage) configured(tools ...string) string {
	for _, f := range u.files {
		if foreign(f) {
			continue
		}
		stem, strong := configStem(path.Base(f))
		for _, t := range tools {
			if fits(stem, alnum(t), strong) {
				return f
			}
		}
	}
	return ""
}

// configFiles are config files whose names do not say whose they are.
var configFiles = map[string]string{"conftest.py": "pytest", "noxfile.py": "nox", "setup.cfg": "setuptools"}

// configStem is the tool a config file's name points at, in lowercase letters
// and digits, "" for a file that is not config. strong says the name has a
// config file's shape (.eslintrc.json, jest.config.js) rather than only a
// config format's extension (tox.ini, nodemon.json).
func configStem(base string) (string, bool) {
	b := strings.ToLower(base)
	if s, ok := configFiles[b]; ok {
		return s, true
	}
	dot := strings.HasPrefix(b, ".")
	b = strings.TrimLeft(b, ".")
	first, rest, _ := strings.Cut(b, ".")
	strong := dot || strings.HasSuffix(first, "rc") || rest == "config" || strings.HasPrefix(rest, "config.") || strings.HasPrefix(rest, "conf.")
	switch {
	case strong:
	case slices.Contains([]string{"ini", "cfg", "toml", "yaml", "yml", "json"}, rest) && !slices.Contains(manifests, b):
	default:
		return "", false
	}
	return alnum(strings.TrimSuffix(first, "rc")), strong
}

// manifests declare dependencies rather than configure a tool.
var manifests = []string{"package.json", "composer.json", "pyproject.toml", "cargo.toml", "pipfile"}

// fits says whether a config file's stem names tool: the same, or the tool's
// name followed by more (pre-commit-config for pre-commit). A file shaped
// like config may also shorten it (tailwind.config.js for tailwindcss).
func fits(stem, tool string, strong bool) bool {
	switch {
	case stem == "" || tool == "":
		return false
	case stem == tool, len(tool) >= 3 && strings.HasPrefix(stem, tool):
		return true
	}
	return strong && len(stem) >= 4 && strings.HasPrefix(tool, stem)
}

func alnum(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// typescript reports whether the project is written in TypeScript or set up
// for it, when the compiler reads every @types package by itself.
func (u *usage) typescript() bool {
	if u.ts != nil {
		return *u.ts
	}
	res := false
	for _, f := range u.files {
		b := strings.ToLower(path.Base(f))
		switch {
		case foreign(f):
		case strings.HasPrefix(b, "tsconfig") && strings.HasSuffix(b, ".json"), b == "jsconfig.json":
			res = true
		case slices.Contains([]string{".ts", ".tsx", ".mts", ".cts"}, path.Ext(b)):
			res = true
		}
	}
	u.ts = &res
	return res
}
