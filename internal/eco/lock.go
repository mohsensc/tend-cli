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

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/ver"
)

// Locked is one package a lockfile pins and the packages it requires, all
// by canonical name.
type Locked struct {
	Name, Version string
	Requires      []string
}

// ReadLock reads a lockfile by its base name: the registry packages it pins,
// read the way Deps reads them. It returns core.ErrNotSupported for a file it
// does not know.
func ReadLock(file string, src []byte) ([]Locked, error) {
	var res []Locked
	switch path.Base(file) {
	case "package-lock.json", "npm-shrinkwrap.json":
		lk, err := packageLock(src, "")
		if err != nil {
			return nil, err
		}
		res = toLocked(lk.all)
	case "pnpm-lock.yaml":
		lk, err := pnpmLock(src, "")
		if err != nil {
			return nil, err
		}
		res = toLocked(lk.all)
	case "yarn.lock":
		res = toLocked(yarnLock(src).all)
	case "uv.lock", "poetry.lock":
		read := uvLock
		if path.Base(file) == "poetry.lock" {
			read = poetryLocked
		}
		ps, err := read(src)
		if err != nil {
			return nil, err
		}
		res = toLocked(ps)
	case "Cargo.lock":
		ps, err := cargoPackages(src)
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			var reqs []string
			for _, d := range p.Dependencies {
				reqs = append(reqs, strings.Fields(d)[0])
			}
			res = append(res, Locked{p.Name, p.Version, reqs})
		}
	case "composer.lock":
		var lock composerLock
		if err := json.Unmarshal(stripBOM(src), &lock); err != nil {
			return nil, err
		}
		for _, p := range append(lock.Packages, lock.PackagesDev...) {
			var reqs []string
			for _, d := range keys(p.Require) {
				if strings.Contains(d, "/") {
					reqs = append(reqs, strings.ToLower(d))
				}
			}
			res = append(res, Locked{strings.ToLower(p.Name), p.Version, reqs})
		}
	case "Gemfile.lock":
		for _, g := range gemLock(src) {
			res = append(res, Locked{g.name, g.version, g.requires})
		}
	case "packages.lock.json":
		var lock nugetLockFile
		if err := json.Unmarshal(stripBOM(src), &lock); err != nil {
			return nil, err
		}
		for _, fw := range lock.Dependencies {
			for id, e := range fw {
				if e.Type == "Project" || e.Resolved == "" {
					continue
				}
				var reqs []string
				for _, d := range keys(e.Dependencies) {
					reqs = append(reqs, strings.ToLower(d))
				}
				res = append(res, Locked{strings.ToLower(id), e.Resolved, reqs})
			}
		}
	default:
		return nil, core.ErrNotSupported
	}
	return merge(res), nil
}

// Canon is the name an ecosystem compares packages by.
func Canon(ecosystem, name string) string {
	switch ecosystem {
	case core.Pip:
		return PyNorm(name)
	case core.Nuget, core.Composer:
		return strings.ToLower(name)
	}
	return name
}

// lockPkg is one package a lockfile installs, as Deps reads it.
type lockPkg struct {
	name, version string
	dev           bool
	requires      []string
}

func toLocked(all []lockPkg) []Locked {
	res := make([]Locked, len(all))
	for i, p := range all {
		res[i] = Locked{p.name, p.version, p.requires}
	}
	return res
}

// merge sorts ls and folds the entries of one name and version into one, as
// a lockfile with several targets or peer sets writes them.
func merge(ls []Locked) []Locked {
	slices.SortFunc(ls, func(a, b Locked) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.Version, b.Version))
	})
	var res []Locked
	for _, l := range ls {
		if n := len(res); n > 0 && res[n-1].Name == l.Name && res[n-1].Version == l.Version {
			res[n-1].Requires = union(res[n-1].Requires, l.Requires)
			continue
		}
		l.Requires = union(nil, l.Requires)
		res = append(res, l)
	}
	return res
}

// keys lists the keys of every map, sorted, each once.
func keys[V any](ms ...map[string]V) []string {
	var res []string
	for _, m := range ms {
		for k := range m {
			res = append(res, k)
		}
	}
	slices.Sort(res)
	return slices.Compact(res)
}

