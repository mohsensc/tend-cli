package eco

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
)

func pyReadLock(root string, m core.Manifest) ([]lockPkg, error) {
	if m.Lock == "" {
		return nil, nil
	}
	p := filepath.Join(root, filepath.FromSlash(m.Lock))
	src, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var res []lockPkg
	switch path.Base(m.Lock) {
	case "uv.lock":
		res, err = uvLock(src)
	case "poetry.lock":
		res, err = poetryLocked(src)
	case "Pipfile.lock":
		res, err = pipfileLock(src)
	case "pdm.lock":
		return nil, nil
	default:
		for _, e := range pyReqEntries(src) {
			if v := pyPinned(e.spec); v != "" {
				res = append(res, lockPkg{name: PyNorm(e.name), version: v})
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", m.Lock, err)
	}
	slices.SortStableFunc(res, func(a, b lockPkg) int { return strings.Compare(a.name, b.name) })
	return res, nil
}

type uvDep struct{ Name string }

type uvPackage struct {
	Name         string
	Version      string
	Source       map[string]any
	Dependencies []uvDep
	Optional     map[string][]uvDep `toml:"optional-dependencies"`
	Dev          map[string][]uvDep `toml:"dev-dependencies"`
}

func (p uvPackage) requires() []string {
	all := p.Dependencies
	for _, ds := range p.Optional {
		all = append(all, ds...)
	}
	for _, ds := range p.Dev {
		all = append(all, ds...)
	}
	var res []string
	for _, d := range all {
		res = append(res, PyNorm(d.Name))
	}
	return res
}

func uvPackages(src []byte) ([]uvPackage, error) {
	var lf struct {
		Package []uvPackage `toml:"package"`
	}
	if err := tomlDecode(src, &lf); err != nil {
		return nil, err
	}
	return lf.Package, nil
}

// uvLock keeps registry packages only; the project itself and path or git
// sources have no registry releases.
func uvLock(src []byte) ([]lockPkg, error) {
	ps, err := uvPackages(src)
	if err != nil {
		return nil, err
	}
	var res []lockPkg
	for _, p := range ps {
		if _, ok := p.Source["registry"]; !ok || p.Version == "" {
			continue
		}
		res = append(res, lockPkg{name: PyNorm(p.Name), version: p.Version, requires: p.requires()})
	}
	return res, nil
}

// uvMemberReqs lists what the workspace members a uv.lock records, other
// than the project at dir relative to the lockfile, require directly.
func uvMemberReqs(ps []uvPackage, dir string) map[string]bool {
	res := map[string]bool{}
	for _, p := range ps {
		at, ok := p.Source["editable"].(string)
		if !ok {
			at, ok = p.Source["virtual"].(string)
		}
		if !ok || path.Clean(at) == path.Clean(dir) {
			continue
		}
		for _, r := range p.requires() {
			res[r] = true
		}
	}
	return res
}

// poetryLocked reads poetry.lock: category = "dev" before Poetry 1.5, a
// groups list after it. Git, path and URL sources have no index releases;
// packages from a configured index, PyPI mirrors among them, stay.
func poetryLocked(src []byte) ([]lockPkg, error) {
	var lf struct {
		Package []struct {
			Name         string
			Version      string
			Category     string
			Groups       []string
			Dependencies map[string]any
			Source       struct{ Type string }
		} `toml:"package"`
	}
	if err := tomlDecode(src, &lf); err != nil {
		return nil, err
	}
	var res []lockPkg
	for _, p := range lf.Package {
		switch p.Source.Type {
		case "git", "directory", "file", "url":
			continue
		}
		if p.Version == "" {
			continue
		}
		dev := p.Category == "dev" || len(p.Groups) > 0 && !slices.Contains(p.Groups, "main")
		var reqs []string
		for _, d := range keys(p.Dependencies) {
			reqs = append(reqs, PyNorm(d))
		}
		res = append(res, lockPkg{name: PyNorm(p.Name), version: p.Version, dev: dev, requires: reqs})
	}
	return res, nil
}

func pipfileLock(src []byte) ([]lockPkg, error) {
	var lf struct {
		Default map[string]struct{ Version string } `json:"default"`
		Develop map[string]struct{ Version string } `json:"develop"`
	}
	if err := json.Unmarshal(stripBOM(src), &lf); err != nil {
		return nil, err
	}
	var res []lockPkg
	for name, p := range lf.Default {
		if p.Version != "" {
			res = append(res, lockPkg{name: PyNorm(name), version: strings.TrimPrefix(p.Version, "==")})
		}
	}
	for name, p := range lf.Develop {
		if _, ok := lf.Default[name]; !ok && p.Version != "" {
			res = append(res, lockPkg{name: PyNorm(name), version: strings.TrimPrefix(p.Version, "=="), dev: true})
		}
	}
	return res, nil
}

// pyCompiledBy tells which tool wrote a pinned requirements file from its
// header: "pip-compile", "uv", or "" when it was written by hand.
func pyCompiledBy(root, rel string) string {
	f, err := os.Open(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 0; n < 10 && sc.Scan(); n++ {
		line := sc.Text()
		switch {
		case !strings.HasPrefix(line, "#"):
			return ""
		case strings.Contains(line, "uv pip compile"):
			return "uv"
		case strings.Contains(line, "pip-compile"):
			return "pip-compile"
		}
	}
	return ""
}
