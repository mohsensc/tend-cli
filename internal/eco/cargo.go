package eco

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/ver"
)

type cargo struct{}

func (cargo) Name() string { return core.Cargo }

type cargoFile struct {
	Workspace *struct {
		Members      []string
		Dependencies map[string]any
	}
	Dependencies      map[string]any
	DevDependencies   map[string]any `toml:"dev-dependencies"`
	BuildDependencies map[string]any `toml:"build-dependencies"`
	Target            map[string]struct {
		Dependencies      map[string]any
		DevDependencies   map[string]any `toml:"dev-dependencies"`
		BuildDependencies map[string]any `toml:"build-dependencies"`
	}
}

type cargoSection struct {
	path []string
	dev  bool
	deps map[string]any
}

func (f cargoFile) sections() []cargoSection {
	res := []cargoSection{
		{[]string{"dependencies"}, false, f.Dependencies},
		{[]string{"dev-dependencies"}, true, f.DevDependencies},
		{[]string{"build-dependencies"}, false, f.BuildDependencies},
	}
	if f.Workspace != nil {
		res = append(res, cargoSection{[]string{"workspace", "dependencies"}, false, f.Workspace.Dependencies})
	}
	names := make([]string, 0, len(f.Target))
	for k := range f.Target {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		t := f.Target[k]
		res = append(res,
			cargoSection{[]string{"target", k, "dependencies"}, false, t.Dependencies},
			cargoSection{[]string{"target", k, "dev-dependencies"}, true, t.DevDependencies},
			cargoSection{[]string{"target", k, "build-dependencies"}, false, t.BuildDependencies},
		)
	}
	return res
}

func (cargo) Detect(root string) ([]core.Manifest, error) {
	files, err := walk(root, "Cargo.toml")
	if err != nil {
		return nil, err
	}
	locks := map[string]string{}
	for _, f := range files {
		lock := path.Join(path.Dir(f), "Cargo.lock")
		if onDisk(root, lock) {
			locks[f] = lock
		}
	}
	// a member has no lockfile of its own; the workspace root holds it
	res := make([]core.Manifest, 0, len(files))
	for _, f := range files {
		res = append(res, core.Manifest{Ecosystem: core.Cargo, Path: f, Lock: locks[f], Tool: "cargo"})
	}
	for _, r := range files {
		var cf cargoFile
		if readTOML(filepath.Join(root, filepath.FromSlash(r)), &cf) != nil || cf.Workspace == nil {
			continue
		}
		base := path.Dir(r)
		for i, f := range files {
			if f == r || !cargoMember(base, path.Dir(f), cf.Workspace.Members) {
				continue
			}
			res[i].Lock = locks[r]
		}
	}
	return res, nil
}

func cargoMember(base, dir string, pats []string) bool {
	rel, ok := strings.CutPrefix(dir, base+"/")
	if base == "." {
		rel, ok = dir, true
	}
	if !ok {
		return false
	}
	for _, p := range pats {
		if m, _ := path.Match(path.Clean(p), rel); m {
			return true
		}
	}
	return false
}

type cargoDep struct {
	key  string
	name string
	req  string
	line int
	dev  bool
}

func (cargo) Deps(root string, m core.Manifest) ([]core.Dep, error) {
	locked, err := cargoLocked(root, m.Lock)
	if err != nil {
		return nil, err
	}
	res, err := cargoDirect(root, m, locked)
	if err != nil {
		return nil, err
	}
	// a workspace shares one lockfile; only the manifest next to it lists the rest
	if m.Lock == "" || path.Dir(m.Lock) != path.Dir(m.Path) {
		return res, nil
	}
	used := map[string]bool{}
	mark := func(ds []core.Dep) {
		for _, d := range ds {
			used[d.Name+"@"+d.Version] = true
		}
	}
	mark(res)
	others, _ := (cargo{}).Detect(root)
	for _, o := range others {
		if o.Path != m.Path && o.Lock == m.Lock {
			if ds, err := cargoDirect(root, o, locked); err == nil {
				mark(ds)
			}
		}
	}
	var rest []core.Dep
	for name, vs := range locked {
		for _, v := range vs {
			if !used[name+"@"+v] {
				rest = append(rest, core.Dep{Ecosystem: core.Cargo, Name: name, Version: v, Manifest: m})
			}
		}
	}
	sortDeps(rest)
	return append(res, rest...), nil
}

