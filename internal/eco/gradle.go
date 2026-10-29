package eco

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/ver"
)

type gradle struct{}

func (gradle) Name() string { return core.Gradle }

func (gradle) Detect(root string) ([]core.Manifest, error) {
	files, err := walk(root, "build.gradle", "build.gradle.kts", "*.versions.toml")
	if err != nil {
		return nil, err
	}
	var res []core.Manifest
	for _, f := range files {
		catalog := strings.HasSuffix(f, ".versions.toml")
		if catalog && path.Base(path.Dir(f)) != "gradle" {
			continue
		}
		m := core.Manifest{Ecosystem: core.Gradle, Path: f, Tool: "gradle"}
		dir := path.Dir(f)
		if catalog {
			dir = path.Dir(dir)
		}
		if lock := path.Join(dir, "gradle.lockfile"); onDisk(root, lock) {
			m.Lock = lock
		}
		res = append(res, m)
	}
	return res, nil
}

// gradleEntry is one dependency and where its version is written, which may
// be a variable, gradle.properties or the catalog's [versions] table.
type gradleEntry struct {
	group, artifact string
	version, req    string
	line            int // in the manifest
	decl            int // where a catalog library is declared, for ordering
	dev             bool
	file            string // repo-relative file holding the version text
	at              int    // its line there
	pre, suf        string // text around the version on that line
	notation        bool   // written as "group:artifact:version" in the manifest
	dynamic         bool   // a version Gradle resolves: 4.+, latest.release, [1.0,2.0)
}

func (e gradleEntry) name() string { return e.group + ":" + e.artifact }

func (gradle) Deps(root string, m core.Manifest) ([]core.Dep, error) {
	entries, err := gradleEntries(root, m)
	if err != nil {
		return nil, err
	}
	var locked map[string]string
	if slices.ContainsFunc(entries, func(e gradleEntry) bool { return e.dynamic }) {
		if locked, err = gradleLocked(root, gradleRoot(root, m)); err != nil {
			return nil, err
		}
	}
	var res []core.Dep
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.name()] {
			continue
		}
		seen[e.name()] = true
		version, req := e.version, e.req
		if e.dynamic {
			if version, req = locked[e.name()], e.version; version == "" {
				continue
			}
		}
		res = append(res, core.Dep{
			Ecosystem: core.Gradle,
			Name:      e.name(),
			Version:   version,
			Req:       req,
			Manifest:  m,
			Line:      e.line,
			Dev:       e.dev,
			Direct:    true,
		})
	}
	return res, nil
}

func gradleEntries(root string, m core.Manifest) ([]gradleEntry, error) {
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(m.Path)))
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(m.Path, ".toml") {
		return catalogEntries(root, m, src)
	}
	return buildEntries(root, m, src), nil
}

var (
	gradleNotation = regexp.MustCompile(`\b(?P<conf>[A-Za-z_]\w*)\s*\(?\s*(?:(?:platform|enforcedPlatform|testFixtures)\s*\(\s*)?(?P<q>["'])(?P<group>[^"'\s:$]+):(?P<artifact>[^"'\s:$]+):(?P<version>[^"'\s:@]+)(?P<rest>(?::[^"'\s@]*)?(?:@[\w.]+)?)["']`)
	gradleMapForm  = regexp.MustCompile(`\b(?P<conf>[A-Za-z_]\w*)\s*\(?\s*group\s*[:=]\s*["'](?P<group>[^"']+)["']\s*,\s*name\s*[:=]\s*["'](?P<artifact>[^"']+)["']\s*,\s*(?P<pre>version\s*[:=]\s*(?P<q>["']))(?P<version>[^"']+)["']`)
	gradleVarRef   = regexp.MustCompile(`^\$\{?([A-Za-z_][\w.]*)\}?$`)
)

var gradleNotDeps = map[string]bool{"module": true, "project": true, "println": true, "print": true, "uri": true, "file": true, "files": true}

