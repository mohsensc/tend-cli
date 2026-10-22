package deliver

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/mohsensc/tend-cli/internal/api"
	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/plan"
)

// FirstBranch is where tend's first PR on a repo lives, whatever it carries.
const FirstBranch = branchPrefix + core.FirstID

// StageBranch is where the catch-up PR of stage k lives, for the stages
// after the first PR.
func StageBranch(k int) string { return branchPrefix + core.StageID(k) }

// titleMax is how long a catch-up PR's title may grow before its counts are
// summed into one.
const titleMax = 80

// firstTitle counts what a catch-up PR does: "Catch up: 12 security fixes, 1
// deprecated library, 6 unused dependencies", or says it only sets tend up.
func firstTitle(b *core.Batch) string {
	did := tally(b, false)
	switch {
	case len(did) > 0:
		s := "Catch up: " + strings.Join(did, ", ")
		if len(s) > titleMax {
			s = "Catch up: " + strings.Join(tally(b, true), ", ")
		}
		if len(s) > titleMax {
			s = "Catch up: " + count(len(b.Changes)+len(b.Unused), "change", "changes")
		}
		return s
	case len(b.Kept)+len(b.Next) == 0:
		return "Set up tend: nothing to catch up on"
	}
	return "Set up tend"
}

// sections are the headings a catch-up PR lists its changes under, in order.
var sections = []string{"Security", "API", "Deprecated", "Runtimes and tooling", "Upgrades"}

// tally counts the changes a batch makes by what they are, in the order the
// body lists them, then the dependencies it removes and the baseline it
// adds. short counts every deprecation as one kind.
func tally(b *core.Batch, short bool) []string {
	var order []string
	n := map[string]int{}
	many := map[string]string{}
	for _, s := range sections {
		for i := range b.Changes {
			ch := &b.Changes[i]
			if group(ch) != s {
				continue
			}
			one, more := noun(ch)
			if short && s == "Deprecated" {
				one, more = "deprecation", "deprecations"
			}
			if n[one] == 0 {
				order = append(order, one)
			}
			n[one]++
			many[one] = more
		}
	}
	var res []string
	if b.Baseline != nil {
		res = append(res, "a test baseline")
	}
	for _, one := range order {
		res = append(res, count(n[one], one, many[one]))
	}
	if k := len(b.Unused); k > 0 {
		res = append(res, count(k, "unused dependency", "unused dependencies"))
	}
	return res
}

// group is the section of a catch-up PR a change it makes goes in.
func group(ch *core.Change) string {
	switch {
	case ch.API != nil:
		return "API"
	case ch.Kind == core.Security:
		return "Security"
	case plan.Tier(ch) == plan.Runtimes:
		return "Runtimes and tooling"
	case ch.Kind == core.Breaking || ch.Kind == core.Update:
		return "Upgrades"
	}
	return "Deprecated"
}

// noun is what a change in a catch-up PR is, for one and for many.
func noun(ch *core.Change) (string, string) {
	switch {
	case ch.API != nil:
		return "API change", "API changes"
	case ch.Kind == core.Security:
		return "security fix", "security fixes"
	case len(ch.Deps) == 0:
		return "deprecation", "deprecations"
	case ch.Deps[0].Min:
		return "runtime minimum", "runtime minimums"
	}
	switch ch.Deps[0].Ecosystem {
	case core.Toolchain, core.Docker:
		return "end-of-life runtime", "end-of-life runtimes"
	case core.Actions:
		return "deprecated Action", "deprecated Actions"
	}
	switch {
	case group(ch) == "Runtimes and tooling":
		return "build tool", "build tools"
	case group(ch) == "Upgrades" && ch.Level == "major":
		return "major upgrade", "major upgrades"
	case group(ch) == "Upgrades":
		return "upgrade", "upgrades"
	}
	if name, _ := plan.Target(ch); name != ch.Deps[0].Name {
		return "replaced library", "replaced libraries"
	}
	return "deprecated library", "deprecated libraries"
}

func count(n int, one, many string) string { return strconv.Itoa(n) + " " + pick(n, one, many) }

