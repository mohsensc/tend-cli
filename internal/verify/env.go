package verify

import (
	"regexp"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
)

// secretShape catches credentials by the shape of their name, beyond what
// core.Scrub drops: STRIPE_KEY, GH_PAT, VAULT_ROOT_TOKEN and the like.
var secretShape = regexp.MustCompile(`(?i)(_KEY|_TOKEN|_SECRET|_PAT|_CREDENTIALS)$|PASSWORD`)

// Filter returns the environment the project's commands see: env without
// anything that looks like a secret and without tend's own variables, plus the
// names in keep (verify.env), plus the settings that make installs quiet and
// reproducible.
func Filter(env []string, keep []string) []string {
	set := map[string]string{
		"CI":               "1",
		"TEND":             "1",
		"npm_config_fund":  "false",
		"npm_config_audit": "false",
		"GOFLAGS":          "-mod=mod",
	}
	var res []string
	for _, kv := range core.Scrub(env, keep) {
		name, val, _ := strings.Cut(kv, "=")
		if secretShape.MatchString(name) && !slices.Contains(keep, name) {
			continue
		}
		if name == "GOFLAGS" {
			if !strings.Contains(val, "-mod=") {
				val = strings.TrimSpace(val + " -mod=mod")
			}
			set[name] = val
			continue
		}
		if _, ok := set[name]; ok {
			continue
		}
		res = append(res, kv)
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		res = append(res, name+"="+set[name])
	}
	return res
}