// buildEntries reads "g:a:v" strings and group/name/version maps given to a
// configuration (implementation, api, testImplementation, ...) inside a
// dependencies block. Catalog references (libs.x) are reported by the
// catalog itself, and $var versions resolve through gradleVars.
func buildEntries(root string, m core.Manifest, src []byte) []gradleEntry {
	clean := gradleOnlyDeps(gradleClean(src))
	vars := gradleVars(root, m)
	var res []gradleEntry
	for _, re := range []*regexp.Regexp{gradleNotation, gradleMapForm} {
		for _, x := range re.FindAllSubmatchIndex(clean, -1) {
			at := func(name string) int { return x[2*re.SubexpIndex(name)] }
			g := func(name string) string {
				i := re.SubexpIndex(name)
				if i < 0 || x[2*i] < 0 {
					return ""
				}
				return string(src[x[2*i]:x[2*i+1]])
			}
			if gradleNotDeps[g("conf")] {
				continue
			}
			e := gradleEntry{group: g("group"), artifact: g("artifact"), req: g("version"), line: lineOf(src, at("version")), dev: gradleTestConf(g("conf"))}
			e.version, e.file, e.at, e.pre, e.suf = e.req, m.Path, e.line, g("pre"), g("q")
			if re == gradleNotation {
				e.notation, e.pre, e.suf = true, e.group+":"+e.artifact+":", g("rest")+g("q")
			}
			if gradleUseVar(&e, vars, g("q")) {
				e.dynamic = gradleDynamic(e.version)
				res = append(res, e)
			}
		}
	}
	slices.SortStableFunc(res, func(a, b gradleEntry) int { return cmp.Compare(a.line, b.line) })
	return res
}

// gradleUseVar points an entry written with $var at the variable's
// definition; a version it cannot find stays unknown. Single quotes do not
// interpolate in Groovy, and a computed version is not something to edit, so
// both leave the entry out.
func gradleUseVar(e *gradleEntry, vars map[string]gradleVar, quote string) bool {
	if !strings.Contains(e.req, "$") {
		return true
	}
	m := gradleVarRef.FindStringSubmatch(e.req)
	if m == nil || quote == "'" {
		return false
	}
	e.version, e.file = "", ""
	if v, ok := vars[strings.TrimPrefix(m[1], "project.")]; ok {
		e.version, e.file, e.at, e.pre, e.suf = v.val, v.file, v.line, v.pre, v.suf
	}
	return true
}

func gradleTestConf(conf string) bool {
	return strings.HasPrefix(conf, "test") || strings.Contains(conf, "Test")
}

type gradleVar struct {
	val, file, pre, suf string
	line                int
}

var (
	gradleAssign = regexp.MustCompile(`(?m)^[ \t]*(?:(?:def|val|var|const val)[ \t]+|(?:project\.)?ext\.)?([A-Za-z_]\w*)[ \t]*=[ \t]*(["'])([^"'$\s]+)["']`)
	gradleExtra  = regexp.MustCompile(`(?m)\bextra\[\s*"(\w+)"\s*\]\s*=\s*(")([^"$\s]+)"`)
)

// gradleVars collects version variables: assignments in the manifest and in
// the build's root script, then gradle.properties from the manifest's
// directory up. The first definition of a name wins.
func gradleVars(root string, m core.Manifest) map[string]gradleVar {
	res := map[string]gradleVar{}
	files := []string{m.Path}
	top := gradleRoot(root, m)
	for _, f := range []string{"build.gradle", "build.gradle.kts"} {
		if p := path.Join(top, f); p != m.Path && onDisk(root, p) {
			files = append(files, p)
		}
	}
	for _, f := range files {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		if err != nil {
			continue
		}
		clean := gradleClean(src)
		for _, re := range []*regexp.Regexp{gradleAssign, gradleExtra} {
			for _, x := range re.FindAllSubmatchIndex(clean, -1) {
				name := string(src[x[2]:x[3]])
				if _, ok := res[name]; !ok {
					q := string(src[x[4]:x[5]])
					res[name] = gradleVar{val: string(src[x[6]:x[7]]), file: f, line: lineOf(src, x[6]), pre: q, suf: q}
				}
			}
		}
	}
	for d := path.Dir(m.Path); ; d = path.Dir(d) {
		gradleProps(root, path.Join(d, "gradle.properties"), res)
		if d == "." {
			break
		}
	}
	return res
}

