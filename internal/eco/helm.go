package eco

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/ver"
)

// helm reads the dependencies of a chart's Chart.yaml and the versions its
// Chart.lock resolved. A dep is named for its repository and chart, as in
// charts.bitnami.com/bitnami/postgresql: two repositories can each have a
// chart of one name.
type helm struct{}

func (helm) Name() string { return core.Helm }

// helmRepos maps a dep's name to its repository URL, as Deps saw it.
var helmRepos sync.Map

// helmName is the name of chart in repo: the repository without its scheme,
// then the chart.
func helmName(repo, chart string) string {
	repo = strings.TrimPrefix(strings.TrimPrefix(repo, "https://"), "http://")
	return repo + "/" + chart
}

func (helm) Detect(root string) ([]core.Manifest, error) {
	files, err := walk(root, "Chart.yaml")
	if err != nil {
		return nil, err
	}
	var res []core.Manifest
	for _, f := range files {
		m := core.Manifest{Ecosystem: core.Helm, Path: f, Tool: "helm"}
		if lock := path.Join(path.Dir(f), "Chart.lock"); onDisk(root, lock) {
			m.Lock = lock
		}
		res = append(res, m)
	}
	return res, nil
}

type helmDep struct {
	Name       string `yaml:"name"`
	Version    string `yaml:"version"`
	Repository string `yaml:"repository"`
}

func (helm) Deps(root string, m core.Manifest) ([]core.Dep, error) {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(m.Path)))
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", m.Path, err)
	}
	locked := map[string]string{}
	if m.Lock != "" {
		var lock struct{ Dependencies []helmDep }
		if readYAML(filepath.Join(root, filepath.FromSlash(m.Lock)), &lock) == nil {
			for _, d := range lock.Dependencies {
				locked[helmName(strings.TrimSuffix(d.Repository, "/"), d.Name)] = d.Version
			}
		}
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	deps := yamlGet(doc.Content[0], "dependencies")
	if deps == nil {
		return nil, nil
	}
	var res []core.Dep
	for _, n := range deps.Content {
		var d helmDep
		v := yamlGet(n, "version")
		if n.Decode(&d) != nil || v == nil || d.Name == "" || !strings.HasPrefix(d.Repository, "http") {
			continue
		}
		repo := strings.TrimSuffix(d.Repository, "/")
		name := helmName(repo, d.Name)
		helmRepos.Store(name, repo)
		version := locked[name]
		if version == "" {
			version = strings.TrimSpace(strings.TrimLeft(d.Version, "~^>=<! "))
		}
		res = append(res, core.Dep{Ecosystem: core.Helm, Name: name, Version: version, Req: d.Version, Manifest: m, Line: v.Line, Direct: true})
	}
	return res, nil
}

// Meta reads the chart's entries from its repository's index.yaml, where a
// chart can be marked deprecated.
func (helm) Meta(ctx context.Context, f core.Fetcher, name string) (*core.Meta, error) {
	chart := path.Base(name)
	repo := "https://" + path.Dir(name)
	if r, ok := helmRepos.Load(name); ok {
		repo = r.(string)
	}
	u := repo + "/index.yaml"
	index, err := helmIndexes.get(ctx, f, u)
	if err != nil {
		return nil, fmt.Errorf("helm: %s: %w", name, err)
	}
	entries, ok := index.Entries[chart]
	if !ok {
		return nil, fmt.Errorf("helm: %s: not in %s", name, u)
	}
	res := &core.Meta{Name: name}
	for _, e := range entries {
		r := core.Release{Version: e.Version, Time: e.Created, Pre: semverPre(e.Version)}
		if e.Deprecated {
			r.Deprecated = "deprecated in the chart repository"
		}
		res.Releases = append(res.Releases, r)
		if res.Repo == "" && len(e.Sources) > 0 {
			res.Repo = e.Sources[0]
		}
	}
	sortReleases(res.Releases)
	if n := len(res.Releases); n > 0 && res.Releases[n-1].Deprecated != "" {
		res.Deprecated = "the chart is deprecated in its repository"
	}
	return res, nil
}

