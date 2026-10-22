package deliver

import (
	"cmp"
	_ "embed"
	"fmt"
	"html"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/ver"
	"github.com/mohsensc/tend-cli/internal/vet"
)

//go:embed body.tmpl
var bodyTmpl string

var tmpl = template.Must(template.New("body").Parse(bodyTmpl))

const (
	noteLines  = 6
	noteMax    = 4 // releases quoted when something else is the reason for the change
	siteMax    = 20
	dateLayout = "2006-01-02"
	tailMax    = 8 << 10
	// bodyMax keeps a body under the 65536 characters GitHub takes, with room
	// for the auto-merge note added later.
	bodyMax = 60000
)

// BodyInput is everything the PR body says about one change.
type BodyInput struct {
	Change  *core.Change
	Patch   *core.Patch
	Proof   *core.Proof
	Vetting *core.Vetting
	Meta    *core.Meta // registry data for the first dep, nil when unknown
	Base    string     // base commit sha
	BaseRef string     // base branch, shown next to the sha
	Rev     int        // 1 for a new PR, bumped on every update
	Option  int        // option the maintainer picked, 0 for tend's own choice
	Notes   []string
	// FirstDue marks a PR that went ahead of tend's first PR, so a lost state
	// still finds the first PR owed.
	FirstDue bool
}

type page struct {
	Heading, Why, Level                      string
	Welcome, Batch                           string // the first PR's, in place of the heading and Touched
	Upstream, Touched, Proof, Vetting, Notes string
	Options, Footer, ID, Base, Due, Stage    string
	Rev                                      int
}

// Body renders the PR description. The last line is the marker ParseMarker reads.
func Body(in BodyInput) string {
	ch := in.Change
	p := page{
		Proof:  proof(in),
		Notes:  bullets(in.Notes),
		Footer: footer(in),
		ID:     markerEscape.Replace(ch.ID),
		Base:   markerEscape.Replace(in.Base),
		Rev:    max(in.Rev, 1),
	}
	if in.FirstDue {
		p.Due = "due"
	}
	if ch.Kind == core.First && ch.Batch != nil {
		p.Welcome, p.Batch = welcome(ch.Batch), batched(in)
		if ch.ID != core.FirstID {
			p.Welcome = intro(ch.Batch)
		}
		if k := ch.Batch.Stage; k > 0 {
			p.Stage = strconv.Itoa(k)
		}
	} else {
		at := shown(in)
		p.Heading, p.Why, p.Level = heading(at.Change), why(in), level(at)
		p.Upstream, p.Touched, p.Options = upstream(ch), touched(in), options(in)
	}
	if v := in.Vetting; v != nil {
		p.Vetting = strings.TrimSpace(vet.Section(v))
		if v.Verdict == "draft" || v.Verdict == "reject" {
			p.Vetting += "\n\nVerdict: " + v.Verdict + "."
		}
	}
	res := render(p)
	// The longest sections give way first; the marker always stays.
	for _, f := range []*string{&p.Upstream, &p.Proof, &p.Touched, &p.Batch, &p.Options, &p.Notes, &p.Vetting} {
		if len(res) <= bodyMax {
			break
		}
		*f = cut(*f, len(*f)-(len(res)-bodyMax))
		res = render(p)
	}
	return res
}

// shown is in with its change as the patch made it: a dep only the lockfile
// names at the release it landed on, past the least one that would do, and
// a replacement the option it applied moves to.
func shown(in BodyInput) BodyInput {
	if at := landedAt(in.Patch, in.Change); at != "" {
		ch := *in.Change
		ch.To = at
		in.Change = &ch
	}
	if o := picked(in); o != nil && o.ToName != "" && (in.Patch == nil || !in.Patch.Decision) {
		ch := *in.Change
		ch.ToName, ch.To = o.ToName, o.To
		in.Change = &ch
	}
	return in
}

func render(p page) string {
	var b strings.Builder
	if err := tmpl.Execute(&b, p); err != nil {
		panic("deliver: body template: " + err.Error())
	}
	return b.String()
}