func cargoDirect(root string, m core.Manifest, locked map[string][]string) ([]core.Dep, error) {
	p := filepath.Join(root, filepath.FromSlash(m.Path))
	var f cargoFile
	if err := readTOML(p, &f); err != nil {
		return nil, err
	}
	lines, err := readLines(p)
	if err != nil {
		return nil, err
	}
	ws, _ := cargoWorkspace(root, m.Path)
	var found []cargoDep
	for _, s := range f.sections() {
		for key, v := range s.deps {
			name, req, inherit, ok := cargoSpec(key, v)
			if inherit && ws != nil {
				name, req, _, ok = cargoSpec(key, ws[key])
			}
			if !ok || req == "" {
				continue
			}
			found = append(found, cargoDep{key, name, req, cargoKeyLine(lines, s.path, key), s.dev})
		}
	}
	sort.SliceStable(found, func(i, j int) bool {
		a, b := found[i], found[j]
		if a.line != b.line && a.line != 0 && b.line != 0 {
			return a.line < b.line
		}
		if a.line != b.line {
			return a.line != 0
		}
		return a.key < b.key
	})
	res := make([]core.Dep, 0, len(found))
	for _, d := range found {
		res = append(res, core.Dep{
			Ecosystem: core.Cargo, Name: d.name, Version: pickVersion(d.req, locked[d.name]), Req: d.req,
			Manifest: m, Line: d.line, Dev: d.dev, Direct: true,
		})
	}
	return res, nil
}

func sortDeps(ds []core.Dep) {
	sort.Slice(ds, func(i, j int) bool {
		if ds[i].Name != ds[j].Name {
			return ds[i].Name < ds[j].Name
		}
		return ver.Less(ds[i].Version, ds[j].Version)
	})
}

func cargoSpec(key string, v any) (name, req string, inherit, ok bool) {
	switch x := v.(type) {
	case string:
		return key, x, false, true
	case map[string]any:
		if b, _ := x["workspace"].(bool); b {
			return key, "", true, true
		}
		for _, k := range []string{"path", "git", "registry"} {
			if _, has := x[k]; has {
				return "", "", false, false
			}
		}
		req, _ = x["version"].(string)
		name = key
		if real, _ := x["package"].(string); real != "" {
			name = real
		}
		return name, req, false, req != ""
	}
	return "", "", false, false
}

func cargoWorkspace(root, p string) (map[string]any, string) {
	for dir := path.Dir(p); ; dir = path.Dir(dir) {
		cand := path.Join(dir, "Cargo.toml")
		var f cargoFile
		if readTOML(filepath.Join(root, filepath.FromSlash(cand)), &f) == nil && f.Workspace != nil {
			return f.Workspace.Dependencies, cand
		}
		if dir == "." {
			return nil, ""
		}
	}
}

var (
	tomlHeader = regexp.MustCompile(`^\s*\[\s*([^\[\]]+?)\s*\]\s*(#.*)?$`)
	tomlKey    = regexp.MustCompile(`^\s*(?:"([^"]+)"|'([^']+)'|([A-Za-z0-9_-]+))\s*[.=]`)
)

func tomlKeyName(m []string) string {
	for _, s := range m[1:4] {
		if s != "" {
			return s
		}
	}
	return ""
}

func splitKey(s string) []string {
	var res []string
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
			cur.WriteByte(c)
		case c == '"' || c == '\'':
			quote = c
		case c == '.':
			res = append(res, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	return append(res, strings.TrimSpace(cur.String()))
}

func cargoKeyLine(lines []string, tbl []string, key string) int {
	var cur []string
	for i, l := range lines {
		if m := tomlHeader.FindStringSubmatch(l); m != nil {
			cur = splitKey(m[1])
			if slices.Equal(cur, append(slices.Clone(tbl), key)) {
				return i + 1
			}
			continue
		}
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "[[") && strings.HasSuffix(t, "]]") {
			cur = nil
			continue
		}
		if !slices.Equal(cur, tbl) {
			continue
		}
		if m := tomlKey.FindStringSubmatch(l); m != nil && tomlKeyName(m) == key {
			return i + 1
		}
	}
	return 0
}

func readLines(p string) ([]string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return strings.Split(string(b), "\n"), nil
}

// cargoLocked is every version Cargo.lock pins, by crate.
func cargoLocked(root, lock string) (map[string][]string, error) {
	ls, err := readLocked(root, lock)
	if err != nil {
		return nil, err
	}
	res := map[string][]string{}
	for _, l := range ls {
		res[l.Name] = append(res[l.Name], l.Version)
	}
	return res, nil
}

type cargoPkg struct {
	Name, Version, Source string
	Dependencies          []string // "name", or "name version" when two versions are locked
}

// cargoPackages reads Cargo.lock: the packages that come from a registry.
func cargoPackages(src []byte) ([]cargoPkg, error) {
	var lf struct{ Package []cargoPkg }
	if err := tomlDecode(src, &lf); err != nil {
		return nil, err
	}
	var res []cargoPkg
	for _, p := range lf.Package {
		if strings.HasPrefix(p.Source, "registry+") || strings.HasPrefix(p.Source, "sparse+") {
			res = append(res, p)
		}
	}
	return res, nil
}

