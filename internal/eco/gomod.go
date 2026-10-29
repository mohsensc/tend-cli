package eco

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/fetch"
)

type gomod struct{}

func (gomod) Name() string { return core.Go }

const goPublicProxy = "https://proxy.golang.org"

// goTimed is how many of the newest versions get a .info request for their
// time; older releases are never candidates, so their time stays zero. At
// most goInfoWorkers of those requests run at once.
const (
	goTimed       = 10
	goInfoWorkers = 4
)

// goMissTTL is how long a proxy's "not found" is believed. Most modules never
// get another major path, and the proxy can take seconds to say so each time.
const goMissTTL = time.Hour

// goDirect is whether Deps saw each module required directly, for Meta.
var goDirect learned[bool]

func (gomod) Detect(root string) ([]core.Manifest, error) {
	files, err := walk(root, "go.mod")
	if err != nil {
		return nil, err
	}
	var res []core.Manifest
	for _, f := range files {
		m := core.Manifest{Ecosystem: core.Go, Path: f, Tool: "go"}
		if sum := path.Join(path.Dir(f), "go.sum"); onDisk(root, sum) {
			m.Lock = sum
		}
		res = append(res, m)
	}
	return res, nil
}

// Deps lists the requires of go.mod. A replaced module is built from
// somewhere else, so registry releases say nothing about it and it is left out.
func (gomod) Deps(root string, m core.Manifest) ([]core.Dep, error) {
	f, err := goParse(root, m.Path)
	if err != nil {
		return nil, err
	}
	replaced := map[string]bool{}
	for _, r := range f.Replace {
		replaced[r.Old.Path+"@"+r.Old.Version] = true
	}
	var res, indirect []core.Dep
	for _, r := range f.Require {
		if replaced[r.Mod.Path+"@"] || replaced[r.Mod.Path+"@"+r.Mod.Version] {
			continue
		}
		d := core.Dep{
			Ecosystem: core.Go,
			Name:      r.Mod.Path,
			Version:   r.Mod.Version,
			Req:       r.Mod.Version,
			Manifest:  m,
			Line:      r.Syntax.Start.Line,
			Direct:    !r.Indirect,
		}
		goDirect.add(r.Mod.Path, !r.Indirect)
		if r.Indirect {
			indirect = append(indirect, d)
			continue
		}
		res = append(res, d)
	}
	slices.SortStableFunc(indirect, func(a, b core.Dep) int { return strings.Compare(a.Name, b.Name) })
	return append(res, indirect...), nil
}

func goParse(root, rel string) (*modfile.File, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	return modfile.Parse(rel, data, nil)
}

var goEnvFile = sync.OnceValue(func() map[string]string {
	p := os.Getenv("GOENV")
	if p == "off" {
		return nil
	}
	if p == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return nil
		}
		p = filepath.Join(dir, "go", "env")
	}
	return readGoEnvFile(p)
})

func readGoEnvFile(p string) map[string]string {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	res := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok && k != "" {
			res[k] = v
		}
	}
	return res
}

func goSetting(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return goEnvFile()[key]
}

// goProxy is the module proxy Meta reads, from GOPROXY the way the go command
// would pick it. "direct" has no proxy protocol, so it reads the public one.
func goProxy(mod string) (string, error) {
	private := cmp.Or(goSetting("GONOPROXY"), goSetting("GOPRIVATE"))
	if private != "" && module.MatchPrefixPatterns(private, mod) {
		return "", fmt.Errorf("%s is private (GOPRIVATE/GONOPROXY): %w", mod, core.ErrNotSupported)
	}
	first := goSetting("GOPROXY")
	if i := strings.IndexAny(first, ",|"); i >= 0 {
		first = first[:i]
	}
	switch first = strings.TrimSpace(first); first {
	case "", "direct":
		return goPublicProxy, nil
	case "off":
		return "", fmt.Errorf("GOPROXY=off: module lookups are disabled: %w", core.ErrNotSupported)
	}
	return strings.TrimRight(first, "/"), nil
}

type goProxyClient struct {
	ctx  context.Context
	f    core.Fetcher
	base string
	miss *goMissCache
}