// cut shortens a section to at most n bytes at a line break, closing what
// the cut leaves open, and says where the rest is.
func cut(s string, n int) string {
	const more = "\n\n_Cut to fit GitHub's limit on a PR body._"
	n -= len(more) + len("\n```\n</details>")
	if n <= 0 {
		return strings.TrimPrefix(more, "\n\n")
	}
	if i := strings.LastIndexByte(s[:min(n, len(s))], '\n'); i > 0 {
		s = s[:i]
	} else {
		s = s[:min(n, len(s))]
	}
	if strings.Count(s, "```")%2 == 1 {
		s += "\n```"
	}
	if strings.Count(s, "<details>") > strings.Count(s, "</details>") {
		s += "\n</details>"
	}
	return s + more
}

// tail keeps the end of a step's output, where the failure is.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// markerEscape keeps an id from closing the comment or the attribute it sits in.
var markerEscape = strings.NewReplacer("&", "&amp;", `"`, "&quot;", "-->", "--&gt;")

var markerRe = regexp.MustCompile(`<!-- tend id="([^"]*)" base="([^"]*)" rev="(\d+)"(?: tidy="(\w+)")?(?: stage="(\d+)")? -->`)

// FirstDue reports whether the marker of body says tend's first PR is still
// owed: the PR went ahead of it. The attribute is named for the tidy-up the
// first PR once was, which markers on open PRs still carry.
func FirstDue(body string) bool {
	all := markerRe.FindAllStringSubmatch(body, -1)
	return len(all) > 0 && all[len(all)-1][4] == "due"
}

// StageOf is the catch-up stage the marker of body names, 0 for a PR that
// is not a catch-up PR or that an older tend opened.
func StageOf(body string) int {
	all := markerRe.FindAllStringSubmatch(body, -1)
	if len(all) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(all[len(all)-1][5])
	return n
}

// ParseMarker reads the marker Body ends with. The last one wins.
func ParseMarker(body string) (id, base string, rev int, ok bool) {
	all := markerRe.FindAllStringSubmatch(body, -1)
	if len(all) == 0 {
		return "", "", 0, false
	}
	m := all[len(all)-1]
	n, err := strconv.Atoi(m[3])
	if err != nil {
		return "", "", 0, false
	}
	return html.UnescapeString(m[1]), html.UnescapeString(m[2]), n, true
}

func heading(ch *core.Change) string {
	if a := ch.API; a != nil {
		return strings.TrimSpace(a.Provider + " " + strings.TrimSpace(strings.ToUpper(a.Method)+" "+a.Path))
	}
	if len(ch.Deps) == 0 {
		return ch.ID
	}
	d := ch.Deps[0]
	name := depName(d)
	from := ""
	if d.Version != "" {
		from = d.Version + " "
	}
	switch {
	case ch.To == "" && ch.ToName == "":
		return name + ": choose a replacement"
	case ch.ToName == "" && ch.To == d.Version:
		return name + " " + d.Version + ": deprecated calls"
	case len(core.Names(ch)) > 1:
		return fmt.Sprintf("%s (%d packages) → %s", groupName(ch), len(ch.Deps), ch.To)
	case ch.ToName != "":
		return strings.TrimSpace(name + " → " + ch.ToName + " " + ch.To)
	}
	return name + " " + from + "→ " + ch.To
}

