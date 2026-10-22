package source

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/ver"
)

// advisory is one OSV record as it bears on a dep, before it becomes a Change.
type advisory struct {
	severity  string
	fixed     string // plain version, "" when no fix exists
	published time.Time
	ev        core.Evidence
}

var severityRank = map[string]int{"": 0, "low": 1, "moderate": 2, "high": 3, "critical": 4}

// securityChanges folds the advisories that hit dep d into one change per fixed version.
func securityChanges(d core.Dep, advs []advisory) []core.Change {
	byTo := map[string]*core.Change{}
	var tos []string
	for _, a := range advs {
		to := asWritten(d.Version, a.fixed)
		c := byTo[to]
		if c == nil {
			c = &core.Change{Kind: core.Security, Deps: []core.Dep{d}, To: to, Level: ver.Step(d.Version, to)}
			byTo[to] = c
			tos = append(tos, to)
		}
		if severityRank[a.severity] > severityRank[c.Severity] {
			c.Severity = a.severity
		}
		if c.Published.IsZero() || !a.published.IsZero() && a.published.Before(c.Published) {
			c.Published = a.published
		}
		c.Evidence = append(c.Evidence, a.ev)
	}
	slices.SortFunc(tos, func(a, b string) int {
		switch {
		case a == b:
			return 0
		case a == "":
			return 1
		case b == "":
			return -1
		case ver.Less(a, b):
			return -1
		}
		return 1
	})
	res := make([]core.Change, 0, len(tos))
	for _, to := range tos {
		c := byTo[to]
		c.ID = core.ChangeID(c)
		res = append(res, *c)
	}
	return res
}

// smallestFixed is the lowest fix above cur, "" when none of fixes is.
func smallestFixed(cur string, fixes []string) string {
	c, ok := ver.Parse(cur)
	if !ok {
		return ""
	}
	res := ""
	var best ver.V
	for _, f := range fixes {
		v, ok := ver.Parse(f)
		if !ok || ver.Compare(v, c) <= 0 {
			continue
		}
		if res == "" || ver.Compare(v, best) < 0 {
			res, best = f, v
		}
	}
	return res
}

// normSeverity maps the spellings databases use onto low|moderate|high|critical.
func normSeverity(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "medium" {
		return "moderate"
	}
	if _, ok := severityRank[s]; ok {
		return s
	}
	return ""
}

func band(score float64) string {
	switch {
	case score >= 9:
		return "critical"
	case score >= 7:
		return "high"
	case score >= 4:
		return "moderate"
	case score > 0:
		return "low"
	}
	return ""
}

var cvssWeights = map[string]map[string]float64{
	"AV": {"N": 0.85, "A": 0.62, "L": 0.55, "P": 0.2},
	"AC": {"L": 0.77, "H": 0.44},
	"UI": {"N": 0.85, "R": 0.62},
	"C":  {"H": 0.56, "L": 0.22, "N": 0},
	"I":  {"H": 0.56, "L": 0.22, "N": 0},
	"A":  {"H": 0.56, "L": 0.22, "N": 0},
}

// cvssBand bands a bare score, or the score of a CVSS v3 vector. v4 vectors
// carry no score and need a lookup table; they band as "".
func cvssBand(score string) string {
	if n, err := strconv.ParseFloat(score, 64); err == nil {
		return band(n)
	}
	n, ok := cvss3(score)
	if !ok {
		return ""
	}
	return band(n)
}

// cvss3 computes the base score of a CVSS v3.x vector.
func cvss3(vec string) (float64, bool) {
	if !strings.HasPrefix(vec, "CVSS:3") {
		return 0, false
	}
	m := map[string]string{}
	for _, part := range strings.Split(vec, "/")[1:] {
		k, v, ok := strings.Cut(part, ":")
		if !ok {
			return 0, false
		}
		m[k] = v
	}
	w := map[string]float64{}
	for k, tab := range cvssWeights {
		x, ok := tab[m[k]]
		if !ok {
			return 0, false
		}
		w[k] = x
	}
	if m["S"] != "C" && m["S"] != "U" {
		return 0, false
	}
	changed := m["S"] == "C"
	pr, ok := map[string][2]float64{"N": {0.85, 0.85}, "L": {0.62, 0.68}, "H": {0.27, 0.5}}[m["PR"]]
	if !ok {
		return 0, false
	}
	w["PR"] = pr[0]
	if changed {
		w["PR"] = pr[1]
	}
	iss := 1 - (1-w["C"])*(1-w["I"])*(1-w["A"])
	impact := 6.42 * iss
	if changed {
		impact = 7.52*(iss-0.029) - 3.25*math.Pow(iss-0.02, 15)
	}
	if impact <= 0 {
		return 0, true
	}
	sum := impact + 8.22*w["AV"]*w["AC"]*w["PR"]*w["UI"]
	if changed {
		sum *= 1.08
	}
	return roundUp(math.Min(sum, 10)), true
}

// roundUp is the CVSS 3.1 round-up to one decimal, immune to float noise.
func roundUp(x float64) float64 {
	n := int(math.Round(x * 100000))
	if n%10000 == 0 {
		return float64(n) / 100000
	}
	return float64(n/10000+1) / 10
}
