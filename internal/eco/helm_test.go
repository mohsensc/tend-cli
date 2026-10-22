package eco

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

const chartYAML = `apiVersion: v2
name: shop
version: 0.3.0
dependencies:
  - name: postgresql
    version: "12.1.2"
    repository: "https://charts.example.test/stable"
  - name: redis
    version: ~17.0.0
    repository: https://charts.example.test/stable/
  - name: internal
    version: 1.0.0
    repository: oci://registry.example.test/charts
`

const chartLock = `dependencies:
- name: postgresql
  repository: https://charts.example.test/stable
  version: 12.1.2
- name: redis
  repository: https://charts.example.test/stable
  version: 17.0.4
digest: sha256:abc
generated: "2026-01-01T00:00:00Z"
`

const chartIndex = `apiVersion: v1
entries:
  postgresql:
  - version: 13.2.0
    created: "2026-09-01T00:00:00Z"
    sources: [https://github.com/example/charts]
  - version: 12.1.2
    created: "2025-01-01T00:00:00Z"
  redis:
  - version: 18.0.0
    created: "2026-08-01T00:00:00Z"
    deprecated: true
  - version: 17.0.4
    created: "2025-02-01T00:00:00Z"
`

func TestHelmDeps(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"deploy/shop/Chart.yaml": chartYAML, "deploy/shop/Chart.lock": chartLock})
	ms := manifestsIn(t, helm{}, root)
	if len(ms) != 1 || ms[0].Lock != "deploy/shop/Chart.lock" {
		t.Fatalf("manifests = %+v", ms)
	}
	ds, err := helm{}.Deps(root, ms[0])
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range ds {
		got = append(got, fmt.Sprintf("%d %s %s (%s)", d.Line, d.Name, d.Version, d.Req))
	}
	want := []string{"6 charts.example.test/stable/postgresql 12.1.2 (12.1.2)", "9 charts.example.test/stable/redis 17.0.4 (~17.0.0)"}
	if !slices.Equal(got, want) {
		t.Errorf("deps = %q, want %q", got, want)
	}
}

// countingFetcher counts the requests per URL it passes on.
type countingFetcher struct {
	core.Fetcher
	mu sync.Mutex
	n  map[string]int
}

func (c *countingFetcher) Get(ctx context.Context, u string, opts ...core.FetchOpt) (*core.Response, error) {
	c.mu.Lock()
	c.n[u]++
	c.mu.Unlock()
	return c.Fetcher.Get(ctx, u, opts...)
}

// freshIndexes starts the test with no index read.
func freshIndexes(t *testing.T) {
	old := helmIndexes
	helmIndexes = &indexCache{m: map[string]*indexEntry{}}
	t.Cleanup(func() { helmIndexes = old })
}

func TestHelmMeta(t *testing.T) {
	freshIndexes(t)
	root := t.TempDir()
	write(t, root, map[string]string{"Chart.yaml": chartYAML})
	depsIn(t, helm{}, root, "Chart.yaml")
	f := &countingFetcher{Fetcher: replayCanned(t, canned{url: "https://charts.example.test/stable/index.yaml", body: chartIndex}), n: map[string]int{}}
	ctx := context.Background()
	m, err := helm{}.Meta(ctx, f, "charts.example.test/stable/postgresql")
	if err != nil || len(m.Releases) != 2 || m.Releases[1].Version != "13.2.0" || m.Repo != "https://github.com/example/charts" || m.Deprecated != "" {
		t.Fatalf("postgresql = %+v, %v", m, err)
	}
	m, err = helm{}.Meta(ctx, f, "charts.example.test/stable/redis")
	if err != nil || m.Deprecated == "" || m.Releases[1].Deprecated == "" {
		t.Fatalf("redis = %+v, %v", m, err)
	}
	if _, err := (helm{}).Meta(ctx, f, "charts.example.test/stable/unknown"); err == nil {
		t.Error("a chart the index does not list has metadata")
	}
	if n := f.n["https://charts.example.test/stable/index.yaml"]; n != 1 {
		t.Errorf("the index was read %d times, want once", n)
	}
}

