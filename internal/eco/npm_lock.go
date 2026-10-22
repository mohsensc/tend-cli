package eco

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"

	"github.com/mohsensc/tend-cli/internal/core"
)

// npmLock is what a lockfile says: the version each requirement resolved to,
// the requirements it links to workspace packages, every registry package it
// installs, and which of those (name@version) other workspace members require
// directly, so the root does not report them a second time.
type npmLock struct {
	resolve func(key, req string) string
	links   map[string]bool
	all     []lockPkg
	members map[string]bool
}

func npmReadLock(root string, m core.Manifest) (npmLock, error) {
	none := npmLock{resolve: func(string, string) string { return "" }}
	if m.Lock == "" {
		return none, nil
	}
	src, err := os.ReadFile(filepath.Join(root, m.Lock))
	if err != nil {
		return none, err
	}
	member := memberDir(path.Dir(m.Lock), path.Dir(m.Path))
	var lk npmLock
	switch path.Base(m.Lock) {
	case "package-lock.json", "npm-shrinkwrap.json":
		lk, err = packageLock(src, member)
	case "pnpm-lock.yaml":
		lk, err = pnpmLock(src, member)
	case "yarn.lock":
		lk = yarnLock(src)
	default:
		return none, nil
	}
	if err != nil {
		return none, fmt.Errorf("%s: %w", m.Lock, err)
	}
	// A workspace member shares the root's lockfile; its transitive deps are
	// reported once, by the root.
	if member != "" {
		lk.all = nil
	}
	lk.all = npmSortLocked(lk.all)
	return lk, nil
}

// memberDir is dir relative to the lockfile's dir, "" when they are the same.
func memberDir(lockDir, dir string) string {
	switch {
	case dir == lockDir:
		return ""
	case lockDir == ".":
		return dir
	}
	return strings.TrimPrefix(dir, lockDir+"/")
}

func npmSortLocked(all []lockPkg) []lockPkg {
	slices.SortFunc(all, func(a, b lockPkg) int {
		return cmp.Or(strings.Compare(a.name, b.name), semver.Compare("v"+a.version, "v"+b.version), strings.Compare(a.version, b.version))
	})
	var res []lockPkg
	for _, p := range all {
		if n := len(res); n > 0 && res[n-1].name == p.name && res[n-1].version == p.version {
			res[n-1].dev = res[n-1].dev && p.dev
			continue
		}
		res = append(res, p)
	}
	return res
}

type packageLockV1 struct {
	Version      string                   `json:"version"`
	Dev          bool                     `json:"dev"`
	Requires     map[string]string        `json:"requires"`
	Dependencies map[string]packageLockV1 `json:"dependencies"`
}

