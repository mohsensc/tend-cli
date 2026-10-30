package eco

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/ver"
)

type python struct{}

func (python) Name() string { return core.Pip }

func (python) Detect(root string) ([]core.Manifest, error) {
	all, err := walk(root, "pyproject.toml", "Pipfile", "setup.cfg", "*.txt", "*.in")
	if err != nil {
		return nil, err
	}
	var res []core.Manifest
	reqs := map[string]bool{}
	for _, f := range all {
		switch base := path.Base(f); {
		case base == "pyproject.toml":
			if m, ok := pyProjectManifest(root, f); ok {
				res = append(res, m)
			}
		case base == "setup.cfg":
			if src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f))); err == nil && len(setupCfgEntries(src)) > 0 {
				res = append(res, core.Manifest{Ecosystem: core.Pip, Path: f, Tool: "pip"})
			}
		case base == "Pipfile":
			m := core.Manifest{Ecosystem: core.Pip, Path: f, Tool: "pipenv"}
			if lock := f + ".lock"; onDisk(root, lock) {
				m.Lock = lock
			}
			res = append(res, m)
		case pyReqFile(f):
			reqs[f] = true
		}
	}
	queue := slices.Sorted(maps.Keys(reqs))
	for i := 0; i < len(queue); i++ {
		in, cons := pyIncludes(root, queue[i])
		for _, inc := range append(in, cons...) {
			if !reqs[inc] && onDisk(root, inc) {
				reqs[inc] = true
				queue = append(queue, inc)
			}
		}
	}
	slices.Sort(queue)
	compiled := map[string]bool{}
	for _, f := range queue {
		if out := strings.TrimSuffix(f, ".in") + ".txt"; strings.HasSuffix(f, ".in") && reqs[out] && pyCompiledBy(root, out) != "" {
			compiled[out] = true
		}
	}
	var reqMs []core.Manifest
	for _, f := range queue {
		if compiled[f] {
			continue
		}
		m := core.Manifest{Ecosystem: core.Pip, Path: f, Tool: "pip"}
		if out := strings.TrimSuffix(f, ".in") + ".txt"; compiled[out] {
			m.Lock = out
		}
		reqMs = append(reqMs, m)
	}
	res = pyAdoptCompiled(root, append(res, reqMs...))
	slices.SortFunc(res, func(a, b core.Manifest) int { return strings.Compare(a.Path, b.Path) })
	return res, nil
}

// pyAdoptCompiled pairs a compiled requirements file that has no .in beside
// it with the source its header's command names, a pyproject.toml or an .in
// file with no lock of its own, which then locks with it. A compiled file
// that names no such source, or shares it with another output, stays a
// manifest of its own whose pins Deps reads as locked, not required.
func pyAdoptCompiled(root string, ms []core.Manifest) []core.Manifest {
	byPath := map[string]int{}
	for i, m := range ms {
		byPath[m.Path] = i
	}
	added := map[int]bool{}
	outs := map[int][]int{}
	for i := range ms {
		m := ms[i]
		if m.Lock != "" || path.Ext(m.Path) != ".txt" || pyCompiledBy(root, m.Path) == "" {
			continue
		}
		for _, src := range pyCompiledFrom(root, m.Path) {
			if path.Base(src) != "pyproject.toml" && path.Ext(src) != ".in" || !pyUnder(path.Dir(src), m.Path) {
				continue
			}
			j, ok := byPath[src]
			if !ok && path.Ext(src) == ".in" && onDisk(root, src) {
				j, ok = len(ms), true
				byPath[src], added[j] = j, true
				ms = append(ms, core.Manifest{Ecosystem: core.Pip, Path: src, Tool: "pip"})
			}
			if ok && ms[j].Lock == "" {
				outs[j] = append(outs[j], i)
			}
			break
		}
	}
	drop := map[int]bool{}
	for j, is := range outs {
		if len(is) == 1 {
			ms[j].Lock = ms[is[0]].Path
			drop[is[0]] = true
		}
	}
	var res []core.Manifest
	for i, m := range ms {
		if !drop[i] && (!added[i] || m.Lock != "") {
			res = append(res, m)
		}
	}
	return res
}

// pyUnder reports whether rel sits in dir or under it, so a lock command
// run in dir can name it.
func pyUnder(dir, rel string) bool {
	return dir == "." || strings.HasPrefix(rel, dir+"/")
}

// pyCompiledFrom lists the sources the command in a compiled file's header
// compiled it from, relative to the repo root: its arguments that are not
// options or their values.
func pyCompiledFrom(root, rel string) []string {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil
	}
	for i, line := range strings.SplitN(string(b), "\n", 11) {
		if i == 10 || !strings.HasPrefix(line, "#") {
			break
		}
		f := strings.Fields(strings.TrimPrefix(line, "#"))
		switch {
		case len(f) > 0 && f[0] == "pip-compile":
			f = f[1:]
		case len(f) > 2 && f[0] == "uv" && f[1] == "pip" && f[2] == "compile":
			f = f[3:]
		default:
			continue
		}
		var res []string
		for k := 0; k < len(f); k++ {
			switch arg := f[k]; {
			case pyValueOpts[arg]:
				k++
			case strings.HasPrefix(arg, "-"):
			default:
				res = append(res, path.Join(path.Dir(rel), arg))
			}
		}
		return res
	}
	return nil
}

// pyValueOpts are the pip-compile and uv pip compile options whose value is
// the next argument.
var pyValueOpts = map[string]bool{
	"-o": true, "--output-file": true, "-c": true, "--constraint": true, "--constraints": true,
	"--override": true, "--overrides": true, "--build-constraint": true, "--build-constraints": true,
	"-P": true, "--upgrade-package": true, "--extra": true, "--group": true, "-i": true, "--index-url": true,
	"--extra-index-url": true, "-f": true, "--find-links": true, "-p": true, "--python": true,
	"--python-version": true, "--python-platform": true, "--resolver": true, "--cache-dir": true,
	"--pip-args": true, "--annotation-style": true, "--unsafe-package": true, "--no-emit-package": true,
	"--config": true, "--index": true, "--default-index": true,
}

// pyReqFile matches requirements.txt, requirements-dev.txt, dev-requirements.in
// and anything in a requirements/ directory.
func pyReqFile(rel string) bool {
	base := path.Base(rel)
	if ext := path.Ext(base); ext != ".txt" && ext != ".in" {
		return false
	}
	return strings.Contains(base, "requirements") || path.Base(path.Dir(rel)) == "requirements"
}

