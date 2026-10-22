package eco

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/fetch"
)

func TestDockerDetect(t *testing.T) {
	root := copyTestdata(t, "docker/project")
	got := manifestsIn(t, docker{}, root)
	var paths []string
	for _, m := range got {
		if m.Ecosystem != "docker" || m.Tool != "docker" || m.Lock != "" {
			t.Errorf("manifest = %+v", m)
		}
		paths = append(paths, m.Path)
	}
	want := []string{"Dockerfile", "compose.yaml", "docker-compose.override.yml", "services/api/Dockerfile.dev", "web.dockerfile"}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("Detect = %v\nwant %v", paths, want)
	}
}

func TestDockerDeps(t *testing.T) {
	root := copyTestdata(t, "docker/project")
	for _, tt := range []struct{ name, manifest string }{
		{"dockerfile", "Dockerfile"},
		{"compose", "compose.yaml"},
		{"override", "docker-compose.override.yml"},
		{"crlf-platform-and-alias-kept", "services/api/Dockerfile.dev"},
		{"dockerfile-extension", "web.dockerfile"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			goldenCheck(t, "docker/deps/"+tt.name+".golden.json", jsonOf(t, depsIn(t, docker{}, root, tt.manifest)))
		})
	}
}

func TestDockerfileRefs(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string // line name version pin
	}{
		{"platform and alias", "FROM --platform=linux/arm64 node:20 AS a\nFROM a\nFROM a AS b\nFROM B\n", []string{"1 library/node 20 "}},
		{"alias is case blind", "from golang:1.22 as Builder\nFROM builder\n", []string{"1 library/golang 1.22 "}},
		{"owner image", "FROM bitnami/redis:7.2\n", []string{"1 bitnami/redis 7.2 "}},
		{"hub host", "FROM index.docker.io/library/alpine:3.20\nFROM docker.io/grafana/grafana\n", []string{"1 library/alpine 3.20 ", "2 grafana/grafana latest "}},
		{"registry port", "FROM registry.local:5000/a/b:1\n", []string{"1 registry.local:5000/a/b 1 "}},
		{"continuation", "FROM \\\n  node:22 \\\n  AS web\n", []string{"1 library/node 22 "}},
		{"comments", "# FROM ignored:1\n  # FROM ignored:2\nFROM kept:1\n", []string{"3 library/kept 1 "}},
		{"arg default with quotes", "ARG V=\"3.19\"\nFROM alpine:$V\nFROM alpine:${V}-x\n", []string{"2 library/alpine 3.19 ", "3 library/alpine 3.19-x "}},
		{"inline default", "FROM alpine:${V:-3.18}\nFROM alpine:${V-3.17}\nFROM alpine${SUFFIX:+-alt}\n", []string{"1 library/alpine 3.18 ", "2 library/alpine 3.17 ", "3 library/alpine latest "}},
		{"arg after first from is invisible", "FROM a:1 AS x\nARG V=2\nFROM b:${V}\n", []string{"1 library/a 1 ", "3 library/b  "}},
		{"arg without default", "ARG V\nFROM b:${V}\nFROM ${V} AS c\n", []string{"2 library/b  "}},
		{"lowercase keywords", "from node:1\narg X=2\n", []string{"1 library/node 1 "}},
		{"no from", "RUN echo hi\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, r := range dockerfileRefs(strings.Split(tt.src, "\n")) {
				got = append(got, fmt.Sprintf("%d %s %s %s", r.line, r.name, r.dep(core.Manifest{}).Version, r.digest))
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestDockerSplit(t *testing.T) {
	tests := []struct{ in, name, tag, digest string }{
		{"node", "node", "", ""},
		{"node:20", "node", "20", ""},
		{"node@sha256:abc", "node", "", "sha256:abc"},
		{"node:20@sha256:abc", "node", "20", "sha256:abc"},
		{"localhost:5000/a/b", "localhost:5000/a/b", "", ""},
		{"localhost:5000/a/b:1", "localhost:5000/a/b", "1", ""},
		{"${A:-x}:${B:-y}", "${A:-x}", "${B:-y}", ""},
		{"${A:-x}", "${A:-x}", "", ""},
		{"a/${B}:1", "a/${B}", "1", ""},
	}
	for _, tt := range tests {
		name, tag, digest := dockerSplit(tt.in)
		if name != tt.name || tag != tt.tag || digest != tt.digest {
			t.Errorf("dockerSplit(%q) = %q %q %q, want %q %q %q", tt.in, name, tag, digest, tt.name, tt.tag, tt.digest)
		}
	}
}

func TestDockerCanon(t *testing.T) {
	tests := map[string]string{
		"node":                             "library/node",
		"docker.io/node":                   "library/node",
		"docker.io/library/node":           "library/node",
		"registry-1.docker.io/a/b":         "a/b",
		"grafana/grafana":                  "grafana/grafana",
		"ghcr.io/a/b":                      "ghcr.io/a/b",
		"localhost/a":                      "localhost/a",
		"localhost:5000/a":                 "localhost:5000/a",
		"quay.io/prometheus/node-exporter": "quay.io/prometheus/node-exporter",
	}
	for in, want := range tests {
		if got := dockerCanon(in); got != want {
			t.Errorf("dockerCanon(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDockerExpand(t *testing.T) {
	val := func(s string) *string { return &s }
	args := map[string]*string{"A": val("1"), "EMPTY": val(""), "NONE": nil}
	tests := []struct {
		in, want string
		missing  []string
	}{
		{"x$A", "x1", nil},
		{"x${A}y", "x1y", nil},
		{"${A:-d}", "1", nil},
		{"${EMPTY:-d}", "d", nil},
		{"${NONE-d}", "d", nil},
		{"${A:+alt}", "alt", nil},
		{"${EMPTY:+alt}", "", nil},
		{"${NONE}", dockerUnknown, []string{"NONE"}},
		{"${GONE}:$GONE2", dockerUnknown + ":" + dockerUnknown, []string{"GONE", "GONE2"}},
		{"no vars", "no vars", nil},
	}
	for _, tt := range tests {
		got, missing := dockerExpand(tt.in, args)
		if got != tt.want || !reflect.DeepEqual(missing, tt.missing) {
			t.Errorf("dockerExpand(%q) = %q, %v; want %q, %v", tt.in, got, missing, tt.want, tt.missing)
		}
	}
}

// digestOf is a made-up sha256 digest, n repeated.
func digestOf(n string) string { return "sha256:" + strings.Repeat(n, 64) }

// withRegistry has Bump ask f for digests, as it asks what Meta last used.
func withRegistry(t *testing.T, f core.Fetcher) {
	t.Helper()
	old := dockerClient.get()
	dockerClient.set(f)
	t.Cleanup(func() { dockerClient.set(old) })
}

func manifest(image, tag, digest string) canned {
	return canned{
		url:    "https://registry-1.docker.io/v2/" + image + "/manifests/" + tag,
		header: map[string][]string{"Docker-Content-Digest": {digest}},
		body:   `{"schemaVersion": 2}`,
	}
}

func TestDockerBump(t *testing.T) {
	withRegistry(t, replayCanned(t,
		manifest("library/golang", "1.23.0-bookworm", digestOf("1")),
		manifest("library/nginx", "1.27", digestOf("2")),
		manifest("library/busybox", "1.37", digestOf("3")),
	))
	bumpGolden(t, docker{}, "docker/project", []bumpCase{
		{"literal-tag", "Dockerfile", "library/python", true, "3.13-slim", ""},
		{"digest-kept", "Dockerfile", "library/golang", true, "1.23.0-bookworm", ""},
		{"arg-default", "Dockerfile", "library/node", true, "22.1-alpine", ""},
		{"digest-only", "Dockerfile", "library/nginx", true, "1.27", ""},
		{"host-with-variable-name", "Dockerfile", "ghcr.io/acme/base", true, "3.3", ""},
		{"local-registry", "Dockerfile", "localhost:5000/team/tool", true, "v1.3.0", ""},
		{"crlf-platform-and-alias-kept", "services/api/Dockerfile.dev", "library/node", true, "22", ""},
		{"dockerfile-extension", "web.dockerfile", "library/nginx", true, "1.27-alpine", ""},
		{"compose", "compose.yaml", "library/postgres", true, "16", ""},
		{"compose-quoted-with-comment", "compose.yaml", "library/redis", true, "7.4-alpine", ""},
		{"compose-inline-default", "compose.yaml", "library/nginx", true, "1.27", ""},
		{"compose-host", "compose.yaml", "ghcr.io/acme/tool", true, "2.2.0", ""},
		{"compose-digest", "compose.yaml", "library/busybox", true, "1.37", ""},
		{"swap", "compose.yaml", "library/postgres", true, "v24.1.0", "cockroachdb/cockroach"},
		{"swap-official", "Dockerfile", "library/python", true, "3.13", "library/pypy"},
	})
}

func TestDockerBumpSharedArg(t *testing.T) {
	root := copyTestdata(t, "docker/project")
	var nodes []core.Dep
	for _, d := range depsIn(t, docker{}, root, "Dockerfile") {
		if d.Name == "library/node" {
			nodes = append(nodes, d)
		}
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes = %+v", nodes)
	}
	first, err := (docker{}).Bump(context.Background(), root, nodes[0], "22.1-alpine", "")
	if err != nil || !reflect.DeepEqual(first.Files, []string{"Dockerfile"}) {
		t.Fatalf("first = %+v, %v", first, err)
	}
	second, err := (docker{}).Bump(context.Background(), root, nodes[1], "22.1-alpine", "")
	if err != nil || len(second.Files) != 0 {
		t.Fatalf("second = %+v, %v", second, err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if n := strings.Count(string(b), "ARG NODE_VERSION=22.1-alpine\n"); n != 1 {
		t.Errorf("ARG changed %d times\n%s", n, b)
	}
}

// TestDockerBumpPinned covers the pins Bump cannot carry to the new tag: each
// refuses the bump and leaves the file as it was.
func TestDockerBumpPinned(t *testing.T) {
	tests := []struct {
		name string
		f    core.Fetcher
		file string
		want string
	}{
		{"no registry", nil, "FROM golang:1.22@" + digestOf("a") + "\n", "no registry to ask"},
		{"tag not found", replayCanned(t, canned{url: "https://registry-1.docker.io/v2/library/golang/manifests/1.23", status: 404}),
			"FROM golang:1.22@" + digestOf("a") + "\n", "HTTP 404"},
		{"not sha256", replayCanned(t, manifest("library/golang", "1.23", "md5:abc")),
			"FROM golang:1.22@" + digestOf("a") + "\n", `digest "md5:abc" is not sha256`},
		{"digest from an ARG", nil, "ARG DIGEST=" + digestOf("a") + "\nFROM golang:1.22@${DIGEST}\n", "not settable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withRegistry(t, tt.f)
			root := t.TempDir()
			write(t, root, map[string]string{"Dockerfile": tt.file})
			d := depIn(t, docker{}, root, "Dockerfile", "library/golang", true)
			_, err := (docker{}).Bump(context.Background(), root, d, "1.23", "")
			if !errors.Is(err, core.ErrNotSupported) || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q and not supported", err, tt.want)
			}
			if got := treeSnapshot(t, root)["Dockerfile"]; got != tt.file {
				t.Errorf("Dockerfile changed:\n%s", got)
			}
		})
	}
}

// TestDockerBumpPinnedArg moves a pin whose tag comes from an ARG: the ARG
// and the digest move together.
func TestDockerBumpPinnedArg(t *testing.T) {
	withRegistry(t, replayCanned(t, manifest("library/node", "22-alpine", digestOf("b"))))
	root := t.TempDir()
	write(t, root, map[string]string{"Dockerfile": "ARG NODE=20-alpine\nFROM node:${NODE}@" + digestOf("a") + " AS web\n"})
	d := depIn(t, docker{}, root, "Dockerfile", "library/node", true)
	if _, err := (docker{}).Bump(context.Background(), root, d, "22-alpine", ""); err != nil {
		t.Fatal(err)
	}
	want := "ARG NODE=22-alpine\nFROM node:${NODE}@" + digestOf("b") + " AS web\n"
	if got := treeSnapshot(t, root)["Dockerfile"]; got != want {
		t.Errorf("Dockerfile =\n%s\nwant\n%s", got, want)
	}
}

// TestDockerDigest asks a registry v2 for a manifest the way a pull does:
// anonymous token, the manifest types a pin can name, and the digest from
// the header or, without one, from the manifest itself.
func TestDockerDigest(t *testing.T) {
	body := `{"schemaVersion": 2, "manifests": []}`
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			fmt.Fprint(w, `{"token": "t0ken"}`)
		case "/v2/team/tool/manifests/v2.0.0", "/v2/team/tool/manifests/v2.1.0":
			if r.Header.Get("Authorization") != "Bearer t0ken" {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="registry.test"`, srv.URL))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			accept := r.Header.Get("Accept")
			for _, mt := range []string{"application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json",
				"application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json"} {
				if !strings.Contains(accept, mt) {
					t.Errorf("Accept %q leaves out %s", accept, mt)
				}
			}
			if strings.HasSuffix(r.URL.Path, "v2.0.0") {
				w.Header().Set("Docker-Content-Digest", digestOf("c"))
			}
			fmt.Fprint(w, body)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	f := fetch.New("", "test")
	ctx := context.Background()
	if got, err := dockerDigest(ctx, f, host+"/team/tool", "v2.0.0"); err != nil || got != digestOf("c") {
		t.Errorf("from the header = %q, %v", got, err)
	}
	want := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(body)))
	if got, err := dockerDigest(ctx, f, host+"/team/tool", "v2.1.0"); err != nil || got != want {
		t.Errorf("from the manifest = %q, %v; want %q", got, err, want)
	}
	if _, err := dockerDigest(ctx, f, host+"/team/tool", "v9"); err == nil {
		t.Error("found a digest for a tag the registry does not have")
	}
}

func TestDockerBumpErrors(t *testing.T) {
	root := copyTestdata(t, "docker/project")
	ctx := context.Background()
	unresolved := depIn(t, docker{}, root, "Dockerfile", "library/golang", true)
	var bad core.Dep
	for _, d := range depsIn(t, docker{}, root, "Dockerfile") {
		if d.Name == "library/golang" && d.Version == "" {
			bad = d
		}
	}
	if bad.Name == "" || !strings.Contains(bad.Req, "unresolved ARG GO_VERSION") {
		t.Fatalf("unresolved dep = %+v", bad)
	}
	if _, err := (docker{}).Bump(ctx, root, bad, "1.23", ""); err == nil {
		t.Error("bumped a tag that comes from a build argument")
	}
	unresolved.Line = 2
	if _, err := (docker{}).Bump(ctx, root, unresolved, "1.23", ""); err == nil {
		t.Error("bumped an image from the wrong line")
	}
	other := core.Dep{Name: "library/none", Line: 1, Manifest: core.Manifest{Path: "missing"}}
	if _, err := (docker{}).Bump(ctx, root, other, "1", ""); err == nil {
		t.Error("bumped in a file that does not exist")
	}
}

func TestDockerMeta(t *testing.T) {
	ctx := context.Background()
	t.Run("recorded hub pages follow the suffix family", func(t *testing.T) {
		m, err := (docker{}).Meta(ctx, fetch.Replay(recordedDir("docker")), "library/node:22-alpine")
		if err != nil {
			t.Fatal(err)
		}
		if m.Name != "library/node" || len(m.Releases) < 3 {
			t.Fatalf("meta = %+v", m)
		}
		for _, r := range m.Releases {
			if !strings.HasSuffix(r.Version, "-alpine") || strings.Count(r.Version, ".") != 0 || !r.Time.IsZero() {
				t.Errorf("release %+v is not a one-part -alpine tag without a time", r)
			}
		}
	})
	t.Run("plain family, precision and suffixes", func(t *testing.T) {
		f := replayCanned(t, canned{url: "https://hub.docker.com/v2/repositories/library/postgres/tags?page_size=100", body: `{"next": "", "results": [
			{"name": "latest", "tag_last_pushed": "2024-05-01T10:00:00.000000Z"},
			{"name": "17", "tag_last_pushed": "2024-05-01T10:00:00.000000Z"},
			{"name": "17.2", "tag_last_pushed": "2024-05-01T10:00:00.000000Z"},
			{"name": "17-alpine", "tag_last_pushed": "2024-05-01T10:00:00.000000Z"},
			{"name": "17-bookworm", "tag_last_pushed": "2024-05-01T10:00:00.000000Z"},
			{"name": "bookworm", "tag_last_pushed": "2024-05-01T10:00:00.000000Z"},
			{"name": "16", "tag_last_pushed": "2024-04-01T10:00:00.000000Z"},
			{"name": "11", "last_updated": "2022-04-01T10:00:00.000000Z"},
			{"name": "18-beta1", "tag_last_pushed": "2025-06-01T10:00:00.000000Z"}]}`})
		m, err := (docker{}).Meta(ctx, f, "library/postgres:11")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range m.Releases {
			got = append(got, fmt.Sprintf("%s %v", r.Version, r.Time.IsZero()))
		}
		// A push time is when a floating tag was last rebuilt, not a release date.
		want := []string{"11 true", "16 true", "17 true"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("releases = %q\nwant %q", got, want)
		}
	})
	t.Run("pre-release in the same family", func(t *testing.T) {
		f := replayCanned(t, canned{url: "https://hub.docker.com/v2/repositories/library/postgres/tags?page_size=100", body: `{"results": [{"name": "18-beta1"}, {"name": "17"}]}`})
		m, err := (docker{}).Meta(ctx, f, "library/postgres:16")
		if err != nil || len(m.Releases) != 1 {
			t.Fatalf("meta = %+v, %v", m, err)
		}
	})
	t.Run("tags from deps when no hint is given", func(t *testing.T) {
		root := copyTestdata(t, "docker/project")
		depsIn(t, docker{}, root, "compose.yaml")
		f := replayCanned(t, canned{url: "https://hub.docker.com/v2/repositories/library/redis/tags?page_size=100&name=-alpine", body: `{"results": [
			{"name": "7.4-alpine"}, {"name": "7.4"}, {"name": "7-alpine"}, {"name": "8.0-alpine"}, {"name": "8.0-alpine3.21"}]}`})
		m, err := (docker{}).Meta(ctx, f, "library/redis")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range m.Releases {
			got = append(got, r.Version)
		}
		if want := []string{"7.4-alpine", "8.0-alpine"}; !reflect.DeepEqual(got, want) {
			t.Errorf("releases = %q, want %q", got, want)
		}
	})
	t.Run("v prefix is its own family", func(t *testing.T) {
		f := replayCanned(t, canned{url: "https://hub.docker.com/v2/repositories/acme/tool/tags?page_size=100", body: `{"results": [{"name": "v2.0.0"}, {"name": "2.0.0"}, {"name": "v1.0.0"}]}`})
		m, err := (docker{}).Meta(ctx, f, "acme/tool:v1.2.3")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range m.Releases {
			got = append(got, r.Version)
		}
		if want := []string{"v1.0.0", "v2.0.0"}; !reflect.DeepEqual(got, want) {
			t.Errorf("releases = %q, want %q", got, want)
		}
	})
	t.Run("paging stops at the cap", func(t *testing.T) {
		var stubs []canned
		for page := 1; page <= 7; page++ {
			u := fmt.Sprintf("https://hub.docker.com/v2/repositories/library/cap/tags?page_size=100&page=%d", page)
			if page == 1 {
				u = "https://hub.docker.com/v2/repositories/library/cap/tags?page_size=100"
			}
			var rs []string
			for i := 0; i < 100; i++ {
				rs = append(rs, fmt.Sprintf(`{"name": "%d"}`, (page-1)*100+i+1))
			}
			next := fmt.Sprintf("https://hub.docker.com/v2/repositories/library/cap/tags?page_size=100&page=%d", page+1)
			stubs = append(stubs, canned{url: u, body: `{"next": "` + next + `", "results": [` + strings.Join(rs, ",") + `]}`})
		}
		m, err := (docker{}).Meta(ctx, replayCanned(t, stubs...), "library/cap:1")
		if err != nil {
			t.Fatal(err)
		}
		if len(m.Releases) != dockerTagCap {
			t.Errorf("%d releases, want %d", len(m.Releases), dockerTagCap)
		}
	})
	t.Run("not found", func(t *testing.T) {
		f := replayCanned(t, canned{url: "https://hub.docker.com/v2/repositories/library/nope/tags?page_size=100", status: 404, body: `{"message": "object not found"}`})
		if _, err := (docker{}).Meta(ctx, f, "library/nope:1"); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestDockerMetaRecord(t *testing.T) {
	wantRecord(t)
	if _, err := (docker{}).Meta(context.Background(), liveClient(t, "docker"), "library/node:22-alpine"); err != nil {
		t.Fatal(err)
	}
}

func TestDockerRegistry(t *testing.T) {
	var tokenQuery atomic.Value
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenQuery.Store(r.URL.Query())
			if r.Header.Get("Authorization") != "" {
				t.Error("the token request carried credentials")
			}
			fmt.Fprint(w, `{"token": "t0ken", "expires_in": 300}`)
		case "/v2/team/tool/tags/list":
			if r.Header.Get("Authorization") != "Bearer t0ken" {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="registry.test",scope="repository:team/tool:pull"`, srv.URL))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.URL.Query().Get("last") == "" {
				w.Header().Set("Link", `</v2/team/tool/tags/list?last=v1.3.0&n=3>; rel="next"`)
				fmt.Fprint(w, `{"name": "team/tool", "tags": ["latest", "v1.2.3", "v1.3.0"]}`)
				return
			}
			fmt.Fprint(w, `{"name": "team/tool", "tags": ["v2.0.0", "v2.1.0-rc.1", "sha-abc"]}`)
		case "/v2/open/img/tags/list":
			fmt.Fprint(w, `{"name": "open/img", "tags": ["1.0", "1.1"]}`)
		case "/v2/locked/img/tags/list":
			w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			w.WriteHeader(http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	f := fetch.New("", "test")
	ctx := context.Background()

	t.Run("bearer challenge and paging", func(t *testing.T) {
		m, err := (docker{}).Meta(ctx, f, host+"/team/tool:v1.2.3")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range m.Releases {
			if !r.Time.IsZero() {
				t.Errorf("registry v2 has no times, got %v for %s", r.Time, r.Version)
			}
			got = append(got, fmt.Sprintf("%s pre=%v", r.Version, r.Pre))
		}
		want := []string{"v1.2.3 pre=false", "v1.3.0 pre=false", "v2.0.0 pre=false"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("releases = %q\nwant %q", got, want)
		}
		q := tokenQuery.Load().(url.Values)
		if q["service"][0] != "registry.test" || q["scope"][0] != "repository:team/tool:pull" {
			t.Errorf("token query = %v", q)
		}
	})
	t.Run("no auth needed", func(t *testing.T) {
		m, err := (docker{}).Meta(ctx, f, host+"/open/img:1.0")
		if err != nil || len(m.Releases) != 2 {
			t.Errorf("meta = %+v, %v", m, err)
		}
	})
	t.Run("credentials required", func(t *testing.T) {
		if _, err := (docker{}).Meta(ctx, f, host+"/locked/img:1.0"); err == nil || !strings.Contains(err.Error(), "credentials") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("not found", func(t *testing.T) {
		if _, err := (docker{}).Meta(ctx, f, host+"/gone/img:1.0"); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestDockerNothingToRun(t *testing.T) {
	if got := (docker{}).Commands("", core.Manifest{}); !reflect.DeepEqual(got, core.Commands{}) {
		t.Errorf("Commands = %+v", got)
	}
	if got := (docker{}).Imports(core.Dep{Name: "library/node"}); got != nil {
		t.Errorf("Imports = %v", got)
	}
}
