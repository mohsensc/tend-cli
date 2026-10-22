package core

import "context"

type ProviderUse struct {
	Provider string
	Hosts    []string
	SDK      []Dep    // SDK packages of this provider found in the manifests
	URLs     []URLUse // raw HTTP uses of its hosts
	Sites    []Site   // SDK member chains
}

type Inventory struct {
	Root      string
	Base      string // base commit sha
	Manifests []Manifest
	Deps      []Dep
	Index     ScanIndex
	Providers []ProviderUse
}

// Source produces changes from upstream knowledge. Registry bumps are produced by plan itself.
type Source interface {
	Name() string
	Changes(ctx context.Context, inv *Inventory) ([]Change, error)
}
