package e2e

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"

	"github.com/mohsensc/tend-cli/internal/cli"
	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/deliver"
	"github.com/mohsensc/tend-cli/internal/fetch"
	"github.com/mohsensc/tend-cli/internal/state"
	"github.com/mohsensc/tend-cli/internal/ver"
)

var regen = flag.Bool("update", false, "rewrite golden files")

// now is the fixed clock every scenario runs at.
var now = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

// testdata is the repo's shared fixture tree.
var testdata = filepath.Join("..", "..", "testdata")

// maintainer is who the fixture clone's git config says commits.
var maintainer = struct{ Name, Email string }{"Ada Maintainer", "ada@example.com"}

// world is one fixture project checked out next to a bare origin, with the
// registries, the forge and the upstream data it needs.
type world struct {
	t       *testing.T
	dir     string // the user's clone
	origin  string // bare repo the clone pushes to
	home    string // TEND_HOME
	proxy   string // the Go module proxy, file:// served
	reg     *Registry
	forge   *deliver.FakeForge
	model   core.Model
	fetch   *router
	getenv  map[string]string
	noForge bool
	stdin   string // what a person types at the prompts
	base    string // main on origin, as the clone last pushed it
}

func need(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found in PATH", tool)
		}
	}
}

// setup is fresh, with tend already past its first PR on the repo: the
// scenario starts in normal operation.
func setup(t *testing.T, name string) *world {
	t.Helper()
	w := fresh(t, name)
	st := w.state()
	st.Onboard = "before"
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	return w
}

// fresh copies testdata/projects/<name> into a new repo with one commit on
// main, pushed to a bare origin, that tend has never run on.
func fresh(t *testing.T, name string) *world {
	t.Helper()
	need(t, "git")
	root := t.TempDir()
	w := &world{
		t:      t,
		dir:    filepath.Join(root, "work"),
		origin: filepath.Join(root, "origin.git"),
		home:   filepath.Join(root, "home"),
		forge:  deliver.NewFake("acme", name, "main"),
		getenv: map[string]string{"GITHUB_REPOSITORY": "acme/" + name},
	}
	w.forge.Now = func() time.Time { return now }
	t.Cleanup(func() {
		if w.forge.Peak > 1 {
			t.Errorf("%d PRs were open at once", w.forge.Peak)
		}
	})
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR", "GIT_COMMITTER"} {
		t.Setenv(k+"_DATE", "2026-10-01T09:00:00Z")
	}
	t.Setenv("TEND_HOME", w.home)

	reg, err := NewRegistry(filepath.Join(testdata, "npmreg"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.Close)
	w.reg = reg
	w.fetch = &router{
		local:  fetch.New("", "e2e"),
		replay: upstream(t),
		osv:    &osv{dir: filepath.Join(testdata, "recorded", "e2e", "osv"), ids: []string{"GO-2026-0001"}},
		spec:   filepath.Join(testdata, "recorded", "e2e", "fixture-api", "v2.json"),
		fail:   map[string]error{},
	}
	t.Setenv("npm_config_registry", w.reg.URL+"/")
	t.Setenv("npm_config_cache", filepath.Join(root, "npm-cache"))
	t.Setenv("npm_config_update_notifier", "false")
	t.Setenv("npm_config_audit", "false")
	t.Setenv("npm_config_fund", "false")
	w.proxy = goProxy(t, filepath.Join(root, "goproxy"))
	w.publish("v1.0.0", "v1.0.1", "v1.1.0")
	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(w.proxy))
	t.Setenv("GOFLAGS", "-mod=mod -modcacherw -count=1")
	t.Setenv("GONOSUMDB", "example.com")
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOMODCACHE", filepath.Join(root, "gomod"))

	if err := CopyProject(filepath.Join(testdata, "projects", name), w.dir, w.reg.Vars()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.dir, "requirements.txt")); err == nil {
		wheels(t, filepath.Join(w.dir, "wheels"))
	}
	w.git(w.dir, "init", "--quiet", "--initial-branch=main")
	w.git(w.dir, "config", "user.name", maintainer.Name)
	w.git(w.dir, "config", "user.email", maintainer.Email)
	w.git(w.dir, "add", "-A")
	w.git(w.dir, "commit", "--quiet", "-m", "Start")
	w.git(root, "init", "--quiet", "--bare", "--initial-branch=main", w.origin)
	w.git(w.dir, "remote", "add", "origin", w.origin)
	w.git(w.dir, "push", "--quiet", "origin", "main")
	w.git(w.dir, "remote", "set-head", "origin", "main")
	w.base = w.git(w.dir, "rev-parse", "HEAD")
	return w
}

