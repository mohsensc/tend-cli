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
	"github.com/mohsensc/tend-cli/internal/deliver"
	"github.com/mohsensc/tend-cli/internal/plan"
)

// nothing is what a repo with nothing tend follows gets told, and what
// tend.yml left out of the search.
func (s *session) nothing() string {
	msg := "nothing to watch: no manifests or API calls found in " + s.root
	if s.cfg != nil && len(s.cfg.Ecosystems) > 0 {
		msg += "; tend.yml's ecosystems lists only " + strings.Join(s.cfg.Ecosystems, ", ")
	}
	return msg
}

func writeCheck(w io.Writer, s *session, p *plan.Plan, empty bool) error {
	if empty {
		_, err := fmt.Fprintf(w, "tend: %s\n", s.nothing())
		return err
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "tend: %d queued, %d skipped, %s\n", len(p.Queue), len(p.Dropped), openSummary(p.Open, s.openWhy))
	if s.o.quiet {
		_, err := w.Write(b.Bytes())
		return err
	}
	openID := plan.OpenID(p.Open, s.st)
	var now time.Time
	if s.e.Now != nil {
		now = s.e.Now()
	}
	if len(p.Queue) > 0 {
		b.WriteString("\n")
		tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		for i, ch := range p.Queue {
			fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\t%s\n", i+1, ch.Kind, subject(&ch), target(&ch), detail(&ch, openID, p.Open, now))
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

// openSummary is the header's word on open PRs: unknown, and why, when
// unknown says why they could not be listed.
func openSummary(pr *core.PR, unknown string) string {
	switch {
	case unknown != "":
		return "open PRs unknown (" + unknown + ")"
	case pr == nil:
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
	if ch.Kind == core.First && ch.Batch != nil && len(deliver.Tally(ch.Batch)) == 0 {
		return "set up tend"
	}
	if ch.Kind == core.First {
		return "catch-up"
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
		var files []string
		for _, x := range ch.Deps {
			if !slices.Contains(files, x.Manifest.Path) {
				files = append(files, x.Manifest.Path)
			}
		}
		return d.Display() + " (" + strings.Join(files, ", ") + ")"
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

func detail(ch *core.Change, openID string, open *core.PR, now time.Time) string {
	var parts []string
	if b := ch.Batch; b != nil {
		parts = append(parts, carries(b))
		if open != nil && ch.ID == openID {
			parts = append(parts, fmt.Sprintf("open PR #%d", open.Number))
		}
		return strings.Join(parts, " · ")
	}
	if ch.Kind == core.Security {
		parts = append(parts, advisory(ch))
	}
	if a := ch.API; a != nil {
		parts = append(parts, apiWhat(a))
	}
	if !ch.Sunset.IsZero() {
		eol := slices.ContainsFunc(ch.Evidence, func(ev core.Evidence) bool { return ev.Source == "eol" })
		label := "sunset"
		switch {
		case eol && ch.Sunset.Before(now):
			label = "EOL since"
		case eol:
			label = "EOL"
		case ch.Sunset.Before(now):
			label = "sunset passed"
		}
		parts = append(parts, label+" "+ch.Sunset.UTC().Format(time.DateOnly))
	}
	if ch.To == "" && ch.API == nil {
		parts = append(parts, ways(ch))
	}
	parts = append(parts, sites(ch))
	if open != nil && ch.ID == openID {
		parts = append(parts, fmt.Sprintf("open PR #%d", open.Number))
	}
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), " · ")
}

// carries sums up a catch-up PR the way its title does, "2 end-of-life
// runtimes · 1 deprecated Action · 3 unused dependencies", then what waits
// for later PRs and what it keeps, or that it only sets tend up.
func carries(b *core.Batch) string {
	parts := deliver.Tally(b)
	if n := len(b.Next); n > 0 {
		parts = append(parts, fmt.Sprintf("%d more in later PRs", n))
	}
	if n := len(b.Kept); n > 0 {
		parts = append(parts, fmt.Sprintf("%d possibly unused, kept", n))
	}
	if len(parts) == 0 {
		return "nothing to catch up on; adds tend.yml"
	}
	return strings.Join(parts, " · ")
}

// ways says what a deprecation without a target can move to: the options
// that move it, not the one that stays.
func ways(ch *core.Change) string {
	var moves []core.Option
	for _, o := range ch.Options {
		if !o.Stay {
			moves = append(moves, o)
		}
	}
	switch len(moves) {
	case 0:
		for _, ev := range ch.Evidence {
			if ev.Source == "registry" && strings.TrimSpace(ev.Text) != "" {
				return "deprecated: " + core.Clip(strings.Join(strings.Fields(ev.Text), " "), 80)
			}
		}
		return "no known replacement"
	case 1:
		if to := strings.TrimSpace(moves[0].ToName + " " + moves[0].To); to != "" {
			return "1 option: → " + to
		}
		return "1 option: " + moves[0].Title
	}
	return fmt.Sprintf("%d options", len(moves))
}

// advisory is the first advisory the evidence cites and its severity, and
// how many more there are, as the PR's title counts them.
func advisory(ch *core.Change) string {
	ids := deliver.Advisories(ch)
	if len(ids) == 0 {
		return ch.Severity
	}
	res := strings.TrimSpace(ids[0] + " " + ch.Severity)
	if n := len(ids) - 1; n > 0 {
		res += fmt.Sprintf(" +%d more", n)
	}
	return res
}

// apiWhat turns "response field removed" and a field into "field
// total_cents removed", and a type change into "field total_cents changed
// type".
func apiWhat(a *core.APIChange) string {
	words := strings.Fields(a.What)
	if a.Field == "" || len(words) < 2 {
		return a.What
	}
	if strings.HasSuffix(a.What, "type changed") {
		return "field " + a.Field + " changed type"
	}
	return words[len(words)-2] + " " + a.Field + " " + words[len(words)-1]
}

func sites(ch *core.Change) string {
	n := core.Lines(ch.Sites)
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
