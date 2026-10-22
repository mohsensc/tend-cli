package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/plan"
)

func writeCheck(w io.Writer, s *session, p *plan.Plan, empty bool) error {
	if empty {
		_, err := fmt.Fprintf(w, "tend: nothing to watch: no manifests or API calls found in %s\n", s.root)
		return err
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "tend: %d queued, %d skipped, %s\n", len(p.Queue), len(p.Dropped), openSummary(p.Open, s.forge != nil))
	if s.o.quiet {
		_, err := w.Write(b.Bytes())
		return err
	}
	openID := plan.OpenID(p.Open, s.st)
	if len(p.Queue) > 0 {
		b.WriteString("\n")
		tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		for i, ch := range p.Queue {
			fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\t%s\n", i+1, ch.Kind, subject(&ch), target(&ch), detail(&ch, openID, p.Open))
		}
		tw.Flush()
	}
	if len(p.Dropped) > 0 {
		b.WriteString("\n")
		if s.o.all {
			b.WriteString("skipped:\n")
			tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
			for _, d := range p.Dropped {
				first, _, _ := strings.Cut(d.Detail, "\n")
				fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", d.Reason, subject(&d.Change), target(&d.Change), first)
			}
			tw.Flush()
		} else {
			fmt.Fprintf(&b, "skipped: %s   (tend check --all)\n", dropCounts(p.Dropped))
		}
	}
	if len(p.Untracked) > 0 {
		writeUntracked(&b, p.Untracked, s.o.all)
	}
	if len(p.Notes) > 0 {
		b.WriteString("\nnotes:\n")
		notes := p.Notes
		if !s.o.all && len(notes) > maxNotes {
			notes = notes[:maxNotes]
		}
		for _, n := range notes {
			fmt.Fprintf(&b, "  - %s\n", n)
		}
		if n := len(p.Notes) - len(notes); n > 0 {
			fmt.Fprintf(&b, "  and %d more   (tend check --all)\n", n)
		}
	}
	_, err := w.Write(trimLines(b.Bytes()))
	return err
}

// writeUntracked prints what tend cannot follow, one line per reason when
// several things share it, and the tend.yml lines that would fix them as one
// block that pastes in whole.
func writeUntracked(b *bytes.Buffer, us []plan.Untracked, all bool) {
	b.WriteString("\nuntracked:\n")
	var whys []string
	by := map[string][]plan.Untracked{}
	for _, u := range us {
		if by[u.Why] == nil {
			whys = append(whys, u.Why)
		}
		by[u.Why] = append(by[u.Why], u)
	}
	for _, why := range whys {
		g := by[why]
		if len(g) == 1 {
			u := g[0]
			fmt.Fprintf(b, "  %s: %s\n", u.What, why)
			if u.Fix != "" {
				fmt.Fprintf(b, "    %s: [%s]\n", u.Key, u.Fix)
			}
			continue
		}
		shown := g
		if !all && len(g) > maxList {
			shown = g[:maxList]
		}
		fmt.Fprintf(b, "  %s:\n", why)
		for _, u := range shown {
			fmt.Fprintf(b, "    %s\n", u.What)
		}
		if n := len(g) - len(shown); n > 0 {
			fmt.Fprintf(b, "    and %d more   (tend check --all)\n", n)
		}
		key := ""
		for _, u := range shown {
			if u.Fix == "" {
				continue
			}
			if u.Key != key {
				key = u.Key
				fmt.Fprintf(b, "  in tend.yml:\n    %s:\n", key)
			}
			fmt.Fprintf(b, "      - %s\n", u.Fix)
		}
	}
}

// maxList is how many entries of one kind check shows before folding.
const maxList = 5

// trimLines drops the spaces tabwriter leaves after an empty last column.
func trimLines(b []byte) []byte {
	lines := bytes.Split(b, []byte("\n"))
	for i, l := range lines {
		lines[i] = bytes.TrimRight(l, " ")
	}
	return bytes.Join(lines, []byte("\n"))
}