// pyIncludes lists the files a requirements file pulls in with -r, and
// those it constrains with -c, relative to the repo root.
func pyIncludes(root, rel string) (reqs, cons []string) {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, nil
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(pyStripComment(line))
		if len(f) == 0 {
			continue
		}
		arg := ""
		switch opt, val, _ := strings.Cut(f[0], "="); {
		case (opt == "-r" || opt == "-c" || opt == "--requirement" || opt == "--constraint") && len(f) > 1:
			arg = f[1]
		case opt == "--requirement" || opt == "--constraint":
			arg = val
		case strings.HasPrefix(f[0], "-r") || strings.HasPrefix(f[0], "-c"):
			arg = f[0][2:]
		}
		if arg == "" || strings.Contains(arg, "://") {
			continue
		}
		p := path.Join(path.Dir(rel), arg)
		switch {
		case p == ".." || strings.HasPrefix(p, "../"):
		case strings.HasPrefix(f[0], "-c") || strings.HasPrefix(f[0], "--constraint"):
			cons = append(cons, p)
		default:
			reqs = append(reqs, p)
		}
	}
	return reqs, cons
}

// pyProjectManifest reads a pyproject.toml that declares a project, or that
// is the virtual root of a uv workspace, whose deps live in its lock alone.
// A uv workspace member shares the lockfile at the workspace root.
func pyProjectManifest(root, rel string) (core.Manifest, bool) {
	var doc map[string]any
	if readTOML(filepath.Join(root, filepath.FromSlash(rel)), &doc) != nil {
		return core.Manifest{}, false
	}
	tool, _ := doc["tool"].(map[string]any)
	_, poetry := tool["poetry"]
	_, pdm := tool["pdm"]
	uv, _ := tool["uv"].(map[string]any)
	_, workspace := uv["workspace"]
	if _, ok := doc["project"]; !ok && !poetry && !workspace {
		return core.Manifest{}, false
	}
	m := core.Manifest{Ecosystem: core.Pip, Path: rel, Tool: "pip"}
	dir := path.Dir(rel)
	switch {
	case onDisk(root, path.Join(dir, "uv.lock")):
		m.Lock, m.Tool = path.Join(dir, "uv.lock"), "uv"
	case onDisk(root, path.Join(dir, "poetry.lock")):
		m.Lock, m.Tool = path.Join(dir, "poetry.lock"), "poetry"
	case onDisk(root, path.Join(dir, "pdm.lock")):
		m.Lock, m.Tool = path.Join(dir, "pdm.lock"), "pdm"
	case poetry:
		m.Tool = "poetry"
	case pdm:
		m.Tool = "pdm"
	default:
		if lock := uvWorkspaceLock(root, dir); lock != "" {
			m.Lock, m.Tool = lock, "uv"
		}
	}
	return m, true
}

// uvWorkspaceLock finds the uv.lock of the workspace dir belongs to: the
// nearest pyproject.toml above it with a [tool.uv.workspace] table, when its
// members globs, less its exclude globs, take dir.
func uvWorkspaceLock(root, dir string) string {
	for d := dir; d != "."; {
		d = path.Dir(d)
		var doc struct {
			Tool struct {
				UV struct {
					Workspace *struct {
						Members []string `toml:"members"`
						Exclude []string `toml:"exclude"`
					} `toml:"workspace"`
				} `toml:"uv"`
			} `toml:"tool"`
		}
		if readTOML(filepath.Join(root, filepath.FromSlash(d), "pyproject.toml"), &doc) != nil {
			continue
		}
		ws := doc.Tool.UV.Workspace
		if ws == nil {
			continue
		}
		member := memberDir(d, dir)
		lock := path.Join(d, "uv.lock")
		if !globsMatch(ws.Members, member) || globsMatch(ws.Exclude, member) || !onDisk(root, lock) {
			return ""
		}
		return lock
	}
	return ""
}

// pyEntry is one requirement as written, with what Bump needs to edit it in
// place: raw is the exact text on the line, and the name and specifier
// offsets point into it.
type pyEntry struct {
	name    string
	spec    string
	line    int
	raw     string
	nameAt  int
	nameEnd int // after the extras
	specAt  int
	dev     bool
	poetry  bool   // spec is a Poetry constraint: ^1.2, ~1.2, 1.2
	hashed  bool   // pinned with --hash, which a new version invalidates
	extra   string // the extra that offers it to users, * for a Poetry one, "" for none
}

func pyEntries(root string, m core.Manifest) ([]pyEntry, error) {
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(m.Path)))
	if err != nil {
		return nil, err
	}
	switch path.Base(m.Path) {
	case "pyproject.toml":
		return pyProjectEntries(m.Path, src)
	case "Pipfile":
		return pipfileEntries(m.Path, src)
	case "setup.cfg":
		return setupCfgEntries(src), nil
	}
	res := pyReqEntries(src)
	if pyDevFile(m.Path) {
		for i := range res {
			res[i].dev = true
		}
	}
	return res, nil
}

// setupCfgEntries reads the requirements a setuptools setup.cfg declares:
// [options] install_requires, and each extra of [options.extras_require],
// one per line, as the value or its indented continuation lines. A file:
// or attr: indirection names no requirement here.
func setupCfgEntries(src []byte) []pyEntry {
	var res []pyEntry
	section, key := "", ""
	add := func(text string, n int) {
		text = pyStripComment(text)
		t := strings.TrimSpace(text)
		if t == "" || strings.HasPrefix(t, "file:") || strings.HasPrefix(t, "attr:") {
			return
		}
		r, ok := pyParseReq(t)
		if !ok {
			return
		}
		r.line, r.raw = n, t
		if section == "options.extras_require" {
			r.extra, r.dev = key, pyDevGroups[PyNorm(key)]
		}
		res = append(res, r)
	}
	for i, l := range strings.Split(string(src), "\n") {
		line := strings.TrimRight(l, "\r")
		t := strings.TrimSpace(line)
		switch {
		case t == "" || t[0] == '#' || t[0] == ';':
		case strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]"):
			section, key = strings.ToLower(strings.TrimSpace(t[1:len(t)-1])), ""
		case line[0] != ' ' && line[0] != '\t':
			k, v, ok := strings.Cut(line, "=")
			if c := strings.IndexByte(line, ':'); c >= 0 && (!ok || c < len(k)) {
				k, v, ok = line[:c], line[c+1:], true
			}
			key = ""
			k = strings.ToLower(strings.TrimSpace(k))
			if ok && (section == "options" && k == "install_requires" || section == "options.extras_require") {
				key = k
				add(v, i+1)
			}
		case key != "":
			add(line, i+1)
		}
	}
	return res
}

