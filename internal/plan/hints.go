package plan

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
)

// A deprecation notice often says what replaced what: "charges.* was replaced
// by paymentIntents.*", "use createClient instead of connect". Each pattern
// captures the old name and the new one, in that order.
var hintPatterns = []struct {
	re       *regexp.Regexp
	old, now int
}{
	{regexp.MustCompile("`?([A-Za-z_$][\\w$.]*?)(\\.\\*)?`?\\s+(?:was|were|is|are|has been|have been)\\s+(?:replaced|renamed|superseded)\\s+(?:by|with|to)\\s+`?([A-Za-z_$][\\w$.]*?)(?:\\.\\*)?`?(?:[\\s,;:)]|$)"), 1, 3},
	{regexp.MustCompile("\\buse\\s+`?([A-Za-z_$][\\w$.]*?)(?:\\.\\*|\\(\\))?`?\\s+instead\\s+of\\s+`?([A-Za-z_$][\\w$.]*?)(?:\\.\\*|\\(\\))?`?(?:[\\s,;:.)]|$)"), 2, 1},
	{regexp.MustCompile("`([A-Za-z_$][\\w$.]*?)(?:\\.\\*)?`\\s*(?:->|→)\\s*`([A-Za-z_$][\\w$.]*?)(?:\\.\\*)?`"), 1, 2},
}

// hint finds the rename a deprecation notice describes, as dotted member paths.
func hint(msg string) (old, now string, ok bool) {
	for _, p := range hintPatterns {
		m := p.re.FindStringSubmatch(msg)
		if m == nil {
			continue
		}
		old, now = strings.TrimSuffix(m[p.old], "."), strings.TrimSuffix(m[p.now], ".")
		if old != "" && now != "" && old != now && !versionish(old) && !versionish(now) {
			return old, now, true
		}
	}
	return "", "", false
}

var versionLike = regexp.MustCompile(`^v?\d+(\.(\d+|x|\*))*$`)

// versionish catches "1.x" and "v2" so "use 2.x instead of 1.x" is not a rename.
func versionish(s string) bool { return versionLike.MatchString(s) }

// renames turns a hinted rename into the codemod rules for the call sites
// that use the old name: every chain that runs through it, rewritten in place.
func renames(old, now string, sites []core.Site) map[string]string {
	want := strings.Split(old, ".")
	res := map[string]string{}
	for _, s := range sites {
		if len(s.Chain) < 2 {
			continue
		}
		tail := s.Chain[1:]
		for i := 0; i+len(want) <= len(tail); i++ {
			if !slices.Equal(tail[i:i+len(want)], want) {
				continue
			}
			from := strings.Join(tail[i:], ".")
			res[from] = strings.Join(append(strings.Split(now, "."), tail[i+len(want):]...), ".")
			break
		}
	}
	return res
}

// deprecationOptions lays out the ways off a deprecated package or version:
// the rename its notice hints at, when the code uses the old name, and
// staying put. Not one of them is mechanical; the model or the maintainer
// picks.
func deprecationOptions(ch *core.Change) {
	if ch.Kind != core.Deprecation || ch.API != nil || ch.ToName != "" || len(ch.Deps) == 0 {
		return
	}
	notice := ""
	for _, ev := range ch.Evidence {
		if ev.Source == "registry" || ev.Source == "archived" {
			notice = ev.Text
			break
		}
	}
	if notice == "" {
		return
	}
	d := ch.Deps[0]
	if old, now, ok := hint(notice); ok && ch.To != "" {
		if rules := renames(old, now, ch.Sites); len(rules) > 0 {
			ch.Options = append(ch.Options, core.Option{
				Title:   fmt.Sprintf("Apply the rename hinted by the deprecation notice (%s.* -> %s.*)", old, now),
				Detail:  fmt.Sprintf("Move to %s %s and rename the calls the notice names. The notice is prose, not a migration guide, so the proof decides whether the rename is all it takes.", d.Name, ch.To),
				To:      ch.To,
				Codemod: &core.Codemod{Renames: rules},
			})
		}
	}
	// An Action deprecated for its runtime stops running when the runners drop
	// that runtime, so staying on it is no option.
	if d.Ecosystem == core.Actions {
		return
	}
	ch.Options = append(ch.Options, stay(d, ch.To))
}

// stay is the option to keep d where it is and have tend stop asking: about
// to and later, or, for a package deprecated as a whole, about anything.
func stay(d core.Dep, to string) core.Option {
	at := Current(d)
	if to == "" {
		return core.Option{
			Title: "Pin to " + at + " and ignore",
			Detail: fmt.Sprintf("Stay on %s %s and have tend stop proposing anything for it, security fixes included, until the entry is removed.\n\nThis goes into tend.yml:\n\n"+
				"```yaml\nignore:\n  - name: %q\n    reason: staying on %s\n```", d.Name, at, d.Name, at),
			To:   at,
			Stay: true,
		}
	}
	return core.Option{
		Title: "Pin to " + at + " and ignore",
		Detail: fmt.Sprintf("Stay on %s and have tend stop proposing %s.\n\nThis goes into tend.yml:\n\n"+
			"```yaml\nignore:\n  - name: %q\n    versions: [\">=%s\"]\n    reason: staying on %s\n```", at, to, d.Name, to, at),
		To:   at,
		Stay: true,
	}
}
