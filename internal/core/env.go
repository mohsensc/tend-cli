package core

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// secretName catches GITHUB_TOKEN, GH_TOKEN, a model provider's API key and
// the rest of their kind by name, and credentials by the shape of their
// name: STRIPE_KEY, GH_PAT, VAULT_ROOT_TOKEN.
var secretName = regexp.MustCompile(`(?i)(token|secret|password|passwd|api[_-]?key|private|credential)|(_KEY|_PAT)$`)

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

var kept struct {
	sync.RWMutex
	names []string
}

// Keep names the variables Environ passes on although they look like
// secrets, such as a private registry's token in verify.env.
func Keep(names []string) {
	kept.Lock()
	defer kept.Unlock()
	kept.names = slices.Clone(names)
}

// Environ is the environment the tools tend runs itself see, a lock tool or
// go doc: tend's own, scrubbed, with the names Keep listed, plus extra.
func Environ(extra ...string) []string {
	kept.RLock()
	defer kept.RUnlock()
	return append(Scrub(os.Environ(), kept.names), extra...)
}