// pyDevFile reports a requirements file named for development or tests:
// requirements-dev.txt, test-requirements.in, requirements/lint.txt.
func pyDevFile(rel string) bool {
	base := strings.TrimSuffix(path.Base(rel), path.Ext(rel))
	for _, w := range strings.FieldsFunc(base, func(r rune) bool { return r == '-' || r == '_' || r == '.' }) {
		if pyDevGroups[strings.ToLower(w)] {
			return true
		}
	}
	return false
}

func pyReqEntries(src []byte) []pyEntry {
	var res []pyEntry
	lines := strings.Split(string(src), "\n")
	for i := 0; i < len(lines); i++ {
		n := i + 1
		text := pyStripComment(lines[i])
		full := text
		for strings.HasSuffix(full, `\`) && i+1 < len(lines) {
			i++
			full = strings.TrimSuffix(full, `\`) + " " + pyStripComment(lines[i])
		}
		text = strings.TrimRight(strings.TrimSuffix(text, `\`), " \t")
		if k := strings.Index(strings.ReplaceAll(text, "\t", " "), " --"); k >= 0 {
			text = strings.TrimRight(text[:k], " \t")
		}
		t := strings.TrimSpace(text)
		if t == "" || t[0] == '-' || t[0] == '.' || t[0] == '/' || strings.Contains(t, "://") {
			continue
		}
		r, ok := pyParseReq(text)
		if !ok {
			continue
		}
		r.line, r.raw, r.hashed = n, text, strings.Contains(full, "--hash")
		res = append(res, r)
	}
	return res
}

// pyStripComment drops a # comment, which needs whitespace or the line start
// before it, and the line's trailing whitespace.
func pyStripComment(s string) string {
	s = strings.TrimRight(s, "\r")
	for i := 0; i < len(s); i++ {
		if s[i] == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t') {
			s = s[:i]
			break
		}
	}
	return strings.TrimRight(s, " \t")
}

// pyParseReq reads a PEP 508 requirement, name[extras] spec ; marker. Direct
// references (name @ url) have no version to manage and are refused.
func pyParseReq(s string) (pyEntry, bool) {
	var r pyEntry
	i := len(s) - len(strings.TrimLeft(s, " \t"))
	r.nameAt = i
	for i < len(s) && pyNameChar(s[i]) {
		i++
	}
	if i == r.nameAt {
		return r, false
	}
	r.name, r.nameEnd = s[r.nameAt:i], i
	j := i + len(s[i:]) - len(strings.TrimLeft(s[i:], " \t"))
	if j < len(s) && s[j] == '[' {
		k := strings.IndexByte(s[j:], ']')
		if k < 0 {
			return r, false
		}
		j += k + 1
		r.nameEnd = j
	}
	end := len(s)
	if k := strings.IndexByte(s[j:], ';'); k >= 0 {
		end = j + k
	}
	spec := strings.TrimSpace(s[j:end])
	if strings.HasPrefix(spec, "(") && strings.HasSuffix(spec, ")") {
		spec = strings.TrimSpace(spec[1 : len(spec)-1])
	}
	if spec != "" && !strings.ContainsRune("<>=!~", rune(spec[0])) {
		return r, false
	}
	r.spec = spec
	r.specAt = j + strings.Index(s[j:end], spec)
	return r, true
}

func pyNameChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
}

// PyNorm is the PEP 503 name: lower case, runs of -_. as one dash.
func PyNorm(name string) string {
	var sb strings.Builder
	dash := false
	for _, c := range strings.ToLower(strings.TrimSpace(name)) {
		if c == '-' || c == '_' || c == '.' {
			dash = true
			continue
		}
		if dash && sb.Len() > 0 {
			sb.WriteByte('-')
		}
		dash = false
		sb.WriteRune(c)
	}
	return sb.String()
}

var pyDevGroups = map[string]bool{
	"dev": true, "develop": true, "test": true, "tests": true, "testing": true,
	"lint": true, "docs": true, "doc": true, "typing": true,
}

type pyProjectFile struct {
	Project struct {
		Dependencies []string            `toml:"dependencies"`
		Optional     map[string][]string `toml:"optional-dependencies"`
	} `toml:"project"`
	Groups map[string][]any `toml:"dependency-groups"`
	Tool   struct {
		Poetry struct {
			Dependencies    map[string]any `toml:"dependencies"`
			DevDependencies map[string]any `toml:"dev-dependencies"`
			Group           map[string]struct {
				Dependencies map[string]any `toml:"dependencies"`
			} `toml:"group"`
		} `toml:"poetry"`
		UV struct {
			DevDependencies []string `toml:"dev-dependencies"`
		} `toml:"uv"`
	} `toml:"tool"`
}

// pyProjectEntries reads PEP 621 dependencies and extras, PEP 735 dependency
// groups, uv's dev-dependencies and Poetry's dependency tables, in file order.
func pyProjectEntries(rel string, src []byte) ([]pyEntry, error) {
	var doc pyProjectFile
	if err := tomlDecode(src, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	lines := tomlScan(src)
	var res []pyEntry
	add := func(table, key string, reqs []string, dev bool) {
		at := tomlFindKey(lines, table, key)
		for _, s := range reqs {
			e, ok := pyParseReq(s)
			if !ok {
				continue
			}
			e.dev = dev
			if table == "project.optional-dependencies" && !dev {
				e.extra = key
			}
			if i, raw := tomlFindString(lines, table, max(at, 0), s); i >= 0 {
				at = i
				e.line, e.raw = i+1, raw
				e.nameAt, e.nameEnd, e.specAt = e.nameAt+1, e.nameEnd+1, e.specAt+1
			}
			res = append(res, e)
		}
	}
	add("project", "dependencies", doc.Project.Dependencies, false)
	for _, g := range slices.Sorted(maps.Keys(doc.Project.Optional)) {
		add("project.optional-dependencies", g, doc.Project.Optional[g], pyDevGroups[PyNorm(g)])
	}
	for _, g := range slices.Sorted(maps.Keys(doc.Groups)) {
		var reqs []string
		for _, v := range doc.Groups[g] {
			if s, ok := v.(string); ok {
				reqs = append(reqs, s)
			}
		}
		add("dependency-groups", g, reqs, true)
	}
	add("tool.uv", "dev-dependencies", doc.Tool.UV.DevDependencies, true)
	poetry := doc.Tool.Poetry
	res = append(res, poetryEntries(lines, "tool.poetry.dependencies", poetry.Dependencies, false)...)
	res = append(res, poetryEntries(lines, "tool.poetry.dev-dependencies", poetry.DevDependencies, true)...)
	for _, g := range slices.Sorted(maps.Keys(poetry.Group)) {
		res = append(res, poetryEntries(lines, "tool.poetry.group."+g+".dependencies", poetry.Group[g].Dependencies, g != "main")...)
	}
	slices.SortStableFunc(res, func(a, b pyEntry) int {
		return cmp.Or(cmp.Compare(cmp.Or(a.line, len(lines)+1), cmp.Or(b.line, len(lines)+1)), strings.Compare(a.name, b.name))
	})
	return res, nil
}

// poetryEntries reads a Poetry dependency table: name = "^1.2" or
// name = { version = "^1.2", ... }. Git, path and url sources carry no
// version and are skipped, as is the python constraint itself.
func poetryEntries(lines []tomlLine, table string, deps map[string]any, dev bool) []pyEntry {
	var res []pyEntry
	for name, v := range deps {
		spec, extra := "", ""
		switch x := v.(type) {
		case string:
			spec = x
		case map[string]any:
			spec, _ = x["version"].(string)
			if opt, _ := x["optional"].(bool); opt && !dev {
				extra = "*"
			}
		}
		if spec == "" || PyNorm(name) == "python" {
			continue
		}
		e := pyEntry{name: name, spec: spec, dev: dev, poetry: true, extra: extra}
		if i := tomlFindKey(lines, table, name); i >= 0 {
			e.line, e.raw, e.specAt = i+1, poetryValue(lines[i].text, spec), 1
		}
		res = append(res, e)
	}
	return res
}

// poetryValue finds the quoted constraint on a Poetry dependency line, inside
// an inline table's version key when there is one.
func poetryValue(line, spec string) string {
	_, rest, _ := strings.Cut(line, "=")
	if i := strings.Index(rest, "version"); i >= 0 && strings.Contains(rest, "{") {
		rest = rest[i:]
	}
	for _, q := range []string{`"`, `'`} {
		if strings.Contains(rest, q+spec+q) {
			return q + spec + q
		}
	}
	return ""
}

func pipfileEntries(rel string, src []byte) ([]pyEntry, error) {
	var doc struct {
		Packages    map[string]any `toml:"packages"`
		DevPackages map[string]any `toml:"dev-packages"`
	}
	if err := tomlDecode(src, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	lines := tomlScan(src)
	var res []pyEntry
	for _, t := range []struct {
		table string
		deps  map[string]any
	}{{"packages", doc.Packages}, {"dev-packages", doc.DevPackages}} {
		for name, v := range t.deps {
			spec, _ := v.(string)
			if m, ok := v.(map[string]any); ok {
				spec, _ = m["version"].(string)
				if _, ok := m["version"]; !ok {
					continue
				}
			}
			res = append(res, pyEntry{name: name, spec: spec, line: tomlFindKey(lines, t.table, name) + 1, dev: t.table == "dev-packages"})
		}
	}
	slices.SortFunc(res, func(a, b pyEntry) int { return cmp.Or(cmp.Compare(a.line, b.line), strings.Compare(a.name, b.name)) })
	return res, nil
}

func (python) Deps(root string, m core.Manifest) ([]core.Dep, error) {
	entries, err := pyEntries(root, m)
	if err != nil {
		return nil, err
	}
	locked, err := pyReadLock(root, m)
	if err != nil {
		return nil, err
	}
	uv, err := pyReadUV(root, m)
	if err != nil {
		return nil, err
	}
	pinsOnly := m.Lock == "" && path.Ext(m.Path) == ".txt" && pyCompiledBy(root, m.Path) != ""
	pyLearnModules(root, path.Dir(m.Path))
	versions := map[string]string{}
	for _, l := range locked {
		versions[l.name] = l.version
	}
	var res []core.Dep
	direct := map[string]bool{}
	for _, e := range entries {
		n := PyNorm(e.name)
		if direct[n] || uv.local[n] {
			continue
		}
		direct[n] = true
		res = append(res, core.Dep{
			Ecosystem: core.Pip,
			Name:      n,
			Version:   cmp.Or(versions[n], pyPinned(e.spec)),
			Req:       e.spec,
			Manifest:  m,
			Line:      e.line,
			Dev:       e.dev,
			Direct:    !pinsOnly,
		})
	}
	for _, l := range locked {
		if !direct[l.name] && !uv.elsewhere(l.name) {
			res = append(res, core.Dep{Ecosystem: core.Pip, Name: l.name, Version: l.version, Manifest: m, Dev: l.dev || pyDevFile(m.Path)})
		}
	}
	return res, nil
}

// pyUVView is what a manifest makes of a uv.lock it may share with a
// workspace: the packages that come from no registry (members, paths, git),
// which are not registry deps however they are written, and whether each
// locked package is reported by another manifest. A member reports only its
// own requirements, and the root leaves out what members require directly.
type pyUVView struct {
	local     map[string]bool
	elsewhere func(string) bool
}

func pyReadUV(root string, m core.Manifest) (pyUVView, error) {
	view := pyUVView{elsewhere: func(string) bool { return false }}
	if path.Base(m.Lock) != "uv.lock" {
		return view, nil
	}
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(m.Lock)))
	if err != nil {
		return view, err
	}
	ps, err := uvPackages(src)
	if err != nil {
		return view, fmt.Errorf("%s: %w", m.Lock, err)
	}
	view.local = map[string]bool{}
	for _, p := range ps {
		if _, ok := p.Source["registry"]; !ok {
			view.local[PyNorm(p.Name)] = true
		}
	}
	if path.Dir(m.Path) != path.Dir(m.Lock) {
		view.elsewhere = func(string) bool { return true }
		return view, nil
	}
	reqs := uvMemberReqs(ps, ".")
	view.elsewhere = func(name string) bool { return reqs[name] }
	return view, nil
}

