package eco

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"

	"github.com/mohsensc/tend-cli/internal/core"
)

type npm struct{}

func (npm) Name() string { return core.Npm }

var npmBlocks = map[string]bool{
	"dependencies":         true,
	"devDependencies":      true,
	"optionalDependencies": true,
	"peerDependencies":     true,
}

// npm reads npm-shrinkwrap.json over package-lock.json when both are there.
var npmLocks = []struct{ file, tool string }{
	{"npm-shrinkwrap.json", "npm"},
	{"package-lock.json", "npm"},
	{"pnpm-lock.yaml", "pnpm"},
	{"yarn.lock", "yarn"},
}

const npmDefaultRegistry = "https://registry.npmjs.org"

func (npm) Detect(root string) ([]core.Manifest, error) {
	files, err := walk(root, "package.json")
	if err != nil {
		return nil, err
	}
	var res []core.Manifest
	for _, f := range files {
		lock, tool := npmLockFor(root, f)
		res = append(res, core.Manifest{Ecosystem: core.Npm, Path: f, Lock: lock, Tool: tool})
	}
	return res, nil
}

type npmInfo struct {
	PackageManager string          `json:"packageManager"`
	Workspaces     json.RawMessage `json:"workspaces"`
	Scripts        map[string]any  `json:"scripts"`
}

// npmReadInfo reads the package.json fields that steer detection and
// commands; a file that does not parse has none of them.
func npmReadInfo(root, rel string) npmInfo {
	var info npmInfo
	if readJSON(filepath.Join(root, rel), &info) != nil {
		return npmInfo{}
	}
	return info
}

// npmLockFor finds the lockfile next to the manifest or, for a workspace
// member, at the workspace root above it.
func npmLockFor(root, rel string) (lock, tool string) {
	dir := path.Dir(rel)
	pm := npmManager(npmReadInfo(root, rel))
	for d := dir; ; d = path.Dir(d) {
		if d == dir || npmMember(root, d, memberDir(d, dir)) {
			for _, l := range npmLocks {
				if p := path.Join(d, l.file); onDisk(root, p) {
					return p, l.tool
				}
			}
			if pm == "" {
				pm = npmManager(npmReadInfo(root, path.Join(d, "package.json")))
			}
		}
		if d == "." {
			break
		}
	}
	if pm == "" {
		pm = "npm"
	}
	return "", pm
}

// npmMember reports whether the workspace root at dir lists member, by the
// workspaces field of its package.json or by pnpm-workspace.yaml.
func npmMember(root, dir, member string) bool {
	var pats []string
	var ws struct {
		Packages []string `json:"packages" yaml:"packages"`
	}
	if readYAML(filepath.Join(root, filepath.FromSlash(dir), "pnpm-workspace.yaml"), &ws) == nil {
		pats = ws.Packages
	}
	raw := npmReadInfo(root, path.Join(dir, "package.json")).Workspaces
	if json.Unmarshal(raw, &pats) != nil && json.Unmarshal(raw, &ws) == nil {
		pats = append(pats, ws.Packages...)
	}
	for _, p := range pats {
		p = strings.TrimSuffix(strings.TrimPrefix(p, "./"), "/")
		if ok, _ := path.Match(p, member); ok {
			return true
		}
		if prefix, ok := strings.CutSuffix(p, "**"); ok && strings.HasPrefix(member, prefix) {
			return true
		}
	}
	return false
}

func npmManager(info npmInfo) string {
	name, _, _ := strings.Cut(info.PackageManager, "@")
	switch name {
	case "npm", "pnpm", "yarn":
		return name
	}
	return ""
}

// npmEntry is one requirement in package.json with the byte offsets Bump
// needs to edit it in place.
type npmEntry struct {
	block            string
	key, req         string
	line             int
	keyStart, keyEnd int // the key's text, inside its quotes
	valStart, valEnd int // the requirement's text, inside its quotes
}

type npmBlock struct {
	name        string
	entries     []npmEntry
	open, close int // the offsets of the block's braces
}

// npmParse reads the dependency blocks of package.json in file order. It walks
// tokens instead of unmarshaling so that order and offsets survive.
func npmParse(src []byte) ([]npmBlock, error) {
	dec := json.NewDecoder(bytes.NewReader(src))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("package.json is not a JSON object")
	}
	var res []npmBlock
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		key, _ := t.(string)
		if !npmBlocks[key] || len(raw) == 0 || raw[0] != '{' {
			continue
		}
		start := int(dec.InputOffset()) - len(raw)
		b, err := npmParseBlock(src, start, key)
		if err != nil {
			return nil, err
		}
		res = append(res, b)
	}
	return res, nil
}