// maxNotes keeps a long list of notes from burying the queue.
const maxNotes = 10

func openSummary(pr *core.PR, known bool) string {
	if !known {
		return "open PRs unknown"
	}
	if pr == nil {
		return "no open PR"
	}
	return fmt.Sprintf("1 open PR (#%d)", pr.Number)
}

// dropCounts reads "9 noise, 1 cooldown, 1 ignored": most first.
func dropCounts(ds []plan.Drop) string {
	count := map[string]int{}
	var reasons []string
	for _, d := range ds {
		if count[d.Reason] == 0 {
			reasons = append(reasons, d.Reason)
		}
		count[d.Reason]++
	}
	slices.SortFunc(reasons, func(a, b string) int {
		if c := count[b] - count[a]; c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	var parts []string
	for _, r := range reasons {
		parts = append(parts, fmt.Sprintf("%d %s", count[r], r))
	}
	return strings.Join(parts, ", ")
}

// subject is what a change is about, as a human names it.
func subject(ch *core.Change) string {
	if ch.Kind == core.First && ch.ID != core.FirstID {
		return plan.StageName(ch.Batch.Stage)
	}
	if ch.Kind == core.First {
		return "first PR"
	}
	if ch.API != nil {
		return ch.API.Provider + " API"
	}
	names := core.Names(ch)
	switch {
	case len(names) == 0:
		return core.Subject(ch.ID)
	case len(names) > 1:
		g := strings.TrimPrefix(core.Subject(ch.ID), ch.Deps[0].Ecosystem+"/group:")
		return fmt.Sprintf("%s (%d packages)", g, len(names))
	}
	d := ch.Deps[0]
	if d.Ecosystem == core.Docker || d.Min {
		return strings.TrimPrefix(d.Name, "library/") + " (" + d.Manifest.Path + ")"
	}
	return d.Name
}

func target(ch *core.Change) string {
	if ch.Kind == core.First {
		return ""
	}
	if a := ch.API; a != nil {
		return strings.TrimSpace(strings.ToUpper(a.Method) + " " + a.Path)
	}
	from := plan.Lowest(ch.Deps)
	switch {
	case ch.ToName != "" && ch.To != "":
		return from + " → " + ch.ToName + " " + ch.To
	case ch.ToName != "":
		return from + " → " + ch.ToName
	case ch.To == "":
		return from
	}
	return from + " → " + ch.To
}

func detail(ch *core.Change, openID string, open *core.PR) string {
	var parts []string
	if b := ch.Batch; b != nil {
		parts = append(parts, carries(b))
		if open != nil && ch.ID == openID {
			parts = append(parts, fmt.Sprintf("open PR #%d", open.Number))
		}
		return strings.Join(parts, " · ")
	}
	if ch.Kind == core.Security {
		parts = append(parts, strings.TrimSpace(advisory(ch)+" "+ch.Severity))
	}
	if a := ch.API; a != nil {
		parts = append(parts, apiWhat(a))
	}
	if !ch.Sunset.IsZero() {
		label := "sunset"
		if slices.ContainsFunc(ch.Evidence, func(ev core.Evidence) bool { return ev.Source == "eol" }) {
			label = "EOL"
		}
		parts = append(parts, label+" "+ch.Sunset.UTC().Format(time.DateOnly))
	}
	if ch.To == "" && ch.API == nil {
		if n := len(ch.Options); n > 0 {
			parts = append(parts, fmt.Sprintf("%d options", n))
		} else {
			parts = append(parts, "no known replacement")
		}
	}
	parts = append(parts, sites(ch))
	if open != nil && ch.ID == openID {
		parts = append(parts, fmt.Sprintf("open PR #%d", open.Number))
	}
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), " · ")
}

