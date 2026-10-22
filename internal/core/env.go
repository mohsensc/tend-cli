package core

import (
	"regexp"
	"slices"
	"strings"
)

// secretName catches GITHUB_TOKEN, GH_TOKEN, a model provider's API key and
// the rest of their kind by name.
var secretName = regexp.MustCompile(`(?i)(token|secret|password|passwd|api[_-]?key|private|credential)`)

// Scrub returns env without anything that looks like a secret and without
// tend's own TEND_ variables, except the names in keep. It is the
// environment everything tend starts runs with, unless it needs more.
func Scrub(env, keep []string) []string {
	var res []string
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(keep, name) && secret(name) {
			continue
		}
		res = append(res, kv)
	}
	return res
}

func secret(name string) bool {
	return strings.HasPrefix(name, "TEND_") || secretName.MatchString(name)
}
