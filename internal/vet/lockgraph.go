package vet

import (
	"slices"

	"github.com/mohsensc/tend-cli/internal/eco"
)

// graph is what a lockfile says: the versions locked for each package and
// which packages each one depends on, by canonical name.
type graph struct {
	vers map[string][]string
	deps map[string][]string
}

func newGraph() *graph {
	return &graph{vers: map[string][]string{}, deps: map[string][]string{}}
}

func (g *graph) add(name, ver string, deps ...string) {
	if name == "" {
		return
	}
	if ver != "" && !slices.Contains(g.vers[name], ver) {
		g.vers[name] = append(g.vers[name], ver)
	}
	if _, ok := g.vers[name]; !ok {
		g.vers[name] = nil
	}
	for _, d := range deps {
		if d != "" && !slices.Contains(g.deps[name], d) {
			g.deps[name] = append(g.deps[name], d)
		}
	}
}

// closure is every package reachable from roots, roots included.
func (g *graph) closure(roots []string) map[string]bool {
	res := map[string]bool{}
	stack := slices.Clone(roots)
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if res[n] {
			continue
		}
		res[n] = true
		stack = append(stack, g.deps[n]...)
	}
	return res
}

// churn lists packages whose locked versions differ between old and new.
func churn(old, new *graph) []string {
	var res []string
	for name, vs := range new.vers {
		if !sameSet(vs, old.vers[name]) {
			res = append(res, name)
		}
	}
	for name := range old.vers {
		if _, ok := new.vers[name]; !ok {
			res = append(res, name)
		}
	}
	slices.Sort(res)
	return res
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, v := range a {
		if !slices.Contains(b, v) {
			return false
		}
	}
	return true
}

// parseLock reads a lockfile into a graph, through the ecosystem that owns it.
func parseLock(file string, b []byte) (*graph, error) {
	ls, err := eco.ReadLock(file, b)
	if err != nil {
		return nil, err
	}
	g := newGraph()
	for _, l := range ls {
		g.add(l.Name, l.Version, l.Requires...)
	}
	return g, nil
}