// pyPinned is the version an exact == or === pin names, "" for anything looser.
func pyPinned(spec string) string {
	v, ok := strings.CutPrefix(spec, "===")
	if !ok {
		v, ok = strings.CutPrefix(spec, "==")
	}
	v = strings.TrimSpace(v)
	if !ok || strings.ContainsAny(v, "*,") {
		return ""
	}
	return v
}

// Imports has no project root, so Deps remembers what an installed virtualenv
// says each distribution provides (top_level.txt) for Imports to prefer, and
// the entry points and requirements it declares (entry_points.txt, METADATA)
// for the first PR.
var (
	pyTopLevel sync.Map // dist -> []string
	pyPoints   sync.Map // dist -> pyEntryPoints
	pyDistReqs sync.Map // dist -> []string, the distributions it requires
)

func pyLearnModules(root, dir string) {
	for _, d := range slices.Compact([]string{".", dir}) {
		for _, venv := range []string{".venv", "venv"} {
			base := filepath.Join(root, filepath.FromSlash(d), venv)
			hits, _ := filepath.Glob(filepath.Join(base, "lib", "python*", "site-packages", "*.dist-info"))
			more, _ := filepath.Glob(filepath.Join(base, "Lib", "site-packages", "*.dist-info"))
			for _, info := range append(hits, more...) {
				dist, _, _ := strings.Cut(filepath.Base(info), "-")
				dist = PyNorm(dist)
				if b, err := os.ReadFile(filepath.Join(info, "top_level.txt")); err == nil {
					if mods := strings.Fields(string(b)); len(mods) > 0 {
						pyTopLevel.Store(dist, mods)
					}
				}
				if b, err := os.ReadFile(filepath.Join(info, "entry_points.txt")); err == nil {
					pyPoints.Store(dist, pyParsePoints(string(b)))
				}
				if b, err := os.ReadFile(filepath.Join(info, "METADATA")); err == nil {
					pyDistReqs.Store(dist, pyParseRequires(string(b)))
				}
			}
		}
	}
}