func pickVersion(req string, vs []string) string {
	best, top := "", ""
	for _, v := range vs {
		if top == "" || ver.Less(top, v) {
			top = v
		}
		pv, ok := ver.Parse(v)
		if ok && admits(req, pv, true) && (best == "" || ver.Less(best, v)) {
			best = v
		}
	}
	if best == "" {
		return top
	}
	return best
}

func (cargo) Meta(ctx context.Context, f core.Fetcher, name string) (*core.Meta, error) {
	var doc struct {
		Crate struct {
			Repository string `json:"repository"`
			MaxStable  string `json:"max_stable_version"`
		} `json:"crate"`
		Versions []struct {
			Num       string    `json:"num"`
			Yanked    bool      `json:"yanked"`
			CreatedAt time.Time `json:"created_at"`
		} `json:"versions"`
	}
	// crates.io turns away requests without a User-Agent; fetch.Client always sends one.
	ok, err := getDoc(ctx, f, "https://crates.io/api/v1/crates/"+name, &doc)
	if err != nil {
		return nil, fmt.Errorf("cargo: %s: %w", name, err)
	}
	if !ok {
		return nil, fmt.Errorf("cargo: %s: not found on crates.io", name)
	}
	res := &core.Meta{Name: name, Latest: doc.Crate.MaxStable, Repo: cleanRepo(doc.Crate.Repository)}
	for _, v := range doc.Versions {
		res.Releases = append(res.Releases, core.Release{Version: v.Num, Time: v.CreatedAt, Pre: semverPre(v.Num), Yanked: v.Yanked})
	}
	sortReleases(res.Releases)
	return res, nil
}

func (cargo) Bump(ctx context.Context, root string, d core.Dep, to, toName string) (*core.Bump, error) {
	if !d.Direct {
		if toName != "" {
			return nil, fmt.Errorf("cargo: %s: cannot swap a transitive crate: %w", d.Name, core.ErrNotSupported)
		}
		return cargoLock(ctx, root, d, to, nil)
	}
	e, err := cargoEntry(root, d)
	if err != nil {
		return nil, err
	}
	if toName != "" {
		return cargoSwap(ctx, root, d, e, to, toName)
	}
	next, err := fixReq(d.Req, to, true)
	if err != nil {
		return nil, fmt.Errorf("cargo: %s: %w", d.Name, err)
	}
	var files []string
	if next != d.Req {
		if err := e.setReq(root, d.Req, next); err != nil {
			return nil, fmt.Errorf("cargo: %s: %w", d.Name, err)
		}
		files = append(files, e.file)
	}
	return cargoLock(ctx, root, d, to, files)
}

func cargoLock(ctx context.Context, root string, d core.Dep, to string, files []string) (*core.Bump, error) {
	res := &core.Bump{Files: files}
	switch {
	case d.Manifest.Lock == "":
		res.Note = "no Cargo.lock"
		return res, nil
	case !have("cargo"):
		res.Note = core.ToolMissing{Tool: "cargo"}.Error()
		return res, nil
	}
	spec := d.Name
	locked, err := cargoLocked(root, d.Manifest.Lock)
	if err != nil {
		return nil, err
	}
	if len(locked[d.Name]) > 1 && d.Version != "" {
		spec += "@" + d.Version
	}
	line := "cargo update -p " + shArg(spec) + " --precise " + shArg(to)
	if err := runIn(ctx, root, path.Dir(d.Manifest.Path), nil, line); err != nil {
		return nil, fmt.Errorf("cargo: %s: %w", d.Name, err)
	}
	if err := lockLanded(root, d.Manifest.Lock, core.Cargo, d.Name, to); err != nil {
		return nil, fmt.Errorf("cargo: %w", err)
	}
	res.Locked = true
	res.Files = append(res.Files, d.Manifest.Lock)
	return res, nil
}

type cargoAt struct {
	file  string
	line  int
	table bool // the dependency has its own [dependencies.name] table
	key   string
}