func npmParseBlock(src []byte, start int, name string) (npmBlock, error) {
	b := npmBlock{name: name, open: start}
	dec := json.NewDecoder(bytes.NewReader(src[start:]))
	if _, err := dec.Token(); err != nil {
		return b, err
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return b, err
		}
		keyEnd := start + int(dec.InputOffset()) - 1
		v, err := dec.Token()
		if err != nil {
			return b, err
		}
		if _, ok := v.(json.Delim); ok {
			if err := skipJSON(dec); err != nil {
				return b, err
			}
			continue
		}
		req, ok := v.(string)
		if !ok {
			continue
		}
		valEnd := start + int(dec.InputOffset()) - 1
		keyStart := openQuote(src, keyEnd) + 1
		b.entries = append(b.entries, npmEntry{
			block:    name,
			key:      t.(string),
			req:      req,
			line:     lineOf(src, keyStart),
			keyStart: keyStart,
			keyEnd:   keyEnd,
			valStart: openQuote(src, valEnd) + 1,
			valEnd:   valEnd,
		})
	}
	if _, err := dec.Token(); err != nil {
		return b, err
	}
	b.close = start + int(dec.InputOffset()) - 1
	return b, nil
}

// openQuote returns the index of the quote that opens the JSON string whose
// closing quote is at end.
func openQuote(src []byte, end int) int {
	for i := end - 1; i >= 0; i-- {
		if src[i] != '"' {
			continue
		}
		n := 0
		for j := i - 1; j >= 0 && src[j] == '\\'; j-- {
			n++
		}
		if n%2 == 0 {
			return i
		}
	}
	return -1
}

