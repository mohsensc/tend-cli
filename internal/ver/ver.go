// Package ver parses and orders the loose version strings registries publish.
package ver

import (
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

type V struct {
	Nums []int  // numeric parts
	Pre  string // pre-release tag, "" if none ("rc1", "beta.2", "a1"); also PEP 440 dev/pre
	Raw  string

	post int // PEP 440 post-release number plus one; 0 when there is none
}

var preWords = map[string]int{
	"dev":          0,
	"snapshot":     0,
	"nightly":      0,
	"canary":       0,
	"experimental": 0,
	"insiders":     0,
	"next":         0,
	"a":            1,
	"alpha":        1,
	"ea":           1,
	"m":            1,
	"milestone":    1,
	"b":            2,
	"beta":         2,
	"c":            3,
	"cr":           3,
	"pre":          3,
	"preview":      3,
	"rc":           3,
}

var postWords = map[string]bool{"post": true, "rev": true}

// Parse accepts "v1.2.3", "1.2", "1.2.3-rc.1", "1.0a1", "1.0.post1", "2024.1.2" and
// docker-style "18-alpine". A suffix that is not a known pre-release word, such as
// "-alpine" or "-slim", is a variant and does not take part in ordering. A PEP 440
// epoch ("1!2.0") outranks every version without one, so a version with one does
// not parse.
func Parse(s string) (V, bool) {
	v := V{Raw: s}
	s = strings.TrimSpace(s)
	if s != "" && (s[0] == 'v' || s[0] == 'V') {
		s = s[1:]
	}
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	if strings.Contains(s, "!") {
		return V{Raw: v.Raw}, false
	}
	for {
		n := digits(s)
		if n == 0 {
			break
		}
		x, err := strconv.Atoi(s[:n])
		if err != nil {
			return V{Raw: v.Raw}, false
		}
		v.Nums = append(v.Nums, x)
		s = s[n:]
		if len(s) < 2 || s[0] != '.' || digits(s[1:]) == 0 {
			break
		}
		s = s[1:]
	}
	if len(v.Nums) == 0 {
		return V{Raw: v.Raw}, false
	}
	rest := strings.ToLower(strings.TrimLeft(s, ".-_"))
	if rest == "" {
		return v, true
	}
	word := letters(rest)
	switch {
	case postWords[word]:
		num := strings.TrimLeft(rest[len(word):], ".-_")
		n, _ := strconv.Atoi(num[:digits(num)])
		v.post = n + 1
	case word == "" && rest[0] >= '0' && rest[0] <= '9':
		v.Pre = rest
	case hasWord(word):
		v.Pre = rest
	}
	return v, true
}

func hasWord(w string) bool {
	_, ok := preWords[w]
	return ok
}

func digits(s string) int {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return n
}

func letters(s string) string {
	n := 0
	for n < len(s) && s[n] >= 'a' && s[n] <= 'z' {
		n++
	}
	return s[:n]
}

// Compare orders numerically, treating missing parts as 0. A pre-release sorts
// below its release and a post-release above it.
func Compare(a, b V) int {
	for i := 0; i < max(len(a.Nums), len(b.Nums)); i++ {
		if c := cmpInt(part(a.Nums, i), part(b.Nums, i)); c != 0 {
			return c
		}
	}
	switch {
	case a.Pre == "" && b.Pre != "":
		return 1
	case a.Pre != "" && b.Pre == "":
		return -1
	}
	if c := comparePre(a.Pre, b.Pre); c != 0 {
		return c
	}
	return cmpInt(a.post, b.post)
}

func part(nums []int, i int) int {
	if i < len(nums) {
		return nums[i]
	}
	return 0
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// comparePre follows semver: identifiers left to right, numbers below words,
// numbers numerically, a shorter list first. Known words rank dev < alpha < beta < rc.
func comparePre(a, b string) int {
	xs, ys := idents(a), idents(b)
	for i := 0; i < len(xs) && i < len(ys); i++ {
		x, y := xs[i], ys[i]
		nx, xNum := atoi(x)
		ny, yNum := atoi(y)
		switch {
		case xNum && yNum:
			if c := cmpInt(nx, ny); c != 0 {
				return c
			}
		case xNum:
			return -1
		case yNum:
			return 1
		default:
			if c := compareWord(x, y); c != 0 {
				return c
			}
		}
	}
	return cmpInt(len(xs), len(ys))
}

func compareWord(x, y string) int {
	rx, okx := preWords[x]
	ry, oky := preWords[y]
	if okx && oky {
		return cmpInt(rx, ry)
	}
	return strings.Compare(x, y)
}

// idents splits "rc.1", "rc1" and "beta-2" alike into ["rc" "1"].
func idents(s string) []string {
	var res []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i < len(s) && !isSep(s[i]) && (i == start || isDigit(s[i]) == isDigit(s[i-1])) {
			continue
		}
		if i > start {
			res = append(res, s[start:i])
		}
		start = i
		if i < len(s) && isSep(s[i]) {
			start = i + 1
		}
	}
	return res
}

func isSep(c byte) bool   { return c == '.' || c == '-' || c == '_' }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func atoi(s string) (int, bool) {
	if digits(s) != len(s) {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// Less reports whether a sorts before b. Unparsable versions sort first, among
// themselves by string.
func Less(a, b string) bool {
	va, okA := Parse(a)
	vb, okB := Parse(b)
	switch {
	case !okA && !okB:
		return a < b
	case !okA:
		return true
	case !okB:
		return false
	}
	return Compare(va, vb) < 0
}

// Level names the size of the step from one version to another: major,
// minor, patch or same. A step off v's compatibility line is major, as a
// caret range reads it: 0.1 to 0.2 is, and so is 0.0.1 to 0.0.2.
func Level(from, to V) string {
	switch {
	case Compare(from, to) == 0:
		return "same"
	case from.Line() != to.Line():
		return "major"
	case part(from.Nums, 1) != part(to.Nums, 1):
		return "minor"
	}
	return "patch"
}

// Step is Level for version strings: "" when either does not parse or they are the same.
func Step(from, to string) string {
	a, okA := Parse(from)
	b, okB := Parse(to)
	if !okA || !okB {
		return ""
	}
	if l := Level(a, b); l != "same" {
		return l
	}
	return ""
}

// StepIn is Step as ecosystem eco reads a version: a Go module keeps its
// import path across v0.x minors, which go get takes as routine upgrades,
// so v0.13 to v0.49 is minor there; everywhere else a caret range rules.
func StepIn(eco, from, to string) string {
	if eco != "go" {
		return Step(from, to)
	}
	a, okA := Parse(from)
	b, okB := Parse(to)
	switch {
	case !okA || !okB || Compare(a, b) == 0:
		return ""
	case a.Major() != b.Major():
		return "major"
	case part(a.Nums, 1) != part(b.Nums, 1):
		return "minor"
	}
	return "patch"
}

func (v V) Major() int { return part(v.Nums, 0) }

// Line is the compatibility line v is on, what a caret range holds fixed:
// the major from 1 on, "0.3" for a 0.3.x, "0.0.4" for 0.0.4.
func (v V) Line() string {
	switch {
	case part(v.Nums, 0) > 0:
		return strconv.Itoa(part(v.Nums, 0))
	case part(v.Nums, 1) > 0:
		return "0." + strconv.Itoa(part(v.Nums, 1))
	}
	return "0.0." + strconv.Itoa(part(v.Nums, 2))
}

// GoLess orders Go module versions, which are strict semver with a v prefix.
func GoLess(a, b string) bool { return semver.Compare(a, b) < 0 }
