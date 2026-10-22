package eco

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/ver"
)

// custom follows the versions tend.yml's deps entries point at: a regexp in
// some file, and a source to ask about releases. A source that is another
// ecosystem's package gets that registry's yanks and deprecations as is.
type custom struct{ deps []config.Dep }

// Custom is the ecosystem for tend.yml's deps entries.
func Custom(deps []config.Dep) core.Ecosystem { return custom{deps} }

func (custom) Name() string { return core.Custom }

func (c custom) Detect(root string) ([]core.Manifest, error) {
	seen := map[string]bool{}
	var res []core.Manifest
	for _, d := range c.deps {
		files, err := walk(root, d.File)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if !seen[f] {
				seen[f] = true
				res = append(res, core.Manifest{Ecosystem: core.Custom, Path: f, Tool: "custom"})
			}
		}
	}
	slices.SortFunc(res, func(a, b core.Manifest) int { return strings.Compare(a.Path, b.Path) })
	return res, nil
}

func (c custom) Deps(root string, m core.Manifest) ([]core.Dep, error) {
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(m.Path)))
	if err != nil {
		return nil, err
	}
	var res []core.Dep
	for _, d := range c.deps {
		if !matches(m.Path, []string{d.File}) {
			continue
		}
		re, err := regexp.Compile(d.Match)
		if err != nil {
			return nil, fmt.Errorf("custom: %s: %w", d.Name, err)
		}
		v := re.SubexpIndex("v")
		for _, loc := range re.FindAllSubmatchIndex(src, -1) {
			if v < 0 || loc[2*v] < 0 {
				continue
			}
			written := string(src[loc[2*v]:loc[2*v+1]])
			res = append(res, core.Dep{
				Ecosystem: core.Custom, Name: d.Name, Version: strings.TrimPrefix(written, "v"), Req: written,
				Manifest: m, Line: 1 + strings.Count(string(src[:loc[2*v]]), "\n"), Direct: true,
			})
		}
	}
	return res, nil
}

func (c custom) source(name string) (string, string, error) {
	for _, d := range c.deps {
		if d.Name == name {
			scheme, rest, _ := strings.Cut(d.Source, ":")
			if scheme == "pypi" {
				scheme = core.Pip
			}
			return scheme, rest, nil
		}
	}
	return "", "", fmt.Errorf("custom: %s: not in tend.yml's deps", name)
}

// Upstream is how the source registry of a deps entry names it, so the
// advisory databases can be asked about it: npm:esbuild is the npm package
// esbuild. ok is false for any other dep, and for a GitHub repo or a URL,
// which no advisory database indexes by version.
func Upstream(deps []config.Dep) func(core.Dep) (core.Dep, bool) {
	c := custom{deps}
	return func(d core.Dep) (core.Dep, bool) {
		if d.Ecosystem != core.Custom {
			return d, false
		}
		return c.upstream(d)
	}
}

func (c custom) upstream(d core.Dep) (core.Dep, bool) {
	scheme, rest, err := c.source(d.Name)
	if err != nil || scheme == "github" || scheme == "url" {
		return d, false
	}
	d.Ecosystem, d.Name = scheme, rest
	return d, true
}

func (c custom) Meta(ctx context.Context, f core.Fetcher, name string) (*core.Meta, error) {
	scheme, rest, err := c.source(name)
	if err != nil {
		return nil, err
	}
	var m *core.Meta
	switch scheme {
	case "github":
		m, _, err = githubMeta(ctx, f, rest)
	case "url":
		m, err = urlMeta(ctx, f, strings.ReplaceAll(rest, "{name}", name))
	default:
		e := ByName(scheme)
		if e == nil || e.Name() == core.Custom {
			return nil, fmt.Errorf("custom: %s: no ecosystem %q", name, scheme)
		}
		m, err = e.Meta(ctx, f, rest)
	}
	if err != nil {
		return nil, fmt.Errorf("custom: %s: %w", name, err)
	}
	// Deps carry their versions without a v, whatever the source tags;
	// Bump writes one back where the file had it.
	m.Name, m.Latest = name, strings.TrimPrefix(m.Latest, "v")
	for i := range m.Releases {
		m.Releases[i].Version = strings.TrimPrefix(m.Releases[i].Version, "v")
	}
	return m, nil
}

var looseVersion = regexp.MustCompile(`\bv?\d+\.\d+(?:\.\d+)?\b`)