// packageLock reads package-lock.json: the packages map of v2 and v3, keyed by
// install path, or the nested dependencies tree of v1.
func packageLock(src []byte, member string) (npmLock, error) {
	var lf struct {
		Packages map[string]struct {
			Name                 string            `json:"name"`
			Version              string            `json:"version"`
			Dev                  bool              `json:"dev"`
			Link                 bool              `json:"link"`
			Dependencies         map[string]string `json:"dependencies"`
			DevDependencies      map[string]string `json:"devDependencies"`
			OptionalDependencies map[string]string `json:"optionalDependencies"`
			PeerDependencies     map[string]string `json:"peerDependencies"`
		} `json:"packages"`
		Dependencies map[string]packageLockV1 `json:"dependencies"`
	}
	if err := json.Unmarshal(src, &lf); err != nil {
		return npmLock{}, err
	}
	lk := npmLock{links: map[string]bool{}, members: map[string]bool{}}
	if lf.Packages != nil {
		byPath := map[string]string{}
		for k, p := range lf.Packages {
			i := strings.LastIndex(k, "node_modules/")
			if i < 0 {
				continue
			}
			key := k[i+len("node_modules/"):]
			if p.Link {
				lk.links[key] = true
			}
			if p.Link || !npmValid(p.Version) {
				continue
			}
			byPath[k] = p.Version
			// An aliased install records the package it really is under name.
			lk.all = append(lk.all, lockPkg{cmp.Or(p.Name, key), p.Version, p.Dev, keys(p.Dependencies, p.OptionalDependencies, p.PeerDependencies)})
		}
		at := func(dir, key string) string {
			return cmp.Or(byPath[dir+"node_modules/"+key], byPath["node_modules/"+key])
		}
		for k, p := range lf.Packages {
			if k == "" || k == member || strings.Contains(k, "node_modules/") {
				continue
			}
			for _, deps := range []map[string]string{p.Dependencies, p.DevDependencies, p.OptionalDependencies} {
				for key := range deps {
					if v := at(k+"/", key); v != "" {
						lk.members[key+"@"+v] = true
					}
				}
			}
		}
		prefix := ""
		if member != "" {
			prefix = member + "/"
		}
		lk.resolve = func(key, _ string) string { return at(prefix, key) }
		return lk, nil
	}
	var visit func(deps map[string]packageLockV1)
	visit = func(deps map[string]packageLockV1) {
		for name, d := range deps {
			if npmValid(d.Version) {
				lk.all = append(lk.all, lockPkg{name, d.Version, d.Dev, keys(d.Requires)})
			}
			visit(d.Dependencies)
		}
	}
	visit(lf.Dependencies)
	lk.resolve = func(key, _ string) string {
		if v := lf.Dependencies[key].Version; npmValid(v) {
			return v
		}
		return ""
	}
	return lk, nil
}

// pnpmRef is an importer entry: a bare version in lockfile v5, a
// {specifier, version} mapping from v6 on.
type pnpmRef string

func (r *pnpmRef) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*r = pnpmRef(n.Value)
		return nil
	}
	var v struct{ Version string }
	if err := n.Decode(&v); err != nil {
		return err
	}
	*r = pnpmRef(v.Version)
	return nil
}

// version drops the peer suffix pnpm appends: 1.2.3(react@18.2.0) in v6 and
// later, 1.2.3_react@18.2.0 in v5. Links and other protocols are not versions.
func (r pnpmRef) version() string {
	s := string(r)
	if i := strings.IndexAny(s, "(_"); i >= 0 {
		s = s[:i]
	}
	if !npmValid(s) {
		return ""
	}
	return s
}

type pnpmImporter struct {
	Dependencies         map[string]pnpmRef `yaml:"dependencies"`
	DevDependencies      map[string]pnpmRef `yaml:"devDependencies"`
	OptionalDependencies map[string]pnpmRef `yaml:"optionalDependencies"`
}

func (im pnpmImporter) get(key string) string {
	for _, deps := range []map[string]pnpmRef{im.Dependencies, im.DevDependencies, im.OptionalDependencies} {
		if r, ok := deps[key]; ok {
			return r.version()
		}
	}
	return ""
}

type pnpmPkg struct {
	Dev                  bool              `yaml:"dev"`
	Dependencies         map[string]string `yaml:"dependencies"`
	OptionalDependencies map[string]string `yaml:"optionalDependencies"`
}

