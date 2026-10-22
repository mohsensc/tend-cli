package vet

import (
	"fmt"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
)

// Verdict is reject when symbols, fields or scope found something left
// unresolved and the patched proof fails; draft when symbols, fields,
// leftovers or lockfile left a flag, or the baseline already fails; else pass.
// Call it again after re-running the proof on a worktree that vetting changed.
func Verdict(v *core.Vetting, proof *core.Proof) string {
	hard, soft := false, false
	for _, f := range v.Findings {
		if f.Action != "flagged" {
			continue
		}
		switch f.Check {
		case "symbols", "fields", "scope":
			hard = true
		}
		switch f.Check {
		case "symbols", "fields", "leftovers", "lockfile":
			soft = true
		}
	}
	failing := func(r *core.Run) bool { return r != nil && !r.Pass }
	switch {
	case hard && proof != nil && failing(proof.Patched):
		return "reject"
	case soft || proof != nil && failing(proof.Base):
		return "draft"
	}
	return "pass"
}

var clean = map[string]string{
	"symbols":   "every changed call resolves in the new version",
	"fields":    "every field the patch reads is in the spec",
	"leftovers": "no references to the old names remain",
	"lockfile":  "only the bumped packages and their dependencies changed",
	"debug":     "nothing added",
	"comments":  "nothing added",
	"dead":      "nothing left unreferenced",
	"scope":     "every changed file is explained by this change",
	"question":  "nothing to note",
}

// Section renders the list under the PR body's Vetting heading: one line per
// check saying what it verified or found, findings nested below, and the
// checks that had nothing to look at named together at the end.
func Section(v *core.Vetting) string {
	if v == nil {
		return ""
	}
	var b strings.Builder
	var idle []string
	for _, c := range v.Checks {
		name, rest, _ := strings.Cut(c, ": ")
		if rest == na {
			idle = append(idle, name)
			continue
		}
		if strings.HasPrefix(rest, "not run: ") {
			fmt.Fprintf(&b, "- %s: %s\n", name, rest)
			continue
		}
		var found []core.Finding
		for _, f := range v.Findings {
			if f.Check == name {
				found = append(found, f)
			}
		}
		var parts []string
		if rest != "" {
			parts = append(parts, rest)
		}
		if len(found) > 0 {
			parts = append(parts, tally(name, found))
		}
		if len(parts) == 0 {
			parts = append(parts, clean[name])
		}
		fmt.Fprintf(&b, "- %s: %s\n", name, strings.Join(parts, "; "))
		for _, f := range found {
			fmt.Fprintf(&b, "  - %s\n", render(f))
		}
	}
	if len(idle) > 0 {
		fmt.Fprintf(&b, "- %s: %s\n", na, strings.Join(idle, ", "))
	}
	return b.String()
}

func render(f core.Finding) string {
	res := f.Msg + " (" + f.Action + ")"
	switch {
	case f.File != "" && f.Line > 0:
		return fmt.Sprintf("`%s:%d` %s", f.File, f.Line, res)
	case f.File != "":
		return fmt.Sprintf("`%s` %s", f.File, res)
	}
	return res
}

// nouns are what a check's findings count: lines for the checks that drop
// lines, files for scope, which drops a file by reverting it.
var (
	nouns = map[string]string{"debug": "line", "comments": "line", "scope": "file"}
	verbs = map[string]string{"scope dropped": "reverted"}
)

func tally(check string, found []core.Finding) string {
	var parts []string
	for _, action := range []string{"flagged", "fixed", "dropped"} {
		n := 0
		for _, f := range found {
			if f.Action == action {
				n++
			}
		}
		if n == 0 {
			continue
		}
		s := fmt.Sprint(n)
		if noun := nouns[check]; noun != "" {
			s = count(n, noun)
		}
		if v := verbs[check+" "+action]; v != "" {
			action = v
		}
		parts = append(parts, s+" "+action)
	}
	return strings.Join(parts, ", ")
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
