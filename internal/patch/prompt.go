package patch

import (
	_ "embed"
	"fmt"
	"slices"
	"strings"
	"text/template"

	"github.com/mohsensc/tend-cli/internal/core"
)

//go:embed prompt.tmpl
var promptText string

var prompts = template.Must(template.New("prompt").Parse(promptText))

const (
	maxEvidence = 10
	maxText     = 1200
	around      = 25
	feedbackMax = 60
	previousMax = 4000
)

// job is what a model migration works from.
type job struct {
	ch      *core.Change
	to      string
	toName  string
	cm      *core.Codemod
	rest    []core.Site
	allowed []string
}

type promptData struct {
	Change   string
	Bumped   bool
	Evidence []core.Evidence
	Hints    []string
	Rewrites []string
	Allowed  []string
	Files    []promptFile
	Retry    string
	Previous string
}

type promptFile struct {
	Path    string
	Imports []string
	Sites   []string
	Context []string
}

// prompt renders the system text and the user prompt. retry and previous carry the
// last failed reply, empty on the first turn.
func prompt(t *tree, in job, o Options, retry, previous string) (string, string, error) {
	d := promptData{
		Change:   summary(in.ch, in.to, in.toName),
		Bumped:   len(in.ch.Deps) > 0,
		Rewrites: rules(in.cm),
		Allowed:  in.allowed,
		Retry:    retry,
		Previous: core.Clip(previous, previousMax),
	}
	for i, ev := range in.ch.Evidence {
		if i == maxEvidence {
			break
		}
		ev.Text = strings.TrimSpace(core.Clip(ev.Text, maxText))
		d.Evidence = append(d.Evidence, ev)
	}
	d.Hints = hints(in.ch, in.cm)
	imps, err := importLines(t, in, o)
	if err != nil {
		return "", "", err
	}
	for _, f := range siteFiles(in.rest) {
		d.Files = append(d.Files, fileSection(t, f, in.rest, imps[f]))
	}

	var sys, user strings.Builder
	if err := prompts.ExecuteTemplate(&sys, "system", nil); err != nil {
		return "", "", err
	}
	if err := prompts.ExecuteTemplate(&user, "user", d); err != nil {
		return "", "", err
	}
	return sys.String(), user.String(), nil
}

// summary names the change in one line: "stripe 14.2.0 -> 16.0.1 (breaking, major)".
func summary(ch *core.Change, to, toName string) string {
	kind := string(ch.Kind)
	if ch.Level != "" {
		kind += ", " + ch.Level
	}
	if a := ch.API; a != nil {
		s := fmt.Sprintf("%s API %s %s", a.Provider, a.Method, a.Path)
		if a.Field != "" {
			s += " field " + a.Field
		}
		return fmt.Sprintf("%s: %s (%s)", s, a.What, kind)
	}
	var deps []string
	for _, d := range ch.Deps {
		deps = append(deps, strings.TrimSpace(d.Name+" "+from(d)))
	}
	target := strings.TrimSpace(toName + " " + to)
	return fmt.Sprintf("%s -> %s (%s)", strings.Join(deps, ", "), target, kind)
}

// rules lists the codemod's rewrites in a fixed order. Field drops and multi-line text
// are never applied mechanically, so they are not listed as done.
func rules(cm *core.Codemod) []string {
	if cm == nil {
		return nil
	}
	var res []string
	for _, k := range keys(cm.Imports) {
		res = append(res, fmt.Sprintf("import %s -> %s", k, cm.Imports[k]))
	}
	for _, k := range keys(cm.Renames) {
		res = append(res, fmt.Sprintf("rename %s -> %s", k, cm.Renames[k]))
	}
	for _, k := range keys(cm.Paths) {
		res = append(res, fmt.Sprintf("path %s -> %s", k, cm.Paths[k]))
	}
	for _, k := range keys(cm.Fields) {
		if cm.Fields[k] != "" {
			res = append(res, fmt.Sprintf("field %s -> %s", k, cm.Fields[k]))
		}
	}
	for _, k := range keys(cm.Text) {
		if !strings.Contains(k+cm.Text[k], "\n") {
			res = append(res, fmt.Sprintf("text %s -> %s", k, cm.Text[k]))
		}
	}
	return res
}