// pnpmLock reads pnpm-lock.yaml. Versions 5 and 6 keep a single project's deps
// at the top level and key packages /name/1.2.3 (v5) or /name@1.2.3 (v6);
// version 9 always uses importers and keys packages name@1.2.3.
func pnpmLock(src []byte, member string) (npmLock, error) {
	var lf struct {
		LockfileVersion any                     `yaml:"lockfileVersion"`
		Importers       map[string]pnpmImporter `yaml:"importers"`
		pnpmImporter    `yaml:",inline"`
		Packages        map[string]pnpmPkg `yaml:"packages"`
		Snapshots       map[string]pnpmPkg `yaml:"snapshots"`
	}
	if err := yaml.Unmarshal(src, &lf); err != nil {
		return npmLock{}, err
	}
	major, _ := strconv.Atoi(strings.SplitN(fmt.Sprint(lf.LockfileVersion), ".", 2)[0])
	lk := npmLock{links: map[string]bool{}, members: map[string]bool{}}
	// Lockfile 9 keeps what a package requires apart, in snapshots, once
	// for each set of peers it was installed with.
	reqs := map[string][]string{}
	for _, m := range []map[string]pnpmPkg{lf.Packages, lf.Snapshots} {
		for k, p := range m {
			if name, v, ok := pnpmKey(k, major); ok {
				reqs[name+"@"+v] = union(reqs[name+"@"+v], keys(p.Dependencies, p.OptionalDependencies))
			}
		}
	}
	var devOnly map[string]bool
	if major >= 9 {
		devOnly = pnpmDevOnly(lf.Importers, lf.Snapshots)
	}
	for k, p := range lf.Packages {
		if name, v, ok := pnpmKey(k, major); ok {
			dev := p.Dev
			if devOnly != nil {
				dev = devOnly[name+"@"+v]
			}
			lk.all = append(lk.all, lockPkg{name, v, dev, reqs[name+"@"+v]})
		}
	}
	for k, other := range lf.Importers {
		if k == cmp.Or(member, ".") {
			continue
		}
		for _, deps := range []map[string]pnpmRef{other.Dependencies, other.DevDependencies, other.OptionalDependencies} {
			for key, r := range deps {
				if v := r.version(); v != "" {
					lk.members[key+"@"+v] = true
				}
			}
		}
	}
	im, ok := lf.Importers[cmp.Or(member, ".")]
	if !ok && member == "" {
		im = lf.pnpmImporter
	}
	for _, deps := range []map[string]pnpmRef{im.Dependencies, im.DevDependencies, im.OptionalDependencies} {
		for k, r := range deps {
			if strings.HasPrefix(string(r), "link:") {
				lk.links[k] = true
			}
		}
	}
	lk.resolve = func(key, _ string) string { return im.get(key) }
	return lk, nil
}

// pnpmDevOnly finds the packages only the importers' devDependencies reach.
// Lockfile 9 dropped the dev flag from packages, so it follows the snapshots
// from what each importer requires.
func pnpmDevOnly(importers map[string]pnpmImporter, snaps map[string]pnpmPkg) map[string]bool {
	edges := map[string][]string{}
	for k, p := range snaps {
		name, v, ok := pnpmKey(k, 9)
		if !ok {
			continue
		}
		for _, deps := range []map[string]string{p.Dependencies, p.OptionalDependencies} {
			for dep, ref := range deps {
				if to, ok := pnpmTarget(dep, ref); ok {
					edges[name+"@"+v] = append(edges[name+"@"+v], to)
				}
			}
		}
	}
	reach := func(roots []string) map[string]bool {
		seen := map[string]bool{}
		for len(roots) > 0 {
			curr := roots[len(roots)-1]
			roots = roots[:len(roots)-1]
			if seen[curr] {
				continue
			}
			seen[curr] = true
			roots = append(roots, edges[curr]...)
		}
		return seen
	}
	var prod, dev []string
	for _, im := range importers {
		prod = pnpmTargets(prod, im.Dependencies, im.OptionalDependencies)
		dev = pnpmTargets(dev, im.DevDependencies)
	}
	inProd, inDev := reach(prod), reach(dev)
	res := map[string]bool{}
	for k := range inDev {
		if !inProd[k] {
			res[k] = true
		}
	}
	return res
}

func pnpmTargets(res []string, ms ...map[string]pnpmRef) []string {
	for _, m := range ms {
		for name, ref := range m {
			if to, ok := pnpmTarget(name, string(ref)); ok {
				res = append(res, to)
			}
		}
	}
	return res
}

