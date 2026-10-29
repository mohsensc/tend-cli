package deliver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/plan"
)

// closedByTend ends every comment tend closes a PR with, so a later run, even
// one without state, tells tend's own closing from a maintainer's.
const closedByTend = "<!-- tend closed -->"

// Closer is a forge that can say who closed a PR, and when.
type Closer interface {
	// ClosedBy is the login that closed PR number and when, "" and the zero
	// time for what the forge does not say.
	ClosedBy(ctx context.Context, number int) (string, time.Time, error)
}

// Close comments msg on the PR and closes it on tend's own account: to keep
// one PR open, to make room for security work, or because the change is no
// longer needed. Such a PR is not a rejection.
func Close(ctx context.Context, forge core.Forge, number int, msg string) error {
	if err := forge.Comment(ctx, number, msg+"\n\n"+closedByTend); err != nil {
		return err
	}
	return forge.ClosePR(ctx, number)
}

// Replaced points a PR tend closed at the one that took its place. It keeps
// the mark, so the PR still reads as closed by tend.
func Replaced(ctx context.Context, forge core.Forge, number, by int) error {
	return forge.Comment(ctx, number, fmt.Sprintf("Replaced by #%d.", by)+"\n\n"+closedByTend)
}

// Declined reports whether pr was closed unmerged as a maintainer's no, which
// outlives tend's own state: someone with write access commented `tend
// skip`, or a person, neither tend nor a bot, closed it. tend may run with a
// person's token, so the close is tend's when it came right after tend's
// closing comment, from the account that posted it.
func Declined(ctx context.Context, forge core.Forge, pr *core.PR) (bool, error) {
	if pr == nil || pr.State != "closed" {
		return false, nil
	}
	cs, err := forge.Comments(ctx, pr.Number)
	if err != nil {
		return false, err
	}
	if c, ok := plan.ParseCommand(cs); ok && c.Verb == "skip" {
		return true, nil
	}
	cl, ok := forge.(Closer)
	if !ok {
		return !tendClosed(cs, "", time.Time{}), nil
	}
	who, at, err := cl.ClosedBy(ctx, pr.Number)
	if err != nil {
		return false, err
	}
	if tendClosed(cs, who, at) {
		return false, nil
	}
	return who != "" && !plan.Bot(who), nil
}

// closeWindow is how long after its closing comment tend's close may land.
const closeWindow = 2 * time.Minute

// tendClosed reports whether the close by who at at is tend's own: the last
// closing comment tend posted by then is who's, and came just before it.
// Who or at left unsaid matches any. What anyone says after the close does
// not change whose it was; a reopen and a later close does.
func tendClosed(cs []core.Comment, who string, at time.Time) bool {
	for i := len(cs) - 1; i >= 0; i-- {
		c := cs[i]
		if !strings.Contains(c.Body, closedByTend) || !at.IsZero() && c.At.After(at) {
			continue
		}
		return (who == "" || strings.EqualFold(c.Author, who)) && (at.IsZero() || at.Sub(c.At) <= closeWindow)
	}
	return false
}