// pyParseRequires reads the distributions a METADATA file's Requires-Dist
// headers name, those of extras included.
func pyParseRequires(src string) []string {
	var res []string
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			break
		}
		v, ok := strings.CutPrefix(line, "Requires-Dist:")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		end := strings.IndexFunc(v, func(r rune) bool {
			return !pyNameRune(r)
		})
		if end < 0 {
			end = len(v)
		}
		if n := PyNorm(v[:end]); n != "" && !slices.Contains(res, n) {
			res = append(res, n)
		}
	}
	return res
}

// pyEntryPoints are the commands a distribution installs and the plugin
// groups it joins, which other tools load from without an import.
type pyEntryPoints struct {
	scripts, groups []string
}

func pyParsePoints(src string) pyEntryPoints {
	var res pyEntryPoints
	group := ""
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";"):
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			group = strings.TrimSpace(line[1 : len(line)-1])
		case group == "console_scripts" || group == "gui_scripts":
			if name, _, ok := strings.Cut(line, "="); ok {
				res.scripts = append(res.scripts, strings.TrimSpace(name))
			}
		case group != "" && !slices.Contains(res.groups, group):
			res.groups = append(res.groups, group)
		}
	}
	return res
}

// pyModules are distributions whose import name is not their name with dashes
// turned into underscores.
var pyModules = map[string][]string{
	"attrs":                {"attr", "attrs"},
	"beautifulsoup4":       {"bs4"},
	"google-cloud-storage": {"google.cloud.storage"},
	"msgpack-python":       {"msgpack"},
	"opencv-python":        {"cv2"},
	"pillow":               {"PIL"},
	"protobuf":             {"google.protobuf"},
	"psycopg2-binary":      {"psycopg2"},
	"pyjwt":                {"jwt"},
	"pymongo":              {"pymongo", "bson", "gridfs"},
	"python-dateutil":      {"dateutil"},
	"python-dotenv":        {"dotenv"},
	"pyyaml":               {"yaml"},
	"scikit-learn":         {"sklearn"},
}

func (python) Traits() core.Traits {
	return core.Traits{OSV: "PyPI", Page: func(name string) string { return "https://pypi.org/project/" + name + "/" }, Transitive: true}
}

func (python) Imports(d core.Dep) []string {
	n := PyNorm(d.Name)
	if mods, ok := pyTopLevel.Load(n); ok {
		return mods.([]string)
	}
	if mods, ok := pyModules[n]; ok {
		return mods
	}
	return []string{strings.ReplaceAll(n, "-", "_")}
}

func (python) Meta(ctx context.Context, f core.Fetcher, name string) (*core.Meta, error) {
	n := PyNorm(name)
	var doc struct {
		Info struct {
			Version     string            `json:"version"`
			HomePage    string            `json:"home_page"`
			ProjectURLs map[string]string `json:"project_urls"`
			Classifiers []string          `json:"classifiers"`
		} `json:"info"`
		Releases map[string][]struct {
			Upload       string  `json:"upload_time_iso_8601"`
			Yanked       bool    `json:"yanked"`
			YankedReason *string `json:"yanked_reason"`
		} `json:"releases"`
	}
	found, err := getDoc(ctx, f, "https://pypi.org/pypi/"+n+"/json", &doc)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("pypi %s: not found", n)
	}
	meta := &core.Meta{Name: n, Latest: doc.Info.Version, Repo: pyRepo(doc.Info.ProjectURLs, doc.Info.HomePage)}
	for v, files := range doc.Releases {
		if len(files) == 0 {
			continue
		}
		r := core.Release{Version: v, Yanked: true}
		reason := ""
		for _, f := range files {
			if t, err := time.Parse(time.RFC3339, f.Upload); err == nil && (r.Time.IsZero() || t.Before(r.Time)) {
				r.Time = t
			}
			r.Yanked = r.Yanked && f.Yanked
			if f.YankedReason != nil {
				reason = *f.YankedReason
			}
		}
		if r.Yanked {
			r.Deprecated = "yanked"
			if reason != "" {
				r.Deprecated += ": " + reason
			}
		}
		r.Pre = isPre(v)
		meta.Releases = append(meta.Releases, r)
	}
	sortReleases(meta.Releases)
	for _, c := range doc.Info.Classifiers {
		if strings.HasPrefix(c, "Development Status :: 7") {
			meta.Deprecated = "marked inactive on PyPI (" + c + ")"
		}
	}
	return meta, nil
}

// pyRepoKeys are the project URL labels that name the source repository, best
// first. Funding and sponsor links point at GitHub too but not at the code.
var pyRepoKeys = []string{"source", "source code", "repository", "code", "github", "homepage", "home", "issues", "bug tracker", "tracker", "changelog"}

func pyRepo(urls map[string]string, home string) string {
	byKey := map[string]string{}
	for k, v := range urls {
		byKey[strings.ToLower(strings.TrimSpace(k))] = v
	}
	for _, k := range pyRepoKeys {
		if r := githubRepo(byKey[k]); r != "" {
			return r
		}
	}
	return githubRepo(home)
}

