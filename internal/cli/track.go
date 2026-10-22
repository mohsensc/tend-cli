package cli

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/plan"
)

// trackMarker finds tend's tracking issue among the repository's issues.
const trackMarker = "<!-- tend tracking -->"

const trackTitle = "tend: what it could not do on its own"

// track keeps tend's one tracking issue in step with what tend could not
// do on base: opened when there is something to list, edited when the list
// changes, closed when it empties, and opened again when it fills. It asks
// the forge nothing while the list is what it last wrote.
func (s *session) track(ctx context.Context, p *plan.Plan, base string) {
	tr, ok := s.forge.(core.Tracker)
	if !ok || s.o.dryRun {
		return
	}
	lines := s.undone(p, base)
	open := len(lines) > 0
	body := issueBody(lines)
	is := &s.st.Issue
	if is.Number != 0 && is.Body == body && is.Open == open {
		return
	}
	if is.Number == 0 {
		found, err := tr.FindIssue(ctx, trackMarker)
		if err != nil {
			s.logf("cannot look for the tracking issue: %v", err)
			return
		}
		if found != nil {
			is.Number, is.Body, is.Open = found.Number, found.Body, found.Open
		}
	}
	switch {
	case is.Number == 0 && !open:
	case is.Number == 0:
		made, err := tr.CreateIssue(ctx, trackTitle, body, s.cfg.Deliver.Labels)
		if err != nil {
			s.logf("cannot open the tracking issue: %v", err)
			return
		}
		is.Number, is.Body, is.Open = made.Number, body, true
		s.logf("opened #%d, which lists what tend could not do on its own", made.Number)
	case is.Body != body || is.Open != open:
		if err := tr.EditIssue(ctx, is.Number, body, open); err != nil {
			s.logf("cannot update the tracking issue #%d: %v", is.Number, err)
			return
		}
		if is.Open && !open {
			s.logf("closed #%d: everything it listed is done", is.Number)
		}
		is.Body, is.Open = body, open
	}
}

// undone lists, a line each with why, what tend tried on base and could
// not prove or make, what it pushed and took back after CI failed, and the
// deprecated packages it knows no replacement for.
func (s *session) undone(p *plan.Plan, base string) []string {
	var res []string
	for _, id := range slices.Sorted(maps.Keys(s.st.Seen)) {
		if x := s.st.Seen[id]; x.Verdict == "unsupported" && base != "" && x.Base == base {
			first, _, _ := strings.Cut(x.Detail, "\n")
			res = append(res, fmt.Sprintf("- `%s`: could not be made or proven on %s: %s", id, core.Short(base), strings.TrimSuffix(first, ".")))
		}
	}
	for _, id := range slices.Sorted(maps.Keys(s.st.Parked)) {
		if x := s.st.Parked[id]; x.Reason == "reverted" {
			res = append(res, fmt.Sprintf("- `%s`: %s", id, x.Detail))
		}
	}
	if p != nil {
		var gone []core.Dep
		for _, ch := range p.Queue {
			if ch.Batch != nil {
				gone = append(gone, ch.Batch.Unused...)
			}
		}
		for _, ch := range p.Queue {
			if slices.ContainsFunc(gone, func(d core.Dep) bool { return len(ch.Deps) > 0 && d.Name == ch.Deps[0].Name }) {
				continue
			}
			if ch.API == nil && ch.To == "" && ch.ToName == "" && len(ch.Options) == 0 && len(ch.Deps) > 0 {
				res = append(res, fmt.Sprintf("- `%s`: deprecated, and tend knows no replacement for it, so it stays", ch.Deps[0].Name))
			}
		}
	}
	return res
}

// issueBody is the tracking issue's text for lines, or the line that
// closes it when there are none.
func issueBody(lines []string) string {
	if len(lines) == 0 {
		return "Everything tend listed here is done or no longer needed.\n\n" + trackMarker + "\n"
	}
	return "tend keeps this list of what it could not do on its own, each with why. It tries each again when the code or the upstream release moves on, and takes a line off once it is done; nothing here waits on an answer.\n\n" +
		strings.Join(lines, "\n") + "\n\n" + trackMarker + "\n"
}