// urlMeta reads every version-shaped string on a page, for a source with no
// API: a download index, a plain text list.
func urlMeta(ctx context.Context, f core.Fetcher, u string) (*core.Meta, error) {
	res, err := f.Get(ctx, u)
	if err != nil {
		return nil, err
	}
	if res.Status != 200 {
		return nil, fmt.Errorf("%s: HTTP %d", u, res.Status)
	}
	m := &core.Meta{}
	seen := map[string]bool{}
	for _, v := range looseVersion.FindAllString(string(res.Body), -1) {
		if v = strings.TrimPrefix(v, "v"); !seen[v] {
			seen[v] = true
			m.Releases = append(m.Releases, core.Release{Version: v})
		}
	}
	sortReleases(m.Releases)
	return m, nil
}

// Bump writes the new version where the dep's regexp matched it, with or
// without the v the file used. The same text elsewhere on the line, such as
// another tool at the same version, stays.
func (c custom) Bump(ctx context.Context, root string, d core.Dep, to, toName string) (*core.Bump, error) {
	if toName != "" {
		return nil, fmt.Errorf("custom: %s: %w", d.Name, core.ErrNotSupported)
	}
	if _, ok := ver.Parse(to); !ok {
		return nil, fmt.Errorf("custom: %s: invalid version %q", d.Name, to)
	}
	next := strings.TrimPrefix(to, "v")
	if strings.HasPrefix(d.Req, "v") {
		next = "v" + next
	}
	p := filepath.Join(root, filepath.FromSlash(d.Manifest.Path))
	src, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	sites, done, err := c.sites(src, d, next)
	if err != nil {
		return nil, err
	}
	res := &core.Bump{Note: "no lockfile"}
	if len(sites) == 0 {
		if !done {
			return nil, fmt.Errorf("custom: %s:%d: %s %q not found", d.Manifest.Path, d.Line, d.Name, d.Req)
		}
		return res, nil
	}
	for _, at := range slices.Backward(sites) {
		src = splice(src, at[0], at[1], next)
	}
	if err := writeKeepMode(p, src); err != nil {
		return nil, err
	}
	res.Files = []string{d.Manifest.Path}
	return res, nil
}

// sites are where d's regexps capture d.Req on d.Line. done is true when they
// capture next there instead: another pin of d on the line moved it already.
func (c custom) sites(src []byte, d core.Dep, next string) (res [][2]int, done bool, err error) {
	for _, cd := range c.deps {
		if cd.Name != d.Name || !matches(d.Manifest.Path, []string{cd.File}) {
			continue
		}
		re, err := regexp.Compile(cd.Match)
		if err != nil {
			return nil, false, fmt.Errorf("custom: %s: %w", d.Name, err)
		}
		v := re.SubexpIndex("v")
		if v < 0 {
			continue
		}
		for _, loc := range re.FindAllSubmatchIndex(src, -1) {
			start, end := loc[2*v], loc[2*v+1]
			if start < 0 || lineOf(src, start) != d.Line {
				continue
			}
			switch string(src[start:end]) {
			case d.Req:
				res = append(res, [2]int{start, end})
			case next:
				done = true
			}
		}
		if len(res) > 0 {
			return res, done, nil
		}
	}
	return nil, done, nil
}

func (custom) Commands(root string, m core.Manifest) core.Commands { return core.Commands{} }

func (custom) Traits() core.Traits { return core.Traits{} }

func (custom) Imports(d core.Dep) []string { return nil }

// Check validates what tend.yml says about ecosystems, which the config
// package cannot know: the names ecosystems and ignore entries give, and the
// registries deps entries point at.
func Check(cfg *config.Config) error {
	var errs []error
	for i, name := range cfg.Ecosystems {
		if name != core.Custom && ByName(name) == nil {
			errs = append(errs, fmt.Errorf("ecosystems[%d]: unknown ecosystem %q", i, name))
		}
	}
	for i, ig := range cfg.Ignore {
		if ig.Ecosystem != "" && ig.Ecosystem != "api" && ig.Ecosystem != core.Custom && ByName(ig.Ecosystem) == nil {
			errs = append(errs, fmt.Errorf("ignore[%d]: unknown ecosystem %q", i, ig.Ecosystem))
		}
	}
	for i, d := range cfg.Deps {
		scheme, _, _ := strings.Cut(d.Source, ":")
		if scheme != "github" && scheme != "pypi" && scheme != "url" && ByName(scheme) == nil {
			errs = append(errs, fmt.Errorf("deps[%d]: source wants github:, npm:, pypi:, url: or another ecosystem's name, got %q", i, d.Source))
		}
	}
	return errors.Join(errs...)
}

// For is every ecosystem tend.yml leaves on, with its deps entries as one more.
func For(cfg *config.Config) []core.Ecosystem {
	var res []core.Ecosystem
	for _, e := range All() {
		if !cfg.Off(e.Name()) {
			res = append(res, e)
		}
	}
	if len(cfg.Deps) > 0 && !cfg.Off(core.Custom) {
		res = append(res, Custom(cfg.Deps))
	}
	return res
}
