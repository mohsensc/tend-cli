package eco

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

func TestGithubLimited(t *testing.T) {
	tests := []struct {
		name   string
		status int
		header map[string][]string
		body   string
		want   string // "" for not a limit
	}{
		{"429", 429, map[string][]string{"Retry-After": {"30"}}, "", "try again in 30s"},
		{"no requests left", 403, map[string][]string{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {"1767225600"}}, "", "try again after 2026-01-01T00:00:00Z"},
		{"secondary limit", 403, nil, `{"message": "You have exceeded a secondary rate limit"}`, "try again later"},
		{"forbidden", 403, map[string][]string{"X-Ratelimit-Remaining": {"4999"}}, `{"message": "Repository access blocked"}`, ""},
		{"not found", 404, nil, `{"message": "Not Found"}`, ""},
		{"ok", 200, map[string][]string{"X-Ratelimit-Remaining": {"0"}}, `{}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := GitHubLimited(&core.Response{Status: tt.status, Header: tt.header, Body: []byte(tt.body)})
			switch {
			case tt.want == "" && err != nil:
				t.Errorf("err = %v, want none", err)
			case tt.want != "" && (!errors.Is(err, errGitHubLimit) || !strings.Contains(err.Error(), tt.want)):
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

// A rate limit on the repo or its releases leaves the answer unknown: Meta
// fails, to be asked again later, rather than reading the repo as live.
func TestGithubMetaLimited(t *testing.T) {
	const api = "https://api.github.com/repos/acme/busy"
	tags := canned{url: api + "/tags?per_page=100", body: `[{"name": "v1.0.0", "commit": {"sha": "a"}}]`}
	limited := map[string][]string{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {"1767225600"}}
	tests := []struct {
		name  string
		stubs []canned
	}{
		{"repo", []canned{tags,
			{url: api + "/releases?per_page=100", body: `[]`},
			{url: api, status: 403, header: limited, body: `{"message": "API rate limit exceeded"}`}}},
		{"releases", []canned{tags,
			{url: api + "/releases?per_page=100", status: 429, body: `{}`}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := githubMeta(context.Background(), replayCanned(t, tt.stubs...), "acme/busy")
			if !errors.Is(err, errGitHubLimit) {
				t.Errorf("err = %v", err)
			}
		})
	}
	t.Run("gone repo is not archived", func(t *testing.T) {
		m, _, err := githubMeta(context.Background(), replayCanned(t, tags,
			canned{url: api + "/releases?per_page=100", body: `[]`},
			canned{url: api, status: 404, body: `{"message": "Not Found"}`}), "acme/busy")
		if err != nil || m.Deprecated != "" {
			t.Errorf("meta = %+v, %v", m, err)
		}
	})
}

// releasePages chains n pages of one release each, v<n>.0.0 down to v1.0.0.
func releasePages(api string, n int) []canned {
	var res []canned
	for i := range n {
		url := api + "/releases?per_page=100"
		if i > 0 {
			url += fmt.Sprintf("&page=%d", i+1)
		}
		c := canned{url: url, body: fmt.Sprintf(`[{"tag_name": "v%d.0.0", "published_at": "2025-01-01T00:00:00Z"}]`, n-i)}
		if i < n-1 {
			c.header = map[string][]string{"Link": {fmt.Sprintf(`<%s/releases?per_page=100&page=%d>; rel="next"`, api, i+2)}}
		}
		res = append(res, c)
	}
	return res
}

func TestGithubReleasesPaging(t *testing.T) {
	ctx := context.Background()
	const api = "https://api.github.com/repos/acme/pages"
	tests := []struct {
		name  string
		pages int
		used  []string
		want  int
	}{
		{"all pages", 3, nil, 3},
		{"stops at the version in use", 5, []string{"v4.0.0", "v3.0.0"}, 3},
		{"stops at the cap", githubPages + 2, nil, githubPages},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := "acme/" + strings.ReplaceAll(tt.name, " ", "-")
			githubUsed.add(repo, tt.used...)
			rels, err := githubReleases(ctx, replayCanned(t, releasePages(api, tt.pages)...), api, githubReached(repo))
			if err != nil || len(rels) != tt.want {
				t.Errorf("read %d releases, %v; want %d", len(rels), err, tt.want)
			}
		})
	}
}

// A bare tag with a hyphen label is a SemVer pre-release, whatever the
// label says, and never the latest over the release it leads up to.
func TestGithubMetaHyphenPre(t *testing.T) {
	const api = "https://api.github.com/repos/acme/tags"
	m, _, err := githubMeta(context.Background(), replayCanned(t,
		canned{url: api + "/tags?per_page=100", body: `[{"name": "v3.0.0-test.1", "commit": {"sha": "b"}}, {"name": "v3.0.0", "commit": {"sha": "c"}}, {"name": "v2.9.0", "commit": {"sha": "a"}}]`},
		canned{url: api + "/releases?per_page=100", body: `[]`},
		canned{url: api, body: `{}`}), "acme/tags")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range m.Releases {
		if r.Pre != (r.Version == "v3.0.0-test.1") {
			t.Errorf("%s: pre = %v", r.Version, r.Pre)
		}
	}
	if m.Latest != "v3.0.0" {
		t.Errorf("latest = %s", m.Latest)
	}
}