// hints are the known ways off the change that were not applied for the model:
// a rename a deprecation notice names, a migration the provider documents.
// Staying on the old version is no hint for a migration.
func hints(ch *core.Change, applied *core.Codemod) []string {
	var res []string
	for _, o := range ch.Options {
		if o.Codemod == applied && applied != nil || o.Codemod == nil && o.ToName == "" {
			continue
		}
		s := o.Title
		if o.Codemod != nil {
			if r := rules(o.Codemod); len(r) > 0 {
				s += ": " + strings.Join(r, "; ")
			}
		}
		res = append(res, s)
	}
	return res
}

// importLines returns, per file, the line numbers that import the changed packages.
func importLines(t *tree, in job, o Options) (map[string][]int, error) {
	var names []string
	for _, d := range in.ch.Deps {
		if e := o.Ecos[d.Ecosystem]; e != nil {
			names = append(names, e.Imports(d)...)
			if in.toName != "" {
				nd := d
				nd.Name = in.toName
				names = append(names, e.Imports(nd)...)
			}
		}
	}
	if in.cm != nil {
		for k, v := range in.cm.Imports {
			names = append(names, k, v)
		}
	}
	names = uniq(names)
	if len(names) == 0 {
		return nil, nil
	}
	idx, err := t.index()
	if err != nil {
		return nil, err
	}
	res := map[string][]int{}
	for _, n := range names {
		for _, imp := range idx.Imports(n) {
			res[imp.File] = append(res[imp.File], imp.Line)
		}
	}
	for f := range res {
		slices.Sort(res[f])
		res[f] = slices.Compact(res[f])
	}
	return res, nil
}

func fileSection(t *tree, f string, sites []core.Site, imps []int) promptFile {
	src := t.read(f)
	var lines []string
	eachLine(src, func(_ int, ln []byte, _ int) { lines = append(lines, strings.TrimSuffix(string(ln), "\r")) })
	width := len(fmt.Sprint(len(lines)))
	numbered := func(n int) string { return fmt.Sprintf("%*d| %s", width, n, lines[n-1]) }

	sites = slices.DeleteFunc(slices.Clone(sites), func(s core.Site) bool { return s.File != f })
	slices.SortStableFunc(sites, func(a, b core.Site) int { return a.Line - b.Line })
	res := promptFile{Path: f}
	for _, n := range imps {
		if n >= 1 && n <= len(lines) {
			res.Imports = append(res.Imports, numbered(n))
		}
	}
	var spans [][2]int
	for _, s := range sites {
		if s.Line < 1 || s.Line > len(lines) {
			continue
		}
		res.Sites = append(res.Sites, fmt.Sprintf("%*d| %s", width, s.Line, label(s)))
		start, end := max(1, s.Line-around), min(len(lines), s.Line+around)
		if k := len(spans) - 1; k >= 0 && start <= spans[k][1]+1 {
			spans[k][1] = max(spans[k][1], end)
			continue
		}
		spans = append(spans, [2]int{start, end})
	}
	for i, sp := range spans {
		if i > 0 {
			res.Context = append(res.Context, strings.Repeat(" ", width)+"| ...")
		}
		for n := sp[0]; n <= sp[1]; n++ {
			res.Context = append(res.Context, numbered(n))
		}
	}
	return res
}

func label(s core.Site) string {
	if len(s.Chain) == 0 {
		return s.Text
	}
	res := strings.Join(s.Chain, ".")
	if s.Call {
		res += "(...)"
	}
	return res
}

// tail returns the last n lines of s.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