// commit adds files to the clone and pushes them, as a maintainer would
// before tend runs.
func (w *world) commit(msg string, files map[string]string) {
	w.t.Helper()
	for name, body := range files {
		p := filepath.Join(w.dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			w.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			w.t.Fatal(err)
		}
	}
	w.git(w.dir, "add", "-A")
	w.git(w.dir, "commit", "--quiet", "-m", msg)
	w.git(w.dir, "push", "--quiet", "origin", "main")
	w.base = w.git(w.dir, "rev-parse", "HEAD")
}

// pull brings the clone up to main on origin, as the Action's checkout of
// the default branch is before each run, tend.yml included.
func (w *world) pull() {
	w.t.Helper()
	w.git(w.dir, "fetch", "--quiet", "origin")
	w.git(w.dir, "reset", "--quiet", "--hard", "origin/main")
}

func (w *world) git(dir string, args ...string) string {
	w.t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		w.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

type result struct {
	code           int
	stdout, stderr string
}

// tend runs the command line in the clone, the way a user would.
func (w *world) tend(args ...string) result {
	w.t.Helper()
	var out, errs bytes.Buffer
	e := cli.Env{
		Dir:     w.dir,
		Stdout:  &out,
		Stderr:  &errs,
		Stdin:   strings.NewReader(w.stdin),
		Fetch:   w.fetch,
		Model:   w.model,
		Now:     func() time.Time { return now },
		Home:    w.home,
		Getenv:  func(k string) string { return w.getenv[k] },
		Version: "dev",
	}
	if !w.noForge {
		e.Forge = w.forge
	}
	code := cli.Main(args, e)
	return result{code, out.String(), errs.String()}
}

// want fails unless the command exited with code.
func (r result) want(t *testing.T, code int) result {
	t.Helper()
	if r.code != code {
		t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", r.code, code, r.stdout, r.stderr)
	}
	return r
}

// merge lands PR n the way GitHub would: main on origin moves to its branch.
func (w *world) merge(n int) {
	w.t.Helper()
	pr := w.forge.PR(n)
	if err := w.forge.Merge(n); err != nil {
		w.t.Fatal(err)
	}
	w.git(w.origin, "update-ref", "refs/heads/main", "refs/heads/"+pr.Branch)
	w.base = w.git(w.origin, "rev-parse", "main")
}

// say comments on PR n as someone with the given role.
func (w *world) say(n int, author, role, body string) {
	w.t.Helper()
	if err := w.forge.Say(n, author, role, body); err != nil {
		w.t.Fatal(err)
	}
}

// closeByHuman closes PR n unmerged and deletes its branch.
func (w *world) closeByHuman(n int) {
	w.t.Helper()
	pr := w.forge.PR(n)
	if err := w.forge.CloseByHuman(n); err != nil {
		w.t.Fatal(err)
	}
	w.git(w.origin, "update-ref", "-d", "refs/heads/"+pr.Branch)
}

// diff is the change a PR branch carries, as a reviewer sees it.
func (w *world) diff(branch string) string {
	return w.git(w.origin, "diff", "main..."+branch)
}

func (w *world) state() *state.State {
	w.t.Helper()
	st, _, err := state.Open(w.home, state.Key(w.origin, w.dir))
	if err != nil {
		w.t.Fatal(err)
	}
	return st
}

// golden compares got, made stable, with testdata/<test>/<name>.
func (w *world) golden(name, got string) {
	w.t.Helper()
	got = w.stable(got)
	p := filepath.Join("testdata", w.t.Name(), name)
	if *regen {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			w.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			w.t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		w.t.Fatalf("%v (run with -update to create it)", err)
	}
	if got != string(want) {
		w.t.Errorf("%s differs from %s:\n%s", name, p, lineDiff(string(want), got))
	}
}

var (
	durations = regexp.MustCompile(`\b\d+(\.\d+)?(ns|µs|ms|s|m\d+(\.\d+)?s|h\d+m\d+(\.\d+)?s)\b`)
	nodeTimes = regexp.MustCompile(`(duration_ms:?) [\d.]+`)
	goTimes   = regexp.MustCompile(`(ok\s+\S+)\s+[\d.]+s`)
	blobs     = regexp.MustCompile(`(?m)^index [0-9a-f]+\.\.[0-9a-f]+`)
)

// stable takes out what changes from one machine or run to the next: temp
// paths, the registry port and timings.
func (w *world) stable(s string) string {
	for _, n := range []int{len(w.base), 12, 7} {
		s = strings.ReplaceAll(s, w.base[:n], "BASE")
	}
	s = blobs.ReplaceAllString(s, "index 0000000..0000000")
	s = strings.ReplaceAll(s, w.reg.URL, "http://registry.test")
	s = strings.ReplaceAll(s, state.Key(w.origin, w.dir), "KEY")
	for _, p := range []struct{ dir, name string }{{w.home, "$TEND_HOME"}, {filepath.Dir(w.dir), "$TMP"}} {
		if real, err := filepath.EvalSymlinks(p.dir); err == nil {
			s = strings.ReplaceAll(s, real, p.name)
		}
		s = strings.ReplaceAll(s, p.dir, p.name)
	}
	s = goTimes.ReplaceAllString(s, "$1 0.0s")
	s = nodeTimes.ReplaceAllString(s, "$1 0")
	s = durations.ReplaceAllString(s, "0s")
	return tails.ReplaceAllString(s, "$1\n(output)\n$2")
}

// tails are the build and test output a PR body quotes. Its shape belongs to
// the tool and its version, not to tend, so goldens leave it out; tests that
// care look for the lines they need.
var tails = regexp.MustCompile("(?s)(<details><summary>[^\n]*</summary>\n\n```+)\n.*?\n(```+\n</details>)")

func lineDiff(want, got string) string {
	a, b := strings.Split(want, "\n"), strings.Split(got, "\n")
	var res []string
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y string
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			res = append(res, fmt.Sprintf("line %d:\n  want %q\n  got  %q", i+1, x, y))
		}
		if len(res) == 8 {
			break
		}
	}
	return strings.Join(res, "\n")
}