// TestHelmSameName has two repositories each with a chart called common.
func TestHelmSameName(t *testing.T) {
	freshIndexes(t)
	root := t.TempDir()
	write(t, root, map[string]string{
		"a/Chart.yaml": "apiVersion: v2\nname: a\ndependencies:\n  - name: common\n    version: 1.0.0\n    repository: https://one.example.test/charts\n",
		"a/Chart.lock": "dependencies:\n- name: common\n  repository: https://one.example.test/charts\n  version: 1.0.0\n",
		"b/Chart.yaml": "apiVersion: v2\nname: b\ndependencies:\n  - name: common\n    version: ~2.0.0\n    repository: https://two.example.test/charts\n",
		"b/Chart.lock": "dependencies:\n- name: common\n  repository: https://two.example.test/charts\n  version: 2.0.3\n",
	})
	one := depsIn(t, helm{}, root, "a/Chart.yaml")[0]
	two := depsIn(t, helm{}, root, "b/Chart.yaml")[0]
	if one.Name != "one.example.test/charts/common" || one.Version != "1.0.0" || two.Name != "two.example.test/charts/common" || two.Version != "2.0.3" {
		t.Fatalf("deps = %+v, %+v", one, two)
	}
	f := replayCanned(t,
		canned{url: "https://one.example.test/charts/index.yaml", body: "entries:\n  common:\n  - version: 1.1.0\n"},
		canned{url: "https://two.example.test/charts/index.yaml", body: "entries:\n  common:\n  - version: 2.5.0\n"},
	)
	for d, want := range map[string]string{one.Name: "1.1.0", two.Name: "2.5.0"} {
		m, err := helm{}.Meta(context.Background(), f, d)
		if err != nil || len(m.Releases) != 1 || m.Releases[0].Version != want {
			t.Errorf("%s = %+v, %v; want %s", d, m, err, want)
		}
	}
}

func TestHelmBump(t *testing.T) {
	tests := []struct {
		chart, to string
		want      string // the version line after the bump
		changed   bool
	}{
		{"redis", "17.0.9", "    version: ~17.0.0", false},
		{"redis", "18.0.0", "    version: ~18.0.0", true},
		{"postgresql", "13.2.0", `    version: "13.2.0"`, true},
	}
	for _, tt := range tests {
		t.Run(tt.chart+"@"+tt.to, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, map[string]string{"Chart.yaml": chartYAML, "Chart.lock": chartLock})
			logged := fakeTool(t, "helm")
			var d core.Dep
			for _, x := range depsIn(t, helm{}, root, "Chart.yaml") {
				if path.Base(x.Name) == tt.chart {
					d = x
				}
			}
			b, err := helm{}.Bump(context.Background(), root, d, tt.to, "")
			if err != nil || !b.Locked {
				t.Fatalf("bump = %+v, %v", b, err)
			}
			want := []string{"Chart.lock"}
			if tt.changed {
				want = []string{"Chart.yaml", "Chart.lock"}
			}
			if !slices.Equal(b.Files, want) {
				t.Errorf("files = %q, want %q", b.Files, want)
			}
			lines := strings.Split(treeSnapshot(t, root)["Chart.yaml"], "\n")
			if got := lines[d.Line-1]; got != tt.want {
				t.Errorf("line %d = %q, want %q", d.Line, got, tt.want)
			}
			if got := logged(root); !slices.Equal(got, []string{". dependency update"}) {
				t.Errorf("commands = %q", got)
			}
		})
	}
}

func TestHelmBumpNoTool(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"Chart.yaml": chartYAML})
	ds := depsIn(t, helm{}, root, "Chart.yaml")
	hideTools(t)
	b, err := helm{}.Bump(context.Background(), root, ds[0], "13.2.0", "")
	if err != nil || b.Note != "no lockfile" {
		t.Fatalf("bump = %+v, %v", b, err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "Chart.yaml"))
	if want := strings.Replace(chartYAML, `version: "12.1.2"`, `version: "13.2.0"`, 1); string(got) != want {
		t.Errorf("Chart.yaml =\n%s", got)
	}
}