func gradleProps(root, rel string, vars map[string]gradleVar) {
	f, err := os.Open(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == '!' {
			continue
		}
		i := strings.IndexAny(line, "=:")
		if i < 0 {
			continue
		}
		k, v := strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:])
		if _, ok := vars[k]; !ok && k != "" && v != "" {
			vars[k] = gradleVar{val: v, file: rel, line: n}
		}
	}
}

// gradleClean blanks comments in a Groovy or Kotlin script, keeping every
// other byte and every newline where it was so offsets and lines still hold.
func gradleClean(src []byte) []byte {
	res := bytes.Clone(src)
	var quote byte
	for i := 0; i < len(res); i++ {
		c := res[i]
		switch {
		case quote != 0 && c == '\\':
			i++
		case quote != 0:
			if c == quote || c == '\n' {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '/' && i+1 < len(res) && res[i+1] == '/':
			for ; i < len(res) && res[i] != '\n'; i++ {
				res[i] = ' '
			}
		case c == '/' && i+1 < len(res) && res[i+1] == '*':
			end := bytes.Index(res[i+2:], []byte("*/"))
			stop := len(res)
			if end >= 0 {
				stop = i + 2 + end + 2
			}
			for ; i < stop; i++ {
				if res[i] != '\n' {
					res[i] = ' '
				}
			}
			i--
		}
	}
	return res
}

var (
	gradlePlugins = regexp.MustCompile(`\bplugins\s*\{`)
	gradleDepsRe  = regexp.MustCompile(`\bdependencies\s*\{`)
)

func gradleOnlyDeps(src []byte) []byte {
	keep := make([]bool, len(src))
	for _, x := range gradleDepsRe.FindAllIndex(src, -1) {
		depth := 0
		for i := x[1] - 1; i < len(src); i++ {
			switch src[i] {
			case '{':
				depth++
			case '}':
				depth--
			}
			keep[i] = true
			if depth == 0 {
				break
			}
		}
	}
	res := bytes.Clone(src)
	for i, k := range keep {
		if !k && res[i] != '\n' {
			res[i] = ' '
		}
	}
	return res
}

func gradleDynamic(v string) bool {
	return strings.HasSuffix(v, "+") || strings.HasPrefix(v, "latest.") || strings.HasPrefix(v, "[") || strings.HasPrefix(v, "(") || strings.HasPrefix(v, "]")
}

func gradleAdmits(spec, v string) bool {
	switch {
	case strings.HasPrefix(spec, "latest."), spec == "+":
		return true
	case strings.HasSuffix(spec, "+"):
		return strings.HasPrefix(v, strings.TrimSuffix(spec, "+"))
	}
	if len(spec) < 2 {
		return false
	}
	lo, hi, ok := strings.Cut(spec[1:len(spec)-1], ",")
	if !ok {
		return spec[0] == '[' && strings.TrimSpace(lo) == v
	}
	lo, hi = strings.TrimSpace(lo), strings.TrimSpace(hi)
	if lo != "" && (ver.Less(v, lo) || spec[0] != '[' && !ver.Less(lo, v)) {
		return false
	}
	end := spec[len(spec)-1]
	return hi == "" || ver.Less(v, hi) || end == ']' && !ver.Less(hi, v)
}

func gradleMove(spec, to string) (string, bool) {
	if !strings.HasSuffix(spec, ".+") {
		return "", false
	}
	n := strings.Count(strings.TrimSuffix(spec, ".+"), ".") + 1
	next := shape(to, n) + ".+"
	return next, gradleAdmits(next, to)
}

var gradleLockLine = regexp.MustCompile(`^([^:#=\s]+):([^:=\s]+):([^:=\s]+)=`)

func gradleLocked(root, top string) (map[string]string, error) {
	locks, err := gradleLocks(root, top)
	if err != nil {
		return nil, err
	}
	res := map[string]string{}
	for _, l := range locks {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(l)))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			m := gradleLockLine.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil {
				continue
			}
			name := m[1] + ":" + m[2]
			if cur := res[name]; cur == "" || ver.Less(cur, m[3]) {
				res[name] = m[3]
			}
		}
	}
	return res, nil
}