func why(in BodyInput) string {
	ch := in.Change
	parts := []string{string(ch.Kind)}
	switch ch.Kind {
	case core.Security:
		ids := advisories(ch)
		switch {
		case len(ids) > 0:
			s := ids[0]
			if ch.Severity != "" {
				s += " (" + ch.Severity + ")"
			}
			if len(ids) > 1 {
				s += fmt.Sprintf(" +%d more", len(ids)-1)
			}
			parts = append(parts, s)
		case ch.Severity != "":
			parts = append(parts, ch.Severity)
		}
		if ch.To != "" {
			parts = append(parts, "fixed in "+ch.To)
		}
	case core.Deprecation:
		parts = append(parts, reason(in))
	case core.Breaking:
		if ch.API != nil {
			parts = append(parts, reason(in))
		} else if ch.Level != "" {
			parts = append(parts, ch.Level+" release")
		}
	case core.Update:
		if ch.Level != "" {
			parts = append(parts, ch.Level+" release")
		}
	}
	text := strings.Join(parts, " · ")
	if !ch.Sunset.IsZero() {
		if date := ch.Sunset.Format(dateLayout); !strings.Contains(text, date) {
			text += " · sunset " + date
		}
	}
	if !ch.Published.IsZero() {
		label := "published"
		switch {
		case ch.Kind == core.Security:
			label = "advisory published"
		case ch.API == nil && ch.To != "" && ch.Kind != core.Deprecation:
			label = "released"
		}
		text += " · " + label + " " + ch.Published.Format(dateLayout)
	}
	return text
}

// reason is what the API change says, else the registry's own deprecation
// message when there is one, else the title of the first piece of evidence.
func reason(in BodyInput) string {
	ch := in.Change
	if a := ch.API; a != nil {
		s := a.What
		if a.Field != "" {
			s += " (" + a.Field + ")"
		}
		return s
	}
	if m := in.Meta; m != nil && len(ch.Deps) > 0 {
		if m.Deprecated != "" {
			return oneLine(m.Deprecated, 120)
		}
		for _, r := range m.Releases {
			if r.Version == ch.Deps[0].Version && r.Deprecated != "" {
				return oneLine(r.Deprecated, 120)
			}
		}
	}
	if len(ch.Evidence) > 0 && ch.Evidence[0].Title != "" {
		return oneLine(ch.Evidence[0].Title, 120)
	}
	return "deprecated"
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = strings.TrimSpace(core.Clip(s, n)) + "…"
	}
	return s
}

func level(in BodyInput) string {
	ch := in.Change
	if ch.Level == "" || len(ch.Deps) == 0 {
		return ""
	}
	res := ch.Level
	from, ok1 := ver.Parse(ch.Deps[0].Version)
	to, ok2 := ver.Parse(ch.To)
	if ok1 && ok2 {
		if ch.Level == "major" {
			res = fmt.Sprintf("major (%d → %d)", from.Major(), to.Major())
		} else {
			res = fmt.Sprintf("%s (%s → %s)", ch.Level, ch.Deps[0].Version, ch.To)
		}
	}
	hit, total := 0, 0
	for _, e := range ch.Evidence {
		if !isNotes(e) {
			continue
		}
		total++
		if breaks(e) {
			hit++
		}
	}
	if hit > 0 {
		res += fmt.Sprintf("; release notes below mention breaking changes in %d of %d releases", hit, total)
	}
	return res
}

func isNotes(e core.Evidence) bool {
	return e.Source == "releases" || e.Source == "changelog"
}

// upstream lists the evidence. When an advisory, a deprecation or an end of
// life is the reason, release notes are background: at most noteMax are
// quoted, those that mention a breaking change first, then the newest.
func upstream(ch *core.Change) string {
	keep := map[int]bool{}
	if slices.ContainsFunc(ch.Evidence, func(e core.Evidence) bool { return !isNotes(e) }) {
		for _, breaking := range []bool{true, false} {
			for i, e := range ch.Evidence {
				if len(keep) < noteMax && isNotes(e) && breaks(e) == breaking {
					keep[i] = true
				}
			}
		}
	}
	var items []string
	left := 0
	for i, e := range ch.Evidence {
		if len(keep) > 0 && isNotes(e) && !keep[i] {
			left++
			continue
		}
		items = append(items, evidence(e))
	}
	switch left {
	case 0:
	case 1:
		items = append(items, "- 1 more release, not quoted")
	default:
		items = append(items, fmt.Sprintf("- %d more releases, not quoted", left))
	}
	return strings.Join(items, "\n")
}