// publish makes versions of the fixture module visible in the proxy's
// listing, the way a new release shows up upstream.
func (w *world) publish(versions ...string) {
	w.t.Helper()
	p := filepath.Join(w.proxy, "example.com", "fixture", "crypto", "@v", "list")
	b, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		w.t.Fatal(err)
	}
	for _, v := range versions {
		b = append(b, v+"\n"...)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		w.t.Fatal(err)
	}
}

// goProxy lays out testdata/goproxy as a file:// module proxy in dir, with
// the module zips built from the sources next to it: src/<version> for the
// crypto module, whose listing starts empty, and mods/<path>/<version> for
// the others.
func goProxy(t *testing.T, dir string) string {
	t.Helper()
	src := filepath.Join(testdata, "goproxy")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := must(filepath.Rel(src, p))
		if strings.HasPrefix(rel, "src"+string(filepath.Separator)) || strings.HasPrefix(rel, "mods"+string(filepath.Separator)) {
			return nil
		}
		out := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return os.WriteFile(out, must(os.ReadFile(p)), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	zip := func(mv module.Version, from string) {
		f := must(os.Create(filepath.Join(dir, filepath.FromSlash(mv.Path), "@v", mv.Version+".zip")))
		defer f.Close()
		if err := modzip.CreateFromDir(f, mv, from); err != nil {
			t.Fatal(err)
		}
	}
	vs, _ := os.ReadDir(filepath.Join(src, "src"))
	for _, v := range vs {
		zip(module.Version{Path: "example.com/fixture/crypto", Version: v.Name()}, filepath.Join(src, "src", v.Name()))
	}
	mods := filepath.Join(src, "mods")
	err = filepath.WalkDir(mods, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.Name() != "go.mod" {
			return err
		}
		version := filepath.Dir(p)
		path := filepath.ToSlash(must(filepath.Rel(mods, filepath.Dir(version))))
		zip(module.Version{Path: path, Version: filepath.Base(version)}, version)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// upstream replays the recorded responses, or records new ones from the real
// services when TEND_RECORD=1.
func upstream(t *testing.T) core.Fetcher {
	dir := filepath.Join(testdata, "recorded", "e2e")
	if os.Getenv("TEND_RECORD") != "1" {
		return fetch.Replay(dir)
	}
	c := fetch.New("", "e2e")
	if out, err := exec.Command("gh", "auth", "token").Output(); err == nil {
		tok := strings.TrimSpace(string(out))
		c.Token = func(host string) string {
			if host == "api.github.com" {
				return tok
			}
			return ""
		}
	}
	return fetch.Record(c, dir)
}

// router sends each request where the scenario wants it: the local
// registries live, OSV to a simulation over recorded advisories, the fixture
// API to its spec file, and everything else to the recordings.
type router struct {
	local  core.Fetcher
	replay core.Fetcher
	osv    *osv
	spec   string
	fail   map[string]error
}

var _ core.Fetcher = (*router)(nil)

func (r *router) route(u string) (core.Fetcher, error) {
	pu, err := url.Parse(u)
	if err != nil {
		return nil, err
	}
	if err := r.fail[pu.Hostname()]; err != nil {
		return nil, err
	}
	if pu.Hostname() == "127.0.0.1" {
		return r.local, nil
	}
	return r.replay, nil
}

func (r *router) Get(ctx context.Context, u string, opts ...core.FetchOpt) (*core.Response, error) {
	f, err := r.route(u)
	switch {
	case err != nil:
		return nil, err
	case strings.HasPrefix(u, "https://api.osv.dev/v1/vulns/"):
		return r.osv.vuln(strings.TrimPrefix(u, "https://api.osv.dev/v1/vulns/"))
	case u == "https://api.apis.guru/v2/list.json":
		return file(filepath.Join(testdata, "recorded", "e2e", "apis-guru", "list.json"))
	case u == "https://api.apis.guru/v2/specs/example-unknown.com/2.0.0/openapi.json":
		return file(filepath.Join(testdata, "recorded", "e2e", "apis-guru", "example-unknown.json"))
	case strings.HasPrefix(u, "https://pypi.org/pypi/fixturelib/json"):
		return file(filepath.Join(testdata, "recorded", "e2e", "pypi", "fixturelib.json"))
	case strings.Contains(u, "nowhere-documented.com/"):
		// A host that publishes no spec anywhere tend looks.
		return &core.Response{Status: 404}, nil
	case strings.HasPrefix(u, "https://api.fixture.dev/"):
		if u != "https://api.fixture.dev/openapi.json" {
			return &core.Response{Status: 404}, nil
		}
		return &core.Response{Status: 200, Body: must(os.ReadFile(r.spec))}, nil
	}
	return f.Get(ctx, u, opts...)
}

func (r *router) Post(ctx context.Context, u string, body []byte, opts ...core.FetchOpt) (*core.Response, error) {
	f, err := r.route(u)
	switch {
	case err != nil:
		return nil, err
	case u == "https://api.osv.dev/v1/querybatch":
		return r.osv.batch(body)
	}
	return f.Post(ctx, u, body, opts...)
}

// osv answers like api.osv.dev from the advisories in dir that the scenario
// switched on.
type osv struct {
	dir string
	ids []string
}

type osvVuln struct {
	ID       string `json:"id"`
	Affected []struct {
		Package struct{ Name, Ecosystem string } `json:"package"`
		Ranges  []struct {
			Events []map[string]string `json:"events"`
		} `json:"ranges"`
	} `json:"affected"`
}

func (o *osv) vuln(id string) (*core.Response, error) {
	b, err := os.ReadFile(filepath.Join(o.dir, id+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return &core.Response{Status: 404, Body: []byte(`{"code":5,"message":"Bug not found."}`)}, nil
	}
	return &core.Response{Status: 200, Body: b}, err
}

func (o *osv) batch(body []byte) (*core.Response, error) {
	var req struct {
		Queries []struct {
			Package struct{ Name, Ecosystem string } `json:"package"`
			Version string                           `json:"version"`
		} `json:"queries"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return &core.Response{Status: 400}, nil
	}
	var vulns []osvVuln
	for _, id := range o.ids {
		var v osvVuln
		if err := json.Unmarshal(must(os.ReadFile(filepath.Join(o.dir, id+".json"))), &v); err != nil {
			return nil, err
		}
		vulns = append(vulns, v)
	}
	type hit struct {
		ID string `json:"id"`
	}
	results := []map[string][]hit{}
	for _, q := range req.Queries {
		res := map[string][]hit{}
		for _, v := range vulns {
			if affects(v, q.Package.Ecosystem, q.Package.Name, q.Version) {
				res["vulns"] = append(res["vulns"], hit{v.ID})
			}
		}
		results = append(results, res)
	}
	b, _ := json.Marshal(map[string]any{"results": results})
	return &core.Response{Status: 200, Body: b}, nil
}

func affects(v osvVuln, eco, name, version string) bool {
	at, ok := ver.Parse(version)
	if !ok {
		return false
	}
	for _, a := range v.Affected {
		if a.Package.Ecosystem != eco || a.Package.Name != name {
			continue
		}
		for _, r := range a.Ranges {
			in := false
			for _, ev := range r.Events {
				if s, ok := ev["introduced"]; ok {
					x, _ := ver.Parse(s)
					in = ver.Compare(at, x) >= 0
				}
				if s, ok := ev["fixed"]; ok && in {
					x, _ := ver.Parse(s)
					in = ver.Compare(at, x) < 0
				}
			}
			if in {
				return true
			}
		}
	}
	return false
}

// wheels builds testdata/pywheels/<name>-<version> into installable wheels
// in dir, the way a project vendors what pip installs offline.
func wheels(t *testing.T, dir string) {
	t.Helper()
	srcs, _ := filepath.Glob(filepath.Join(testdata, "pywheels", "*"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, src := range srcs {
		name, version, _ := strings.Cut(filepath.Base(src), "-")
		info := name + "-" + version + ".dist-info/"
		files := map[string][]byte{
			info + "METADATA":      []byte("Metadata-Version: 2.1\nName: " + name + "\nVersion: " + version + "\n"),
			info + "WHEEL":         []byte("Wheel-Version: 1.0\nGenerator: tend-e2e\nRoot-Is-Purelib: true\nTag: py3-none-any\n"),
			info + "top_level.txt": []byte(name + "\n"),
		}
		err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel := must(filepath.Rel(src, p))
			files[filepath.ToSlash(rel)] = must(os.ReadFile(p))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		var record strings.Builder
		for _, n := range slices.Sorted(maps.Keys(files)) {
			sum := sha256.Sum256(files[n])
			fmt.Fprintf(&record, "%s,sha256=%s,%d\n", n, base64.RawURLEncoding.EncodeToString(sum[:]), len(files[n]))
		}
		record.WriteString(info + "RECORD,,\n")
		files[info+"RECORD"] = []byte(record.String())

		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for _, n := range slices.Sorted(maps.Keys(files)) {
			f, err := zw.CreateHeader(&zip.FileHeader{Name: n, Method: zip.Deflate, Modified: time.Unix(499162500, 0)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write(files[n]); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+"-"+version+"-py3-none-any.whl"), buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func file(p string) (*core.Response, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return &core.Response{Status: 200, Body: b}, nil
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
