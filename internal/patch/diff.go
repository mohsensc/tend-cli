package patch

import (
	"context"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/gitx"
)

// Diff returns the staged diff of wt against base and the files it touches, sorted.
// Prefixes are forced so a user's diff.noprefix or diff.mnemonicPrefix cannot change the output.
func Diff(ctx context.Context, wt, base string) (string, []string, error) {
	diff, err := gitx.Raw(ctx, wt, "diff", "--cached", "--no-renames", "--no-color", "--no-ext-diff",
		"--no-textconv", "--src-prefix=a/", "--dst-prefix=b/", base, "--")
	if err != nil {
		return "", nil, err
	}
	names, err := gitx.Raw(ctx, wt, "diff", "--cached", "--no-renames", "--name-only", "-z", base, "--")
	if err != nil {
		return "", nil, err
	}
	var files []string
	for _, f := range strings.Split(names, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	slices.Sort(files)
	return diff, files, nil
}