func (py python) Bump(ctx context.Context, root string, d core.Dep, to, toName string) (*core.Bump, error) {
	m := d.Manifest
	if path.Base(m.Path) == "Pipfile" {
		return nil, fmt.Errorf("%s is read only: %w", m.Path, core.ErrNotSupported)
	}
	if err := lockUnsupported(m); err != nil {
		return nil, err
	}
	if toName != "" && !d.Direct {
		return nil, fmt.Errorf("%s: replacing %s, which only the lockfile names: %w", m.Path, d.Name, core.ErrNotSupported)
	}
	snap := snapFiles(root, m.Path, m.Lock)
	if d.Direct {
		if err := py.edit(root, m, d.Name, to, toName); err != nil {
			return nil, err
		}
	} else if m.Lock == "" {
		return nil, fmt.Errorf("%s has no lockfile for %s: %w", m.Path, d.Name, core.ErrNotSupported)
	}
	edited := len(snap.changed(root)) > 0
	if m.Lock == "" {
		return &core.Bump{Files: snap.changed(root), Note: "no lockfile"}, nil
	}
	tool := pyLockTool(root, m)
	if !have(tool) {
		return nil, errors.Join(core.ToolMissing{Tool: tool}, snap.restore(root))
	}
	exact := d.Direct && toName == "" && tool == "poetry"
	targeted := !edited && !exact
	line := pyLockLine(ctx, root, m, cmp.Or(toName, d.Name), to, targeted)
	// Poetry's update takes the newest release the ranges allow, not the
	// one tend chose; what a targeted run locked is checked against it, as
	// a floor, for every lock that can be read back.
	check := targeted && slices.Contains([]string{"uv.lock", "poetry.lock"}, path.Base(m.Lock))
	var before map[string]bool
	if check {
		var err error
		if before, err = lockedVersions(root, m.Lock, core.Pip, d.Name); err != nil {
			return nil, err
		}
	}
	if exact {
		if err := py.lockAt(ctx, root, m, d.Name, to, line); err != nil {
			return nil, err
		}
	}
	if err := runIn(ctx, root, path.Dir(m.Path), nil, line); err != nil {
		return nil, err
	}
	landed := ""
	if check {
		var err error
		if landed, err = lockedTo(root, m.Lock, core.Pip, d.Name, to, before); err != nil {
			return nil, errors.Join(err, snap.restore(root))
		}
	}
	return &core.Bump{Files: snap.changed(root), Locked: true, Landed: landed}, nil
}

// lockAt locks name at exactly to with Poetry, which takes no version on the
// command line: the requirement reads ==to for one pass, then goes back to
// the range, which the locked to satisfies and the next pass keeps.
func (python) lockAt(ctx context.Context, root string, m core.Manifest, name, to, line string) error {
	p := filepath.Join(root, filepath.FromSlash(m.Path))
	src, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	entries, err := pyEntries(root, m)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if PyNorm(e.name) != PyNorm(name) || e.raw == "" {
			continue
		}
		exact := e.raw[:e.specAt] + "==" + to + e.raw[e.specAt+len(e.spec):]
		if err := rewriteLine(p, e.line, e.raw, exact); err != nil {
			return err
		}
	}
	err = runIn(ctx, root, path.Dir(m.Path), nil, line)
	if werr := writeKeepMode(p, src); err == nil {
		err = werr
	}
	return err
}

// edit rewrites every requirement on name in the manifest so it admits to.
// Requirements that already do are left as written. Nothing is written
// unless every requirement can be rewritten.
func (python) edit(root string, m core.Manifest, name, to, toName string) error {
	entries, err := pyEntries(root, m)
	if err != nil {
		return err
	}
	type change struct {
		line     int
		from, to string
	}
	var todo []change
	for _, e := range entries {
		if PyNorm(e.name) != PyNorm(name) {
			continue
		}
		raw, err := pyRewrite(e, to, toName)
		if err != nil {
			return fmt.Errorf("%s:%d: %w", m.Path, e.line, err)
		}
		if e.hashed && m.Lock == "" && raw != e.raw {
			return fmt.Errorf("%s:%d: %s is pinned by hash: %w", m.Path, e.line, e.name, core.ErrNotSupported)
		}
		todo = append(todo, change{e.line, e.raw, raw})
	}
	if len(todo) == 0 {
		return fmt.Errorf("%s: no requirement for %s", m.Path, name)
	}
	p := filepath.Join(root, filepath.FromSlash(m.Path))
	for _, c := range todo {
		if c.from == c.to {
			continue
		}
		if err := rewriteLine(p, c.line, c.from, c.to); err != nil {
			return err
		}
	}
	return nil
}

func pyRewrite(e pyEntry, to, toName string) (string, error) {
	if e.raw == "" {
		return "", fmt.Errorf("cannot locate %s as written", e.name)
	}
	spec, err := pyNewSpec(e.spec, to)
	if err != nil {
		return "", err
	}
	end := e.specAt + len(e.spec)
	if toName == "" {
		return e.raw[:e.specAt] + spec + e.raw[end:], nil
	}
	if e.poetry {
		return "", fmt.Errorf("replacing a Poetry table entry: %w", core.ErrNotSupported)
	}
	if spec == e.spec || spec == "" {
		spec = ">=" + to
	}
	return e.raw[:e.nameAt] + toName + e.raw[e.nameEnd:e.specAt] + spec + e.raw[end:], nil
}

func pyLockTool(root string, m core.Manifest) string {
	switch path.Base(m.Lock) {
	case "uv.lock":
		return "uv"
	case "poetry.lock":
		return "poetry"
	case "pdm.lock":
		return "pdm"
	}
	if pyCompiledBy(root, m.Lock) == "uv" {
		return "uv"
	}
	return "pip-compile"
}

// pyLockLine is the command that regenerates m's lock with name at to. uv and
// pip-compile take the version on the command line; for Poetry, targeted
// moves only the named package within what the manifest allows.
func pyLockLine(ctx context.Context, root string, m core.Manifest, name, to string, targeted bool) string {
	q := shArg(name)
	pin := shArg(name + "==" + to)
	switch path.Base(m.Lock) {
	case "uv.lock":
		return "uv lock --upgrade-package " + pin
	case "poetry.lock":
		if targeted {
			return "poetry update --lock " + q
		}
		return poetryLock(ctx, root, path.Dir(m.Path))
	}
	in, out := path.Base(m.Path), memberDir(path.Dir(m.Path), m.Lock)
	if pyCompiledBy(root, m.Lock) == "uv" {
		return "uv pip compile --quiet --upgrade-package " + pin + " " + shArg(in) + " -o " + shArg(out)
	}
	return "pip-compile --quiet --upgrade-package " + pin + " " + shArg(in) + " -o " + shArg(out)
}