// carries sums up a catch-up PR: "fix 2 security · 1 API · move off 1
// deprecated · remove 3 unused · 4 in later stages · 2 to look at", or that
// it only sets tend up.
func carries(b *core.Batch) string {
	var sec, api, dep int
	for _, ch := range b.Changes {
		switch {
		case ch.API != nil:
			api++
		case ch.Kind == core.Security:
			sec++
		default:
			dep++
		}
	}
	var parts []string
	for _, x := range []struct {
		n    int
		text string
	}{
		{sec, "fix %d security"},
		{api, "%d API"},
		{dep, "move off %d deprecated"},
		{len(b.Unused), "remove %d unused"},
		{len(b.Next), "%d in later stages"},
		{len(b.Kept), "%d to look at"},
	} {
		if x.n > 0 {
			parts = append(parts, fmt.Sprintf(x.text, x.n))
		}
	}
	if len(parts) == 0 {
		return "nothing to catch up on; adds tend.yml"
	}
	return strings.Join(parts, " · ")
}

// advisory is the first advisory id the evidence cites.
func advisory(ch *core.Change) string {
	for _, ev := range ch.Evidence {
		if ev.Source != "osv" {
			continue
		}
		if f := strings.Fields(ev.Title); len(f) > 0 {
			return f[0]
		}
	}
	return ""
}

// apiWhat turns "response field removed" and a field into "field total_cents removed".
func apiWhat(a *core.APIChange) string {
	words := strings.Fields(a.What)
	if a.Field == "" || len(words) < 2 {
		return a.What
	}
	return words[len(words)-2] + " " + a.Field + " " + words[len(words)-1]
}

func sites(ch *core.Change) string {
	n := len(ch.Sites)
	switch {
	case n == 1 && ch.Sites[0].Chain == nil && len(ch.Deps) > 0:
		return fmt.Sprintf("%s:%d", ch.Sites[0].File, ch.Sites[0].Line)
	case n == 1:
		return "1 site"
	case n > 1:
		return fmt.Sprintf("%d sites", n)
	}
	return ""
}

type checkOut struct {
	Repo      string           `json:"repo"`
	Base      string           `json:"base"`
	Queue     []core.Change    `json:"queue"`
	Dropped   []plan.Drop      `json:"dropped"`
	Untracked []plan.Untracked `json:"untracked"`
	Open      *core.PR         `json:"open"`
	Notes     []string         `json:"notes"`
}

func checkJSON(repo, base string, p *plan.Plan) checkOut {
	return checkOut{
		Repo:      repo,
		Base:      base,
		Queue:     list(p.Queue),
		Dropped:   list(p.Dropped),
		Untracked: list(p.Untracked),
		Open:      p.Open,
		Notes:     list(p.Notes),
	}
}

// list makes a nil slice print as [] rather than null.
func list[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// writeJSON prints v with camelCase keys throughout and without the empty
// strings, nulls and zero times the shared types carry, so the output reads as
// one schema.
func writeJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var x any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&x); err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(tidy(x, true))
}

// data are the maps whose keys are content, not field names.
var data = map[string]bool{"Renames": true, "Imports": true, "Paths": true, "Fields": true, "Text": true}

const zeroTime = "0001-01-01T00:00:00Z"

func tidy(x any, top bool) any {
	switch v := x.(type) {
	case map[string]any:
		res := map[string]any{}
		for k, val := range v {
			if !top && (val == nil || val == "" || val == zeroTime) {
				continue
			}
			if data[k] {
				res[camel(k)] = val
				continue
			}
			res[camel(k)] = tidy(val, false)
		}
		return res
	case []any:
		for i := range v {
			v[i] = tidy(v[i], false)
		}
		return v
	}
	return x
}

// camel lowercases a leading run of capitals: ID -> id, ToName -> toName,
// API -> api, NodeID -> nodeID.
func camel(s string) string {
	r := []rune(s)
	n := 0
	for n < len(r) && unicode.IsUpper(r[n]) {
		n++
	}
	if n > 1 && n < len(r) {
		n--
	}
	for i := range n {
		r[i] = unicode.ToLower(r[i])
	}
	return string(r)
}
