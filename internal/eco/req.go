package eco

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/mohsensc/tend-cli/internal/ver"
)

type term struct{ op, gap, num string }

var termRe = regexp.MustCompile(`(\^|~>|~|>=|<=|!=|==|>|<|=)?([ \t]*)(v?\d[0-9A-Za-z]*(?:\.[0-9A-Za-z]+)*(?:\.[*xX])?(?:-[0-9A-Za-z.]+)?|[*xX])`)

type termAt struct {
	start, end int
	t          term
}

func findTerms(s string) ([]termAt, bool) {
	var res []termAt
	prev := 0
	sub := func(m []int, i int) string {
		if m[2*i] < 0 {
			return ""
		}
		return s[m[2*i]:m[2*i+1]]
	}
	for _, m := range termRe.FindAllStringSubmatchIndex(s, -1) {
		if strings.Trim(s[prev:m[0]], " \t,") != "" {
			return nil, false
		}
		prev = m[1]
		res = append(res, termAt{m[0], m[1], term{sub(m, 1), sub(m, 2), sub(m, 3)}})
	}
	return res, len(res) > 0 && strings.Trim(s[prev:], " \t,") == ""
}

func (t term) wild() (int, bool) {
	switch {
	case t.num == "*" || t.num == "x" || t.num == "X":
		return 0, true
	case strings.HasSuffix(t.num, ".*") || strings.HasSuffix(t.num, ".x") || strings.HasSuffix(t.num, ".X"):
		return strings.Count(t.num, "."), true
	}
	return 0, false
}

func prefixOf(have, want []int) bool {
	for i, w := range want {
		h := 0
		if i < len(have) {
			h = have[i]
		}
		if h != w {
			return false
		}
	}
	return true
}

func bumpAt(nums []int, i int) ver.V {
	res := append(slices.Clone(nums[:i]), nums[i]+1)
	return ver.V{Nums: res}
}

// holds reports whether v satisfies the term. cargo selects the one dialect
// that differs: a bare version is a caret range, ~1.2 pins the minor, =1.2 is a prefix.
func (t term) holds(v ver.V, cargo bool) bool {
	if n, wild := t.wild(); wild {
		base, _ := ver.Parse(t.num[:max(len(t.num)-2, 0)])
		return n == 0 || prefixOf(v.Nums, base.Nums)
	}
	pv, ok := ver.Parse(t.num)
	if !ok || (v.Pre != "" && pv.Pre == "") {
		return false
	}
	c := ver.Compare(v, pv)
	nums := pv.Nums
	op := t.op
	if op == "" && cargo {
		op = "^"
	}
	switch op {
	case ">=":
		return c >= 0
	case ">":
		return c > 0
	case "<=":
		return c <= 0
	case "<":
		return c < 0
	case "!=":
		return c != 0
	case "=", "==", "":
		if cargo && op != "" {
			return prefixOf(v.Nums, nums)
		}
		return c == 0
	case "^":
		i := slices.IndexFunc(nums, func(n int) bool { return n != 0 })
		if i < 0 {
			i = len(nums) - 1
		}
		return c >= 0 && ver.Compare(v, bumpAt(nums, i)) < 0
	}
	i := len(nums) - 2
	switch {
	case len(nums) == 1:
		i = 0
	case cargo:
		i = 1
	}
	return c >= 0 && ver.Compare(v, bumpAt(nums, i)) < 0
}

// move rewrites the term so that it admits to, keeping its operator, spacing and precision.
func (t term) move(to string, cargo bool) string {
	n, wild := t.wild()
	lead := ""
	if strings.HasPrefix(t.num, "v") {
		lead = "v"
	}
	whole := strings.TrimPrefix(to, "v")
	switch {
	case t.op == "!=" || (wild && n == 0):
		return t.op + t.gap + t.num
	case wild:
		return t.op + t.gap + lead + shape(to, n) + t.num[len(t.num)-2:]
	}
	pv, _ := ver.Parse(t.num)
	switch t.op {
	case ">=", "<=", "=", "==":
		return t.op + t.gap + lead + whole
	case ">":
		return ">=" + t.gap + lead + whole
	case "<":
		return "<" + t.gap + lead + nextMajor(to, len(pv.Nums))
	case "":
		if !cargo {
			return t.gap + lead + whole
		}
	}
	return t.op + t.gap + lead + shape(to, len(pv.Nums))
}

func shape(to string, n int) string {
	v, ok := ver.Parse(to)
	if !ok || v.Pre != "" {
		return strings.TrimPrefix(to, "v")
	}
	parts := make([]string, max(n, 1))
	for i := range parts {
		x := 0
		if i < len(v.Nums) {
			x = v.Nums[i]
		}
		parts[i] = strconv.Itoa(x)
	}
	return strings.Join(parts, ".")
}

func nextMajor(to string, n int) string {
	v, _ := ver.Parse(to)
	parts := make([]string, max(n, 1))
	for i := range parts {
		parts[i] = "0"
	}
	if len(v.Nums) > 0 {
		parts[0] = strconv.Itoa(v.Nums[0] + 1)
	}
	return strings.Join(parts, ".")
}

var altRe = regexp.MustCompile(`\|\|?`)

func alts(req string) [][2]int {
	var res [][2]int
	start := 0
	for _, m := range altRe.FindAllStringIndex(req, -1) {
		res = append(res, [2]int{start, m[0]})
		start = m[1]
	}
	return append(res, [2]int{start, len(req)})
}

func allHold(ts []termAt, v ver.V, cargo bool) bool {
	for _, t := range ts {
		if !t.t.holds(v, cargo) {
			return false
		}
	}
	return true
}

func admits(req string, v ver.V, cargo bool) bool {
	for _, a := range alts(req) {
		if ts, ok := findTerms(req[a[0]:a[1]]); ok && allHold(ts, v, cargo) {
			return true
		}
	}
	return false
}

// raises reports whether ts is a lone ">=" floor below want. Bumping such a
// requirement raises the floor even though the target is inside it.
func raises(ts []termAt, want ver.V) bool {
	if len(ts) != 1 || ts[0].t.op != ">=" {
		return false
	}
	floor, ok := ver.Parse(ts[0].t.num)
	return ok && ver.Compare(want, floor) > 0
}

// fixReq returns req changed just enough to admit to. It is req itself when it
// already does. Of several alternatives the last one is moved.
func fixReq(req, to string, cargo bool) (string, error) {
	want, ok := ver.Parse(to)
	if !ok {
		return "", fmt.Errorf("version %q does not parse", to)
	}
	var last []termAt
	var off int
	for _, a := range alts(req) {
		ts, ok := findTerms(req[a[0]:a[1]])
		if !ok {
			return "", fmt.Errorf("cannot rewrite requirement %q", req)
		}
		if allHold(ts, want, cargo) && !raises(ts, want) {
			return req, nil
		}
		last, off = ts, a[0]
	}
	res := req
	for i := len(last) - 1; i >= 0; i-- {
		t := last[i]
		if !t.t.holds(want, cargo) || raises(last, want) {
			res = res[:off+t.start] + t.t.move(to, cargo) + res[off+t.end:]
		}
	}
	return res, nil
}
