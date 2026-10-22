package verify

import (
	"path"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/eco"
)

// Detect merges the commands in config with each ecosystem's defaults. A
// non-empty install, build or test list in config replaces that list entirely.
// Each ecosystem runs its lines from the right directory itself; installs that
// a manifest higher up already covers (npm workspaces, a nested requirements
// file without a lock of its own) are left out.
func Detect(root string, manifests []core.Manifest, ecos map[string]core.Ecosystem, cfg config.Verify) core.Commands {
	var res core.Commands
	for _, e := range eco.All() {
		name := e.Name()
		ec := ecos[name]
		if ec == nil {
			continue
		}
		ms := byDepth(manifests, name)
		for _, m := range ms {
			c := ec.Commands(root, m)
			if !covered(ms, m) {
				res.Install = append(res.Install, c.Install...)
			}
			res.Build = append(res.Build, c.Build...)
			res.Test = append(res.Test, c.Test...)
		}
	}
	res.Install = pick(cfg.Install, res.Install)
	res.Build = pick(cfg.Build, res.Build)
	res.Test = pick(cfg.Test, res.Test)
	return res
}

func byDepth(manifests []core.Manifest, name string) []core.Manifest {
	var res []core.Manifest
	for _, m := range manifests {
		if m.Ecosystem == name {
			res = append(res, m)
		}
	}
	slices.SortStableFunc(res, func(a, b core.Manifest) int {
		if d := strings.Count(a.Path, "/") - strings.Count(b.Path, "/"); d != 0 {
			return d
		}
		return strings.Compare(a.Path, b.Path)
	})
	return res
}

// covered reports whether a manifest in a parent directory installs for m.
func covered(ms []core.Manifest, m core.Manifest) bool {
	dir := path.Dir(m.Path)
	if dir == "." || m.Lock != "" && path.Dir(m.Lock) == dir {
		return false
	}
	for _, n := range ms {
		up := path.Dir(n.Path)
		if up != dir && (up == "." || strings.HasPrefix(dir, up+"/")) {
			return true
		}
	}
	return false
}

func pick(override, detected []string) []string {
	if len(override) > 0 {
		return slices.Clone(override)
	}
	var res []string
	for _, l := range detected {
		if !slices.Contains(res, l) {
			res = append(res, l)
		}
	}
	return res
}