type helmIndex struct {
	Entries map[string][]struct {
		Version    string    `yaml:"version"`
		Created    time.Time `yaml:"created"`
		Deprecated bool      `yaml:"deprecated"`
		Sources    []string  `yaml:"sources"`
	} `yaml:"entries"`
}

// helmIndexTTL is how long a parsed index serves. A repository's index lists
// every version of every chart in it, often tens of megabytes, and each of
// its charts asks for it; this is long enough for one run to read it once.
const helmIndexTTL = 15 * time.Minute

var helmIndexes = &indexCache{m: map[string]*indexEntry{}}

type indexCache struct {
	mu sync.Mutex
	m  map[string]*indexEntry
}

type indexEntry struct {
	mu    sync.Mutex
	at    time.Time
	index *helmIndex
}

// get returns the parsed index at u. Charts of one repository asking at once
// wait for the one read.
func (c *indexCache) get(ctx context.Context, f core.Fetcher, u string) (*helmIndex, error) {
	c.mu.Lock()
	e := c.m[u]
	if e == nil {
		e = &indexEntry{}
		c.m[u] = e
	}
	c.mu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.index != nil && time.Since(e.at) < helmIndexTTL {
		return e.index, nil
	}
	got, err := f.Get(ctx, u)
	if err != nil {
		return nil, err
	}
	if got.Status != 200 {
		return nil, fmt.Errorf("%s: HTTP %d", u, got.Status)
	}
	var index helmIndex
	if err := yaml.Unmarshal(got.Body, &index); err != nil {
		return nil, fmt.Errorf("%s: %w", u, err)
	}
	e.index, e.at = &index, time.Now()
	return e.index, nil
}

// Bump leaves a range that already admits to as written; the lock alone
// moves. Otherwise the range moves just enough to admit it.
func (helm) Bump(ctx context.Context, root string, d core.Dep, to, toName string) (*core.Bump, error) {
	if toName != "" {
		return nil, fmt.Errorf("helm: %s: %w", d.Name, core.ErrNotSupported)
	}
	res := &core.Bump{}
	if next := helmReq(d.Req, to); next != d.Req {
		if err := rewriteLine(filepath.Join(root, filepath.FromSlash(d.Manifest.Path)), d.Line, d.Req, next); err != nil {
			return nil, fmt.Errorf("helm: %w", err)
		}
		res.Files = append(res.Files, d.Manifest.Path)
	}
	switch {
	case d.Manifest.Lock == "":
		res.Note = "no lockfile"
	case !have("helm"):
		res.Note = core.ToolMissing{Tool: "helm"}.Error()
	default:
		if err := runIn(ctx, root, path.Dir(d.Manifest.Path), nil, "helm dependency update"); err != nil {
			return nil, fmt.Errorf("helm: %s: %w", d.Name, err)
		}
		res.Locked = true
		res.Files = append(res.Files, d.Manifest.Lock)
	}
	return res, nil
}

// helmReq is the version range req written for to. One that already admits
// to stays, and one tend cannot read becomes to itself.
func helmReq(req, to string) string {
	if v, ok := ver.Parse(to); ok && admits(req, v, false) {
		return req
	}
	next, err := fixReq(req, to, false)
	if err != nil {
		return to
	}
	return next
}

// Commands lint the chart where helm is installed; see terraform's for why
// not elsewhere.
func (helm) Commands(root string, m core.Manifest) core.Commands {
	if !have("helm") {
		return core.Commands{}
	}
	dir := path.Dir(m.Path)
	return core.Commands{
		Install: []string{inDir(dir, "helm dependency build")},
		Test:    []string{inDir(dir, "helm lint")},
	}
}

func (helm) Traits() core.Traits { return core.Traits{} }

func (helm) Imports(d core.Dep) []string { return nil }