func breaks(e core.Evidence) bool { return strings.Contains(strings.ToLower(e.Text), "breaking") }

func evidence(e core.Evidence) string {
	var b strings.Builder
	name := e.Title
	if name == "" {
		name = e.Source
	}
	b.WriteString("- **" + name + "**")
	if e.URL != "" {
		label := "source"
		if isNotes(e) {
			label = "full notes"
		}
		b.WriteString(" · [" + label + "](" + e.URL + ")")
	}
	lines := textLines(e.Text)
	if len(lines) == 0 {
		return b.String()
	}
	if len(lines) > noteLines {
		lines = append(lines[:noteLines], "…")
	}
	b.WriteString("\n" + indent(fenced(strings.Join(lines, "\n")), "  "))
	return b.String()
}

// textLines drops blank lines and trailing spaces so six lines carry six lines of content.
func textLines(s string) []string {
	var res []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		l = strings.TrimRight(l, " \t")
		if l != "" {
			res = append(res, l)
		}
	}
	return res
}

var ticksRe = regexp.MustCompile("`+")

// ticks is a run of backticks longer than any in s, and at least n long.
func ticks(s string, n int) string {
	for _, run := range ticksRe.FindAllString(s, -1) {
		n = max(n, len(run)+1)
	}
	return strings.Repeat("`", n)
}

// fenced wraps s in a code fence longer than any backtick run inside it, so
// upstream text cannot close the block, link, or mention anyone.
func fenced(s string) string {
	fence := ticks(s, 3)
	return fence + "\n" + s + "\n" + fence
}

func indent(s, pad string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = pad + l
		}
	}
	return strings.Join(lines, "\n")
}

func code(s string) string {
	d := ticks(s, 1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		s = " " + s + " "
	}
	return d + s + d
}

func bullets(lines []string) string {
	var res []string
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			res = append(res, "- "+l)
		}
	}
	return strings.Join(res, "\n")
}

func touched(in BodyInput) string {
	ch, p := in.Change, in.Patch
	if p == nil {
		return ""
	}
	if p.Decision {
		res := "Nothing was changed in the code."
		if p.Note != "" {
			res += " " + p.Note
		}
		if lines := siteLines(ch.Sites, nil); len(lines) > 0 {
			res += "\n\nWhere the code uses it:\n\n" + strings.Join(lines, "\n")
		}
		return res
	}
	lines := siteLines(ch.Sites, replacements(p.Diff))
	var others []string
	for _, f := range p.Files {
		if !slices.ContainsFunc(ch.Sites, func(s core.Site) bool { return s.File == f }) {
			others = append(others, code(f))
		}
	}
	if len(others) > 0 {
		lines = append(lines, "- "+strings.Join(others, ", "))
	}
	res := strings.Join(lines, "\n")
	for _, s := range []string{lockOnly(ch, p), past(ch, p), goMoved(ch, p.Diff), raised(ch)} {
		if s != "" {
			res += "\n\n" + s
		}
	}
	switch {
	case p.Model && p.Note != "":
		res += "\n\nPart of this change was written by a model, in " + p.Note + ". The vetting section below covers it."
	case p.Model:
		res += "\n\nPart of this change was written by a model. The vetting section below covers it."
	}
	return strings.TrimSpace(res)
}

// siteLines lists each call site once, with what replaced it where repl says,
// up to siteMax.
func siteLines(sites []core.Site, repl map[string]map[int][]string) []string {
	var lines []string
	seen := map[string]bool{}
	for _, s := range sites {
		at := s.File + ":" + strconv.Itoa(s.Line)
		if seen[at] {
			continue
		}
		seen[at] = true
		lines = append(lines, siteLine(s, repl[s.File][s.Line]))
	}
	if len(lines) > siteMax {
		lines = append(lines[:siteMax], fmt.Sprintf("- … and %d more", len(lines)-siteMax))
	}
	return lines
}

