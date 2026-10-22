package plan

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/eco"
	"github.com/mohsensc/tend-cli/internal/state"
	"github.com/mohsensc/tend-cli/internal/ver"
)

// drop says why a change gets no PR, or "" when it does.
func (b *builder) drop(ch *core.Change) (reason, detail string) {
	cfg, st := b.in.Cfg, b.in.State
	for _, d := range subjects(ch) {
		if why, ok := cfg.Ignored(d, ch.To); ok {
			return "ignored", why
		}
	}
	if !cfg.Wants(ch.Kind, ch.Level) {
		return "noise", noise(ch)
	}
	cd := time.Duration(cfg.Cooldown)
	if cooling(ch) && b.in.Now.Sub(ch.Published) < cd {
		return "cooldown", fmt.Sprintf("released %s, cooldown is %s", ch.Published.UTC().Format(time.DateOnly), cfg.Cooldown)
	}
	if p, ok := parked(st, ch); ok {
		if p.Reason == "reverted" {
			return "parked", p.Detail
		}
		return "parked", fmt.Sprintf("#%d was closed without merging", p.PR)
	}
	if why := transitiveOnly(ch); why != "" {
		return "unsupported", why
	}
	if st != nil {
		if s, ok := st.Seen[ch.ID]; ok && s.Verdict == "unsupported" && s.Base != "" && s.Base == b.in.Base {
			return "unsupported", s.Detail
		}
	}
	return "", ""
}

// cooling says whether the cooldown applies: to a release tend would move to
// only because it is newer. A security fix, a deprecation and an API change
// that is already live do not wait.
func cooling(ch *core.Change) bool {
	return ch.Kind != core.Security && ch.Kind != core.Deprecation && ch.API == nil && ch.To != "" && !ch.Published.IsZero()
}

