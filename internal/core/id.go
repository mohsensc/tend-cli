package core

import (
	"slices"
	"strings"
)

// ChangeID is stable across runs: kind-independent, target-dependent.
//
//	dep bump:     <eco>/<name>@<to>            npm/stripe@16.1.0
//	swap:         <eco>/<name>-><toName>@<to>  npm/request->undici@6.19.8
//	group:        <eco>/group:<group>@<to>     npm/group:aws-sdk@3.700.0
//	api:          api/<provider>/<METHOD> <path>[#<field>]
//
// A group change carries its group name in c.ID; when it has none yet, the
// name is the longest common prefix of the dep names. A tidy change is TidyID.
func ChangeID(c *Change) string {
	if c.Kind == Tidy {
		return TidyID
	}
	if c.API != nil {
		a := c.API
		id := "api/" + a.Provider + "/" + strings.TrimSpace(strings.ToUpper(a.Method)+" "+a.Path)
		if a.Field != "" {
			id += "#" + a.Field
		}
		return id
	}
	if len(c.Deps) == 0 {
		return c.ID
	}
	d := c.Deps[0]
	if len(Names(c)) > 1 {
		return d.Ecosystem + "/group:" + groupName(c) + "@" + c.To
	}
	if c.ToName != "" {
		return d.Ecosystem + "/" + d.Name + "->" + c.ToName + "@" + c.To
	}
	return d.Ecosystem + "/" + d.Name + "@" + c.To
}

// TidyID is the id of tend's first PR on a repo, whatever it clears out.
const TidyID = "tidy-up"

// Names lists the packages a change moves, each once, in the order of its
// deps. One package written in two manifests, or on two lines of one, is one
// name; a group has several.
func Names(c *Change) []string {
	var res []string
	for _, d := range c.Deps {
		if !slices.Contains(res, d.Name) {
			res = append(res, d.Name)
		}
	}
	return res
}

func groupName(c *Change) string {
	prefix := c.Deps[0].Ecosystem + "/group:"
	if name, ok := strings.CutPrefix(c.ID, prefix); ok {
		if i := strings.LastIndexByte(name, '@'); i > 0 {
			return name[:i]
		}
		return name
	}
	common := c.Deps[0].Name
	for _, d := range c.Deps[1:] {
		n := 0
		for n < len(common) && n < len(d.Name) && common[n] == d.Name[n] {
			n++
		}
		common = common[:n]
	}
	if i := strings.LastIndexAny(common, "/-_.:"); i >= 0 && i < len(common)-1 {
		common = common[:i]
	}
	common = strings.Trim(common, "@/-_.:")
	if common == "" {
		return c.Deps[0].Name
	}
	return common
}

// Slug makes a branch-safe lowercase token: [a-z0-9.-]+, at most 60 chars.
func Slug(s string) string {
	var b strings.Builder
	var prev byte
	for _, r := range strings.ToLower(s) {
		c := byte('-')
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' {
			c = byte(r)
		}
		// git refuses ".." in ref names, and runs of "-" are just noise.
		if (c == '-' || c == '.') && c == prev {
			continue
		}
		b.WriteByte(c)
		prev = c
	}
	res := strings.Trim(b.String(), "-.")
	if len(res) > 60 {
		res = strings.TrimRight(res[:60], "-.")
	}
	return res
}

// Subject is a change id without its target: the same thing at another
// version has the same subject.
func Subject(id string) string {
	if strings.HasPrefix(id, "api/") {
		return id
	}
	if i := strings.LastIndexByte(id, '@'); i > strings.IndexByte(id, '/') {
		return id[:i]
	}
	return id
}

// TargetOf is the version a change id ends in, "" for API changes and for
// changes with no target.
func TargetOf(id string) string {
	if s := Subject(id); s != id {
		return id[len(s)+1:]
	}
	return ""
}