// lockOnly explains a bump that left the manifest alone: the requirement
// written there already allows the new version.
func lockOnly(ch *core.Change, p *core.Patch) string {
	if len(ch.Deps) != 1 || ch.ToName != "" || ch.To == "" {
		return ""
	}
	d := ch.Deps[0]
	m := d.Manifest
	if m.Lock == "" || d.Req == "" || !slices.Contains(p.Files, m.Lock) || slices.Contains(p.Files, m.Path) {
		return ""
	}
	return fmt.Sprintf("%s in %s already allows %s, so only %s changed.", code(d.Req), code(m.Path), ch.To, code(m.Lock))
}

// past explains a dep only the lockfile names that landed past ch.To: the
// tool moves such a dep to the newest release the ranges on it allow.
func past(ch *core.Change, p *core.Patch) string {
	at := landedAt(p, ch)
	if at == "" {
		return ""
	}
	d := ch.Deps[0]
	tool := cmp.Or(d.Manifest.Tool, "the lock tool")
	return fmt.Sprintf("tend asked for %s; %s names %s only as a dependency of other packages, and %s moves such a dependency to the newest release their ranges allow, %s.", ch.To, code(d.Manifest.Lock), code(d.Name), code(tool), at)
}

// raised explains a runtime minimum: it is what the project promises to
// run on, so moving it up is a change for the people who use the project.
func raised(ch *core.Change) string {
	if len(ch.Deps) == 0 || !ch.Deps[0].Min || ch.To == "" {
		return ""
	}
	d := ch.Deps[0]
	return fmt.Sprintf("%s names the oldest %s this project supports, not one it runs on. Raising it to %s drops releases past their end of life: anyone still on one has to upgrade to use what comes next.", code(d.Manifest.Path), d.Name, ch.To)
}

var goDirective = regexp.MustCompile(`^([-+])go (\d+(?:\.\d+)*)$`)

// goMoved explains a go line that go get raised along with the bump: from
// now on, building the module takes that Go.
func goMoved(ch *core.Change, diff string) string {
	if len(ch.Deps) == 0 || ch.Deps[0].Ecosystem != core.Go {
		return ""
	}
	var file, old, now string
	for _, l := range strings.Split(diff, "\n") {
		if rest, ok := strings.CutPrefix(l, "diff --git a/"); ok {
			file, _, _ = strings.Cut(rest, " b/")
			continue
		}
		if file != "go.mod" && !strings.HasSuffix(file, "/go.mod") {
			continue
		}
		if m := goDirective.FindStringSubmatch(l); m != nil && m[1] == "-" {
			old = m[2]
		} else if m != nil {
			now = m[2]
		}
	}
	if old == "" || now == "" || old == now {
		return ""
	}
	return fmt.Sprintf("The go line in %s moved from %s to %s, as %s %s requires, so building this module now takes Go %s or later.", code(file), old, now, ch.Deps[0].Name, ch.To, now)
}

func siteLine(s core.Site, after []string) string {
	at := code(s.File + ":" + strconv.Itoa(s.Line))
	before := strings.TrimSpace(s.Text)
	switch {
	case len(after) == 0 && before == "":
		return "- " + at
	case len(after) == 0:
		return "- " + at + " " + code(before)
	}
	next := strings.TrimSpace(after[0])
	if len(after) > 1 {
		next += " …"
	}
	if before == "" {
		return "- " + at + " → " + code(next)
	}
	return "- " + at + " " + code(before) + " → " + code(next)
}