// goMissCache remembers the proxy URLs that answered 404 or 410 for
// goMissTTL. With an on-disk HTTP cache it is kept beside it, so the next
// run believes it too.
type goMissCache struct {
	mu   sync.Mutex
	file string
	now  func() time.Time
	m    map[string]time.Time
}

var goMisses = struct {
	sync.Mutex
	by map[*fetch.Client]*goMissCache
}{by: map[*fetch.Client]*goMissCache{}}

// goMissesFor is the cache that goes with f's own, nil for a fetcher that
// keeps none.
func goMissesFor(f core.Fetcher) *goMissCache {
	c, ok := f.(*fetch.Client)
	if !ok {
		return nil
	}
	goMisses.Lock()
	defer goMisses.Unlock()
	if mc := goMisses.by[c]; mc != nil {
		return mc
	}
	mc := &goMissCache{now: c.Now, m: map[string]time.Time{}}
	if mc.now == nil {
		mc.now = time.Now
	}
	if c.Dir != "" {
		mc.file = filepath.Join(c.Dir, "go-missing.json")
		if b, err := os.ReadFile(mc.file); err == nil {
			_ = json.Unmarshal(b, &mc.m)
		}
	}
	goMisses.by[c] = mc
	return mc
}

func (mc *goMissCache) has(url string) bool {
	if mc == nil {
		return false
	}
	mc.mu.Lock()
	defer mc.mu.Unlock()
	at, ok := mc.m[url]
	return ok && mc.now().Sub(at) < goMissTTL
}

// add is best effort: a cache that cannot be written costs one more lookup.
func (mc *goMissCache) add(url string) {
	if mc == nil {
		return
	}
	mc.mu.Lock()
	defer mc.mu.Unlock()
	now := mc.now()
	for u, at := range mc.m {
		if now.Sub(at) >= goMissTTL {
			delete(mc.m, u)
		}
	}
	mc.m[url] = now
	if mc.file == "" {
		return
	}
	b, err := json.Marshal(mc.m)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(mc.file), ".tmp-*")
	if err != nil {
		return
	}
	_, err = tmp.Write(b)
	if cerr := tmp.Close(); err == nil && cerr == nil {
		err = os.Rename(tmp.Name(), mc.file)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
}

// get reads one proxy file. found is false for the 404 and 410 a proxy
// answers for an unknown module or version. A file:// proxy is read from disk.
func (c goProxyClient) get(mod, file string) (body []byte, found bool, err error) {
	esc, err := module.EscapePath(mod)
	if err != nil {
		return nil, false, err
	}
	rel := esc + "/@" + file
	if dir, ok := strings.CutPrefix(c.base, "file://"); ok {
		b, err := os.ReadFile(filepath.Join(filepath.FromSlash(dir), filepath.FromSlash(rel)))
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return b, err == nil, err
	}
	url := c.base + "/" + rel
	if c.miss.has(url) {
		return nil, false, nil
	}
	res, err := c.f.Get(c.ctx, url)
	switch {
	case err != nil:
		return nil, false, err
	case res.Status == 404 || res.Status == 410:
		c.miss.add(url)
		return nil, false, nil
	case res.Status != 200:
		return nil, false, fmt.Errorf("GET %s/%s: status %d", c.base, rel, res.Status)
	}
	return res.Body, true, nil
}

func (c goProxyClient) list(mod string) ([]string, bool, error) {
	b, found, err := c.get(mod, "v/list")
	if !found {
		return nil, false, err
	}
	var res []string
	for _, v := range strings.Fields(string(b)) {
		if semver.IsValid(v) {
			res = append(res, v)
		}
	}
	return res, true, nil
}

type goInfo struct {
	Version string
	Time    time.Time
}

func (c goProxyClient) info(mod, file string) (goInfo, bool, error) {
	var res goInfo
	b, found, err := c.get(mod, file)
	if !found {
		return res, false, err
	}
	if err := json.Unmarshal(b, &res); err != nil {
		return res, false, fmt.Errorf("%s/@%s: %w", mod, file, err)
	}
	return res, true, nil
}

