package config

import (
	"fmt"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/ver"
)

// Ignored reports whether config ignores bumping d to version to, and why. API
// changes are matched as ecosystem "api" with the provider as the name.
func (c *Config) Ignored(d core.Dep, to string) (string, bool) {
	for _, ig := range c.Ignore {
		if ig.Ecosystem != "" && ig.Ecosystem != d.Ecosystem {
			continue
		}
		if !Glob(ig.Name, d.Name) || !inRanges(ig.Versions, to) {
			continue
		}
		if ig.Reason != "" {
			return ig.Reason, true
		}
		return "ignored by tend.yml (" + ig.Name + ")", true
	}
	return "", false
}

// Keeps reports whether an ignore entry names d, whatever versions it covers:
// the first PR never removes a dependency tend.yml mentions.
func (c *Config) Keeps(d core.Dep) bool {
	for _, ig := range c.Ignore {
		if (ig.Ecosystem == "" || ig.Ecosystem == d.Ecosystem) && Glob(ig.Name, d.Name) {
			return true
		}
	}
	return false
}

func inRanges(ranges []string, v string) bool {
	if len(ranges) == 0 {
		return true
	}
	for _, s := range ranges {
		r, err := parseRange(s)
		if err == nil && r.has(v) {
			return true
		}
	}
	return false
}

// Glob matches a canonical package name against a pattern where * is any run
// of characters, slashes included, and ? is one character. "@aws-sdk/*",
// "actions/*" and "github.com/aws/*" all do what they look like.
func Glob(pattern, name string) bool {
	if pattern == "" {
		return name == ""
	}
	switch pattern[0] {
	case '*':
		for i := 0; i <= len(name); i++ {
			if Glob(pattern[1:], name[i:]) {
				return true
			}
		}
		return false
	case '?':
		return name != "" && Glob(pattern[1:], name[1:])
	}
	return name != "" && pattern[0] == name[0] && Glob(pattern[1:], name[1:])
}

type cmp struct {
	op string
	v  ver.V
}

// rng is one comma-joined list of comparators; all must hold.
type rng []cmp

func parseRange(s string) (rng, error) {
	var res rng
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		op := ""
		for _, o := range []string{">=", "<=", "==", ">", "<", "^", "~"} {
			if rest, ok := strings.CutPrefix(part, o); ok {
				op, part = o, strings.TrimSpace(rest)
				break
			}
		}
		v, ok := ver.Parse(part)
		if !ok {
			return nil, fmt.Errorf("bad version range %q", s)
		}
		switch op {
		case "^":
			res = append(res, cmp{">=", v}, cmp{"<", caret(v)})
		case "~":
			res = append(res, cmp{">=", v}, cmp{"<", tilde(v)})
		case "":
			res = append(res, cmp{"==", v})
		default:
			res = append(res, cmp{op, v})
		}
	}
	return res, nil
}

func (r rng) has(s string) bool {
	v, ok := ver.Parse(s)
	if !ok {
		return false
	}
	for _, c := range r {
		n := ver.Compare(v, c.v)
		hold := false
		switch c.op {
		case ">=":
			hold = n >= 0
		case ">":
			hold = n > 0
		case "<=":
			hold = n <= 0
		case "<":
			hold = n < 0
		case "==":
			hold = n == 0
		}
		if !hold {
			return false
		}
	}
	return true
}

// caret is the exclusive upper bound of ^v: the first non-zero part goes up
// by one, or the last written part when every part is zero.
func caret(v ver.V) ver.V {
	i := len(v.Nums) - 1
	for j, n := range v.Nums {
		if n != 0 {
			i = j
			break
		}
	}
	return bumped(v, i)
}

// tilde is the exclusive upper bound of ~v: the minor goes up, or the major
// when only the major is written.
func tilde(v ver.V) ver.V {
	if len(v.Nums) == 1 {
		return bumped(v, 0)
	}
	return bumped(v, 1)
}

func bumped(v ver.V, i int) ver.V {
	nums := make([]int, i+1)
	copy(nums, v.Nums)
	nums[i]++
	return ver.V{Nums: nums}
}