// replacements maps file -> old line number -> the lines that replaced it,
// read from a unified diff. A run of removed lines followed by added lines is
// one edit: paired one to one when the counts match, else the first removed
// line takes all the added ones.
func replacements(diff string) map[string]map[int][]string {
	res := map[string]map[int][]string{}
	var file string
	var old, oldLeft, newLeft int
	var gone []int
	var added []string
	flush := func() {
		defer func() { gone, added = nil, nil }()
		if file == "" || len(gone) == 0 || len(added) == 0 {
			return
		}
		if res[file] == nil {
			res[file] = map[int][]string{}
		}
		if len(gone) != len(added) {
			res[file][gone[0]] = added
			return
		}
		for i, n := range gone {
			res[file][n] = []string{added[i]}
		}
	}
	for _, l := range strings.Split(diff, "\n") {
		if oldLeft > 0 || newLeft > 0 {
			switch {
			case strings.HasPrefix(l, "-"):
				gone = append(gone, old)
				old++
				oldLeft--
			case strings.HasPrefix(l, "+"):
				added = append(added, l[1:])
				newLeft--
			case strings.HasPrefix(l, "\\"):
			default:
				flush()
				old++
				oldLeft--
				newLeft--
			}
			continue
		}
		flush()
		switch {
		case strings.HasPrefix(l, "diff --git"):
			file = ""
		case strings.HasPrefix(l, "+++ "):
			file = strings.TrimPrefix(strings.TrimPrefix(l, "+++ "), "b/")
			if file == "/dev/null" {
				file = ""
			}
		case strings.HasPrefix(l, "@@"):
			old, oldLeft, newLeft = hunk(l)
		}
	}
	flush()
	return res
}

var hunkRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+\d+(?:,(\d+))? @@`)

func hunk(l string) (start, oldN, newN int) {
	m := hunkRe.FindStringSubmatch(l)
	if m == nil {
		return 0, 0, 0
	}
	count := func(s string) int {
		if s == "" {
			return 1
		}
		n, _ := strconv.Atoi(s)
		return n
	}
	start, _ = strconv.Atoi(m[1])
	return start, count(m[2]), count(m[3])
}

func proof(in BodyInput) string {
	pr := in.Proof
	if pr == nil {
		return ""
	}
	if pr.Nothing {
		return "No build or test command was found for this project, so nothing was run. Set `verify.test` in tend.yml to give the proof something to run."
	}
	label := "base"
	if at := baseLabel(in); at != "" {
		label += " (" + at + ")"
	}
	var b strings.Builder
	b.WriteString("| step | " + label + " | patched |\n|---|---|---|")
	for _, cmd := range stepCmds(pr) {
		b.WriteString("\n| " + code(cmd) + " | " + cell(pr.Base, cmd) + " | " + cell(pr.Patched, cmd) + " |")
	}
	if s := verdictLine(in); s != "" {
		b.WriteString("\n\n" + s)
	}
	if s := pr.Container; s != "" {
		b.WriteString("\n\nRan in container " + code(s) + ".")
	}
	if st := tailStep(pr.Patched); st != nil {
		fmt.Fprintf(&b, "\n\n<details><summary>%s (patched), end of its output</summary>\n\n%s\n</details>", html.EscapeString(st.Cmd), fenced(tail(clean(st.Tail), tailMax)))
	}
	return b.String()
}

func stepCmds(pr *core.Proof) []string {
	var res []string
	seen := map[string]bool{}
	for _, r := range []*core.Run{pr.Patched, pr.Base} {
		if r == nil {
			continue
		}
		for _, s := range r.Steps {
			if !seen[s.Cmd] {
				seen[s.Cmd] = true
				res = append(res, s.Cmd)
			}
		}
	}
	return res
}

func cell(r *core.Run, cmd string) string {
	if r == nil {
		return "not run"
	}
	for _, s := range r.Steps {
		if s.Cmd != cmd {
			continue
		}
		switch {
		case s.Skipped != "":
			return "skipped (" + s.Skipped + ")"
		case s.Exit != 0:
			return fmt.Sprintf("failed (exit %d) · %s", s.Exit, dur(s.Took))
		}
		return "ok · " + dur(s.Took)
	}
	return "not run"
}

func dur(d time.Duration) string {
	switch {
	case d < time.Second:
		return strconv.Itoa(int(d/time.Millisecond)) + "ms"
	case d < time.Minute:
		return strconv.Itoa(int(d.Round(time.Second)/time.Second)) + "s"
	}
	d = d.Round(time.Second)
	return fmt.Sprintf("%dm%02ds", int(d/time.Minute), int(d%time.Minute/time.Second))
}

func baseLabel(in BodyInput) string {
	switch {
	case in.BaseRef != "" && in.Base != "":
		return in.BaseRef + "@" + core.Short(in.Base)
	case in.BaseRef != "":
		return in.BaseRef
	}
	return core.Short(in.Base)
}

// verdictLine says the one thing the table alone does not: the baseline never
// ran, or it was already red, or the patched tree is red.
func verdictLine(in BodyInput) string {
	pr := in.Proof
	at := "the base commit"
	if l := baseLabel(in); l != "" {
		at = code(l)
	}
	decision := in.Patch != nil && in.Patch.Decision
	switch {
	case pr.Base != nil && !pr.Base.Pass:
		return "Tests already fail on " + at + ", so this PR is a draft."
	case decision && pr.Patched != nil && !pr.Patched.Pass:
		return "The patched column is what tend tried before deciding to ask; its failing output is below."
	case pr.Patched != nil && !pr.Patched.Pass:
		return "The patched tree does not pass."
	case pr.Base == nil:
		return "The baseline could not be run, so there is nothing to compare the patched column with."
	}
	return ""
}

func tailStep(r *core.Run) *core.Step {
	if r == nil {
		return nil
	}
	var res *core.Step
	for i := range r.Steps {
		s := &r.Steps[i]
		if s.Skipped != "" || strings.TrimSpace(s.Tail) == "" {
			continue
		}
		res = s
		if s.Exit != 0 {
			break
		}
	}
	return res
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

func clean(s string) string {
	return strings.TrimRight(ansiRe.ReplaceAllString(s, ""), "\n ")
}

// options lists the ways off a change on a decision PR, each with the comment
// that picks it. A PR that applied one says which; one whose fix needed none
// of them leaves them out.
func options(in BodyInput) string {
	ch := in.Change
	applied := appliedOption(in)
	decision := in.Patch != nil && in.Patch.Decision
	if len(ch.Options) == 0 || !decision && applied == 0 {
		return ""
	}
	var res []string
	for i, o := range ch.Options {
		line := fmt.Sprintf("%d. **%s**", i+1, o.Title)
		if !o.Safe && !pins(ch, &o) {
			line += " (not mechanical)"
		}
		first, rest, _ := strings.Cut(strings.TrimSpace(o.Detail), "\n\n")
		if first != "" {
			line += " — " + oneLine(first, 600)
		}
		if i+1 == applied {
			line += " Applied in this PR."
		} else {
			line += " " + code("tend apply "+strconv.Itoa(i+1))
		}
		if rest = strings.TrimSpace(rest); rest != "" {
			line += "\n\n" + indent(rest, "   ")
		}
		res = append(res, line)
	}
	if applied == 0 {
		res = append(res, "", "Nothing was applied. Comment on this PR with the number you want.")
	}
	if c := ch.Choice; c != nil && in.Option == 0 && applied == c.N {
		res = append([]string{fmt.Sprintf("tend took option %d on its own: %s. Comment `tend apply N` to have another instead.", c.N, Clause(c.Why)), ""}, res...)
	}
	return strings.Join(res, "\n")
}

// appliedOption is the option the patch carries: the maintainer's pick, else
// the one tend took by its own rule, else the first mechanical one, which is
// what a patch without a decision applied.
func appliedOption(in BodyInput) int {
	if in.Option > 0 {
		return in.Option
	}
	if in.Patch == nil || in.Patch.Decision {
		return 0
	}
	if c := in.Change.Choice; c != nil {
		return c.N
	}
	for i, o := range in.Change.Options {
		if o.Safe {
			return i + 1
		}
	}
	return 0
}

func footer(in BodyInput) string {
	if options(in) != "" {
		return "tend · comment `tend apply N`, `tend skip` or `tend retry`"
	}
	return "tend · comment `tend skip` or `tend retry`"
}