func gradleNoPlugins(src []byte) []byte {
	for _, x := range gradlePlugins.FindAllIndex(src, -1) {
		depth := 0
		for i := x[1] - 1; i < len(src); i++ {
			switch src[i] {
			case '{':
				depth++
			case '}':
				depth--
			}
			if depth == 0 {
				break
			}
			if src[i] != '\n' && i >= x[1] {
				src[i] = ' '
			}
		}
	}
	return src
}

type catalogFile struct {
	Versions  map[string]any `toml:"versions"`
	Libraries map[string]any `toml:"libraries"`
}

// catalogEntries reads a version catalog's [libraries]. A version.ref points
// the entry at the [versions] line, which every library sharing it follows.
func catalogEntries(root string, m core.Manifest, src []byte) ([]gradleEntry, error) {
	var cat catalogFile
	if err := tomlDecode(src, &cat); err != nil {
		return nil, fmt.Errorf("%s: %w", m.Path, err)
	}
	lines := tomlScan(src)
	confs := catalogUses(root, m)
	var res []gradleEntry
	for alias, v := range cat.Libraries {
		i := tomlFindKey(lines, "libraries", alias)
		if i < 0 {
			continue
		}
		e, ok := catalogEntry(lines, i, v, cat.Versions)
		if !ok {
			continue
		}
		e.file = m.Path
		if c := confs[PyNorm(alias)]; len(c) > 0 {
			e.dev = !slices.ContainsFunc(c, func(s string) bool { return !gradleTestConf(s) })
		}
		res = append(res, e)
	}
	slices.SortFunc(res, func(a, b gradleEntry) int { return cmp.Compare(a.decl, b.decl) })
	return res, nil
}

func catalogEntry(lines []tomlLine, i int, v any, versions map[string]any) (gradleEntry, bool) {
	e := gradleEntry{line: i + 1, at: i + 1, decl: i + 1}
	var module string
	var ver any
	switch x := v.(type) {
	case string:
		parts := strings.Split(x, ":")
		if len(parts) < 2 {
			return e, false
		}
		module = parts[0] + ":" + parts[1]
		if len(parts) == 3 {
			e.notation = true
			e.req, e.version = parts[2], parts[2]
			e.pre = module + ":"
			e.dynamic = gradleDynamic(e.version)
			return catalogModule(e, module)
		}
	case map[string]any:
		module, _ = x["module"].(string)
		if g, _ := x["group"].(string); g != "" {
			n, _ := x["name"].(string)
			module = g + ":" + n
		}
		ver = x["version"]
	}
	if ref, ok := ver.(map[string]any); ok && ref["ref"] != nil {
		name, _ := ref["ref"].(string)
		j := tomlFindKey(lines, "versions", name)
		if j < 0 {
			return e, false
		}
		e.req = name
		e.version = catalogVersion(versions[name])
		e.line, e.at = j+1, j+1
	} else {
		e.version = catalogVersion(ver)
		e.req = e.version
	}
	e.dynamic = gradleDynamic(e.version)
	if e.version != "" {
		q := `"`
		if strings.Contains(lines[e.at-1].text, `'`+e.version+`'`) {
			q = `'`
		}
		e.pre, e.suf = q, q
	}
	return catalogModule(e, module)
}

func catalogModule(e gradleEntry, module string) (gradleEntry, bool) {
	g, a, ok := strings.Cut(module, ":")
	e.group, e.artifact = g, a
	return e, ok && g != "" && a != ""
}

