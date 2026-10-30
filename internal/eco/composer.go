package eco

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
)

type composer struct{}

func (composer) Name() string { return core.Composer }

// composerNS remembers the PSR-4 namespaces Deps found, for Imports.
var composerNS learned[string]

func (composer) Detect(root string) ([]core.Manifest, error) {
	files, err := walk(root, "composer.json")
	if err != nil {
		return nil, err
	}
	res := make([]core.Manifest, 0, len(files))
	for _, f := range files {
		lock := path.Join(path.Dir(f), "composer.lock")
		if !onDisk(root, lock) {
			lock = ""
		}
		res = append(res, core.Manifest{Ecosystem: core.Composer, Path: f, Lock: lock, Tool: "composer"})
	}
	return res, nil
}

type jsonPairs []struct{ key, val string }

func (p *jsonPairs) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return err
	}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return err
		}
		var v string
		if err := dec.Decode(&v); err != nil {
			return err
		}
		*p = append(*p, struct{ key, val string }{k.(string), v})
	}
	return nil
}

type composerLock struct {
	Packages    []composerPkg `json:"packages"`
	PackagesDev []composerPkg `json:"packages-dev"`
}

type composerPkg struct {
	Name     string            `json:"name"`
	Version  string            `json:"version"`
	Require  map[string]string `json:"require"`
	Autoload struct {
		PSR4 map[string]json.RawMessage `json:"psr-4"`
	} `json:"autoload"`
}

func (composer) Deps(root string, m core.Manifest) ([]core.Dep, error) {
	p := filepath.Join(root, filepath.FromSlash(m.Path))
	var mf struct {
		Require    jsonPairs `json:"require"`
		RequireDev jsonPairs `json:"require-dev"`
	}
	if err := readJSON(p, &mf); err != nil {
		return nil, err
	}
	lines, err := readLines(p)
	if err != nil {
		return nil, err
	}
	var lock composerLock
	if m.Lock != "" {
		if err := readJSON(filepath.Join(root, filepath.FromSlash(m.Lock)), &lock); err != nil {
			return nil, err
		}
	}
	locked := map[string]composerPkg{}
	dev := map[string]bool{}
	vendor := filepath.Join(root, filepath.FromSlash(path.Dir(m.Path)), "vendor")
	for _, l := range []struct {
		pkgs []composerPkg
		dev  bool
	}{{lock.Packages, false}, {lock.PackagesDev, true}} {
		for _, pkg := range l.pkgs {
			name := strings.ToLower(pkg.Name)
			locked[name], dev[name] = pkg, l.dev
			composerNS.add(name, composerVendorNS(vendor, name)...)
			composerNS.add(name, psr4(pkg)...)
		}
	}
	var res []core.Dep
	direct := map[string]bool{}
	for _, sec := range []struct {
		name  string
		pairs jsonPairs
		dev   bool
	}{{"require", mf.Require, false}, {"require-dev", mf.RequireDev, true}} {
		for _, kv := range sec.pairs {
			name := strings.ToLower(kv.key)
			if !strings.Contains(name, "/") {
				continue
			}
			direct[name] = true
			res = append(res, core.Dep{
				Ecosystem: core.Composer, Name: name, Version: locked[name].Version, Req: kv.val,
				Manifest: m, Line: jsonKeyLine(lines, sec.name, kv.key), Dev: sec.dev, Direct: true,
			})
		}
	}
	var rest []core.Dep
	for name, pkg := range locked {
		if !direct[name] {
			rest = append(rest, core.Dep{Ecosystem: core.Composer, Name: name, Version: pkg.Version, Manifest: m, Dev: dev[name]})
		}
	}
	sortDeps(rest)
	return append(res, rest...), nil
}

