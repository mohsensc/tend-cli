package core

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
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
// name is the longest common prefix of the dep names. The first PR is FirstID.
func ChangeID(c *Change) string {
	if c.Kind == First {
		if c.ID != "" && StageOf(c.ID) > 0 {
			return c.ID
		}
		return FirstID
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

// FirstID is the id of tend's first PR on a repo, whatever it carries. It
// keeps the name the first PR had when it only tidied up, so a first PR an
// earlier tend opened is updated in place.
const FirstID = "tidy-up"

// StageID is the id of a catch-up PR after the first, for stage k.
func StageID(k int) string { return "catch-up-" + strconv.Itoa(k) }

// StageOf is the stage a later catch-up PR's id names, 0 for any other id.
func StageOf(id string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "catch-up-"))
	if err != nil || !strings.HasPrefix(id, "catch-up-") || n < 1 {
		return 0
	}
	return n
}

// CatchUp reports whether id is a catch-up PR's, the first or a later one.
func CatchUp(id string) bool { return id == FirstID || StageOf(id) > 0 }

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
// A longer one is cut and ends in a hash of s, so two that share their
// first 60 chars stay apart.
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
	if len(res) > slugMax {
		res = Cut(res, slugMax, s)
	}
	return res
}

const slugMax = 60

// Cut shortens slug to at most n bytes, the last nine a dash and a hash of
// whole, which tells apart what the cut would make the same.
func Cut(slug string, n int, whole string) string {
	sum := sha256.Sum256([]byte(whole))
	tag := "-" + hex.EncodeToString(sum[:])[:8]
	return strings.TrimRight(slug[:max(n-len(tag), 1)], "-.") + tag
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
