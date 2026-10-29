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
// Each ecosystem runs its lines from the right directory itself. A nested
// install is left out when a manifest higher up installs for it, as a
// workspace root does for its members, or when nothing runs in its
// directory, as with a requirements file another one pulls in.
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
			if !covered(ms, m) && (path.Dir(m.Path) == "." || len(c.Build)+len(c.Test) > 0) {
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

// covered reports whether a manifest higher up installs for m: m's lock
// sits in an ancestor directory that has a manifest of its own, as with an
// npm, pnpm, yarn or cargo workspace member. A nested manifest with no lock,
// or a lock of its own, installs for itself.
func covered(ms []core.Manifest, m core.Manifest) bool {
	dir := path.Dir(m.Path)
	if dir == "." || m.Lock == "" {
		return false
	}
	up := path.Dir(m.Lock)
	if up == dir || up != "." && !strings.HasPrefix(dir, up+"/") {
		return false
	}
	return slices.ContainsFunc(ms, func(n core.Manifest) bool { return path.Dir(n.Path) == up })
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
