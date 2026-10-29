package api

import (
	"strings"
	"unicode"
)

// verbs is what an SDK verb usually means over REST; a provider's own verbs win.
var verbs = map[string]string{
	"create":   "POST",
	"retrieve": "GET/{id}",
	"list":     "GET",
	"update":   "PATCH/{id}",
	"del":      "DELETE/{id}",
	"cancel":   "POST/{id}/cancel",
	"capture":  "POST/{id}/capture",
	"confirm":  "POST/{id}/confirm",
	"send":     "POST/{id}/send",
}

var aliases = map[string]string{
	"get":     "retrieve",
	"fetch":   "retrieve",
	"find":    "retrieve",
	"all":     "list",
	"modify":  "update",
	"delete":  "del",
	"remove":  "del",
	"destroy": "del",
}

// Operation maps an SDK member chain such as ["stripe", "paymentIntents", "create"]
// to the operation it calls: POST /v1/payment_intents. The result is a guess
// until the spec confirms it.
func (p *Provider) Operation(chain []string, lang string) (method, path string, ok bool) {
	return p.guess(chain, lang, false)
}

// Nested is Operation's other guess for a chain of several resources, each
// below an item of the one before: customers.sources.create is POST
// /v1/customers/{id}/sources. ok is false for a chain with one resource.
func (p *Provider) Nested(chain []string, lang string) (method, path string, ok bool) {
	if len(chain) < 4 {
		return "", "", false
	}
	return p.guess(chain, lang, true)
}

func (p *Provider) guess(chain []string, lang string, nested bool) (method, path string, ok bool) {
	if len(chain) < 2 {
		return "", "", false
	}
	verb := chain[len(chain)-1]
	if verb == "" {
		return "", "", false
	}
	verb = string(unicode.ToLower(rune(verb[0]))) + verb[1:]
	// Ruby's .new builds an object locally; only Go's New calls the API.
	if lang == "go" && verb == "new" {
		verb = "create"
	}
	spec, ok := p.verb(verb)
	if !ok && len(chain) > 2 && verb != "new" {
		spec, ok = "POST/{id}/"+snake(verb), true
	}
	if !ok {
		return "", "", false
	}

	// With nothing between the root and the verb, the root is the resource:
	// a Go package (charge.New) or a class (Charge.create in Java or Python).
	res := chain[1 : len(chain)-1]
	plural := len(res) == 0
	if plural {
		res = chain[:1]
	}
	last := res[len(res)-1]
	if lang != "go" && last != "" && unicode.IsUpper(rune(last[0])) {
		plural = true
	}
	segs := make([]string, len(res))
	for i, s := range res {
		segs[i] = snake(s)
	}
	if plural {
		segs[len(segs)-1] = pluralize(segs[len(segs)-1])
	}

	method, suffix, _ := strings.Cut(spec, "/")
	if suffix != "" {
		suffix = "/" + suffix
	}
	sep := "/"
	if nested {
		sep = "/{id}/"
	}
	return method, strings.TrimRight(p.Base, "/") + "/" + strings.Join(segs, sep) + suffix, true
}

func (p *Provider) verb(v string) (string, bool) {
	if s, ok := p.Verbs[v]; ok {
		return s, true
	}
	if a, ok := aliases[v]; ok {
		v = a
	}
	if s, ok := p.Verbs[v]; ok {
		return s, true
	}
	s, ok := verbs[v]
	return s, ok
}

// snake turns paymentIntents, PaymentIntent and HTTPClient into payment_intents,
// payment_intent and http_client.
func snake(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i, r := range rs {
		if unicode.IsUpper(r) {
			if i > 0 && (unicode.IsLower(rs[i-1]) || unicode.IsDigit(rs[i-1]) || i+1 < len(rs) && unicode.IsLower(rs[i+1])) && rs[i-1] != '_' {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// pluralize is just enough English for resource names. A word that already
// ends in a single s is taken as plural.
func pluralize(s string) string {
	switch {
	case strings.HasSuffix(s, "ss"), strings.HasSuffix(s, "x"), strings.HasSuffix(s, "z"),
		strings.HasSuffix(s, "ch"), strings.HasSuffix(s, "sh"):
		return s + "es"
	case strings.HasSuffix(s, "s"):
		return s
	case len(s) > 1 && strings.HasSuffix(s, "y") && !strings.ContainsRune("aeiou", rune(s[len(s)-2])):
		return s[:len(s)-1] + "ies"
	}
	return s + "s"
}