// poetryLock keeps other locked versions where they are. Poetry 2 does that
// by default and dropped the --no-update flag Poetry 1 needs for it.
func poetryLock(ctx context.Context, root, dir string) string {
	out, err := run(ctx, filepath.Join(root, filepath.FromSlash(dir)), nil, "poetry --version")
	if err != nil {
		return "poetry lock --no-update"
	}
	_, v, _ := strings.Cut(out, "version ")
	if n, _ := strconv.Atoi(strings.SplitN(strings.TrimSpace(v), ".", 2)[0]); n >= 2 {
		return "poetry lock"
	}
	return "poetry lock --no-update"
}

func (python) Commands(root string, m core.Manifest) core.Commands {
	dir := pyProjDir(root, m)
	install, py := "python3 -m venv .venv && .venv/bin/pip install "+pyPipArgs(root, m), pyRun(m)
	switch m.Tool {
	case "uv":
		install = "uv sync --frozen"
	case "poetry":
		install = "poetry install"
	case "pipenv":
		install = "pipenv sync --dev"
	case "pdm":
		install = "pdm install"
		if m.Lock != "" {
			install = "pdm sync"
		}
	}
	c := core.Commands{Install: []string{inDir(dir, install)}}
	if t := pyTestLine(root, m); t != "" {
		c.Test = []string{inDir(dir, py+" -m "+t)}
	}
	return c
}

// pyRun is the interpreter a manifest's tool runs the project's code with.
func pyRun(m core.Manifest) string {
	switch m.Tool {
	case "uv":
		return "uv run python"
	case "poetry":
		return "poetry run python"
	case "pipenv":
		return "pipenv run python"
	case "pdm":
		return "pdm run python"
	}
	return ".venv/bin/python"
}

func pyPipArgs(root string, m core.Manifest) string {
	dir := path.Dir(m.Path)
	if path.Base(m.Path) == "pyproject.toml" {
		var doc pyProjectFile
		if readTOML(filepath.Join(root, filepath.FromSlash(m.Path)), &doc) != nil {
			return "-e ."
		}
		var extras []string
		for _, g := range slices.Sorted(maps.Keys(doc.Project.Optional)) {
			if pyDevGroups[PyNorm(g)] {
				extras = append(extras, g)
			}
		}
		if len(extras) == 0 {
			return "-e ."
		}
		return "-e " + shArg(".["+strings.Join(extras, ",")+"]")
	}
	if path.Base(m.Path) == "setup.cfg" {
		return "-e ."
	}
	proj := pyProjDir(root, m)
	file := cmp.Or(m.Lock, m.Path)
	res := "-r " + shArg(memberDir(proj, file))
	if dev := path.Join(proj, "requirements-dev.txt"); dev != file && onDisk(root, dev) {
		res += " -r requirements-dev.txt"
	}
	if pyInstallable(root, proj) && (proj != dir || onDisk(root, path.Join(proj, "src"))) {
		res += " -e ."
	}
	return res
}

// pyProjMarks are what makes a directory a Python project's: its build
// files, its tests, its pytest configuration.
var pyProjMarks = []string{"setup.py", "setup.cfg", "pyproject.toml", "tests", "test", "pytest.ini", "tox.ini", "conftest.py"}

// pyProjDir is the directory a requirements file's project runs from: the
// file's own, or the nearest one above it that holds the project, as for
// requirements/tests.txt next to setup.py and tests/. Other manifests sit
// in theirs.
func pyProjDir(root string, m core.Manifest) string {
	dir := path.Dir(m.Path)
	if b := path.Base(m.Path); b == "pyproject.toml" || b == "Pipfile" || b == "setup.cfg" {
		return dir
	}
	for d := dir; ; d = path.Dir(d) {
		if slices.ContainsFunc(pyProjMarks, func(f string) bool { return onDisk(root, path.Join(d, f)) }) {
			return d
		}
		if d == "." || d == "/" {
			return dir
		}
	}
}

// pyInstallable says whether dir holds a package pip can install with -e.
func pyInstallable(root, dir string) bool {
	if onDisk(root, path.Join(dir, "setup.py")) || onDisk(root, path.Join(dir, "setup.cfg")) {
		return true
	}
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path.Join(dir, "pyproject.toml"))))
	return err == nil && (bytes.Contains(b, []byte("[project]")) || bytes.Contains(b, []byte("[build-system]")))
}

// pyTestLine picks pytest when the project configures or depends on it, and
// unittest discovery when there are test files but no pytest.
func pyTestLine(root string, m core.Manifest) string {
	dir := filepath.Join(root, filepath.FromSlash(pyProjDir(root, m)))
	if pyUsesPytest(root, m, dir) {
		return "pytest"
	}
	if hits, _ := filepath.Glob(filepath.Join(dir, "tests", "*.py")); len(hits) > 0 {
		if onDisk(dir, filepath.Join("tests", "__init__.py")) {
			return "unittest discover -s tests -t ."
		}
		return "unittest discover -s tests"
	}
	if hits, _ := filepath.Glob(filepath.Join(dir, "test*.py")); len(hits) > 0 {
		return "unittest discover"
	}
	return ""
}

func pyUsesPytest(root string, m core.Manifest, dir string) bool {
	for _, f := range []string{"pytest.ini", "conftest.py", filepath.Join("tests", "conftest.py")} {
		if onDisk(dir, f) {
			return true
		}
	}
	for f, section := range map[string]string{"pyproject.toml": "[tool.pytest", "setup.cfg": "[tool:pytest]", "tox.ini": "[pytest]"} {
		if b, err := os.ReadFile(filepath.Join(dir, f)); err == nil && strings.Contains(string(b), section) {
			return true
		}
	}
	entries, _ := pyEntries(root, m)
	for _, e := range entries {
		if PyNorm(e.name) == "pytest" {
			return true
		}
	}
	return false
}

// pyNewSpec rewrites a version specifier so that it admits to, keeping its
// style: ==1.0 -> ==2.0, >=1,<2 -> >=2.1,<3 (when 2.1 is outside), ~=1.4 ->
// ~=2.0, and Poetry's ^1.2 -> ^2.0 and 2.* -> 3.*. A specifier that already admits to comes
// back unchanged.
func pyNewSpec(spec, to string) (string, error) {
	if _, ok := ver.Parse(to); !ok {
		return "", fmt.Errorf("invalid version %q", to)
	}
	if strings.TrimSpace(spec) == "" || pySatisfies(spec, to) {
		return spec, nil
	}
	if strings.Contains(spec, "|") {
		return "", fmt.Errorf("cannot rewrite %q to admit %s: %w", spec, to, core.ErrNotSupported)
	}
	clauses, sep := strings.Split(spec, ","), ","
	if cs := pyClauses(spec); len(clauses) == 1 && len(cs) > 1 {
		clauses, sep = cs, " " // Poetry: >=1.2 <2.0
	}
	for i, c := range clauses {
		t := strings.TrimSpace(c)
		op, v := pySplitOp(t)
		gap := t[len(op) : len(t)-len(v)]
		nop, nv := pyNewClause(op, v, to)
		at := strings.Index(c, t)
		clauses[i] = c[:at] + nop + gap + nv + c[at+len(t):]
	}
	res := strings.Join(clauses, sep)
	if !pySatisfies(res, to) {
		return "", fmt.Errorf("cannot rewrite %q to admit %s: %w", spec, to, core.ErrNotSupported)
	}
	return res, nil
}

