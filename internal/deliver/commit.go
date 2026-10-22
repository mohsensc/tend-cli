package deliver

import (
	"context"
	"errors"
	"fmt"

	"github.com/mohsensc/tend-cli/internal/verify"
)

// ErrMoved means the branch moved on the remote while tend worked. Nothing
// was pushed: the tree on top of the new tip was never proven.
var ErrMoved = errors.New("the branch moved while tend worked")

// Commit pushes the worktree's commits to branch on r and returns the sha
// that landed. The push must fast-forward from the commit the worktree was
// made from.
func Commit(ctx context.Context, wt *verify.Worktree, r *Remote, branch string) (string, error) {
	err := Push(ctx, wt, r, branch, false)
	if errors.Is(err, ErrRejected) {
		return "", fmt.Errorf("%w: %s on %s", ErrMoved, branch, r.Name)
	}
	if err != nil {
		return "", err
	}
	return head(ctx, wt, "HEAD")
}