// catalogVersion reads a plain version or a rich one, preferring the
// strictest bound it states.
func catalogVersion(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case map[string]any:
		for _, k := range []string{"strictly", "require", "prefer"} {
			if s, ok := x[k].(string); ok {
				return s
			}
		}
	}
	return ""
}

var catalogRef = regexp.MustCompile(`\b([A-Za-z_]\w*)\s*\(?\s*(?:(?:platform|enforcedPlatform)\s*\(\s*)?([A-Za-z_]\w*)\.([A-Za-z0-9_.]+)`)

// catalogUses maps each library alias to the configurations the build
// scripts add it to, so a library only tests use counts as Dev.
func catalogUses(root string, m core.Manifest) map[string][]string {
	accessor := strings.TrimSuffix(path.Base(m.Path), ".versions.toml")
	top := path.Dir(path.Dir(m.Path))
	files, err := walk(root, "build.gradle", "build.gradle.kts")
	if err != nil {
		return nil
	}
	res := map[string][]string{}
	for _, f := range files {
		if top != "." && !strings.HasPrefix(f, top+"/") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		if err != nil {
			continue
		}
		for _, x := range catalogRef.FindAllSubmatch(gradleNoPlugins(gradleClean(src)), -1) {
			alias := string(x[3])
			if string(x[2]) != accessor || strings.HasPrefix(alias, "bundles.") || strings.HasPrefix(alias, "versions.") || strings.HasPrefix(alias, "plugins.") {
				continue
			}
			alias = PyNorm(alias)
			res[alias] = append(res[alias], string(x[1]))
		}
	}
	return res
}

func (gradle) Meta(ctx context.Context, f core.Fetcher, name string) (*core.Meta, error) {
	return mavenMeta(ctx, f, name, mavenCentral, googleMaven)
}

func (gradle) Bump(ctx context.Context, root string, d core.Dep, to, toName string) (*core.Bump, error) {
	m := d.Manifest
	entries, err := gradleEntries(root, m)
	if err != nil {
		return nil, err
	}
	var e *gradleEntry
	for i := range entries {
		if entries[i].name() == d.Name && (e == nil || entries[i].line == d.Line) {
			e = &entries[i]
		}
	}
	if e == nil {
		return nil, fmt.Errorf("%s: no dependency on %s", m.Path, d.Name)
	}
	if e.file == "" || e.version == "" {
		return nil, fmt.Errorf("%s: version %q of %s is not written in the repo: %w", m.Path, e.req, d.Name, core.ErrNotSupported)
	}
	top := gradleRoot(root, m)
	locks, err := gradleLocks(root, top)
	if err != nil {
		return nil, err
	}
	if e.dynamic {
		return gradleBumpDynamic(ctx, root, top, locks, *e, d, to, toName)
	}
	snap := snapFiles(root, append([]string{e.file}, locks...)...)
	p := filepath.Join(root, filepath.FromSlash(e.file))
	switch {
	case toName != "":
		g, a, ok := strings.Cut(toName, ":")
		if !e.notation || !ok || e.req != e.version {
			return nil, fmt.Errorf("%s: replacing %s with %s: %w", m.Path, d.Name, toName, core.ErrNotSupported)
		}
		err = rewriteLine(p, e.at, e.pre+e.version, g+":"+a+":"+to)
	case e.version != to:
		err = rewriteLine(p, e.at, e.pre+e.version+e.suf, e.pre+to+e.suf)
	}
	if err != nil {
		return nil, err
	}
	if len(locks) == 0 {
		return &core.Bump{Files: snap.changed(root), Note: "no lockfile"}, nil
	}
	tool := gradleTool(root, top)
	if tool == "gradle" && !have(tool) {
		files := snap.changed(root)
		if len(files) == 0 {
			return nil, core.ToolMissing{Tool: tool}
		}
		return &core.Bump{Files: files, Note: "gradle not found"}, nil
	}
	if err := runIn(ctx, root, top, nil, tool+" "+gradleLockTasks(top, locks)+" --write-locks"); err != nil {
		return nil, err
	}
	return &core.Bump{Files: snap.changed(root), Locked: true}, nil
}

