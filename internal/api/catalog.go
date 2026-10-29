package api

import (
	_ "embed"
	"slices"
	"sync"

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
	Title     string            `yaml:"title"` // the name as the provider writes it: "GitHub"
	Hosts     []string          `yaml:"hosts"`
	Spec      string            `yaml:"spec"`      // URL, or a path relative to the repo root
	More      []string          `yaml:"-"`         // more specs of the same API, from a spec: list, merged into Spec's
	Changelog string            `yaml:"changelog"` // RSS, Atom or HTML page, for a provider without a spec
	SDKs      []SDK             `yaml:"sdks"`
	Base      string            `yaml:"base"`   // path prefix SDK calls go under: "/v1"
	Verbs     map[string]string `yaml:"verbs"`  // SDK verb -> "METHOD[/suffix]", over the defaults in chains.go
	Sunset    string            `yaml:"sunset"` // dotted key on an operation holding its removal date
}

// UnmarshalYAML takes spec as one URL or a list of them, for an API whose
// provider splits its spec into several files.
func (p *Provider) UnmarshalYAML(n *yaml.Node) error {
	type plain Provider
	rest := *n
	rest.Content = nil
	var spec *yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "spec" {
			spec = n.Content[i+1]
			continue
		}
		rest.Content = append(rest.Content, n.Content[i], n.Content[i+1])
	}
	if err := rest.Decode((*plain)(p)); err != nil {
		return err
	}
	if spec == nil {
		return nil
	}
	if spec.Kind != yaml.SequenceNode {
		return spec.Decode(&p.Spec)
	}
	var specs []string
	if err := spec.Decode(&specs); err != nil {
		return err
	}
	if len(specs) > 0 {
		p.Spec, p.More = specs[0], specs[1:]
	}
	return nil
}

// Specs are all the spec files of the provider, Spec first.
func (p *Provider) Specs() []string {
	if p.Spec == "" {
		return nil
	}
	return append([]string{p.Spec}, p.More...)
}

// Catalog is the built-in list of providers.
func Catalog() []Provider {
	var res []Provider
	if err := yaml.Unmarshal(providers, &res); err != nil {
		panic("api: providers.yml: " + err.Error())
	}
	return res
}

var catalog = sync.OnceValue(Catalog)

// Title is the name a provider goes by in a PR: the catalog's spelling, or the
// name as tend.yml gives it.
func Title(name string) string {
	for _, p := range catalog() {
		if p.Name == name && p.Title != "" {
			return p.Title
		}
	}
	return name
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
			res[i].Spec, res[i].More = a.Spec, nil
		}
		if a.Changelog != "" {
			res[i].Changelog = a.Changelog
		}
	}
	return res
}