// union is a with what b adds, in order, each once.
func union(a, b []string) []string {
	for _, s := range b {
		if !slices.Contains(a, s) {
			a = append(a, s)
		}
	}
	return a
}

// readLocked reads the lockfile at lock under root; no lockfile reads as
// nothing locked.
func readLocked(root, lock string) ([]Locked, error) {
	if lock == "" {
		return nil, nil
	}
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(lock)))
	if err != nil {
		return nil, err
	}
	ls, err := ReadLock(lock, src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", lock, err)
	}
	return ls, nil
}

// lockedVersions is every version the lockfile at lock pins for name.
func lockedVersions(root, lock, ecosystem, name string) (map[string]bool, error) {
	ls, err := readLocked(root, lock)
	if err != nil {
		return nil, err
	}
	name = Canon(ecosystem, name)
	res := map[string]bool{}
	for _, l := range ls {
		if l.Name == name {
			res[l.Version] = true
		}
	}
	return res, nil
}

// lockedTo checks a targeted update of a dep only the lockfile names against
// what it actually locked. The tool moves such a dep to the newest release
// its ranges allow, so to is a floor: every version of name the update
// brought in must be to or later on to's major, and one of them must be
// locked. A copy on another release line that the update only moved up
// within its line is another fix's business. It returns the newest one when
// that is past to, else "".
func lockedTo(root, lock, ecosystem, name, to string, before map[string]bool) (string, error) {
	after, err := lockedVersions(root, lock, ecosystem, name)
	if err != nil {
		return "", err
	}
	floor, ok := ver.Parse(to)
	fits := func(v string) bool {
		x, okX := ver.Parse(v)
		if !ok || !okX {
			return sameVersion(v, to)
		}
		return ver.Compare(x, floor) >= 0 && x.Major() == floor.Major()
	}
	raised := func(v string) bool {
		x, okX := ver.Parse(v)
		if !okX {
			return false
		}
		for b := range before {
			y, okY := ver.Parse(b)
			if okY && y.Line() == x.Line() && ver.Compare(y, x) < 0 && y.Line() != floor.Line() {
				return true
			}
		}
		return false
	}
	var other, in []string
	for _, v := range keys(after) {
		switch {
		case before[v] && !fits(v):
		case fits(v):
			in = append(in, v)
		case raised(v):
		default:
			other = append(other, v)
		}
	}
	switch {
	case len(other) > 0:
		return "", fmt.Errorf("%s: the update locked %s at %s, not %s or a later %s", lock, name, strings.Join(other, ", "), to, series(to))
	case len(in) == 0:
		return "", fmt.Errorf("%s: the update did not lock %s at %s or a later %s", lock, name, to, series(to))
	}
	slices.SortFunc(in, func(a, b string) int {
		x, _ := ver.Parse(a)
		y, _ := ver.Parse(b)
		return ver.Compare(x, y)
	})
	if top := in[len(in)-1]; !sameVersion(top, to) {
		return top, nil
	}
	return "", nil
}

// series names the major release line of v: "7.x" for 7.5.21.
func series(v string) string {
	if x, ok := ver.Parse(v); ok {
		return strconv.Itoa(x.Major()) + ".x"
	}
	return "release"
}

// lockLanded checks that the lockfile at lock pins name at to. A targeted
// update that resolves elsewhere would leave the proof testing a release
// tend did not choose. Any one locked copy at to will do: a crate can be
// locked at several versions.
func lockLanded(root, lock, ecosystem, name, to string) error {
	after, err := lockedVersions(root, lock, ecosystem, name)
	if err != nil {
		return err
	}
	seen := keys(after)
	switch {
	case slices.ContainsFunc(seen, func(v string) bool { return sameVersion(v, to) }):
		return nil
	case len(seen) == 0:
		return fmt.Errorf("%s: %s is not locked after the update to %s", lock, name, to)
	}
	return fmt.Errorf("%s: %s locked at %s, not %s", lock, name, strings.Join(seen, ", "), to)
}

// sameVersion compares by value, so composer's v2.0.0 is 2.0.0.
func sameVersion(a, b string) bool {
	va, okA := ver.Parse(a)
	vb, okB := ver.Parse(b)
	if okA && okB {
		return ver.Compare(va, vb) == 0
	}
	return a == b
}