func skipJSON(dec *json.Decoder) error {
	depth := 1
	for depth > 0 {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		switch t {
		case json.Delim('{'), json.Delim('['):
			depth++
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
	}
	return nil
}

// npmDirect lists each requirement once, in file order. A peer dependency that
// is also a dev or regular dependency is left to that entry.
func npmDirect(blocks []npmBlock) []npmEntry {
	installed := map[string]bool{}
	for _, b := range blocks {
		if b.name != "peerDependencies" {
			for _, e := range b.entries {
				installed[e.key] = true
			}
		}
	}
	var res []npmEntry
	seen := map[string]bool{}
	for _, b := range blocks {
		for _, e := range b.entries {
			if seen[e.key] || b.name == "peerDependencies" && installed[e.key] {
				continue
			}
			seen[e.key] = true
			res = append(res, e)
		}
	}
	return res
}

// npmRegistryName is the registry package an entry installs, following npm:
// aliases. Workspace, file, link, git and URL requirements are not registry
// packages and are left out of the inventory.
func npmRegistryName(key, req string) (string, bool) {
	if rest, ok := strings.CutPrefix(req, "npm:"); ok {
		i := strings.LastIndexByte(rest, '@')
		if i <= 0 {
			return rest, rest != ""
		}
		return rest[:i], true
	}
	if strings.Contains(req, ":") || strings.Contains(req, "/") {
		return "", false
	}
	return key, true
}

func (npm) Deps(root string, m core.Manifest) ([]core.Dep, error) {
	src, err := os.ReadFile(filepath.Join(root, m.Path))
	if err != nil {
		return nil, err
	}
	blocks, err := npmParse(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", m.Path, err)
	}
	direct := npmDirect(blocks)
	lk, err := npmReadLock(root, m)
	if err != nil {
		return nil, err
	}
	dir := path.Dir(m.Path)
	rc := npmrc(root, dir)
	var res []core.Dep
	seen := map[string]bool{}
	for _, e := range direct {
		name, ok := npmRegistryName(e.key, e.req)
		if !ok || lk.links[e.key] {
			continue
		}
		v := lk.resolve(e.key, e.req)
		if v == "" && npmValid(e.req) {
			v = e.req
		}
		seen[name+"@"+v] = true
		npmLearnRegistry(dir, name, rc)
		res = append(res, core.Dep{
			Ecosystem: core.Npm,
			Name:      name,
			Version:   v,
			Req:       e.req,
			Manifest:  m,
			Line:      e.line,
			Dev:       e.block == "devDependencies",
			Direct:    true,
		})
	}
	for _, p := range lk.all {
		if seen[p.name+"@"+p.version] || lk.members[p.name+"@"+p.version] {
			continue
		}
		npmLearnRegistry(dir, p.name, rc)
		res = append(res, core.Dep{Ecosystem: core.Npm, Name: p.name, Version: p.version, Manifest: m, Dev: p.dev})
	}
	return res, nil
}

// Meta gets no project root, so Deps remembers the registry each scope
// resolves to under each manifest dir's .npmrc, keyed by dir and scope ("" for
// unscoped packages). Two dirs may map one scope to different registries.
var npmRegistries sync.Map

type npmScopeAt struct{ dir, scope string }

func npmScope(name string) string {
	if scope, _, ok := strings.Cut(name, "/"); ok && strings.HasPrefix(scope, "@") {
		return scope
	}
	return ""
}

func npmLearnRegistry(dir, name string, rc npmConfig) {
	npmRegistries.Store(npmScopeAt{dir, npmScope(name)}, rc.registry(name))
}

type npmConfig map[string]string

// npmrc layers registry settings the way npm does: ~/.npmrc, then the
// project's, then the package's own when it sits below the root.
func npmrc(root, dir string) npmConfig {
	cfg := npmConfig{}
	if home, err := os.UserHomeDir(); err == nil {
		cfg.read(filepath.Join(home, ".npmrc"))
	}
	if root == "" {
		return cfg
	}
	cfg.read(filepath.Join(root, ".npmrc"))
	if dir != "." {
		cfg.read(filepath.Join(root, filepath.FromSlash(dir), ".npmrc"))
	}
	return cfg
}

func (c npmConfig) read(p string) {
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k != "registry" && !strings.HasSuffix(k, ":registry") {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		c[k] = strings.TrimRight(os.ExpandEnv(v), "/")
	}
}

func (c npmConfig) registry(name string) string {
	if scope := npmScope(name); scope != "" {
		if r := c[scope+":registry"]; r != "" {
			return r
		}
	}
	return cmp.Or(c["registry"], npmDefaultRegistry)
}

// npmRegistriesFor lists the registries the manifests Deps read resolve
// name's scope to, by manifest dir, each once; the user's own when none did.
func npmRegistriesFor(name string) []string {
	scope := npmScope(name)
	byDir := map[string]string{}
	npmRegistries.Range(func(k, v any) bool {
		if at := k.(npmScopeAt); at.scope == scope {
			byDir[at.dir] = v.(string)
		}
		return true
	})
	var res []string
	for _, dir := range keys(byDir) {
		if r := byDir[dir]; !slices.Contains(res, r) {
			res = append(res, r)
		}
	}
	if len(res) == 0 {
		return []string{npmrc("", "").registry(name)}
	}
	return res
}

type npmDoc struct {
	Tags     map[string]string `json:"dist-tags"`
	Versions map[string]struct {
		Deprecated any               `json:"deprecated"`
		Repository any               `json:"repository"`
		Peers      map[string]string `json:"peerDependencies"`
	} `json:"versions"`
	Time       map[string]string `json:"time"`
	Repository any               `json:"repository"`
}

// Meta reads the first registry that has the package, of those the
// project's manifests point its scope at.
func (npm) Meta(ctx context.Context, f core.Fetcher, name string) (*core.Meta, error) {
	regs := npmRegistriesFor(name)
	var errs []error
	for _, reg := range regs {
		var doc npmDoc
		found, err := getDoc(ctx, f, reg+"/"+strings.Replace(name, "/", "%2F", 1), &doc)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if found {
			return npmDocMeta(name, doc), nil
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return nil, fmt.Errorf("npm %s: not found at %s", name, strings.Join(regs, ", "))
}

func npmDocMeta(name string, doc npmDoc) *core.Meta {
	meta := &core.Meta{Name: name, Latest: doc.Tags["latest"]}
	for v, info := range doc.Versions {
		if !npmValid(v) {
			continue
		}
		dep, _ := info.Deprecated.(string)
		t, _ := time.Parse(time.RFC3339, doc.Time[v])
		meta.Releases = append(meta.Releases, core.Release{
			Version:    v,
			Time:       t,
			Pre:        semver.Prerelease("v"+v) != "",
			Deprecated: dep,
			Peers:      info.Peers,
		})
	}
	slices.SortFunc(meta.Releases, func(a, b core.Release) int {
		return cmp.Or(semver.Compare("v"+a.Version, "v"+b.Version), strings.Compare(a.Version, b.Version))
	})
	meta.Deprecated = npmAllDeprecated(meta)
	repo := doc.Repository
	if repo == nil {
		repo = doc.Versions[meta.Latest].Repository
	}
	meta.Repo = githubRepo(npmRepoURL(repo))
	return meta
}

// npmAllDeprecated is the package's deprecation: npm has none of its own, so
// a package counts as deprecated when every version is.
func npmAllDeprecated(meta *core.Meta) string {
	if len(meta.Releases) == 0 {
		return ""
	}
	msg := meta.Releases[len(meta.Releases)-1].Deprecated
	for _, r := range meta.Releases {
		if r.Deprecated == "" {
			return ""
		}
		if r.Version == meta.Latest {
			msg = r.Deprecated
		}
	}
	return msg
}

func npmRepoURL(v any) string {
	switch r := v.(type) {
	case string:
		return r
	case map[string]any:
		s, _ := r["url"].(string)
		return s
	}
	return ""
}

// npmValid accepts a full semver version as npm writes it: no v prefix and
// all three numbers, which golang.org/x/mod/semver alone does not insist on.
func npmValid(v string) bool {
	nums, _, _ := strings.Cut(v, "+")
	nums, _, _ = strings.Cut(nums, "-")
	return v != "" && v[0] != 'v' && strings.Count(nums, ".") == 2 && semver.IsValid("v"+v)
}

func (n npm) Bump(ctx context.Context, root string, d core.Dep, to, toName string) (*core.Bump, error) {
	m := d.Manifest
	if toName != "" && !d.Direct {
		return nil, fmt.Errorf("%s: replacing %s, which only the lockfile names: %w", m.Path, d.Name, core.ErrNotSupported)
	}
	snap := snapFiles(root, m.Path, m.Lock)
	key := d.Name
	if d.Direct {
		e, err := n.edit(root, d, to, toName)
		if err != nil {
			return nil, err
		}
		key = e.key
	}
	edited := len(snap.changed(root)) > 0
	if m.Lock == "" {
		if !d.Direct {
			return nil, fmt.Errorf("%s has no lockfile for %s: %w", m.Path, d.Name, core.ErrNotSupported)
		}
		return &core.Bump{Files: snap.changed(root), Note: "no lockfile"}, nil
	}
	exact := d.Direct && toName == ""
	targeted := !edited && !exact
	line, err := npmLockLine(root, m, key, targeted)
	if err != nil {
		return nil, err
	}
	if !have(m.Tool) {
		if !edited {
			return nil, core.ToolMissing{Tool: m.Tool}
		}
		return &core.Bump{Files: snap.changed(root), Note: m.Tool + " not found"}, nil
	}
	var before map[string]bool
	if targeted {
		if before, err = lockedVersions(root, m.Lock, core.Npm, d.Name); err != nil {
			return nil, err
		}
	}
	if exact {
		if err := n.lockAt(ctx, root, []core.Dep{d}, to, line); err != nil {
			return nil, err
		}
	}
	if err := runIn(ctx, root, path.Dir(m.Lock), nil, line); err != nil {
		return nil, err
	}
	if targeted {
		if err := lockedTo(root, m.Lock, core.Npm, d.Name, to, before); err != nil {
			return nil, errors.Join(fmt.Errorf("%w; %s moves a transitive dep only to the newest release its ranges allow", err, m.Tool), snap.restore(root))
		}
	}
	return &core.Bump{Files: snap.changed(root), Locked: true}, nil
}

// lockAt locks deps, all of one manifest, at exactly to. Left to itself the
// lock tool takes the newest release the range allows, which may be past the
// release tend chose; so each requirement reads to alone for one pass, and
// then goes back to the range, which the locked to satisfies and the next
// pass keeps.
func (npm) lockAt(ctx context.Context, root string, deps []core.Dep, to, line string) error {
	m := deps[0].Manifest
	p := filepath.Join(root, m.Path)
	src, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	blocks, err := npmParse(src)
	if err != nil {
		return fmt.Errorf("%s: %w", m.Path, err)
	}
	var es []*npmEntry
	for _, d := range deps {
		if _, e := npmFind(blocks, d); e != nil && e.block != "peerDependencies" {
			es = append(es, e)
		}
	}
	if len(es) == 0 {
		return nil
	}
	// splice from the end, so the offsets before each edit hold
	slices.SortFunc(es, func(a, b *npmEntry) int { return b.valStart - a.valStart })
	out := src
	for _, e := range es {
		req := to
		if strings.HasPrefix(e.req, "npm:") {
			name, _ := npmRegistryName(e.key, e.req)
			req = "npm:" + name + "@" + to
		}
		out = splice(out, e.valStart, e.valEnd, req)
	}
	if err := writeKeepMode(p, out); err != nil {
		return err
	}
	err = runIn(ctx, root, path.Dir(m.Lock), nil, line)
	if werr := writeKeepMode(p, src); err == nil {
		err = werr
	}
	return err
}

// BumpGroup moves deps, direct requirements of one manifest, to to in one
// lock pass: a package that pins another as a peer only resolves when both
// move at once.
func (n npm) BumpGroup(ctx context.Context, root string, deps []core.Dep, to string) (*core.Bump, error) {
	m := deps[0].Manifest
	snap := snapFiles(root, m.Path, m.Lock)
	for _, d := range deps {
		if !d.Direct || d.Manifest.Path != m.Path {
			return nil, fmt.Errorf("%s: %s is not a requirement of it", m.Path, d.Name)
		}
		if _, err := n.edit(root, d, to, ""); err != nil {
			return nil, err
		}
	}
	switch {
	case m.Lock == "":
		return &core.Bump{Files: snap.changed(root), Note: "no lockfile"}, nil
	case !have(m.Tool):
		return &core.Bump{Files: snap.changed(root), Note: m.Tool + " not found"}, nil
	}
	line, err := npmLockLine(root, m, "", false)
	if err != nil {
		return nil, err
	}
	if err := n.lockAt(ctx, root, deps, to, line); err != nil {
		return nil, err
	}
	if err := runIn(ctx, root, path.Dir(m.Lock), nil, line); err != nil {
		return nil, err
	}
	return &core.Bump{Files: snap.changed(root), Locked: true}, nil
}

// edit rewrites the requirement for d in package.json, or leaves the file as
// it is when the written range already admits to.
func (npm) edit(root string, d core.Dep, to, toName string) (*npmEntry, error) {
	p := filepath.Join(root, d.Manifest.Path)
	src, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	blocks, err := npmParse(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", d.Manifest.Path, err)
	}
	b, e := npmFind(blocks, d)
	if e == nil {
		return nil, fmt.Errorf("%s: no requirement for %s", d.Manifest.Path, d.Name)
	}
	if _, ok := npmRegistryName(e.key, e.req); !ok {
		return nil, fmt.Errorf("%s: %s %q is not a registry requirement: %w", d.Manifest.Path, e.key, e.req, core.ErrNotSupported)
	}
	out, err := npmEdit(src, b, e, to, toName)
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", d.Manifest.Path, e.key, err)
	}
	if bytes.Equal(out, src) {
		return e, nil
	}
	return e, writeKeepMode(p, out)
}

func npmEdit(src []byte, b *npmBlock, e *npmEntry, to, toName string) ([]byte, error) {
	if toName != "" {
		return npmSwap(src, b, e, toName, to)
	}
	req, err := npmNewReq(e.req, to)
	if err != nil {
		return nil, err
	}
	return splice(src, e.valStart, e.valEnd, req), nil
}

func npmFind(blocks []npmBlock, d core.Dep) (*npmBlock, *npmEntry) {
	var b *npmBlock
	var e *npmEntry
	for i := range blocks {
		for j := range blocks[i].entries {
			x := &blocks[i].entries[j]
			if name, _ := npmRegistryName(x.key, x.req); name != d.Name && x.key != d.Name {
				continue
			}
			if x.line == d.Line {
				return &blocks[i], x
			}
			if e == nil || e.block == "peerDependencies" {
				b, e = &blocks[i], x
			}
		}
	}
	return b, e
}

// npmNewReq rewrites a range so that it admits to, keeping its style:
// ^1.2.0 -> ^2.0.0, ~1.2.0 -> ~2.0.0, 1.x -> 2.x, >=1 <2 -> >=2.0.0 <3.0.0.
// A range that already admits to, or a dist-tag, comes back unchanged.
func npmNewReq(req, to string) (string, error) {
	if rest, ok := strings.CutPrefix(req, "npm:"); ok {
		i := strings.LastIndexByte(rest, '@')
		if i <= 0 {
			return "", core.ErrNotSupported
		}
		r, err := npmNewReq(rest[i+1:], to)
		return "npm:" + rest[:i+1] + r, err
	}
	if !npmValid(to) {
		return "", fmt.Errorf("invalid version %q", to)
	}
	if npmTag.MatchString(req) && !npmSimple.MatchString(req) || npmSatisfies(req, to) {
		return req, nil
	}
	res, ok := npmRewrite(strings.TrimSpace(req), to)
	if !ok || !npmSatisfies(res, to) {
		return "", fmt.Errorf("cannot rewrite %q to admit %s: %w", req, to, core.ErrNotSupported)
	}
	return res, nil
}

var npmTag = regexp.MustCompile(`^[A-Za-z][\w.-]*$`)

var npmSimple = regexp.MustCompile(`^(\^|~>|~|>=|<=|>|<|=)?\s*(v?)((?:\d+|[xX*])(?:\.(?:\d+|[xX*])){0,2})(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

func npmRewrite(r, to string) (string, bool) {
	if strings.Contains(r, "||") {
		return r + " || ^" + to, true
	}
	if lo, _, ok := strings.Cut(r, " - "); ok {
		return strings.TrimSpace(lo) + " - " + to, true
	}
	toks := npmTokens(r)
	switch len(toks) {
	case 1:
		return npmRewriteOne(toks[0], to)
	case 2:
		lo, hi := npmSimple.FindStringSubmatch(toks[0]), npmSimple.FindStringSubmatch(toks[1])
		if lo == nil || hi == nil || !strings.HasPrefix(lo[1], ">") || !strings.HasPrefix(hi[1], "<") {
			return "", false
		}
		return ">=" + lo[2] + to + " <" + hi[2] + strconv.Itoa(semverMajor(to)+1) + ".0.0", true
	}
	return "", false
}

func npmRewriteOne(tok, to string) (string, bool) {
	m := npmSimple.FindStringSubmatch(tok)
	if m == nil {
		return "", false
	}
	op, vp, parts := m[1], m[2], strings.Split(m[3], ".")
	switch op {
	case "<":
		return "", false
	case ">":
		op = ">="
	}
	if len(parts) == 3 && !strings.ContainsAny(m[3], "xX*") || semver.Prerelease("v"+to) != "" {
		return op + vp + to, true
	}
	nums := strings.Split(strings.SplitN(to, "-", 2)[0], ".")
	for i, p := range parts {
		if p != "x" && p != "X" && p != "*" {
			parts[i] = nums[i]
		}
	}
	return op + vp + strings.Join(parts, "."), true
}

// npmTokens splits a space separated comparator set, joining an operator that
// stands apart from its version (">= 1.2.0").
func npmTokens(r string) []string {
	var res []string
	for _, f := range strings.Fields(r) {
		if n := len(res); n > 0 && strings.Trim(res[n-1], "<>=~^") == "" {
			res[n-1] += f
			continue
		}
		res = append(res, f)
	}
	return res
}

func semverMajor(v string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(semver.Major("v"+strings.TrimPrefix(v, "v")), "v"))
	return n
}

// npmSwap replaces the entry with toName at ^to in the same block. When the
// block was sorted with one entry per line, the new entry moves to its sorted
// place the way npm itself would write it.
func npmSwap(src []byte, b *npmBlock, e *npmEntry, toName, to string) ([]byte, error) {
	for _, x := range b.entries {
		if x.key == toName {
			return nil, fmt.Errorf("%s already lists %s", b.name, toName)
		}
	}
	sorted := npmOneLine(src, b) && slices.IsSortedFunc(b.entries, func(x, y npmEntry) int { return strings.Compare(x.key, y.key) })
	out := splice(src, e.valStart, e.valEnd, "^"+to)
	out = splice(out, e.keyStart, e.keyEnd, toName)
	if !sorted {
		return out, nil
	}
	blocks, err := npmParse(out)
	if err != nil {
		return nil, err
	}
	for _, nb := range blocks {
		if nb.name == b.name {
			return npmSortLines(out, nb), nil
		}
	}
	return out, nil
}

func npmOneLine(src []byte, b *npmBlock) bool {
	for i, e := range b.entries {
		start, end := lineStart(src, e.keyStart), lineEnd(src, e.valEnd)
		if strings.TrimSpace(string(src[start:e.keyStart-1])) != "" {
			return false
		}
		if rest := strings.TrimSpace(string(src[e.valEnd+1 : end])); rest != "" && rest != "," {
			return false
		}
		if i > 0 && lineEnd(src, b.entries[i-1].valEnd) != start {
			return false
		}
	}
	return len(b.entries) > 0
}

func npmSortLines(src []byte, b npmBlock) []byte {
	type ln struct{ key, body, eol string }
	var lines []ln
	for _, e := range b.entries {
		s := string(src[lineStart(src, e.keyStart):lineEnd(src, e.valEnd)])
		body := strings.TrimRight(s, "\r\n")
		body = strings.TrimSuffix(strings.TrimRight(body, " \t"), ",")
		lines = append(lines, ln{e.key, body, s[len(strings.TrimRight(s, "\r\n")):]})
	}
	slices.SortStableFunc(lines, func(x, y ln) int { return strings.Compare(x.key, y.key) })
	var sb strings.Builder
	for i, l := range lines {
		sb.WriteString(l.body)
		if i < len(lines)-1 {
			sb.WriteByte(',')
		}
		sb.WriteString(l.eol)
	}
	first, last := b.entries[0], b.entries[len(b.entries)-1]
	return splice(src, lineStart(src, first.keyStart), lineEnd(src, last.valEnd), sb.String())
}

// npmLockLine is the native command that brings the lockfile in line. When
// the manifest was not edited it targets the one package.
func npmLockLine(root string, m core.Manifest, name string, targeted bool) (string, error) {
	q := shArg(name)
	switch m.Tool {
	case "pnpm":
		if targeted {
			return "pnpm update " + q + " --lockfile-only", nil
		}
		return "pnpm install --lockfile-only --ignore-scripts", nil
	case "yarn":
		if yarnBerry(root, m) {
			if targeted {
				return "yarn up -R " + q + " --mode=update-lockfile", nil
			}
			return "yarn install --mode=update-lockfile", nil
		}
		if targeted {
			return "", fmt.Errorf("yarn v1 cannot update %s alone: %w", name, core.ErrNotSupported)
		}
		return "yarn install --ignore-scripts --non-interactive", nil
	}
	if targeted {
		return "npm update " + q + " --package-lock-only --ignore-scripts --no-audit --no-fund", nil
	}
	return "npm install --package-lock-only --ignore-scripts --no-audit --no-fund", nil
}

func yarnBerry(root string, m core.Manifest) bool {
	if m.Lock == "" {
		return onDisk(root, path.Join(path.Dir(m.Path), ".yarnrc.yml"))
	}
	b, err := os.ReadFile(filepath.Join(root, m.Lock))
	return err == nil && bytes.Contains(b, []byte("\n__metadata:"))
}

func (npm) Commands(root string, m core.Manifest) core.Commands {
	dir := path.Dir(m.Path)
	lockDir := dir
	if m.Lock != "" {
		lockDir = path.Dir(m.Lock)
	}
	tool := cmp.Or(m.Tool, "npm")
	var install string
	switch {
	case tool == "pnpm":
		install = "pnpm install --frozen-lockfile"
	case tool == "yarn" && yarnBerry(root, m):
		install = "yarn install --immutable"
	case tool == "yarn":
		install = "yarn install --frozen-lockfile"
	default:
		install = "npm ci"
	}
	if m.Lock == "" {
		install = tool + " install"
	}
	c := core.Commands{Install: []string{inDir(lockDir, install)}}
	scripts := npmReadInfo(root, m.Path).Scripts
	if s, _ := scripts["build"].(string); s != "" {
		c.Build = []string{inDir(dir, tool+" run build")}
	}
	if s, _ := scripts["test"].(string); s != "" && !strings.Contains(s, "no test specified") {
		c.Test = []string{inDir(dir, tool+" test")}
	}
	return c
}

// Imports is the package name; scan matches subpath imports (name/sub) to it.
func (npm) Traits() core.Traits {
	return core.Traits{OSV: "npm", Page: prefix("https://www.npmjs.com/package/"), Transitive: true}
}

func (npm) Imports(d core.Dep) []string { return []string{d.Name} }

// npmSatisfies reports whether version is inside an npm range: caret, tilde,
// x-ranges, comparators, hyphen ranges and || unions, with node-semver's rule
// that a pre-release only matches a comparator on the same major.minor.patch
// that itself names a pre-release.
func npmSatisfies(rng, version string) bool {
	version = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(version), "="), "v")
	if !npmValid(version) {
		return false
	}
	v := semver.Canonical("v" + version)
	for _, alt := range strings.Split(rng, "||") {
		set, ok := npmComparators(strings.TrimSpace(alt))
		if ok && npmMatch(set, v) {
			return true
		}
	}
	return false
}

type npmCmp struct {
	op string // >= > < <= =
	v  string // canonical semver with a v prefix
}

func npmMatch(set []npmCmp, v string) bool {
	for _, c := range set {
		n := semver.Compare(v, c.v)
		ok := false
		switch c.op {
		case "=":
			ok = n == 0
		case ">":
			ok = n > 0
		case ">=":
			ok = n >= 0
		case "<":
			ok = n < 0
		case "<=":
			ok = n <= 0
		}
		if !ok {
			return false
		}
	}
	if semver.Prerelease(v) == "" {
		return true
	}
	base := strings.TrimSuffix(v, semver.Prerelease(v))
	for _, c := range set {
		if semver.Prerelease(c.v) != "" && strings.TrimSuffix(c.v, semver.Prerelease(c.v)) == base {
			return true
		}
	}
	return false
}

// npmPartial is a possibly incomplete version: n is how many of the three
// numbers were given before a wildcard or the end.
type npmPartial struct {
	nums [3]int
	n    int
	pre  string
}

func npmParsePartial(s string) (npmPartial, bool) {
	var p npmPartial
	s = strings.TrimPrefix(strings.TrimPrefix(s, "="), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s, p.pre = s[:i], s[i:]
	}
	if s == "" {
		return p, true
	}
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return p, false
	}
	for _, part := range parts {
		if part == "x" || part == "X" || part == "*" {
			break
		}
		x, err := strconv.Atoi(part)
		if err != nil || x < 0 {
			return p, false
		}
		p.nums[p.n] = x
		p.n++
	}
	if p.n < 3 {
		p.pre = ""
	}
	return p, true
}

func (p npmPartial) at(a, b, c int, pre string) string {
	return fmt.Sprintf("v%d.%d.%d%s", a, b, c, pre)
}

func (p npmPartial) full() string { return p.at(p.nums[0], p.nums[1], p.nums[2], p.pre) }

// next is the smallest version above everything p covers, as an exclusive
// bound that also keeps out the pre-releases of that version.
func (p npmPartial) next(level int) string {
	x, y := p.nums[0], p.nums[1]
	switch level {
	case 0:
		return p.at(x+1, 0, 0, "-0")
	case 1:
		return p.at(x, y+1, 0, "-0")
	}
	return p.at(x, y, p.nums[2]+1, "-0")
}

var anyVersion = []npmCmp{{">=", "v0.0.0"}}

func npmComparators(s string) ([]npmCmp, bool) {
	if s == "" {
		return anyVersion, true
	}
	if lo, hi, ok := strings.Cut(s, " - "); ok {
		a, ok1 := npmParsePartial(strings.TrimSpace(lo))
		b, ok2 := npmParsePartial(strings.TrimSpace(hi))
		if !ok1 || !ok2 {
			return nil, false
		}
		res := []npmCmp{{">=", a.full()}}
		switch b.n {
		case 3:
			res = append(res, npmCmp{"<=", b.full()})
		case 1, 2:
			res = append(res, npmCmp{"<", b.next(b.n - 1)})
		}
		return res, true
	}
	var res []npmCmp
	for _, tok := range npmTokens(s) {
		set, ok := npmDesugar(tok)
		if !ok {
			return nil, false
		}
		res = append(res, set...)
	}
	return res, true
}

var npmOp = regexp.MustCompile(`^(~>|~|\^|>=|<=|>|<|=)?\s*(.*)$`)

func npmDesugar(tok string) ([]npmCmp, bool) {
	m := npmOp.FindStringSubmatch(tok)
	op := m[1]
	p, ok := npmParsePartial(m[2])
	if !ok {
		return nil, false
	}
	switch op {
	case "", "=":
		if p.n == 3 {
			return []npmCmp{{"=", p.full()}}, true
		}
		return p.xrange(), true
	case "~", "~>":
		if p.n == 3 {
			return []npmCmp{{">=", p.full()}, {"<", p.next(1)}}, true
		}
		return p.xrange(), true
	case "^":
		if p.n == 0 {
			return anyVersion, true
		}
		level := 0
		switch {
		case p.nums[0] > 0 || p.n == 1:
		case p.nums[1] > 0 || p.n == 2:
			level = 1
		default:
			level = 2
		}
		return []npmCmp{{">=", p.full()}, {"<", p.next(level)}}, true
	case ">":
		if p.n == 0 {
			return []npmCmp{{"<", "v0.0.0-0"}}, true
		}
		if p.n == 3 {
			return []npmCmp{{">", p.full()}}, true
		}
		return []npmCmp{{">=", strings.TrimSuffix(p.next(p.n-1), "-0")}}, true
	case ">=":
		return []npmCmp{{">=", p.full()}}, true
	case "<":
		if p.n == 3 {
			return []npmCmp{{"<", p.full()}}, true
		}
		return []npmCmp{{"<", p.full() + "-0"}}, true
	case "<=":
		switch p.n {
		case 0:
			return anyVersion, true
		case 3:
			return []npmCmp{{"<=", p.full()}}, true
		}
		return []npmCmp{{"<", p.next(p.n - 1)}}, true
	}
	return nil, false
}

func (p npmPartial) xrange() []npmCmp {
	if p.n == 0 {
		return anyVersion
	}
	return []npmCmp{{">=", p.full()}, {"<", p.next(p.n - 1)}}
}

func lineStart(src []byte, off int) int { return bytes.LastIndexByte(src[:off], '\n') + 1 }

func lineEnd(src []byte, off int) int {
	if i := bytes.IndexByte(src[off:], '\n'); i >= 0 {
		return off + i + 1
	}
	return len(src)
}