func pick(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// ahead counts the stages a batch leaves for later catch-up PRs.
func ahead(b *core.Batch) int {
	var ks []int
	for _, l := range b.Next {
		if l.Stage > b.Stage && !slices.Contains(ks, l.Stage) {
			ks = append(ks, l.Stage)
		}
	}
	return len(ks)
}

// welcome opens tend's first PR: what tend is, what this PR does, what
// comes after it, and how to steer it.
func welcome(b *core.Batch) string {
	did := tally(b, false)
	s := []string{"Thanks for adding tend: it looks after the packages this repository depends on and the APIs its code calls."}
	more := ahead(b)
	found := len(b.Kept)+len(b.Next) > 0
	switch {
	case len(did) > 0 && more > 0:
		s = append(s, "This first pull request starts catching the repository up: "+and(did)+".",
			"The rest comes in "+count(more, "more pull request", "more pull requests")+", one stage at a time, listed under Next stages and each proven the same way.")
	case len(did) > 0:
		s = append(s, "This first pull request catches up in one go: "+and(did)+".")
	case more > 0:
		s = append(s, "Its first look found nothing to fix right away, so this pull request only adds a `tend.yml` with every key commented out.",
			"The catch-up comes in "+count(more, "more pull request", "more pull requests")+", one stage at a time, listed under Next stages.")
	case found:
		s = append(s, "Its first look found nothing it could fix or remove for certain, so this pull request only adds a `tend.yml` with every key commented out.")
	default:
		s = append(s, "Its first look found nothing to catch up on, so this pull request only adds a `tend.yml` with every key commented out.")
	}
	if found && more == 0 {
		s = append(s, "What it found and left alone is listed below, for you to decide or for a pull request of its own.")
	}
	then := "From now on it opens one pull request at a time"
	if more > 0 {
		then = "Once the catch-up is done it opens one pull request at a time"
	}
	s = append(s,
		then+", only for changes that touch your code, and each one shows the build and test runs that prove it.",
		"Steer it with `tend.yml`, or comment `tend skip`, `tend retry` or `tend apply N` on its pull requests.")
	return strings.Join(s, " ")
}

// intro opens a catch-up PR after the first: which stage it is, what it
// does and what is left.
func intro(b *core.Batch) string {
	s := "This pull request carries on tend's catch-up with " + plan.StageName(b.Stage)
	if did := tally(b, false); len(did) > 0 {
		s += ": " + and(did)
	}
	s += "."
	if more := ahead(b); more > 0 {
		return s + " " + count(more, "more stage follows", "more stages follow") + ", listed under Next stages."
	}
	return s + " It is the last stage; after it, tend opens one pull request at a time, only for changes that touch your code."
}

// and joins a list the way a sentence does: "a, b and c".
func and(s []string) string {
	if len(s) < 2 {
		return strings.Join(s, "")
	}
	return strings.Join(s[:len(s)-1], ", ") + " and " + s[len(s)-1]
}

// batched is a catch-up PR's own sections, each left out when empty: the
// baseline, Security, one per API provider, Deprecated, Runtimes and
// tooling, Upgrades, Unused, Next stages and Not done, after a word on the
// parts a model wrote.
func batched(in BodyInput) string {
	b, p := in.Change.Batch, in.Patch
	repl := map[string]map[int][]string{}
	if p != nil {
		repl = replacements(p.Diff)
	}
	var parts []string
	switch {
	case p == nil:
	case p.Model && p.Note != "":
		parts = append(parts, "Part of this pull request was written by a model, in "+p.Note+". The vetting section below covers it.")
	case p.Model:
		parts = append(parts, "Part of this pull request was written by a model. The vetting section below covers it.")
	}
	if s := baseline(in); s != "" {
		parts = append(parts, "### Baseline\n\n"+s)
	}
	if s := secured(b.Changes, p, repl); s != "" {
		parts = append(parts, "### Security\n\n"+s)
	}
	var providers []string
	for _, ch := range b.Changes {
		if a := ch.API; a != nil && !slices.Contains(providers, a.Provider) {
			providers = append(providers, a.Provider)
		}
	}
	for _, name := range providers {
		var lines []string
		for i := range b.Changes {
			ch := &b.Changes[i]
			if a := ch.API; a != nil && a.Provider == name {
				lines = append(lines, "- "+code(op(a))+": "+reason(BodyInput{Change: ch}))
				lines = append(lines, sitesMade(ch, repl)...)
			}
		}
		parts = append(parts, "### "+api.Title(name)+" API\n\n"+strings.Join(lines, "\n"))
	}
	for _, s := range sections[2:] {
		var lines []string
		for i := range b.Changes {
			ch := &b.Changes[i]
			if group(ch) == s {
				lines = append(lines, "- "+moved(ch))
				lines = append(lines, sitesMade(ch, repl)...)
			}
		}
		if len(lines) > 0 {
			parts = append(parts, "### "+s+"\n\n"+strings.Join(lines, "\n"))
		}
	}
	if s := unused(b); s != "" {
		parts = append(parts, "### Unused\n\n"+s)
	}
	if s := stages(b); s != "" {
		parts = append(parts, "### Next stages\n\n"+s)
	}
	if s := undone(b); s != "" {
		parts = append(parts, "### Not done\n\n"+s)
	}
	return strings.Join(parts, "\n\n")
}

// baseline says why the PR adds tests first and what they are.
func baseline(in BodyInput) string {
	bl := in.Change.Batch.Baseline
	if bl == nil {
		return ""
	}
	at := baseLabel(in)
	lines := []string{"This project's tests are too thin to prove the catch-up by: " + Clause(bl.Why) + ". So this pull request first adds tests that pin down how the code behaves on " + at + ", and every proof from here on runs them:", ""}
	for _, f := range bl.Files {
		switch {
		case strings.Contains(f, "smoke") && bl.Modules == 1:
			lines = append(lines, "- "+code(f)+": loads the one module that loads on the base commit, and exits")
		case strings.Contains(f, "smoke"):
			lines = append(lines, "- "+code(f)+": loads each of the "+count(bl.Modules, "module", "modules")+" that load on the base commit, and exits")
		case bl.Model:
			lines = append(lines, "- "+code(f)+": tests a model wrote of the current behavior")
		default:
			lines = append(lines, "- "+code(f))
		}
	}
	for _, c := range bl.Checks {
		lines = append(lines, "- "+code(c)+", which passes on the base commit")
	}
	if bl.Command != "" {
		lines = append(lines, "- "+code(bl.Command)+" in `verify.test` in `tend.yml`, after the project's own test lines")
	}
	return strings.Join(append(lines, "", "They pass on "+at+": the base column of the proof below is that commit with these tests added."), "\n")
}

// secured lists the security fixes, under the ecosystem of each.
func secured(chs []core.Change, p *core.Patch, repl map[string]map[int][]string) string {
	var ecos []string
	by := map[string][]string{}
	for i := range chs {
		ch := &chs[i]
		if group(ch) != "Security" {
			continue
		}
		e := ch.Deps[0].Ecosystem
		if by[e] == nil {
			ecos = append(ecos, e)
		}
		by[e] = append(by[e], "- "+fixed(ch, p))
		by[e] = append(by[e], sitesMade(ch, repl)...)
	}
	var res []string
	for _, e := range ecos {
		res = append(res, e+":\n\n"+strings.Join(by[e], "\n"))
	}
	return strings.Join(res, "\n\n")
}

// fixed names a security fix: the package, the move, the advisories, and
// the release the lockfile took when that is past the one that fixes them.
func fixed(ch *core.Change, p *core.Patch) string {
	at := landedAt(p, ch)
	s := named(ch, cmp.Or(at, ch.To))
	if !ch.Deps[0].Direct {
		s += " (transitive)"
	}
	if ids := advisories(ch); len(ids) > 0 {
		s += ": " + ids[0]
		if ch.Severity != "" {
			s += " (" + ch.Severity + ")"
		}
		if len(ids) > 1 {
			s += fmt.Sprintf(" +%d more", len(ids)-1)
		}
	}
	if at != "" {
		s += "; " + ch.To + " fixes it, and the lockfile took the newest release its ranges allow"
	}
	return s
}

// named names what a change moves and where to: "`tar` 7.5.20 → 7.5.22".
func named(ch *core.Change, to string) string {
	d := ch.Deps[0]
	s := code(depName(d))
	if n := len(core.Names(ch)); n > 1 {
		s = code(groupName(ch)) + fmt.Sprintf(" (%d packages)", n)
	}
	from := plan.Lowest(ch.Deps)
	if from != "" {
		s += " " + from
	}
	name, at := plan.Target(ch)
	if ch.Choice == nil {
		at = to
	}
	switch {
	case name != d.Name:
		s = strings.TrimSpace(s + " → " + code(name) + " " + at)
	case at != "" && at != from:
		s += " → " + at
	}
	return s
}

// moved names a move and what is behind it, with the file a pin sits in,
// the releases a ladder climbed through and the option tend took.
func moved(ch *core.Change) string {
	if len(ch.Deps) == 0 {
		return code(ch.ID)
	}
	s := named(ch, ch.To)
	switch ch.Deps[0].Ecosystem {
	case core.Toolchain, core.Docker, core.Actions:
		s += " in " + code(ch.Deps[0].Manifest.Path)
	}
	if len(ch.Via) > 0 {
		s += ", by way of " + and(ch.Via) + ", each proven"
	}
	if c := ch.Choice; c != nil && c.N <= len(ch.Options) {
		return s + fmt.Sprintf(", picked from %d options: %s", len(ch.Options), Clause(c.Why))
	}
	switch ch.Kind {
	case core.Breaking, core.Update:
		if ch.Level != "" {
			return s + ": a new " + ch.Level + " release"
		}
		return s + ": a new release"
	}
	return s + because(ch)
}

// because is the deprecation behind a move, after a colon, or "".
func because(ch *core.Change) string {
	for _, ev := range ch.Evidence {
		if (ev.Source == "registry" || ev.Source == "archived") && ev.Text != "" {
			return ": " + oneLine(ev.Text, 160)
		}
	}
	for _, ev := range ch.Evidence {
		if ev.Title != "" {
			return ": " + oneLine(ev.Title, 160)
		}
	}
	return ""
}

// sitesMade lists the call sites of ch the patch changed, each with what
// replaced it, indented under the change.
func sitesMade(ch *core.Change, repl map[string]map[int][]string) []string {
	var made []core.Site
	for _, s := range core.Calls(ch) {
		if len(repl[s.File][s.Line]) > 0 {
			made = append(made, s)
		}
	}
	var res []string
	for _, l := range siteLines(made, repl) {
		res = append(res, "  "+l)
	}
	return res
}

// unused lists the dependencies the first PR removes, then those it keeps
// though they look unused, each with why.
func unused(b *core.Batch) string {
	var parts []string
	if len(b.Unused) > 0 {
		lines := []string{"Removed, since nothing in the project uses " + pick(len(b.Unused), "it:", "them:"), ""}
		for _, d := range b.Unused {
			lines = append(lines, "- "+declared(d))
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}
	if len(b.Kept) > 0 {
		lines := []string{"Possibly unused, kept: these look unused, but tend cannot prove it, so they are yours to decide.", ""}
		for _, a := range b.Kept {
			lines = append(lines, "- "+declared(a.Dep)+": "+Clause(a.Why))
		}
		parts = append(parts, strings.Join(lines, "\n"))
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

// stages lists the catch-up PRs still to come, stage by stage, with what
// each will carry.
func stages(b *core.Batch) string {
	var ks []int
	for _, l := range b.Next {
		if l.Stage > b.Stage && !slices.Contains(ks, l.Stage) {
			ks = append(ks, l.Stage)
		}
	}
	if len(ks) == 0 {
		return ""
	}
	slices.Sort(ks)
	lines := []string{"Each comes as a pull request of its own once this one is merged, proven the same way.", ""}
	for _, k := range ks {
		lines = append(lines, strconv.Itoa(k)+". "+upper(plan.StageName(k)))
		for _, l := range b.Next {
			if l.Stage == k {
				lines = append(lines, "   - "+later(l))
			}
		}
	}
	return strings.Join(lines, "\n")
}

// undone lists what a batch left out with nowhere left to go: the catch-up
// ends without it, and it comes back one change at a time.
func undone(b *core.Batch) string {
	var lines []string
	for _, l := range b.Next {
		if l.Stage <= b.Stage {
			lines = append(lines, "- "+later(l))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	head := "These could not be made or proven here. Each comes back as a pull request of its own after the catch-up, and tend's tracking issue keeps the list."
	return head + "\n\n" + strings.Join(lines, "\n")
}

// later names a change a batch leaves for later, with why, and the ways off
// it its own PR offers.
func later(l core.Later) string {
	ch := &l.Change
	var s string
	switch {
	case ch.API != nil:
		s = code(op(ch.API)) + " on the " + api.Title(ch.API.Provider) + " API"
	case len(ch.Deps) == 0:
		s = code(ch.ID)
	default:
		s = named(ch, ch.To)
		if m := ch.Deps[0].Manifest.Path; m != "" && len(core.Names(ch)) == 1 {
			s += " in " + code(m)
		}
	}
	why := oneLine(Clause(l.Why), 200)
	if why == "" {
		why = strings.TrimSuffix(what(ch), ".")
	}
	line := s + ": " + why
	if !strings.HasSuffix(why, "…") {
		line += "."
	}
	if tries := plan.Ranked(ch, nil); l.Why == "" && len(tries) > 0 {
		var names []string
		for _, t := range tries {
			names = append(names, choiceName(&ch.Options[t.N-1]))
		}
		line += " tend takes the first of " + and(names) + " its proof passes, in that order."
	}
	return line
}

// what says what a change waiting for a later stage is.
func what(ch *core.Change) string {
	switch {
	case ch.API != nil:
		return reason(BodyInput{Change: ch})
	case ch.Kind == core.Breaking || ch.Kind == core.Update:
		if ch.Level != "" {
			return "a new " + ch.Level + " release"
		}
		return "a new release"
	case len(core.Calls(ch)) > plan.Spread:
		return fmt.Sprintf("a migration of %d call sites", len(core.Calls(ch)))
	case len(ch.Deps) > 0 && ch.Deps[0].Min:
		return "the oldest " + ch.Deps[0].Name + " the project supports, past its end of life"
	case ch.To == "" && ch.ToName == "" && len(ch.Options) == 0:
		return "deprecated, with no known replacement"
	case ch.To == "" && ch.ToName == "":
		return "deprecated, with no single replacement"
	}
	if s := because(ch); s != "" {
		return strings.TrimPrefix(s, ": ")
	}
	return string(ch.Kind)
}

// choiceName is what an option moves to, as a list names it.
func choiceName(o *core.Option) string {
	if o.ToName != "" {
		return code(o.ToName)
	}
	return o.Title
}

func upper(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// landedAt is the release the patch locked ch's dep at when the lockfile
// took one past ch.To, else "".
func landedAt(p *core.Patch, ch *core.Change) string {
	if p == nil || len(ch.Deps) == 0 {
		return ""
	}
	d := ch.Deps[0]
	return p.Landed[d.Ecosystem+"/"+d.Name]
}

func op(a *core.APIChange) string {
	return strings.TrimSpace(strings.ToUpper(a.Method) + " " + a.Path)
}

// Clause makes a sentence the second half of one: "No model is configured."
// reads "no model is configured" after a colon.
func Clause(s string) string {
	s = strings.TrimSuffix(strings.TrimSpace(s), ".")
	if len(s) > 1 && s[0] >= 'A' && s[0] <= 'Z' && (s[1] < 'A' || s[1] > 'Z') {
		s = string(s[0]+'a'-'A') + s[1:]
	}
	return s
}

// Displaced reports whether pr was closed by tend to make room, for a
// security fix, for the first PR or to keep one PR open, rather than
// declined: it comes back. It reads a close the way Declined does, so a word
// someone leaves on the PR after tend closed it does not turn the close into
// theirs.
func Displaced(ctx context.Context, forge core.Forge, pr *core.PR) (bool, error) {
	if pr == nil || pr.State != "closed" {
		return false, nil
	}
	no, err := Declined(ctx, forge, pr)
	return !no, err
}
