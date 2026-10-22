package deliver

import (
	"context"
	"fmt"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/plan"
)

// closedByTend ends every comment tend closes a PR with, so a later run, even
// one without state, tells tend's own closing from a maintainer's.
const closedByTend = "<!-- tend closed -->"

// Closer is a forge that can say who closed a PR.
type Closer interface {
	// ClosedBy is the login that closed PR number, "" when the forge does not say.
	ClosedBy(ctx context.Context, number int) (string, error)
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
// skip`, or a person, neither tend nor a bot, closed it.
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
	if tendClosed(cs) {
		return false, nil
	}
	cl, ok := forge.(Closer)
	if !ok {
		return true, nil
	}
	who, err := cl.ClosedBy(ctx, pr.Number)
	if err != nil {
		return false, err
	}
	return who != "" && !plan.Bot(who), nil
}

// tendClosed reports whether tend's closing comment is the last word on the
// PR. What bots say after it, a CI report say, does not change that.
func tendClosed(cs []core.Comment) bool {
	for i := len(cs) - 1; i >= 0; i-- {
		if strings.Contains(cs[i].Body, closedByTend) {
			return true
		}
		if !plan.Bot(cs[i].Author) {
			return false
		}
	}
	return false
}