// Meta reads the module and every higher major version that exists under its
// own /vN path, so that v2 shows up as a release of a module required at v1.
// Retractions come from each path's latest go.mod, the deprecation from the
// required path's.
func (gomod) Meta(ctx context.Context, f core.Fetcher, name string) (*core.Meta, error) {
	base, err := goProxy(name)
	if err != nil {
		return nil, err
	}
	c := goProxyClient{ctx, f, base, goMissesFor(f)}
	vs, found, err := c.list(name)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("go %s: not found at %s", name, base)
	}
	meta := &core.Meta{Name: name}
	paths := map[string][]string{name: vs}
	order := []string{name}
	if goMayMove(name) {
		majors, err := c.majors(name, goHighestMajor(name, vs))
		if err != nil {
			return nil, err
		}
		for _, m := range majors {
			paths[m.path] = m.vs
			order = append(order, m.path)
		}
	}
	for _, p := range order {
		rel, err := c.releases(p, paths[p], meta, p == name)
		if err != nil {
			return nil, err
		}
		meta.Releases = append(meta.Releases, rel...)
	}
	slices.SortFunc(meta.Releases, func(a, b core.Release) int {
		return cmp.Or(semver.Compare(a.Version, b.Version), strings.Compare(a.Version, b.Version))
	})
	meta.Releases = slices.CompactFunc(meta.Releases, func(a, b core.Release) bool { return a.Version == b.Version })
	if err := c.times(order, meta.Releases); err != nil {
		return nil, err
	}
	meta.Repo = goRepo(name)
	return meta, nil
}

// goMajorMax caps how far above the major in use the proxy is asked about.
const goMajorMax = 1000

type goMajor struct {
	path string
	vs   []string
}

// majors finds the major versions above top that a module has under /vN
// paths: the next one, the one to move a major at a time to, and the
// highest, the latest. It gallops, +1, +2, +4 and on until a path is
// missing, then halves the gap, so go-github's v28 to v90 takes a dozen
// requests and not sixty.
func (c goProxyClient) majors(name string, top int) ([]goMajor, error) {
	seen := map[int][]string{}
	at := func(n int) (bool, error) {
		if _, ok := seen[n]; ok {
			return true, nil
		}
		p := goPathAt(name, n)
		if p == "" {
			return false, nil
		}
		vs, found, err := c.list(p)
		if err != nil || !found || len(vs) == 0 {
			return false, err
		}
		seen[n] = vs
		return true, nil
	}
	hit, miss := top, 0
	for step := 1; ; step *= 2 {
		n := top + step
		if n > top+goMajorMax {
			miss = n
			break
		}
		ok, err := at(n)
		if err != nil {
			return nil, err
		}
		if !ok {
			miss = n
			break
		}
		hit = n
	}
	for miss-hit > 1 {
		mid := hit + (miss-hit)/2
		ok, err := at(mid)
		if err != nil {
			return nil, err
		}
		if ok {
			hit = mid
		} else {
			miss = mid
		}
	}
	var res []goMajor
	for _, n := range []int{top + 1, hit} {
		if vs, ok := seen[n]; ok && n > top && !slices.ContainsFunc(res, func(m goMajor) bool { return m.path == goPathAt(name, n) }) {
			res = append(res, goMajor{goPathAt(name, n), vs})
		}
	}
	return res, nil
}

// releases lists one module path's versions with its retractions applied,
// and fills Latest (the newest path wins) and, for the required path,
// Deprecated. A proxy that does not serve @latest gets the newest listed
// release instead.
func (c goProxyClient) releases(mod string, vs []string, meta *core.Meta, own bool) ([]core.Release, error) {
	latest, found, err := c.info(mod, "latest")
	if err != nil {
		return nil, err
	}
	if !found {
		latest = goInfo{Version: goNewest(vs)}
	}
	if latest.Version == "" {
		return nil, fmt.Errorf("go %s: no versions", mod)
	}
	meta.Latest = latest.Version
	var retract []*modfile.Retract
	if b, found, err := c.get(mod, "v/"+latest.Version+".mod"); err != nil {
		return nil, err
	} else if found {
		mf, err := modfile.ParseLax(mod+"@"+latest.Version+"/go.mod", b, nil)
		if err != nil {
			return nil, err
		}
		retract = mf.Retract
		if own && mf.Module != nil {
			meta.Deprecated = mf.Module.Deprecated
		}
	}
	if len(vs) == 0 {
		vs = []string{latest.Version}
	}
	res := make([]core.Release, 0, len(vs))
	for _, v := range vs {
		r := core.Release{Version: v, Pre: semver.Prerelease(v) != ""}
		if v == latest.Version {
			r.Time = latest.Time
		}
		for _, x := range retract {
			if semver.Compare(v, x.Low) >= 0 && semver.Compare(v, x.High) <= 0 {
				r.Yanked = true
				r.Deprecated = "retracted"
				if x.Rationale != "" {
					r.Deprecated += ": " + x.Rationale
				}
			}
		}
		res = append(res, r)
	}
	return res, nil
}

