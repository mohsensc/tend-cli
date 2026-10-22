package deliver

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/plan"
)

// TidyBranch is where tend's first PR on a repo lives, whatever it tidies.
const TidyBranch = branchPrefix + core.TidyID

// tidyTitle says what the tidy-up does: "Tidy up: remove 4 unused
// dependencies, move off 1 deprecated library", or that it only sets tend up.
func tidyTitle(c *core.Cleanup) string {
	var did []string
	if n := len(c.Unused); n > 0 {
		did = append(did, "remove "+count(n, "unused dependency", "unused dependencies"))
	}
	if n := len(c.Moves); n > 0 {
		did = append(did, "move off "+count(n, "deprecated library", "deprecated libraries"))
	}
	switch {
	case len(did) > 0:
		return "Tidy up: " + strings.Join(did, ", ")
	case len(c.Kept)+len(c.Left) == 0:
		return "Set up tend: nothing to tidy up"
	}
	return "Set up tend"
}

func count(n int, one, many string) string { return strconv.Itoa(n) + " " + pick(n, one, many) }

func pick(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// welcome opens tend's first PR: what tend is, what this PR does, what it
// does from now on, and how to steer it.
func welcome(c *core.Cleanup) string {
	var did []string
	if n := len(c.Unused); n > 0 {
		did = append(did, "removes "+count(n, "dependency", "dependencies")+" nothing in the project uses")
	}
	if n := len(c.Moves); n > 0 {
		did = append(did, "moves the code off "+count(n, "deprecated library", "deprecated libraries"))
	}
	s := []string{"Thanks for adding tend: it looks after the packages this repository depends on and the APIs its code calls."}
	found := len(c.Kept)+len(c.Left) > 0
	switch {
	case len(did) > 0:
		s = append(s, "This first pull request tidies up: it "+strings.Join(did, " and ")+".")
	case found:
		s = append(s, "Its first look found nothing it could remove or move for certain, so this pull request only adds a `tend.yml` with every key commented out.")
	default:
		s = append(s, "Its first look found nothing to tidy up, so this pull request only adds a `tend.yml` with every key commented out.")
	}
	if found {
		s = append(s, "What it found and left alone is listed below, for you to decide.")
	}
	s = append(s,
		"From now on it opens one pull request at a time, only for changes that touch your code, and each one shows the build and test runs that prove it.",
		"Steer it with `tend.yml`, or comment `tend skip`, `tend retry` or `tend apply N` on its pull requests.")
	return strings.Join(s, " ")
}

// cleaned is a tidy-up's What changed section: what it removed and moved,
// then what it names and leaves alone, each with why.
func cleaned(in BodyInput) string {
	c, p := in.Change.Cleanup, in.Patch
	var parts []string
	if len(c.Unused) > 0 {
		lines := []string{"Removed, since nothing in the project uses " + pick(len(c.Unused), "it:", "them:"), ""}
		for _, d := range c.Unused {
			lines = append(lines, "- "+declared(d))
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}
	if len(c.Moves) > 0 {
		lines := []string{"Moved off " + pick(len(c.Moves), "a deprecated library:", "deprecated libraries:"), ""}
		repl := map[string]map[int][]string{}
		if p != nil {
			repl = replacements(p.Diff)
		}
		for _, m := range c.Moves {
			lines = append(lines, "- "+moved(&m))
			for _, l := range siteLines(m.Sites, repl) {
				lines = append(lines, "  "+l)
			}
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}
	if len(c.Kept) > 0 {
		lines := []string{"Possibly unused, kept: these look unused, but tend cannot prove it, so they are yours to decide.", ""}
		for _, a := range c.Kept {
			lines = append(lines, "- "+declared(a.Dep)+": "+clause(a.Why))
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}
	if len(c.Left) > 0 {
		parts = append(parts, left(c.Left))
	}
	switch {
	case p == nil || len(p.Files) == 0:
	case len(c.Unused)+len(c.Moves) == 0 && slices.Equal(p.Files, []string{"tend.yml"}):
		parts = append(parts, "Adds `tend.yml` with every key commented out, so it changes nothing until you uncomment a line.")
	default:
		var files []string
		for _, f := range p.Files {
			files = append(files, code(f))
		}
		parts = append(parts, "Files: "+strings.Join(files, ", ")+".")
	}
	if p != nil && p.Model {
		s := "Part of this change was written by a model. The vetting section below covers it."
		if p.Note != "" {
			s = "Part of this change was written by a model, in " + p.Note + ". The vetting section below covers it."
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n\n")
}

// declared names a dependency and where the project declares it.
func declared(d core.Dep) string {
	s := code(d.Name)
	if v := plan.Current(d); v != "" {
		s += " " + v
	}
	if d.Manifest.Path != "" {
		s += " in " + code(d.Manifest.Path)
	}
	return s
}

// moved names a library move and the deprecation behind it.
func moved(m *core.Change) string {
	d := m.Deps[0]
	s := code(d.Name) + " " + plan.Current(d) + " → "
	if m.ToName != "" {
		s += code(m.ToName) + " "
	}
	s += m.To
	for _, ev := range m.Evidence {
		if (ev.Source == "registry" || ev.Source == "archived") && ev.Text != "" {
			return s + ": " + oneLine(ev.Text, 160)
		}
	}
	return s
}

// left lists the deprecated libraries the tidy-up does not move, with why,
// and the ways off each, which its own PR offers next.
func left(as []core.Aside) string {
	lines := []string{"Deprecated, not moved: each gets a pull request of its own after this one.", ""}
	for _, a := range as {
		line := "- " + declared(a.Dep) + ": " + clause(a.Why) + "."
		if len(a.Options) > 0 {
			line += " `tend apply N` on its pull request picks one of:"
		}
		lines = append(lines, line)
		for i, o := range a.Options {
			lines = append(lines, fmt.Sprintf("  %d. %s", i+1, o.Title))
		}
	}
	return strings.Join(lines, "\n")
}

// clause makes a sentence the second half of one: "No model is configured."
// reads "no model is configured" after a colon.
func clause(s string) string {
	s = strings.TrimSuffix(strings.TrimSpace(s), ".")
	if len(s) > 1 && s[0] >= 'A' && s[0] <= 'Z' && (s[1] < 'A' || s[1] > 'Z') {
		s = string(s[0]+'a'-'A') + s[1:]
	}
	return s
}

// Displaced reports whether pr was closed by tend to make room, for a
// security fix or to keep one PR open, rather than declined: it comes back.
// It reads a close the way Declined does, so a word someone leaves on the
// PR after tend closed it does not turn the close into theirs.
func Displaced(ctx context.Context, forge core.Forge, pr *core.PR) (bool, error) {
	if pr == nil || pr.State != "closed" {
		return false, nil
	}
	no, err := Declined(ctx, forge, pr)
	return !no, err
}