func gradleBumpDynamic(ctx context.Context, root, top string, locks []string, e gradleEntry, d core.Dep, to, toName string) (*core.Bump, error) {
	m := d.Manifest
	if toName != "" || len(locks) == 0 {
		return nil, fmt.Errorf("%s: %s is at %s, which Gradle resolves: %w", m.Path, d.Name, e.version, core.ErrNotSupported)
	}
	next := e.version
	if !gradleAdmits(next, to) {
		moved, ok := gradleMove(next, to)
		if !ok {
			return nil, fmt.Errorf("%s: %s is at %s, which does not admit %s and cannot be moved: %w", m.Path, d.Name, e.version, to, core.ErrNotSupported)
		}
		next = moved
	}
	tool := gradleTool(root, top)
	if tool == "gradle" && !have(tool) {
		return nil, core.ToolMissing{Tool: tool}
	}
	snap := snapFiles(root, append([]string{e.file}, locks...)...)
	if next != e.version {
		if err := rewriteLine(filepath.Join(root, filepath.FromSlash(e.file)), e.at, e.pre+e.version+e.suf, e.pre+next+e.suf); err != nil {
			return nil, err
		}
	}
	if err := runIn(ctx, root, top, nil, tool+" "+gradleLockTasks(top, locks)+" --update-locks "+shArg(d.Name)); err != nil {
		_ = snap.restore(root)
		return nil, err
	}
	return &core.Bump{Files: snap.changed(root), Locked: true}, nil
}

func gradleLocks(root, top string) ([]string, error) {
	files, err := walk(root, "gradle.lockfile", "buildscript-gradle.lockfile", "*/gradle/dependency-locks/*.lockfile", "gradle/dependency-locks/*.lockfile")
	if err != nil {
		return nil, err
	}
	var res []string
	for _, f := range files {
		if top == "." || strings.HasPrefix(f, top+"/") {
			res = append(res, f)
		}
	}
	return res, nil
}

// gradleLockTasks names a dependencies task for every project that keeps a
// lockfile: --write-locks only writes the locks of the projects it resolves.
func gradleLockTasks(top string, locks []string) string {
	var res []string
	for _, l := range locks {
		dir := memberDir(top, path.Dir(l))
		dir, _, _ = strings.Cut(dir, "gradle/dependency-locks")
		dir = strings.Trim(dir, "/")
		task := "dependencies"
		if dir != "" && dir != "." {
			task = ":" + strings.ReplaceAll(dir, "/", ":") + ":dependencies"
		}
		if !slices.Contains(res, task) {
			res = append(res, task)
		}
	}
	return strings.Join(res, " ")
}

// gradleRoot is the build's root directory: the nearest one up from the
// manifest that has a settings script, else the manifest's own.
func gradleRoot(root string, m core.Manifest) string {
	start := path.Dir(m.Path)
	if strings.HasSuffix(m.Path, ".toml") {
		start = path.Dir(start)
	}
	for d := start; ; d = path.Dir(d) {
		if onDisk(root, path.Join(d, "settings.gradle")) || onDisk(root, path.Join(d, "settings.gradle.kts")) {
			return d
		}
		if d == "." {
			return start
		}
	}
}

func gradleTool(root, dir string) string {
	if onDisk(root, path.Join(dir, "gradlew")) {
		return "./gradlew"
	}
	return "gradle"
}

func (gradle) Commands(root string, m core.Manifest) core.Commands {
	dir := gradleRoot(root, m)
	tool := gradleTool(root, dir)
	return core.Commands{
		Build: []string{inDir(dir, tool+" build -x test")},
		Test:  []string{inDir(dir, tool+" test")},
	}
}

func (gradle) Traits() core.Traits {
	return core.Traits{OSV: "Maven", Page: centralPage, KeepSuffix: true}
}

func (gradle) Imports(d core.Dep) []string { return mavenImports(d.Name) }