func psr4(pkg composerPkg) []string {
	var res []string
	for ns := range pkg.Autoload.PSR4 {
		if ns = strings.TrimRight(ns, `\`); ns != "" {
			res = append(res, ns)
		}
	}
	sort.Strings(res)
	return res
}

func composerVendorNS(vendor, name string) []string {
	var pkg composerPkg
	if readJSON(filepath.Join(vendor, filepath.FromSlash(name), "composer.json"), &pkg) != nil {
		return nil
	}
	return psr4(pkg)
}

func jsonKeyLine(lines []string, section, key string) int {
	open := regexp.MustCompile(`^\s*"` + regexp.QuoteMeta(section) + `"\s*:`)
	start := slices.IndexFunc(lines, open.MatchString)
	if start < 0 {
		return 0
	}
	quoted := slashKey(key)
	own := regexp.MustCompile(`(?:^|,)\s*` + quoted + `\s*:`)
	inline := regexp.MustCompile(`[{,]\s*` + quoted + `\s*:`)
	depth := 0
	for i := start; i < len(lines); i++ {
		switch {
		case i == start && inline.MatchString(lines[i]):
			return i + 1
		case i > start && depth == 1 && own.MatchString(lines[i]):
			return i + 1
		}
		var quote bool
		for j := 0; j < len(lines[i]); j++ {
			switch c := lines[i][j]; {
			case c == '\\' && quote:
				j++
			case c == '"':
				quote = !quote
			case quote:
			case c == '{':
				depth++
			case c == '}':
				depth--
			}
		}
		if depth == 0 {
			return 0
		}
	}
	return 0
}

func slashKey(name string) string {
	return `(?i)"` + strings.ReplaceAll(regexp.QuoteMeta(name), "/", `\\?/`) + `"`
}

func (composer) Meta(ctx context.Context, f core.Fetcher, name string) (*core.Meta, error) {
	var doc struct {
		Minified string                      `json:"minified"`
		Packages map[string][]map[string]any `json:"packages"`
	}
	ok, err := getDoc(ctx, f, "https://repo.packagist.org/p2/"+name+".json", &doc)
	if err != nil {
		return nil, fmt.Errorf("composer: %s: %w", name, err)
	}
	if !ok {
		return nil, fmt.Errorf("composer: %s: not found on packagist", name)
	}
	list := doc.Packages[name]
	if doc.Minified != "" {
		list = unminify(list)
	}
	res := &core.Meta{Name: name}
	entries := map[string]map[string]any{}
	for _, e := range list {
		v, _ := e["version"].(string)
		norm, _ := e["version_normalized"].(string)
		if v == "" || strings.HasPrefix(v, "dev-") || strings.HasSuffix(norm, "-dev") || strings.HasPrefix(norm, "dev-") {
			continue
		}
		t, _ := time.Parse(time.RFC3339, str(e["time"]))
		res.Releases = append(res.Releases, core.Release{Version: v, Time: t, Pre: isPre(v)})
		entries[v] = e
	}
	sortReleases(res.Releases)
	if len(res.Releases) == 0 {
		return res, nil
	}
	// abandonment and the repo are package-wide; read them off the newest stable release
	newest := res.Releases[len(res.Releases)-1]
	for _, r := range slices.Backward(res.Releases) {
		if !r.Pre {
			newest = r
			break
		}
	}
	e := entries[newest.Version]
	if src, ok := e["source"].(map[string]any); ok {
		res.Repo = cleanRepo(str(src["url"]))
	}
	switch a := e["abandoned"].(type) {
	case bool:
		if a {
			res.Deprecated = "package is abandoned"
		}
	case string:
		res.Deprecated, res.Replacement = "package is abandoned, use "+a+" instead", a
	}
	return res, nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// unminify undoes Composer's minified metadata: each entry holds only what
// differs from the one before it, and "__unset" removes a key.
func unminify(list []map[string]any) []map[string]any {
	res := make([]map[string]any, 0, len(list))
	var prev map[string]any
	for _, e := range list {
		cur := maps.Clone(prev)
		if cur == nil {
			cur = map[string]any{}
		}
		for k, v := range e {
			if s, ok := v.(string); ok && s == "__unset" {
				delete(cur, k)
				continue
			}
			cur[k] = v
		}
		res = append(res, cur)
		prev = cur
	}
	return res
}

func (composer) Bump(ctx context.Context, root string, d core.Dep, to, toName string) (*core.Bump, error) {
	res := &core.Bump{}
	// Composer takes the version with the name, so the lock lands on to and
	// not on the newest release the constraint allows.
	names := []string{d.Name + ":" + to}
	if d.Direct {
		changed, err := composerEdit(root, d, to, toName)
		if err != nil {
			return nil, err
		}
		if changed {
			res.Files = append(res.Files, d.Manifest.Path)
		}
		if toName != "" {
			names = []string{toName + ":" + to, d.Name}
		}
	} else if toName != "" {
		return nil, fmt.Errorf("composer: %s: cannot swap a transitive package: %w", d.Name, core.ErrNotSupported)
	}
	switch {
	case d.Manifest.Lock == "":
		res.Note = "no composer.lock"
		return res, nil
	case !have("composer"):
		res.Note = core.ToolMissing{Tool: "composer"}.Error()
		return res, nil
	}
	line := "composer update " + shArgs(names) + " --with-all-dependencies --no-install --no-scripts --no-interaction"
	if err := runIn(ctx, root, path.Dir(d.Manifest.Path), nil, line); err != nil {
		return nil, fmt.Errorf("composer: %s: %w", d.Name, err)
	}
	if err := lockLanded(root, d.Manifest.Lock, core.Composer, cmp.Or(toName, d.Name), to); err != nil {
		return nil, fmt.Errorf("composer: %w", err)
	}
	res.Locked = true
	res.Files = append(res.Files, d.Manifest.Lock)
	return res, nil
}

func shArgs(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shArg(a)
	}
	return strings.Join(q, " ")
}

func composerEdit(root string, d core.Dep, to, toName string) (bool, error) {
	p := filepath.Join(root, filepath.FromSlash(d.Manifest.Path))
	lines, err := readLines(p)
	if err != nil {
		return false, err
	}
	if d.Line < 1 || d.Line > len(lines) {
		return false, fmt.Errorf("composer: %s: no line %d in %s", d.Name, d.Line, d.Manifest.Path)
	}
	text := lines[d.Line-1]
	key := regexp.MustCompile(slashKey(d.Name)).FindString(text)
	if key == "" || !strings.Contains(text, `"`+d.Req+`"`) {
		return false, fmt.Errorf("composer: %s: %s:%d does not declare it as %q", d.Name, d.Manifest.Path, d.Line, d.Req)
	}
	next := toName
	if toName != "" {
		if err := rewriteLine(p, d.Line, key, `"`+toName+`"`); err != nil {
			return false, err
		}
		next = "^" + shape(to, 2)
	} else if next, err = fixReq(d.Req, to, anyDialect); err != nil {
		return false, fmt.Errorf("composer: %s: %w", d.Name, err)
	}
	if next == d.Req {
		return toName != "", nil
	}
	return true, rewriteLine(p, d.Line, `"`+d.Req+`"`, `"`+next+`"`)
}

func (composer) Commands(root string, m core.Manifest) core.Commands {
	dir := path.Dir(m.Path)
	res := core.Commands{Install: []string{inDir(dir, "composer install --no-interaction --no-progress")}}
	for _, n := range []string{"phpunit.xml", "phpunit.xml.dist"} {
		if onDisk(root, path.Join(dir, n)) {
			res.Test = []string{inDir(dir, "vendor/bin/phpunit")}
			break
		}
	}
	return res
}

func (composer) Traits() core.Traits {
	return core.Traits{OSV: "Packagist", Page: prefix("https://packagist.org/packages/"), Transitive: true}
}

func (composer) Imports(d core.Dep) []string {
	if ns := composerNS.get(d.Name); len(ns) > 0 {
		return ns
	}
	vendor, pkg, _ := strings.Cut(d.Name, "/")
	return []string{composerCamel(vendor) + `\` + composerCamel(pkg)}
}

func composerCamel(s string) string {
	var b strings.Builder
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == '-' || r == '_' || r == '.' }) {
		b.WriteString(strings.ToUpper(p[:1]) + p[1:])
	}
	return b.String()
}