// goNewest is the newest release in vs, or the newest pre-release when there
// is no release, the way the go command picks @latest.
func goNewest(vs []string) string {
	res, pre := "", ""
	for _, v := range vs {
		if semver.Prerelease(v) != "" {
			if semver.Compare(v, pre) > 0 {
				pre = v
			}
		} else if semver.Compare(v, res) > 0 {
			res = v
		}
	}
	return cmp.Or(res, pre)
}

// goMayMove reports whether a module could move to another major path. One
// that Deps only ever saw required indirectly cannot: no code imports it, so
// the lookup of its next major would not matter.
func goMayMove(name string) bool {
	seen := goDirect.get(name)
	return len(seen) == 0 || slices.Contains(seen, true)
}

func (c goProxyClient) times(paths []string, rs []core.Release) error {
	var todo []int
	for i := len(rs) - 1; i >= 0 && i >= len(rs)-goTimed; i-- {
		if rs[i].Time.IsZero() {
			todo = append(todo, i)
		}
	}
	errs := make([]error, len(todo))
	sem := make(chan struct{}, goInfoWorkers)
	var wg sync.WaitGroup
	for j, i := range todo {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			info, found, err := c.info(goModuleFor(paths, rs[i].Version), "v/"+rs[i].Version+".info")
			if found {
				rs[i].Time = info.Time
			}
			errs[j] = err
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// goModuleFor picks which of the probed paths publishes version v.
func goModuleFor(paths []string, v string) string {
	if semver.Build(v) == "+incompatible" {
		return paths[0]
	}
	for _, p := range slices.Backward(paths) {
		if goPathFor(p, v) == p {
			return p
		}
	}
	return paths[0]
}

// goHighestMajor is the largest major among the path's own versions,
// +incompatible ones included, so probing starts above majors tagged without
// a /vN path (go-chi/chi has v4.1.2+incompatible and then /v5).
func goHighestMajor(mod string, vs []string) int {
	_, pm, _ := module.SplitPathVersion(mod)
	res := goMajorNum(module.PathMajorPrefix(pm))
	for _, v := range vs {
		res = max(res, goMajorNum(semver.Major(v)))
	}
	return max(res, 1)
}

func goMajorNum(major string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(major, "v"))
	return n
}

func goPathAt(mod string, n int) string {
	prefix, _, ok := module.SplitPathVersion(mod)
	if !ok {
		return ""
	}
	if strings.HasPrefix(mod, "gopkg.in/") {
		return prefix + ".v" + strconv.Itoa(n)
	}
	if n < 2 {
		return prefix
	}
	return prefix + "/v" + strconv.Itoa(n)
}

// goPathFor is the module path that version to lives under, given the path
// currently required. +incompatible versions stay on the unsuffixed path.
func goPathFor(mod, to string) string {
	if semver.Build(to) == "+incompatible" {
		return mod
	}
	p := goPathAt(mod, goMajorNum(semver.Major(to)))
	if p == "" {
		return mod
	}
	return p
}

func goRepo(mod string) string {
	parts := strings.Split(mod, "/")
	switch {
	case len(parts) >= 3 && parts[0] == "github.com":
		return "https://github.com/" + parts[1] + "/" + parts[2]
	case len(parts) >= 3 && parts[0] == "golang.org" && parts[1] == "x":
		return "https://github.com/golang/" + parts[2]
	}
	return ""
}

func (gomod) Bump(ctx context.Context, root string, d core.Dep, to, toName string) (*core.Bump, error) {
	if toName != "" {
		return nil, fmt.Errorf("go: replacing %s with %s: %w", d.Name, toName, core.ErrNotSupported)
	}
	if !semver.IsValid(to) {
		return nil, fmt.Errorf("go: invalid version %q", to)
	}
	if !have("go") {
		return nil, core.ToolMissing{Tool: "go"}
	}
	m := d.Manifest
	dir := path.Dir(m.Path)
	snap := snapFiles(root, m.Path, path.Join(dir, "go.sum"))
	mod := goPathFor(d.Name, to)
	var moved []string
	if mod != d.Name {
		var err error
		if moved, err = goMoveMajor(root, m, d.Name, mod, to); err != nil {
			return nil, err
		}
	}
	for _, line := range []string{"go get " + shArg(mod+"@"+to), "go mod tidy"} {
		if err := runIn(ctx, root, dir, goEnv(), line); err != nil {
			return nil, err
		}
	}
	if err := goKeep(root, m, snap, d.Name, mod); err != nil {
		return nil, err
	}
	vendored := onDisk(root, path.Join(dir, "vendor/modules.txt"))
	if vendored {
		if err := runIn(ctx, root, dir, goEnv(), "go mod vendor"); err != nil {
			return nil, err
		}
	}
	files := append(snap.changed(root), moved...)
	if vendored {
		files = append(files, path.Join(dir, "vendor"))
	}
	slices.Sort(files)
	return &core.Bump{Files: slices.Compact(files), Locked: true}, nil
}

// goKeep puts back the requires go mod tidy dropped as unused: a bump PR
// moves the bumped module and leaves the rest of go.mod as the project had
// it. go.sum then keeps every line it had, but those of the bump's own
// modules, named in skip, so the kept requires still verify.
func goKeep(root string, m core.Manifest, snap fileSnap, skip ...string) error {
	before, err := modfile.Parse(m.Path, snap[m.Path], nil)
	if err != nil {
		return err
	}
	after, err := goParse(root, m.Path)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	var reqs []*modfile.Require
	for _, r := range after.Require {
		have[r.Mod.Path] = true
		reqs = append(reqs, r)
	}
	n := len(reqs)
	for _, r := range before.Require {
		if !have[r.Mod.Path] && !slices.Contains(skip, r.Mod.Path) {
			reqs = append(reqs, r)
		}
	}
	if len(reqs) == n {
		return nil
	}
	after.SetRequireSeparateIndirect(reqs)
	after.Cleanup()
	out, err := after.Format()
	if err != nil {
		return err
	}
	if err := writeKeepMode(filepath.Join(root, filepath.FromSlash(m.Path)), out); err != nil {
		return err
	}
	sum := path.Join(path.Dir(m.Path), "go.sum")
	p := filepath.Join(root, filepath.FromSlash(sum))
	now, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	lines := strings.Split(string(now), "\n")
	for _, l := range strings.Split(string(snap[sum]), "\n") {
		if f := strings.Fields(l); len(f) == 3 && !slices.Contains(skip, f[0]) && !slices.Contains(lines, l) {
			lines = append(lines, l)
		}
	}
	lines = slices.DeleteFunc(lines, func(l string) bool { return l == "" })
	slices.SortFunc(lines, goSumCompare)
	return writeKeepMode(p, []byte(strings.Join(lines, "\n")+"\n"))
}

func goSumCompare(a, b string) int {
	fa, fb := strings.Fields(a), strings.Fields(b)
	if len(fa) != 3 || len(fb) != 3 {
		return strings.Compare(a, b)
	}
	if c := strings.Compare(fa[0], fb[0]); c != 0 {
		return c
	}
	va, sa, _ := strings.Cut(fa[1], "/")
	vb, sb, _ := strings.Cut(fb[1], "/")
	return cmp.Or(semver.Compare(va, vb), strings.Compare(va, vb), strings.Compare(sa, sb), strings.Compare(fa[2], fb[2]))
}

// goEnv forces module mode for go get and tidy, and keeps a go.work above the
// module from deciding what gets edited. The proxy and checksum settings the
// caller runs with are left alone.
func goEnv() []string {
	var flags []string
	for _, f := range strings.Fields(os.Getenv("GOFLAGS")) {
		if !strings.HasPrefix(f, "-mod=") {
			flags = append(flags, f)
		}
	}
	return []string{"GOFLAGS=" + strings.Join(append(flags, "-mod=mod"), " "), "GOWORK=off"}
}

// goMoveMajor moves a module across a major version path: the require line in
// go.mod and every import of the old path in the module's .go files. Nothing
// else on those lines changes.
func goMoveMajor(root string, m core.Manifest, from, to, version string) ([]string, error) {
	f, err := goParse(root, m.Path)
	if err != nil {
		return nil, err
	}
	p := filepath.Join(root, filepath.FromSlash(m.Path))
	for _, r := range f.Require {
		if r.Mod.Path != from {
			continue
		}
		line := r.Syntax.Start.Line
		if err := rewriteLine(p, line, r.Mod.Path, to); err != nil {
			return nil, err
		}
		if err := rewriteLine(p, line, r.Mod.Version, version); err != nil {
			return nil, err
		}
	}
	srcs, err := goSources(root, path.Dir(m.Path))
	if err != nil {
		return nil, err
	}
	var res []string
	for _, rel := range srcs {
		changed, err := goRewriteImports(filepath.Join(root, filepath.FromSlash(rel)), from, to)
		if err != nil {
			return nil, err
		}
		if changed {
			res = append(res, rel)
		}
	}
	return res, nil
}

// goSources lists the .go files of the module in dir by the go command's own
// rules: vendor, testdata, dot and underscore directories and nested modules
// are not part of it.
func goSources(root, dir string) ([]string, error) {
	top := filepath.Join(root, filepath.FromSlash(dir))
	var res []string
	err := filepath.WalkDir(top, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p == top {
				return nil
			}
			if name == "vendor" || name == "testdata" || name[0] == '.' || name[0] == '_' || onDisk(p, "go.mod") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(name, ".go") && d.Type().IsRegular() {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			res = append(res, filepath.ToSlash(rel))
		}
		return nil
	})
	return res, err
}