// pyNewClause rewrites one clause of a specifier that, as a whole, keeps to
// out. Lower bounds move up to to, so the old floor (perhaps the very version
// being fixed) is no longer allowed; upper bounds move only when they are in
// the way, and exclusions stay.
func pyNewClause(op, v, to string) (string, string) {
	switch op {
	case "<", "<=", "!=":
		if pyClause(op, v, to) {
			return op, v
		}
	}
	switch op {
	case "===":
		return op, to
	case "", "==":
		if prefix, ok := strings.CutSuffix(v, ".*"); ok {
			return op, pyTrimTo(prefix, to, true) + ".*"
		}
		if op == "" {
			return op, to
		}
		return op, pyTrimTo(v, to, false)
	case ">", ">=":
		return ">=", pyTrimTo(v, to, false)
	case "<":
		return op, pyNextMajor(v, to)
	case "~=":
		return op, pyCompatible(v, to)
	case "^", "~":
		return op, pyTrimTo(v, to, false)
	case "!=":
		return op, v
	}
	return op, to
}

var pyOps = []string{"===", "==", "!=", "~=", ">=", "<=", ">", "<", "^", "~"}

func pySplitOp(s string) (op, v string) {
	for _, o := range pyOps {
		if rest, ok := strings.CutPrefix(s, o); ok {
			return o, strings.TrimSpace(rest)
		}
	}
	return "", s
}

// pyTrimTo writes to with as many parts as old had, when dropping the rest
// loses nothing (old ~=1.4 and to 2.0.0 give 2.0) or when the caller asks for
// a prefix; otherwise it writes to in full.
func pyTrimTo(old, to string, prefix bool) string {
	k := len(strings.Split(old, "."))
	v, ok := ver.Parse(to)
	if !ok || v.Pre != "" || len(v.Nums) <= k {
		return to
	}
	for _, n := range v.Nums[k:] {
		if n != 0 && !prefix {
			return to
		}
	}
	parts := make([]string, k)
	for i := range parts {
		parts[i] = strconv.Itoa(v.Nums[i])
	}
	return strings.Join(parts, ".")
}

// pyCompatible rewrites a ~= clause without narrowing it: ~=1.4 admits any
// 1.x from 1.4, so 2.1.3 gives ~=2.1,>=2.1.3, the 2.x series from the fixed
// release on, and 2.0.0 gives plain ~=2.0.
func pyCompatible(old, to string) string {
	k := len(strings.Split(old, "."))
	v, ok := ver.Parse(to)
	if !ok || k < 2 || len(v.Nums) <= k {
		return to
	}
	parts := make([]string, k)
	for i := range parts {
		parts[i] = strconv.Itoa(v.Nums[i])
	}
	head := strings.Join(parts, ".")
	if h, _ := ver.Parse(head); ver.Compare(h, v) == 0 {
		return head
	}
	if res := head + ",>=" + to; pySatisfies("~="+res, to) {
		return res
	}
	return to
}

// pyNextMajor is the next major after to, written with as many parts as old.
func pyNextMajor(old, to string) string {
	v, _ := ver.Parse(to)
	parts := make([]string, len(strings.Split(old, ".")))
	for i := range parts {
		parts[i] = "0"
	}
	parts[0] = strconv.Itoa(v.Major() + 1)
	return strings.Join(parts, ".")
}

// pySatisfies reports whether version meets a PEP 440 specifier or a Poetry
// constraint (^, ~, bare versions, || unions, space separated clauses).
func pySatisfies(spec, version string) bool {
	if _, ok := ver.Parse(version); !ok {
		return false
	}
	for _, alt := range strings.Split(strings.ReplaceAll(spec, "||", "|"), "|") {
		ok := true
		for _, c := range pyClauses(alt) {
			op, v := pySplitOp(c)
			if !pyClause(op, v, version) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func pyClauses(s string) []string {
	var res []string
	for _, part := range strings.Split(s, ",") {
		for _, f := range strings.Fields(part) {
			if n := len(res); n > 0 && strings.Trim(res[n-1], "<>=!~^") == "" {
				res[n-1] += f
				continue
			}
			res = append(res, f)
		}
	}
	return res
}

func pyClause(op, want, version string) bool {
	if want == "*" || want == "" && op == "" {
		return true
	}
	v, _ := ver.Parse(version)
	if op == "===" {
		return want == version
	}
	if prefix, ok := strings.CutSuffix(want, ".*"); ok {
		w, _ := ver.Parse(prefix)
		match := pyPrefix(w.Nums, v.Nums)
		return (op == "==" || op == "") && match || op == "!=" && !match
	}
	w, ok := ver.Parse(want)
	if !ok {
		return false
	}
	n := ver.Compare(v, w)
	switch op {
	case "", "==":
		return n == 0
	case "!=":
		return n != 0
	case ">=":
		return n >= 0
	case "<=":
		return n <= 0
	case ">":
		return n > 0
	case "<":
		return n < 0
	case "~=":
		return n >= 0 && len(w.Nums) > 1 && pyPrefix(w.Nums[:len(w.Nums)-1], v.Nums)
	case "^":
		i := slices.IndexFunc(w.Nums, func(x int) bool { return x != 0 })
		if i < 0 {
			i = len(w.Nums) - 1
		}
		return n >= 0 && pyBelow(v, w.Nums, i)
	case "~":
		return n >= 0 && pyBelow(v, w.Nums, min(1, len(w.Nums)-1))
	}
	return false
}

// pyBelow reports whether v is below nums with the part at i raised by one
// and everything after it dropped.
func pyBelow(v ver.V, nums []int, i int) bool {
	upper := slices.Clone(nums[:i+1])
	upper[i]++
	return ver.Compare(v, ver.V{Nums: upper, Pre: "0"}) < 0
}

func pyPrefix(prefix, nums []int) bool {
	for i, p := range prefix {
		x := 0
		if i < len(nums) {
			x = nums[i]
		}
		if x != p {
			return false
		}
	}
	return true
}

func pyNameRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.'
}