func noise(ch *core.Change) string {
	switch ch.Kind {
	case core.Update:
		if n := len(ch.Sites); n > 0 {
			return fmt.Sprintf("%s, %s but no security, deprecation or breaking change", ch.Level, plural(n, "site"))
		}
		return ch.Level + ", 0 sites"
	case core.Deprecation:
		return "updates.deprecations is off"
	}
	return "updates.security is off"
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// subjects are what ignore rules match: the deps, or the API provider.
func subjects(ch *core.Change) []core.Dep {
	if ch.API != nil {
		return []core.Dep{{Ecosystem: "api", Name: ch.API.Provider}}
	}
	return ch.Deps
}

// parked finds a rejection of this change at the same or a newer target, a
// maintainer's or that of the CI that made tend revert it. A preempted
// change is not parked: it comes back by itself.
func parked(st *state.State, ch *core.Change) (state.Parked, bool) {
	if st == nil {
		return state.Parked{}, false
	}
	var res state.Parked
	found := false
	for id, p := range st.Parked {
		if p.Reason == "preempted" || core.Subject(id) != core.Subject(ch.ID) || ver.Less(p.To, ch.To) {
			continue
		}
		if !found || p.PR > res.PR {
			res, found = p, true
		}
	}
	return res, found
}

// transitiveOnly explains why a bump of only transitive deps cannot be made in
// ecosystems without a command that moves one locked package by itself.
func transitiveOnly(ch *core.Change) string {
	if ch.API != nil || len(ch.Deps) == 0 {
		return ""
	}
	for _, d := range ch.Deps {
		if d.Direct {
			return ""
		}
	}
	d := ch.Deps[0]
	if e := eco.ByName(d.Ecosystem); e == nil || e.Traits().Transitive {
		return ""
	}
	return fmt.Sprintf("%s is only a transitive dependency and %s cannot move it alone; declare it directly at %s to override", d.Name, d.Ecosystem, ch.To)
}

// Stale says why the change behind an open PR is no longer wanted, or ""
// while it may still be: the plan dropped it for good, or the dep it moves is
// gone or already at the target. A change that is merely missing, say
// because a registry lookup failed, is not stale.
func Stale(p *Plan, inv *core.Inventory, id string) string {
	if id == "" {
		return ""
	}
	for _, d := range p.Dropped {
		if core.Subject(d.Change.ID) != core.Subject(id) {
			continue
		}
		switch d.Reason {
		case "ignored":
			return "tend.yml ignores it now (" + d.Detail + ")"
		case "noise":
			return "it no longer gets a PR (" + d.Detail + ")"
		case "unsupported":
			first, _, _ := strings.Cut(d.Detail, "\n")
			return "tend can no longer carry it through: " + first
		}
	}
	eco, name, ok := strings.Cut(core.Subject(id), "/")
	if !ok || eco == "api" || strings.HasPrefix(name, "group:") {
		return ""
	}
	name, _, swap := strings.Cut(name, "->")
	var deps []core.Dep
	for _, d := range inv.Deps {
		if d.Ecosystem == eco && d.Name == name {
			deps = append(deps, d)
		}
	}
	if len(deps) == 0 {
		return name + " is no longer a dependency"
	}
	to, ok := ver.Parse(core.TargetOf(id))
	if swap || !ok {
		return ""
	}
	for _, d := range deps {
		if v, ok := ver.Parse(Current(d)); !ok || ver.Compare(v, to) < 0 {
			return ""
		}
	}
	return fmt.Sprintf("%s is at %s already", name, Current(deps[0]))
}

// OpenID is the change id behind tend's open PR: from state when it tracks
// that PR, else from the marker in the PR body.
func OpenID(pr *core.PR, st *state.State) string {
	if pr == nil {
		return ""
	}
	if st != nil && st.Open != nil && st.Open.PR == pr.Number && st.Open.ID != "" {
		return st.Open.ID
	}
	if m := marker.FindStringSubmatch(pr.Body); m != nil {
		return m[1]
	}
	return ""
}

var marker = regexp.MustCompile(`<!-- tend id="([^"]+)"`)

// Pick chooses what this run works on. At most one of pick and wait is set;
// preempt is the open PR the pick displaces: any other PR when the first PR
// is due, which folds its change in, a non-security PR when security work
// arrives, or the same change at an older target.
func Pick(p *Plan, st *state.State) (pick *core.Change, preempt *core.PR, wait *core.PR) {
	if len(p.Queue) == 0 {
		return nil, nil, p.Open
	}
	if p.Open == nil {
		return &p.Queue[0], nil, nil
	}
	id := OpenID(p.Open, st)
	open := find(p.Queue, func(ch *core.Change) bool { return ch.ID == id })
	if open == nil {
		open = find(p.Queue, func(ch *core.Change) bool { return core.Subject(ch.ID) == core.Subject(id) })
	}
	top := &p.Queue[0]
	switch {
	case top.Kind == core.First && id != top.ID:
		return top, p.Open, nil
	case top.Kind == core.Security && (open == nil || open.Kind != core.Security):
		return top, p.Open, nil
	case open == nil:
		return nil, nil, p.Open
	case open.ID != id:
		return open, p.Open, nil
	}
	return open, nil, nil
}

func find(chs []core.Change, ok func(*core.Change) bool) *core.Change {
	for i := range chs {
		if ok(&chs[i]) {
			return &chs[i]
		}
	}
	return nil
}

// Command is a maintainer's instruction left as a PR comment.
type Command struct {
	Verb   string // apply|skip|retry
	N      int    // the option for apply
	Author string
	At     time.Time
}

var command = regexp.MustCompile(`^tend (apply ([1-9][0-9]*)|skip|retry)$`)

var trusted = []string{"OWNER", "MEMBER", "COLLABORATOR"}

// ParseCommand returns the newest command from a person with write access to
// the repo. Anyone else's comments, and every bot's, are ignored, whatever
// they say.
func ParseCommand(cs []core.Comment) (Command, bool) {
	var res Command
	found := false
	for _, c := range cs {
		if !slices.Contains(trusted, strings.ToUpper(c.Role)) || Bot(c.Author) {
			continue
		}
		m := command.FindStringSubmatch(strings.TrimSpace(c.Body))
		if m == nil || found && c.At.Before(res.At) {
			continue
		}
		res = Command{Verb: strings.Fields(m[1])[0], Author: c.Author, At: c.At}
		if m[2] != "" {
			res.N, _ = strconv.Atoi(m[2])
		}
		found = true
	}
	return res, found
}

// Bot reports whether login is an app's account, such as dependabot[bot] or
// github-actions[bot], rather than a person's.
func Bot(login string) bool {
	return strings.HasSuffix(strings.ToLower(login), "[bot]")
}