// goRewriteImports points imports of module from (and its packages) at module
// to. An import of another major of the same module, from/v3, is left alone.
func goRewriteImports(p, from, to string) (bool, error) {
	src, err := os.ReadFile(p)
	if err != nil {
		return false, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, p, src, parser.ImportsOnly)
	if err != nil {
		return false, err
	}
	var out []byte
	last := 0
	for _, im := range f.Imports {
		ip, err := strconv.Unquote(im.Path.Value)
		if err != nil {
			continue
		}
		rest, ok := strings.CutPrefix(ip, from)
		if !ok || rest != "" && rest[0] != '/' || goMajorSuffix(rest) {
			continue
		}
		lit := im.Path.Value
		start := fset.Position(im.Path.Pos()).Offset
		out = append(out, src[last:start]...)
		out = append(out, lit[:1]+to+rest+lit[len(lit)-1:]...)
		last = start + len(lit)
	}
	if out == nil {
		return false, nil
	}
	return true, writeKeepMode(p, append(out, src[last:]...))
}

// goMajorSuffix reports whether rest starts with a /vN element, N >= 2.
func goMajorSuffix(rest string) bool {
	elem, _, _ := strings.Cut(strings.TrimPrefix(rest, "/"), "/")
	n, err := strconv.Atoi(strings.TrimPrefix(elem, "v"))
	return strings.HasPrefix(elem, "v") && err == nil && n >= 2
}

func (gomod) Commands(root string, m core.Manifest) core.Commands {
	dir := path.Dir(m.Path)
	c := core.Commands{Build: []string{inDir(dir, "go build ./...")}}
	if goHasPackages(filepath.Join(root, filepath.FromSlash(dir))) {
		c.Test = []string{inDir(dir, "go vet ./... && go test ./...")}
	}
	return c
}

// goHasPackages says whether a file of the module at dir builds under the
// default build context, leaving out the modules nested in it.
func goHasPackages(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || found {
			return filepath.SkipDir
		}
		n := d.Name()
		if d.IsDir() {
			if p != dir && (n == "testdata" || n == "vendor" || strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_") || onDisk(p, "go.mod")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(n, ".go") {
			if ok, err := build.Default.MatchFile(filepath.Dir(p), n); err == nil && ok {
				found = true
			}
		}
		return nil
	})
	return found
}

// Imports is the module path; scan matches the module's packages by prefix.
func (gomod) Traits() core.Traits {
	return core.Traits{OSV: "Go", Page: prefix("https://pkg.go.dev/"), Transitive: true}
}

func (gomod) Imports(d core.Dep) []string { return []string{d.Name} }
