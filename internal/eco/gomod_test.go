package eco

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/fetch"
)

func TestGoDetect(t *testing.T) {
	root := copyTestdata(t, "go/mono")
	got := manifestsIn(t, gomod{}, root)
	want := []core.Manifest{
		{Ecosystem: "go", Path: "go.mod", Lock: "go.sum", Tool: "go"},
		{Ecosystem: "go", Path: "tools/go.mod", Tool: "go"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Detect = %+v\nwant %+v", got, want)
	}
}

func TestGoDeps(t *testing.T) {
	root := copyTestdata(t, "go/mono")
	for _, tt := range []struct{ name, manifest string }{
		{"mono", "go.mod"},
		{"tools", "tools/go.mod"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			goldenCheck(t, "go/deps/"+tt.name+".golden.json", jsonOf(t, depsIn(t, gomod{}, root, tt.manifest)))
		})
	}
}

// goPublic points Meta at the recordings of the public proxy whatever the
// environment says.
func goPublic(t *testing.T) {
	t.Helper()
	for _, k := range []string{"GOPROXY", "GOPRIVATE", "GONOPROXY"} {
		t.Setenv(k, "")
	}
	withGoEnvFile(t, nil)
}

func TestGoMeta(t *testing.T) {
	goPublic(t)
	f := fetch.Replay(recordedDir("go"))
	for _, name := range []string{"github.com/BurntSushi/toml", "github.com/golang/protobuf", "github.com/mattn/go-sqlite3", "github.com/go-chi/chi"} {
		t.Run(name, func(t *testing.T) {
			meta, err := gomod{}.Meta(context.Background(), f, name)
			if err != nil {
				t.Fatal(err)
			}
			goldenCheck(t, "go/meta/"+strings.ReplaceAll(name, "/", "_")+".golden.json", jsonOf(t, meta))
		})
	}
	t.Run("not found", func(t *testing.T) {
		_, err := gomod{}.Meta(context.Background(), f, "example.com/tend/nope")
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("err = %v", err)
		}
	})
}

// goServer is a module proxy with one module, github.com/acme/<name>, at
// v1.0.0 through v1.<n-1>.0. It counts requests by path and the most .info
// requests it served at once.
type goServer struct {
	*httptest.Server
	mu         sync.Mutex
	hits       map[string]int
	busy, most int
}

