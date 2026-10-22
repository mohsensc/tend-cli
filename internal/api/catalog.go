package api

import (
	_ "embed"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/mohsensc/tend-cli/internal/config"
)

//go:embed providers.yml
var providers []byte

type SDK struct {
	Eco  string `yaml:"eco"`
	Name string `yaml:"name"`
}

type Provider struct {
	Name      string            `yaml:"name"`
	Hosts     []string          `yaml:"hosts"`
	Spec      string            `yaml:"spec"`      // URL, or a path relative to the repo root
	Changelog string            `yaml:"changelog"` // RSS, Atom or HTML page, for a provider without a spec
	SDKs      []SDK             `yaml:"sdks"`
	Base      string            `yaml:"base"`   // path prefix SDK calls go under: "/v1"
	Verbs     map[string]string `yaml:"verbs"`  // SDK verb -> "METHOD[/suffix]", over the defaults in chains.go
	Sunset    string            `yaml:"sunset"` // dotted key on an operation holding its removal date
}

// Catalog is the built-in list of providers.
func Catalog() []Provider {
	var res []Provider
	if err := yaml.Unmarshal(providers, &res); err != nil {
		panic("api: providers.yml: " + err.Error())
	}
	return res
}

// Merge applies the apis entries of tend.yml to the catalog: an entry with a
// known name replaces that provider's hosts and spec, any other is appended.
func Merge(cat []Provider, cfg []config.API) []Provider {
	res := slices.Clone(cat)
	for _, a := range cfg {
		i := slices.IndexFunc(res, func(p Provider) bool { return p.Name == a.Name })
		if i < 0 {
			res = append(res, Provider{Name: a.Name, Hosts: a.Hosts, Spec: a.Spec, Changelog: a.Changelog})
			continue
		}
		if len(a.Hosts) > 0 {
			res[i].Hosts = a.Hosts
		}
		if a.Spec != "" {
			res[i].Spec = a.Spec
		}
		if a.Changelog != "" {
			res[i].Changelog = a.Changelog
		}
	}
	return res
}