func cargoEntry(root string, d core.Dep) (*cargoAt, error) {
	if d.Line < 1 {
		return nil, fmt.Errorf("cargo: %s: no line in %s", d.Name, d.Manifest.Path)
	}
	file := d.Manifest.Path
	lines, err := readLines(filepath.Join(root, filepath.FromSlash(file)))
	if err != nil {
		return nil, err
	}
	if d.Line > len(lines) {
		return nil, fmt.Errorf("cargo: %s: no line %d in %s", d.Name, d.Line, file)
	}
	text := lines[d.Line-1]
	e := &cargoAt{file: file, line: d.Line}
	if m := tomlHeader.FindStringSubmatch(text); m != nil {
		e.table = true
		e.key = splitKey(m[1])[len(splitKey(m[1]))-1]
		return e, nil
	}
	m := tomlKey.FindStringSubmatch(text)
	if m == nil {
		return nil, fmt.Errorf("cargo: %s: %s:%d is not a dependency", d.Name, file, d.Line)
	}
	e.key = tomlKeyName(m)
	if !workspaceRe.MatchString(text) {
		return e, nil
	}
	_, wsFile := cargoWorkspace(root, d.Manifest.Path)
	if wsFile == "" {
		return nil, fmt.Errorf("cargo: %s: no workspace declares it", d.Name)
	}
	wsLines, err := readLines(filepath.Join(root, filepath.FromSlash(wsFile)))
	if err != nil {
		return nil, err
	}
	at := cargoKeyLine(wsLines, []string{"workspace", "dependencies"}, e.key)
	if at == 0 {
		return nil, fmt.Errorf("cargo: %s: not in [workspace.dependencies] of %s", d.Name, wsFile)
	}
	return &cargoAt{file: wsFile, line: at, key: e.key, table: tomlHeader.MatchString(wsLines[at-1])}, nil
}

var workspaceRe = regexp.MustCompile(`\bworkspace\s*=\s*true\b`)

// setReq leaves a line alone that already carries next: a second member that
// inherits the same workspace entry comes after the first one edited it.
func (e *cargoAt) setReq(root, old, next string) error {
	p := filepath.Join(root, filepath.FromSlash(e.file))
	lines, err := readLines(p)
	if err != nil {
		return err
	}
	end := e.line
	if e.table {
		for end < len(lines) && !tomlHeader.MatchString(lines[end]) {
			end++
		}
	}
	quoted := func(req string) *regexp.Regexp {
		q := regexp.QuoteMeta(req)
		return regexp.MustCompile(`version\s*=\s*"` + q + `"|version\s*=\s*'` + q + `'|"` + q + `"|'` + q + `'`)
	}
	for i := e.line; i <= end && i <= len(lines); i++ {
		if from := quoted(old).FindString(lines[i-1]); from != "" {
			at := strings.LastIndex(from, old)
			return rewriteLine(p, i, from, from[:at]+next+from[at+len(old):])
		}
		if quoted(next).MatchString(lines[i-1]) {
			return nil
		}
	}
	return fmt.Errorf("%s:%d: requirement %q not found", e.file, e.line, old)
}

func cargoSwap(ctx context.Context, root string, d core.Dep, e *cargoAt, to, toName string) (*core.Bump, error) {
	p := filepath.Join(root, filepath.FromSlash(e.file))
	lines, err := readLines(p)
	if err != nil {
		return nil, err
	}
	text := lines[e.line-1]
	if e.table || e.file != d.Manifest.Path || workspaceRe.MatchString(text) {
		return nil, fmt.Errorf("cargo: %s: cannot swap this layout: %w", d.Name, core.ErrNotSupported)
	}
	indent := text[:len(text)-len(strings.TrimLeft(text, " \t"))]
	if err := rewriteLine(p, e.line, strings.TrimRight(text, "\r"), indent+toName+` = "`+to+`"`); err != nil {
		return nil, err
	}
	res := &core.Bump{Files: []string{e.file}}
	switch {
	case d.Manifest.Lock == "":
		res.Note = "no Cargo.lock"
	case !have("cargo"):
		res.Note = core.ToolMissing{Tool: "cargo"}.Error()
	default:
		// The new caret requirement admits releases past to; --precise holds it there.
		dir := path.Dir(d.Manifest.Path)
		if err := runIn(ctx, root, dir, nil, "cargo update --workspace"); err != nil {
			return nil, fmt.Errorf("cargo: %s: %w", d.Name, err)
		}
		if err := runIn(ctx, root, dir, nil, "cargo update -p "+shArg(toName)+" --precise "+shArg(to)); err != nil {
			return nil, fmt.Errorf("cargo: %s: %w", toName, err)
		}
		if err := lockLanded(root, d.Manifest.Lock, core.Cargo, toName, to); err != nil {
			return nil, fmt.Errorf("cargo: %w", err)
		}
		res.Locked = true
		res.Files = append(res.Files, d.Manifest.Lock)
	}
	return res, nil
}

func (cargo) Commands(root string, m core.Manifest) core.Commands {
	dir := path.Dir(m.Path)
	return core.Commands{
		Build: []string{inDir(dir, "cargo build")},
		Test:  []string{inDir(dir, "cargo test")},
	}
}

func (cargo) Traits() core.Traits {
	return core.Traits{OSV: "crates.io", Page: prefix("https://crates.io/crates/"), Transitive: true}
}

func (cargo) Imports(d core.Dep) []string {
	return []string{strings.ReplaceAll(d.Name, "-", "_")}
}