// pnpmTarget is the name@version a lockfile 9 entry points at: ref is a
// version with its peer suffix, or name@version for an alias.
func pnpmTarget(name, ref string) (string, bool) {
	if i := strings.IndexByte(ref, '('); i >= 0 {
		ref = ref[:i]
	}
	if npmValid(ref) {
		return name + "@" + ref, true
	}
	if n, v, ok := pnpmKey(ref, 9); ok {
		return n + "@" + v, true
	}
	return "", false
}

func pnpmKey(k string, major int) (name, version string, ok bool) {
	k = strings.TrimPrefix(k, "/")
	if i := strings.IndexByte(k, '('); i >= 0 {
		k = k[:i]
	}
	if major > 0 && major < 6 {
		i := strings.LastIndexByte(k, '/')
		if i <= 0 {
			return "", "", false
		}
		name, version = k[:i], k[i+1:]
		if j := strings.IndexByte(version, '_'); j >= 0 {
			version = version[:j]
		}
	} else {
		i := strings.LastIndexByte(k, '@')
		if i <= 0 {
			return "", "", false
		}
		name, version = k[:i], k[i+1:]
	}
	return name, version, npmValid(version)
}

// yarnLock reads yarn.lock, classic and berry alike: blocks headed by the
// comma separated name@range specs they resolve, each with a version line.
// Berry writes ranges with an npm: protocol; workspace: ranges are links.
func yarnLock(src []byte) npmLock {
	bySpec := map[string]string{}
	byName := map[string][]string{}
	lk := npmLock{links: map[string]bool{}}
	var specs []string
	var block []int // entries of lk.all the current block added
	deps := false
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case line == "" || line[0] == '#':
		case line[0] != ' ':
			specs, block, deps = strings.Split(strings.TrimSuffix(line, ":"), ", "), nil, false
		case strings.HasPrefix(line, "    "):
			if deps {
				name, _, _ := strings.Cut(strings.TrimSpace(line), " ")
				name = strings.Trim(strings.TrimSuffix(strings.Trim(name, `"`), ":"), `"`)
				for _, i := range block {
					lk.all[i].requires = append(lk.all[i].requires, name)
				}
			}
		case !strings.HasPrefix(line, "  version"):
			switch strings.TrimSpace(line) {
			case "dependencies:", "optionalDependencies:", "peerDependencies:":
				deps = true
			default:
				deps = false
			}
		case specs != nil:
			v := strings.Trim(strings.TrimSpace(line[len("  version"):]), `:"' `)
			for _, spec := range specs {
				name, rng, ok := yarnSpec(spec)
				switch {
				case !ok:
				case strings.HasPrefix(rng, "workspace:"):
					lk.links[name] = true
				case !strings.Contains(rng, ":") && npmValid(v):
					bySpec[name+"@"+rng] = v
					if !slices.Contains(byName[name], v) {
						byName[name] = append(byName[name], v)
						block = append(block, len(lk.all))
						lk.all = append(lk.all, lockPkg{name: yarnAliased(name, rng), version: v})
					}
				}
			}
			specs = nil
		}
	}
	lk.resolve = func(key, req string) string {
		if v, ok := bySpec[key+"@"+strings.TrimPrefix(req, "npm:")]; ok {
			return v
		}
		if vs := byName[key]; len(vs) == 1 {
			return vs[0]
		}
		return ""
	}
	return lk
}

// yarnAliased is the package an alias@npm:pkg@range spec installs.
func yarnAliased(name, rng string) string {
	if i := strings.LastIndexByte(rng, '@'); i > 0 {
		return rng[:i]
	}
	return name
}

// yarnSpec splits a lockfile spec, quoted or not, into name and range, and
// drops berry's npm: protocol from the range.
func yarnSpec(spec string) (name, rng string, ok bool) {
	s := strings.Trim(spec, `"'`)
	i := strings.IndexByte(s[min(1, len(s)):], '@') + 1
	if i <= 0 {
		return "", "", false
	}
	return s[:i], strings.TrimPrefix(s[i+1:], "npm:"), true
}
