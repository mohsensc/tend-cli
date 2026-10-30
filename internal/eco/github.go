package eco

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/ver"
)

// githubPages caps how many pages of 100 releases githubMeta reads.
const githubPages = 10

// errGitHubLimit is GitHub refusing a request for its rate limit. It says
// nothing about the repo: the answer is unknown, to be asked again later.
var errGitHubLimit = errors.New("GitHub rate limit")

// githubUsed is the versions of each GitHub repo, owner/repo, that Deps found
// in use, so githubMeta can stop paging releases once it has passed them.
var githubUsed learned[string]

type githubRelease struct {
	Tag       string    `json:"tag_name"`
	Published time.Time `json:"published_at"`
	Pre       bool      `json:"prerelease"`
}

// githubMeta reads a GitHub repo's version tags as its releases, dated and
// flagged from its release list where it has one, and an archived repo as a
// deprecated package. It also returns the tags, name to commit.
func githubMeta(ctx context.Context, f core.Fetcher, repo string) (*core.Meta, map[string]string, error) {
	api := "https://api.github.com/repos/" + repo
	tags, err := actionTags(ctx, f, api+"/tags?per_page=100")
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", repo, err)
	}
	if tags == nil {
		return nil, nil, fmt.Errorf("%s: not found on GitHub", repo)
	}
	// release dates and flags are extras; the tags alone make a usable
	// listing, unless the limit hid them
	rels, err := githubReleases(ctx, f, api, githubReached(repo))
	if errors.Is(err, errGitHubLimit) {
		return nil, nil, fmt.Errorf("%s: %w", repo, err)
	}
	released := map[string]int{}
	for i, r := range rels {
		if _, ok := released[r.Tag]; !ok {
			released[r.Tag] = i
		}
	}
	res := &core.Meta{Name: repo, Repo: "https://github.com/" + repo}
	for tag := range tags {
		if _, ok := ver.Parse(tag); !ok {
			continue
		}
		r := core.Release{Version: tag, Pre: isPre(tag) || semverPre(tag)}
		if i, ok := released[tag]; ok {
			r.Time, r.Pre = rels[i].Published, r.Pre || rels[i].Pre
		}
		res.Releases = append(res.Releases, r)
	}
	sortReleases(res.Releases)
	for _, r := range slices.Backward(res.Releases) {
		if !r.Pre {
			res.Latest = r.Version
			break
		}
	}
	archived, err := githubArchived(ctx, f, api)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: archived: %w", repo, err)
	}
	if archived {
		res.Deprecated = "repository archived"
	}
	return res, tags, nil
}

// githubArchived reports whether the repo is archived. Only a rate limit is
// an error: any other failure leaves the repo as it was, not archived.
func githubArchived(ctx context.Context, f core.Fetcher, api string) (bool, error) {
	got, err := f.Get(ctx, api)
	if err != nil {
		return false, nil
	}
	if err := GitHubLimited(got); err != nil {
		return false, err
	}
	var info actionRepo
	if got.Status != 200 || json.Unmarshal(got.Body, &info) != nil {
		return false, nil
	}
	return info.Archived, nil
}

// githubReleases pages through the repo's releases, newest first, until stop
// holds for one of them, the list ends, or githubPages pages are read.
func githubReleases(ctx context.Context, f core.Fetcher, api string, stop func(tag string) bool) ([]githubRelease, error) {
	return GitHubPages(ctx, f, api+"/releases?per_page=100", githubPages, func(list []githubRelease) bool {
		return slices.ContainsFunc(list, func(r githubRelease) bool { return stop(r.Tag) })
	})
}

// GitHubPages reads a GitHub list API at url and the pages its Link headers
// name, until stop holds for a page, the list ends, or max pages are read.
// Release lists come newest first, so stop is "reached the version in use":
// the pages past it hold nothing a bump can move to. It returns what it read
// before a failure along with the failure; a rate limit is errGitHubLimit.
func GitHubPages[T any](ctx context.Context, f core.Fetcher, url string, max int, stop func([]T) bool, opts ...core.FetchOpt) ([]T, error) {
	var res []T
	next := url
	for page := 0; next != "" && page < max; page++ {
		got, err := f.Get(ctx, next, opts...)
		if err != nil {
			return res, err
		}
		if err := GitHubLimited(got); err != nil {
			return res, err
		}
		if got.Status != 200 {
			return res, fmt.Errorf("%s: HTTP %d", next, got.Status)
		}
		var list []T
		if err := json.Unmarshal(got.Body, &list); err != nil {
			return res, fmt.Errorf("%s: %w", next, err)
		}
		res = append(res, list...)
		if stop(list) {
			break
		}
		next = nextPageLink("", http.Header(got.Header).Get("Link"))
	}
	return res, nil
}

// githubReached holds for a tag at or below the oldest version of repo in
// use: the releases past it are those a bump can move to. With none known it
// never holds.
func githubReached(repo string) func(tag string) bool {
	var low ver.V
	found := false
	for _, u := range githubUsed.get(repo) {
		if v, ok := ver.Parse(u); ok && (!found || ver.Compare(v, low) < 0) {
			low, found = v, true
		}
	}
	return func(tag string) bool {
		v, ok := ver.Parse(tag)
		return found && ok && ver.Compare(v, low) <= 0
	}
}

// GitHubLimited reads GitHub's rate-limit headers. A 429, or a 403 with no
// requests left, a Retry-After or a message that says so, is the limit and
// not an answer about the repo.
func GitHubLimited(res *core.Response) error {
	h := http.Header(res.Header)
	switch {
	case res.Status == 429:
	case res.Status == 403 && (h.Get("X-Ratelimit-Remaining") == "0" || h.Get("Retry-After") != "" ||
		strings.Contains(strings.ToLower(string(res.Body)), "rate limit")):
	default:
		return nil
	}
	when := "later"
	if n, err := strconv.Atoi(h.Get("Retry-After")); err == nil {
		when = "in " + (time.Duration(n) * time.Second).String()
	} else if n, err := strconv.ParseInt(h.Get("X-Ratelimit-Reset"), 10, 64); err == nil {
		when = "after " + time.Unix(n, 0).UTC().Format(time.RFC3339)
	}
	return fmt.Errorf("%w (HTTP %d): try again %s", errGitHubLimit, res.Status, when)
}
