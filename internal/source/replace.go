package source

import (
	"cmp"
	_ "embed"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/mohsensc/tend-cli/internal/core"
)

//go:embed replacements.yml
var replacements []byte

type replacement struct {
	Eco     string `yaml:"eco"`
	Name    string `yaml:"name"`
	To      string `yaml:"to"`
	Options []struct {
		To  string `yaml:"to"`
		Why string `yaml:"why"` // what choosing it means, next to the others
	} `yaml:"options"`
	Note string `yaml:"note"`
	URL  string `yaml:"url"`
}

var table = sync.OnceValue(func() map[string]replacement {
	var rows []replacement
	if err := yaml.Unmarshal(replacements, &rows); err != nil {
		panic("source: replacements.yml: " + err.Error())
	}
	res := make(map[string]replacement, len(rows))
	for _, r := range rows {
		res[r.Eco+"/"+r.Name] = r
	}
	return res
})

// Replacement returns the single mechanical replacement for a deprecated
// package, or the options when there is more than one way off it.
func Replacement(eco, name string) (*core.Option, []core.Option, bool) {
	r, ok := table()[eco+"/"+name]
	if !ok {
		return nil, nil, false
	}
	detail := strings.TrimSpace(r.Note + " " + r.URL)
	if r.To != "" {
		return &core.Option{Title: "Switch to " + r.To, Detail: detail, Safe: true, ToName: r.To}, nil, true
	}
	if len(r.Options) == 0 {
		return nil, nil, false
	}
	res := make([]core.Option, len(r.Options))
	for i, o := range r.Options {
		res[i] = core.Option{Title: "Switch to " + o.To, Detail: strings.TrimSpace(cmp.Or(o.Why, r.Note) + " " + r.URL), ToName: o.To}
	}
	return nil, res, true
}
