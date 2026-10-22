package source

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/eco"
	"github.com/mohsensc/tend-cli/internal/fetch"
)

type repoInfo struct {
	FullName string `json:"full_name"`
	URL      string `json:"html_url"`
	Archived bool   `json:"archived"`
	Pushed   string `json:"pushed_at"`
}

// Archived asks GitHub which of repos, source repository URLs as registries
// give them, are archived, and returns what to cite for each one that is,
// keyed by the URL as given. A repo that is gone or private is left out. A
// rate limit says nothing either way: the repos it hid are left out too, and
// the error says so.
func Archived(ctx context.Context, f core.Fetcher, repos []string) (map[string]core.Evidence, error) {
	var paths []string
	pathOf := map[string]string{}
	for _, r := range repos {
		owner, name, ok := ParseRepo(r)
		if !ok {
			continue
		}
		p := owner + "/" + name
		if _, seen := pathOf[r]; !seen {
			pathOf[r] = p
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	got := make([]*repoInfo, len(paths))
	errs := make([]error, len(paths))
	Parallel(len(paths), func(i int) {
		got[i], errs[i] = repo(ctx, f, paths[i])
	})
	infos := map[string]*repoInfo{}
	var first error
	for i, p := range paths {
		if errs[i] != nil {
			first = cmp.Or(first, errs[i])
			continue
		}
		infos[p] = got[i]
	}
	res := map[string]core.Evidence{}
	for r, p := range pathOf {
		if info := infos[p]; info != nil && info.Archived {
			res[r] = core.Evidence{
				Source: "archived",
				Title:  info.FullName + " is archived",
				Text:   fmt.Sprintf("%s is read-only. Last push: %s.", info.FullName, core.Clip(info.Pushed, 10)),
				URL:    info.URL,
			}
		}
	}
	return res, first
}

// repo returns nil for a repo that is gone or private, and an error for a
// rate limit.
func repo(ctx context.Context, f core.Fetcher, path string) (*repoInfo, error) {
	resp, err := f.Get(ctx, "https://api.github.com/repos/"+path, ghOpts(fetch.TTL(day))...)
	if err != nil {
		return nil, err
	}
	if err := eco.GitHubLimited(resp); err != nil {
		return nil, fmt.Errorf("archived: %s: %w", path, err)
	}
	if resp.Status != 200 {
		return nil, nil
	}
	info := &repoInfo{}
	if err := json.Unmarshal(resp.Body, info); err != nil {
		return nil, fmt.Errorf("archived: %s: %w", path, err)
	}
	return info, nil
}