func newGoServer(t *testing.T, name string, n int) *goServer {
	t.Helper()
	s := &goServer{hits: map[string]int{}}
	mod := "/github.com/acme/" + name + "/@"
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits[r.URL.Path]++
		s.mu.Unlock()
		p, ok := strings.CutPrefix(r.URL.Path, mod)
		switch {
		case !ok:
			http.NotFound(w, r)
		case p == "v/list":
			for i := range n {
				fmt.Fprintf(w, "v1.%d.0\n", i)
			}
		case p == "latest":
			fmt.Fprintf(w, `{"Version": "v1.%d.0", "Time": "2026-01-01T00:00:00Z"}`, n-1)
		case strings.HasSuffix(p, ".mod"):
			fmt.Fprintf(w, "module github.com/acme/%s\n", name)
		case strings.HasSuffix(p, ".info"):
			s.mu.Lock()
			s.busy++
			s.most = max(s.most, s.busy)
			s.mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			s.mu.Lock()
			s.busy--
			s.mu.Unlock()
			fmt.Fprintf(w, `{"Version": %q, "Time": "2025-01-01T00:00:00Z"}`, strings.TrimSuffix(strings.TrimPrefix(p, "v/"), ".info"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *goServer) hit(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

// A proxy's 404 for the next major path is believed for goMissTTL, by the
// next run too when there is a cache dir.
func TestGoMetaMissCached(t *testing.T) {
	goPublic(t)
	srv := newGoServer(t, "miss", 2)
	t.Setenv("GOPROXY", srv.URL)
	dir := t.TempDir()
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	client := func() *fetch.Client {
		c := fetch.New(dir, "test")
		c.Now = func() time.Time { return now }
		return c
	}
	ctx := context.Background()
	const v2 = "/github.com/acme/miss/v2/@v/list"
	c := client()
	for range 2 {
		if _, err := (gomod{}).Meta(ctx, c, "github.com/acme/miss"); err != nil {
			t.Fatal(err)
		}
	}
	if got := srv.hit(v2); got != 1 {
		t.Errorf("same run asked for /v2 %d times", got)
	}
	if _, err := (gomod{}).Meta(ctx, client(), "github.com/acme/miss"); err != nil {
		t.Fatal(err)
	}
	if got := srv.hit(v2); got != 1 {
		t.Errorf("next run asked for /v2 again: %d", got)
	}
	now = now.Add(goMissTTL)
	if _, err := (gomod{}).Meta(ctx, client(), "github.com/acme/miss"); err != nil {
		t.Fatal(err)
	}
	if got := srv.hit(v2); got != 2 {
		t.Errorf("after the TTL /v2 was asked for %d times, want 2", got)
	}
}

// A module only required indirectly cannot move to another major path, so
// its next major is not looked up.
func TestGoMetaIndirectNoProbe(t *testing.T) {
	goPublic(t)
	srv := newGoServer(t, "deep", 2)
	t.Setenv("GOPROXY", srv.URL)
	root := t.TempDir()
	write(t, root, map[string]string{"go.mod": "module x\n\ngo 1.22\n\nrequire github.com/acme/deep v1.0.0 // indirect\n"})
	depsIn(t, gomod{}, root, "go.mod")
	meta, err := gomod{}.Meta(context.Background(), fetch.New("", "test"), "github.com/acme/deep")
	if err != nil {
		t.Fatal(err)
	}
	if got := srv.hit("/github.com/acme/deep/v2/@v/list"); got != 0 || meta.Latest != "v1.1.0" {
		t.Errorf("/v2 asked %d times, meta = %+v", got, meta)
	}
}

// Release times come from .info requests run a few at a time.
func TestGoMetaTimesBounded(t *testing.T) {
	goPublic(t)
	srv := newGoServer(t, "many", 12)
	t.Setenv("GOPROXY", srv.URL)
	meta, err := gomod{}.Meta(context.Background(), fetch.New("", "test"), "github.com/acme/many")
	if err != nil {
		t.Fatal(err)
	}
	timed := 0
	for _, r := range meta.Releases {
		if !r.Time.IsZero() {
			timed++
		}
	}
	if timed != goTimed {
		t.Errorf("%d releases have a time, want %d", timed, goTimed)
	}
	if srv.most > goInfoWorkers || srv.most < 2 {
		t.Errorf("%d .info requests ran at once, want 2 to %d", srv.most, goInfoWorkers)
	}
}

func TestGoMetaRecord(t *testing.T) {
	wantRecord(t)
	goPublic(t)
	f := liveClient(t, "go")
	for _, name := range []string{"github.com/BurntSushi/toml", "github.com/golang/protobuf", "github.com/mattn/go-sqlite3", "github.com/go-chi/chi", "example.com/tend/nope"} {
		if _, err := (gomod{}).Meta(context.Background(), f, name); err != nil {
			t.Log(err)
		}
	}
}

func TestGoProxy(t *testing.T) {
	tests := []struct {
		proxy, private, mod string
		want                string
		err                 bool
	}{
		{"", "", "github.com/x/y", "https://proxy.golang.org", false},
		{"direct", "", "github.com/x/y", "https://proxy.golang.org", false},
		{"https://goproxy.corp.example/,direct", "", "github.com/x/y", "https://goproxy.corp.example", false},
		{"https://a.example|https://b.example", "", "github.com/x/y", "https://a.example", false},
		{"file:///srv/goproxy", "", "github.com/x/y", "file:///srv/goproxy", false},
		{"off", "", "github.com/x/y", "", true},
		{"", "corp.example/*,github.com/corp", "github.com/corp/lib", "", true},
		{"", "corp.example/*", "github.com/x/y", "https://proxy.golang.org", false},
	}
	withGoEnvFile(t, nil)
	for _, tt := range tests {
		t.Setenv("GOPROXY", tt.proxy)
		t.Setenv("GOPRIVATE", tt.private)
		t.Setenv("GONOPROXY", "")
		got, err := goProxy(tt.mod)
		if got != tt.want || (err != nil) != tt.err || err != nil && !errors.Is(err, core.ErrNotSupported) {
			t.Errorf("GOPROXY=%q GOPRIVATE=%q %s: %q, %v", tt.proxy, tt.private, tt.mod, got, err)
		}
	}
}

func TestGoPaths(t *testing.T) {
	tests := []struct{ mod, to, want string }{
		{"github.com/x/y", "v1.9.0", "github.com/x/y"},
		{"github.com/x/y", "v2.0.0", "github.com/x/y/v2"},
		{"github.com/x/y/v2", "v3.1.0", "github.com/x/y/v3"},
		{"github.com/x/y/v3", "v1.0.0", "github.com/x/y"},
		{"github.com/x/y", "v4.1.2+incompatible", "github.com/x/y"},
		{"gopkg.in/yaml.v2", "v3.0.1", "gopkg.in/yaml.v3"},
		{"github.com/x/y", "v0.3.0", "github.com/x/y"},
	}
	for _, tt := range tests {
		if got := goPathFor(tt.mod, tt.to); got != tt.want {
			t.Errorf("goPathFor(%s, %s) = %s, want %s", tt.mod, tt.to, got, tt.want)
		}
	}
	if got := goHighestMajor("github.com/go-chi/chi", []string{"v1.5.4", "v3.3.4+incompatible", "v4.1.2+incompatible"}); got != 4 {
		t.Errorf("goHighestMajor = %d", got)
	}
	if got := goNewest([]string{"v1.0.0", "v1.2.0-rc.1", "v1.1.0"}); got != "v1.1.0" {
		t.Errorf("goNewest = %s", got)
	}
	if got := goNewest([]string{"v0.1.0-pre", "v0.2.0-pre"}); got != "v0.2.0-pre" {
		t.Errorf("goNewest = %s", got)
	}
}

func TestGoMoveMajor(t *testing.T) {
	root := copyTestdata(t, "go/major")
	before := treeSnapshot(t, root)
	m := core.Manifest{Ecosystem: "go", Path: "go.mod", Tool: "go"}
	files, err := goMoveMajor(root, m, "example.com/lib", "example.com/lib/v2", "v2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"internal/billing/billing.go", "internal/billing/billing_test.go", "main.go"}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("files = %v, want %v", files, want)
	}
	goldenCheck(t, "go/bump/move-major.golden", treeDiff(before, treeSnapshot(t, root)))
}

func TestGoBumpRefused(t *testing.T) {
	root := copyTestdata(t, "go/mono")
	d := depIn(t, gomod{}, root, "go.mod", "github.com/google/uuid", true)
	ctx := context.Background()
	if _, err := (gomod{}).Bump(ctx, root, d, "v1.7.0", "github.com/gofrs/uuid"); !errors.Is(err, core.ErrNotSupported) {
		t.Errorf("swap: err = %v", err)
	}
	if _, err := (gomod{}).Bump(ctx, root, d, "1.7.0", ""); err == nil {
		t.Error("bumped to a version without the v")
	}
	hideTools(t)
	if _, err := (gomod{}).Bump(ctx, root, d, "v1.7.0", ""); !errors.Is(err, core.ToolMissing{Tool: "go"}) {
		t.Errorf("no go: err = %v", err)
	}
}

func TestGoCommands(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"tools/go.mod":         "module example.com/tools\n",
		"tools/main.go":        "package main\n",
		"ci/go.mod":            "module example.com/ci\n",
		"ci/magefile.go":       "//go:build mage\n\npackage main\n",
		"ci/mage.go":           "// +build ignore\n\npackage main\n",
		"ci/sub/go.mod":        "module example.com/ci/sub\n",
		"ci/sub/x.go":          "package sub\n",
		"ci/testdata/t/one.go": "package t\n",
	})
	got := gomod{}.Commands(root, core.Manifest{Path: "tools/go.mod"})
	want := core.Commands{Build: []string{"cd tools && go build ./..."}, Test: []string{"cd tools && go vet ./... && go test ./..."}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Commands = %+v", got)
	}
	if got := (gomod{}).Commands(root, core.Manifest{Path: "ci/go.mod"}); !reflect.DeepEqual(got, core.Commands{Build: []string{"cd ci && go build ./..."}}) {
		t.Errorf("a module of tagged files: Commands = %+v", got)
	}
	if got := (gomod{}).Imports(core.Dep{Name: "github.com/x/y/v2"}); !reflect.DeepEqual(got, []string{"github.com/x/y/v2"}) {
		t.Errorf("Imports = %v", got)
	}
}

func TestGoEnv(t *testing.T) {
	t.Setenv("GOFLAGS", "-modcacherw -mod=vendor -tags=integration")
	want := []string{"GOFLAGS=-modcacherw -tags=integration -mod=mod", "GOWORK=off"}
	if got := goEnv(); !reflect.DeepEqual(got, want) {
		t.Errorf("goEnv = %q", got)
	}
}

// goModule is a module version for a file:// proxy built by goFileProxy.
type goModule struct {
	path, version string
	files         map[string]string
}

// goFileProxy writes a GOPROXY directory serving mods, the layout the go
// command reads from a file:// URL. It leaves out @latest, which such a
// proxy need not serve.
func goFileProxy(t *testing.T, mods ...goModule) string {
	t.Helper()
	dir := t.TempDir()
	lists := map[string][]string{}
	for _, m := range mods {
		esc, err := module.EscapePath(m.path)
		if err != nil {
			t.Fatal(err)
		}
		at := filepath.Join(dir, filepath.FromSlash(esc), "@v")
		src := t.TempDir()
		files := map[string]string{"go.mod": "module " + m.path + "\n\ngo 1.21\n"}
		for k, v := range m.files {
			files[k] = v
		}
		write(t, src, files)
		write(t, at, map[string]string{
			m.version + ".mod":  files["go.mod"],
			m.version + ".info": `{"Version":"` + m.version + `","Time":"2026-01-02T03:04:05Z"}`,
		})
		zf, err := os.Create(filepath.Join(at, m.version+".zip"))
		if err != nil {
			t.Fatal(err)
		}
		if err := modzip.CreateFromDir(zf, module.Version{Path: m.path, Version: m.version}, src); err != nil {
			t.Fatal(err)
		}
		zf.Close()
		lists[at] = append(lists[at], m.version)
	}
	for at, vs := range lists {
		write(t, at, map[string]string{"list": strings.Join(vs, "\n") + "\n"})
	}
	return "file://" + filepath.ToSlash(dir)
}

func TestGoMetaFileProxy(t *testing.T) {
	lib := map[string]string{"lib.go": "package lib\n"}
	proxy := goFileProxy(t,
		goModule{"example.com/Lib", "v1.0.0", lib},
		goModule{"example.com/Lib", "v1.1.0", lib},
		goModule{"example.com/Lib/v2", "v2.0.0", lib},
	)
	goPublic(t)
	t.Setenv("GOPROXY", proxy)
	meta, err := gomod{}.Meta(context.Background(), nil, "example.com/Lib")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range meta.Releases {
		got = append(got, r.Version+" "+r.Time.Format("2006-01-02"))
	}
	want := []string{"v1.0.0 2026-01-02", "v1.1.0 2026-01-02", "v2.0.0 2026-01-02"}
	if !reflect.DeepEqual(got, want) || meta.Latest != "v2.0.0" {
		t.Errorf("releases = %v, latest %s", got, meta.Latest)
	}
}

// TestGoBumpReal runs go get and go mod tidy against a file:// proxy, across a
// minor version and then across a major version path.
func TestGoBumpReal(t *testing.T) {
	if !have("go") {
		t.Skip("go not found")
	}
	hello := func(s string) map[string]string {
		return map[string]string{"lib.go": "package lib\n\nfunc Hello() string { return \"" + s + "\" }\n"}
	}
	proxy := goFileProxy(t,
		goModule{"example.com/lib", "v1.0.0", hello("v1.0.0")},
		goModule{"example.com/lib", "v1.1.0", hello("v1.1.0")},
		goModule{"example.com/lib/v2", "v2.0.0", hello("v2.0.0")},
	)
	goPublic(t)
	t.Setenv("GOPROXY", proxy)
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOFLAGS", "-modcacherw")
	t.Setenv("GOMODCACHE", t.TempDir())
	t.Setenv("GOTOOLCHAIN", "local")
	ctx := context.Background()
	root := t.TempDir()
	write(t, root, map[string]string{
		"go.mod":  "module example.com/app\n\ngo 1.21\n\nrequire example.com/lib v1.0.0\n",
		"main.go": "package main\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/lib\"\n)\n\nfunc main() { fmt.Println(lib.Hello()) }\n",
	})
	if out, err := run(ctx, root, goEnv(), "go mod tidy"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	d := depIn(t, gomod{}, root, "go.mod", "example.com/lib", true)

	res, err := gomod{}.Bump(ctx, root, d, "v1.1.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Locked || !reflect.DeepEqual(res.Files, []string{"go.mod", "go.sum"}) {
		t.Errorf("minor bump = %+v", res)
	}

	d = depIn(t, gomod{}, root, "go.mod", "example.com/lib", true)
	if d.Version != "v1.1.0" {
		t.Fatalf("after minor bump: %+v", d)
	}
	res, err = gomod{}.Bump(ctx, root, d, "v2.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Locked || !reflect.DeepEqual(res.Files, []string{"go.mod", "go.sum", "main.go"}) {
		t.Errorf("major bump = %+v", res)
	}
	if d := depIn(t, gomod{}, root, "go.mod", "example.com/lib/v2", true); d.Version != "v2.0.0" {
		t.Errorf("after major bump: %+v", d)
	}
	out, err := run(ctx, root, goEnv(), "go run .")
	if err != nil || out != "v2.0.0\n" {
		t.Errorf("go run = %q, %v", out, err)
	}
}

// go mod tidy drops a require nothing imports; the bump puts it back, with
// its go.sum lines, so the PR moves only the bumped module.
func TestGoBumpKeepsUnusedRequires(t *testing.T) {
	if !have("go") {
		t.Skip("go not found")
	}
	hello := func(s string) map[string]string {
		return map[string]string{"lib.go": "package lib\n\nfunc Hello() string { return \"" + s + "\" }\n"}
	}
	proxy := goFileProxy(t,
		goModule{"example.com/lib", "v1.0.0", hello("v1.0.0")},
		goModule{"example.com/lib", "v1.1.0", hello("v1.1.0")},
		goModule{"example.com/unused", "v1.0.0", hello("unused")},
		goModule{"example.com/old", "v1.2.0", hello("old")},
	)
	goPublic(t)
	t.Setenv("GOPROXY", proxy)
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOFLAGS", "-modcacherw")
	t.Setenv("GOMODCACHE", t.TempDir())
	t.Setenv("GOTOOLCHAIN", "local")
	ctx := context.Background()
	root := t.TempDir()
	write(t, root, map[string]string{
		"go.mod":  "module example.com/app\n\ngo 1.21\n\nrequire example.com/lib v1.0.0\n",
		"main.go": "package main\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/lib\"\n)\n\nfunc main() { fmt.Println(lib.Hello()) }\n",
	})
	if out, err := run(ctx, root, goEnv(), "go mod tidy && go get example.com/unused@v1.0.0 example.com/old@v1.2.0"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// a direct require the code stopped importing, and an indirect one
	write(t, root, map[string]string{"go.mod": "module example.com/app\n\ngo 1.21\n\nrequire (\n\texample.com/lib v1.0.0\n\texample.com/old v1.2.0\n)\n\nrequire example.com/unused v1.0.0 // indirect\n"})
	mod := func() string {
		b, err := os.ReadFile(filepath.Join(root, "go.mod"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	was := mod()
	d := depIn(t, gomod{}, root, "go.mod", "example.com/lib", true)
	if _, err := (gomod{}).Bump(ctx, root, d, "v1.1.0", ""); err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(was, "example.com/lib v1.0.0", "example.com/lib v1.1.0", 1); mod() != want {
		t.Errorf("go.mod after the bump:\n%s\nwant\n%s", mod(), want)
	}
	if out, err := run(ctx, root, append(goEnv()[1:], "GOFLAGS=-mod=readonly"), "go list -m all && go build ./..."); err != nil {
		t.Errorf("the kept requires do not verify: %v\n%s", err, out)
	}
}

func withGoEnvFile(t *testing.T, m map[string]string) {
	t.Helper()
	old := goEnvFile
	goEnvFile = func() map[string]string { return m }
	t.Cleanup(func() { goEnvFile = old })
}

func TestGoProxyEnvFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(p, []byte("GOPRIVATE=github.com/corp/*\nGOPROXY=https://goproxy.corp.example,direct\nbroken line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withGoEnvFile(t, readGoEnvFile(p))
	t.Setenv("GOPROXY", "")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GONOPROXY", "")
	if _, err := goProxy("github.com/corp/lib"); !errors.Is(err, core.ErrNotSupported) {
		t.Errorf("private module from the env file: %v", err)
	}
	if got, err := goProxy("github.com/x/y"); err != nil || got != "https://goproxy.corp.example" {
		t.Errorf("proxy from the env file = %q, %v", got, err)
	}
	t.Setenv("GOPROXY", "https://other.example")
	t.Setenv("GONOPROXY", "none.example")
	if got, err := goProxy("github.com/corp/lib"); err != nil || got != "https://other.example" {
		t.Errorf("the environment wins = %q, %v", got, err)
	}
}

func TestGoSumOrder(t *testing.T) {
	lines := []string{
		"example.com/a v0.10.0/go.mod h1:b=",
		"example.com/b v1.0.0 h1:e=",
		"example.com/a v0.9.0 h1:c=",
		"example.com/a v0.10.0 h1:a=",
		"example.com/a v1.0.0-rc.1 h1:f=",
		"example.com/a v0.9.0/go.mod h1:d=",
		"example.com/a v1.0.0 h1:g=",
	}
	slices.SortFunc(lines, goSumCompare)
	want := []string{
		"example.com/a v0.9.0 h1:c=",
		"example.com/a v0.9.0/go.mod h1:d=",
		"example.com/a v0.10.0 h1:a=",
		"example.com/a v0.10.0/go.mod h1:b=",
		"example.com/a v1.0.0-rc.1 h1:f=",
		"example.com/a v1.0.0 h1:g=",
		"example.com/b v1.0.0 h1:e=",
	}
	if !slices.Equal(lines, want) {
		t.Errorf("order =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

// A module far behind its newest major finds it in a few requests: majors
// v2 through v90 of github.com/acme/far exist, and the latest is v90.
func TestGoMetaFarMajors(t *testing.T) {
	goPublic(t)
	var mu sync.Mutex
	lists := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, ok := strings.CutPrefix(r.URL.Path, "/github.com/acme/far")
		if !ok {
			http.NotFound(w, r)
			return
		}
		major, file := 1, strings.TrimPrefix(rest, "/@")
		if m, f, ok := strings.Cut(strings.TrimPrefix(rest, "/v"), "/@"); ok && strings.HasPrefix(rest, "/v") {
			if _, err := fmt.Sscanf(m, "%d", &major); err != nil || major > 90 {
				http.NotFound(w, r)
				return
			}
			file = f
		}
		v := fmt.Sprintf("v%d.0.0", major)
		mod := "github.com/acme/far"
		if major > 1 {
			mod += fmt.Sprintf("/v%d", major)
		}
		switch {
		case file == "v/list":
			mu.Lock()
			lists++
			mu.Unlock()
			fmt.Fprintln(w, v)
		case file == "latest" || strings.HasSuffix(file, ".info"):
			fmt.Fprintf(w, `{"Version": %q, "Time": "2026-01-01T00:00:00Z"}`, v)
		case strings.HasSuffix(file, ".mod"):
			fmt.Fprintf(w, "module %s\n", mod)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("GOPROXY", srv.URL)
	meta, err := (gomod{}).Meta(context.Background(), fetch.New("", "test"), "github.com/acme/far/v28")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Latest != "v90.0.0" {
		t.Errorf("latest = %q, want v90.0.0", meta.Latest)
	}
	var vs []string
	for _, r := range meta.Releases {
		vs = append(vs, r.Version)
	}
	if !slices.Equal(vs, []string{"v28.0.0", "v29.0.0", "v90.0.0"}) {
		t.Errorf("releases = %v", vs)
	}
	if lists > 20 {
		t.Errorf("%d list requests, want a few", lists)
	}
}
