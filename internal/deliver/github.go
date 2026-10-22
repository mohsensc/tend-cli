// Package deliver turns a proven, vetted patch into one pull request or one commit.
package deliver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
)

// writeTimeout is a variable so a test can shorten it.
var writeTimeout = 30 * time.Second

const (
	apiBase    = "https://api.github.com"
	apiVersion = "2022-11-28"
	labelColor = "0e8a16"
	pageSize   = 100
	pageMax    = 10
	waitMax    = time.Minute
)

// GitHub is the core.Forge for github.com: reads through the fetcher, writes with http.
type GitHub struct {
	fetch       core.Fetcher
	http        *http.Client
	token       string
	owner, name string

	api   string
	warn  io.Writer
	sleep func(context.Context, time.Duration) error
	now   func() time.Time
}

var (
	_ core.Forge = (*GitHub)(nil)
	_ Closer     = (*GitHub)(nil)
)

func NewGitHub(f core.Fetcher, hc *http.Client, token, owner, name string) core.Forge {
	if hc == nil {
		hc = &http.Client{}
	}
	return &GitHub{
		fetch: f, http: hc, token: token, owner: owner, name: name,
		api: apiBase, warn: os.Stderr, sleep: sleep, now: time.Now,
	}
}

type pull struct {
	Number   int     `json:"number"`
	NodeID   string  `json:"node_id"`
	HTMLURL  string  `json:"html_url"`
	Title    string  `json:"title"`
	Body     string  `json:"body"`
	State    string  `json:"state"`
	Draft    bool    `json:"draft"`
	MergedAt *string `json:"merged_at"`
	Head     struct {
		Ref  string `json:"ref"`
		Sha  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (p *pull) pr() core.PR {
	state := p.State
	if p.MergedAt != nil {
		state = "merged"
	}
	return core.PR{
		Number: p.Number, NodeID: p.NodeID, URL: p.HTMLURL, Branch: p.Head.Ref, Head: p.Head.Sha,
		Base: p.Base.Ref, Title: p.Title, Body: p.Body, State: state, Draft: p.Draft,
	}
}

func (g *GitHub) repoPath(rest string) string {
	return "/repos/" + g.owner + "/" + g.name + rest
}

func (g *GitHub) Repo(ctx context.Context) (core.RepoInfo, error) {
	var res struct {
		Default string `json:"default_branch"`
	}
	if err := g.do(ctx, "GET", g.repoPath(""), nil, &res); err != nil {
		return core.RepoInfo{}, err
	}
	return core.RepoInfo{Owner: g.owner, Name: g.name, Default: res.Default}, nil
}

// FindPR returns the open PR for branch, else the newest one. Sorting by
// update would let a comment on an old closed PR hide the open one.
func (g *GitHub) FindPR(ctx context.Context, branch string) (*core.PR, error) {
	q := "?head=" + url.QueryEscape(g.owner+":"+branch) + "&state=all&sort=created&direction=desc&per_page=10"
	var res []pull
	if err := g.do(ctx, "GET", g.repoPath("/pulls"+q), nil, &res); err != nil {
		return nil, err
	}
	if len(res) == 0 {
		return nil, nil
	}
	pick := res[0]
	for _, p := range res {
		if p.State == "open" {
			pick = p
			break
		}
	}
	pr := pick.pr()
	return &pr, nil
}

// OpenPRs lists tend's open PRs: from a branch under prefix in this repo, not
// a fork, with tend's marker in the body. A PR someone opens by hand on such
// a branch is theirs, and tend leaves it alone.
func (g *GitHub) OpenPRs(ctx context.Context, prefix string) ([]core.PR, error) {
	all, err := getAll[pull](ctx, g, g.repoPath("/pulls?state=open"))
	if err != nil {
		return nil, err
	}
	var res []core.PR
	for _, p := range all {
		if g.tends(p, prefix) {
			res = append(res, p.pr())
		}
	}
	return res, nil
}

// HadPR looks through the newest thousand PRs, in every state, for one of
// tend's: from a branch under prefix in this repo, with its marker. A
// repository whose last thousand PRs hold none of tend's has not seen tend
// at work in a while either.
func (g *GitHub) HadPR(ctx context.Context, prefix string) (bool, error) {
	for page := 1; page <= pageMax; page++ {
		var got []pull
		p := g.repoPath(fmt.Sprintf("/pulls?state=all&sort=created&direction=desc&per_page=%d&page=%d", pageSize, page))
		if err := g.do(ctx, "GET", p, nil, &got); err != nil {
			return false, err
		}
		for _, x := range got {
			if g.tends(x, prefix) {
				return true, nil
			}
		}
		if len(got) < pageSize {
			break
		}
	}
	return false, nil
}

// tends reports whether p is one of tend's PRs: from a branch under prefix in
// this repo, not a fork, with tend's marker in the body.
func (g *GitHub) tends(p pull, prefix string) bool {
	_, _, _, ok := ParseMarker(p.Body)
	return ok && strings.HasPrefix(p.Head.Ref, prefix) && strings.EqualFold(p.Head.Repo.FullName, g.owner+"/"+g.name)
}

func (g *GitHub) CreatePR(ctx context.Context, in core.PRInput) (*core.PR, error) {
	body := map[string]any{"title": in.Title, "head": in.Branch, "base": in.Base, "body": in.Body, "draft": in.Draft}
	var p pull
	if err := g.do(ctx, "POST", g.repoPath("/pulls"), body, &p); err != nil {
		if isStatus(err, http.StatusForbidden) && strings.Contains(err.Error(), "not permitted to create or approve pull requests") {
			err = fmt.Errorf(`%w; turn on "Allow GitHub Actions to create and approve pull requests" under Settings > Actions > General, or pass a fine-grained token as the token input`, err)
		}
		return nil, err
	}
	pr := p.pr()
	if err := g.label(ctx, pr.Number, in.Labels); err != nil {
		return &pr, err
	}
	return &pr, g.review(ctx, pr.Number, in.Reviewers)
}

func (g *GitHub) label(ctx context.Context, number int, names []string) error {
	if len(names) == 0 {
		return nil
	}
	for _, n := range names {
		if err := g.ensureLabel(ctx, n); err != nil {
			return err
		}
	}
	return g.do(ctx, "POST", g.repoPath("/issues/"+strconv.Itoa(number)+"/labels"), map[string]any{"labels": names}, nil)
}

func (g *GitHub) ensureLabel(ctx context.Context, name string) error {
	err := g.do(ctx, "GET", g.repoPath("/labels/"+url.PathEscape(name)), nil, nil)
	if !isStatus(err, http.StatusNotFound) {
		return err
	}
	err = g.do(ctx, "POST", g.repoPath("/labels"), map[string]string{"name": name, "color": labelColor}, nil)
	if isStatus(err, http.StatusUnprocessableEntity) {
		return nil
	}
	return err
}

func isStatus(err error, status int) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Status == status
}

// review requests reviewers. GitHub answers 422 for a login that cannot
// review, such as the PR's own author; that is worth a line, not a failure.
func (g *GitHub) review(ctx context.Context, number int, names []string) error {
	if len(names) == 0 {
		return nil
	}
	body := map[string][]string{"reviewers": {}}
	for _, n := range names {
		if _, team, ok := strings.Cut(n, "/"); ok {
			body["team_reviewers"] = append(body["team_reviewers"], team)
			continue
		}
		body["reviewers"] = append(body["reviewers"], n)
	}
	err := g.do(ctx, "POST", g.repoPath("/pulls/"+strconv.Itoa(number)+"/requested_reviewers"), body, nil)
	if isStatus(err, http.StatusUnprocessableEntity) {
		fmt.Fprintf(g.warn, "tend: could not request reviewers on #%d: %v\n", number, err)
		return nil
	}
	return err
}

func (g *GitHub) UpdatePR(ctx context.Context, number int, in core.PRInput) error {
	path := g.repoPath("/pulls/" + strconv.Itoa(number))
	var cur pull
	if err := g.do(ctx, "GET", path, nil, &cur); err != nil {
		return err
	}
	body := map[string]string{"title": in.Title, "body": in.Body}
	if err := g.do(ctx, "PATCH", path, body, nil); err != nil {
		return err
	}
	if cur.Draft == in.Draft {
		return nil
	}
	if in.Draft {
		return g.graphql(ctx, `mutation($id: ID!) { convertPullRequestToDraft(input: {pullRequestId: $id}) { pullRequest { id } } }`, map[string]any{"id": cur.NodeID})
	}
	return g.graphql(ctx, `mutation($id: ID!) { markPullRequestReadyForReview(input: {pullRequestId: $id}) { pullRequest { id } } }`, map[string]any{"id": cur.NodeID})
}

func (g *GitHub) ClosePR(ctx context.Context, number int) error {
	return g.do(ctx, "PATCH", g.repoPath("/pulls/"+strconv.Itoa(number)), map[string]string{"state": "closed"}, nil)
}

func (g *GitHub) Comments(ctx context.Context, number int) ([]core.Comment, error) {
	type raw struct {
		ID   int64 `json:"id"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		Role string    `json:"author_association"`
		Body string    `json:"body"`
		At   time.Time `json:"created_at"`
	}
	all, err := getAll[raw](ctx, g, g.repoPath("/issues/"+strconv.Itoa(number)+"/comments?"))
	if err != nil {
		return nil, err
	}
	res := make([]core.Comment, len(all))
	for i, c := range all {
		res[i] = core.Comment{ID: c.ID, Author: c.User.Login, Role: role(c.Role), Body: c.Body, At: c.At}
	}
	return res, nil
}

// ClosedBy asks the issue behind the PR who closed it; the pulls endpoint does not say.
func (g *GitHub) ClosedBy(ctx context.Context, number int) (string, error) {
	var res struct {
		ClosedBy *struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"closed_by"`
	}
	if err := g.do(ctx, "GET", g.repoPath("/issues/"+strconv.Itoa(number)), nil, &res); err != nil {
		return "", err
	}
	c := res.ClosedBy
	if c == nil {
		return "", nil
	}
	if strings.EqualFold(c.Type, "Bot") && !strings.HasSuffix(strings.ToLower(c.Login), "[bot]") {
		return c.Login + "[bot]", nil
	}
	return c.Login, nil
}

func role(s string) string {
	switch s {
	case "OWNER", "MEMBER", "COLLABORATOR", "CONTRIBUTOR":
		return s
	}
	return "NONE"
}

func (g *GitHub) Comment(ctx context.Context, number int, body string) error {
	return g.do(ctx, "POST", g.repoPath("/issues/"+strconv.Itoa(number)+"/comments"), map[string]string{"body": body}, nil)
}

// AutoMerge asks GitHub to merge the PR once its checks pass. The repository
// may not allow it; the caller reports that error rather than failing the run.
func (g *GitHub) AutoMerge(ctx context.Context, pr *core.PR, method string) error {
	switch method {
	case "merge", "squash", "rebase":
	default:
		return fmt.Errorf("auto-merge: unknown merge method %q", method)
	}
	id := pr.NodeID
	if id == "" {
		var cur pull
		if err := g.do(ctx, "GET", g.repoPath("/pulls/"+strconv.Itoa(pr.Number)), nil, &cur); err != nil {
			return fmt.Errorf("auto-merge: %w", err)
		}
		id = cur.NodeID
	}
	q := `mutation($id: ID!, $method: PullRequestMergeMethod!) { enablePullRequestAutoMerge(input: {pullRequestId: $id, mergeMethod: $method}) { pullRequest { id } } }`
	err := g.graphql(ctx, q, map[string]any{"id": id, "method": strings.ToUpper(method)})
	if err != nil {
		return fmt.Errorf("auto-merge: %w", err)
	}
	return nil
}

func (g *GitHub) graphql(ctx context.Context, query string, vars map[string]any) error {
	var res struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := g.do(ctx, "POST", "/graphql", map[string]any{"query": query, "variables": vars}, &res); err != nil {
		return err
	}
	var msgs []string
	for _, e := range res.Errors {
		msgs = append(msgs, e.Message)
	}
	if len(msgs) > 0 {
		return errors.New(strings.Join(msgs, "; "))
	}
	return nil
}

func getAll[T any](ctx context.Context, g *GitHub, path string) ([]T, error) {
	sep := "&"
	if strings.HasSuffix(path, "?") {
		sep = ""
	} else if !strings.Contains(path, "?") {
		sep = "?"
	}
	var res []T
	for page := 1; page <= pageMax; page++ {
		p := path + sep + "per_page=" + strconv.Itoa(pageSize)
		if page > 1 {
			p += "&page=" + strconv.Itoa(page)
		}
		var got []T
		if err := g.do(ctx, "GET", p, nil, &got); err != nil {
			return nil, err
		}
		res = append(res, got...)
		if len(got) < pageSize {
			break
		}
	}
	return res, nil
}

type apiError struct {
	Method, Path string
	Status       int
	Message      string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("github: %s %s: %d %s", e.Method, e.Path, e.Status, e.Message)
}

type reply struct {
	status int
	header http.Header
	body   []byte
}

func (g *GitHub) do(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = b
	}
	res, err := g.send(ctx, method, path, body)
	if err != nil {
		return err
	}
	if wait, limited := limit(res, g.now()); limited {
		if wait > waitMax {
			return fmt.Errorf("github: rate limited until %s, not waiting that long", g.now().Add(wait).UTC().Format(time.RFC3339))
		}
		fmt.Fprintf(g.warn, "tend: github rate limit, waiting %s\n", wait.Round(time.Second))
		if err := g.sleep(ctx, wait); err != nil {
			return err
		}
		if res, err = g.send(ctx, method, path, body); err != nil {
			return err
		}
		if _, limited := limit(res, g.now()); limited {
			return fmt.Errorf("github: still rate limited after waiting, try again later (%s %s)", method, path)
		}
	}
	if res.status < 200 || res.status > 299 {
		return &apiError{Method: method, Path: path, Status: res.status, Message: message(res.body)}
	}
	if out == nil || len(bytes.TrimSpace(res.body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(res.body, out); err != nil {
		return fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	return nil
}

func (g *GitHub) send(ctx context.Context, method, path string, body []byte) (*reply, error) {
	u := g.api + path
	h := map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": apiVersion,
		"Authorization":        "Bearer " + g.token,
	}
	if method == "GET" {
		res, err := g.fetch.Get(ctx, u, func(o *core.FetchOpts) { o.NoCache, o.Header = true, h })
		if err != nil {
			return nil, err
		}
		return &reply{res.Status, http.Header(res.Header), res.Body}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range h {
		req.Header.Set(k, v)
	}
	req.Header.Set("User-Agent", "tend (+https://github.com/mohsensc/tend-cli)")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := g.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	return &reply{res.StatusCode, res.Header, b}, nil
}

func limit(res *reply, now time.Time) (time.Duration, bool) {
	if res.status != http.StatusForbidden && res.status != http.StatusTooManyRequests {
		return 0, false
	}
	if v := res.header.Get("Retry-After"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Second, true
		}
	}
	if res.header.Get("X-Ratelimit-Remaining") != "0" {
		return time.Minute, res.status == http.StatusTooManyRequests
	}
	n, err := strconv.ParseInt(res.header.Get("X-Ratelimit-Reset"), 10, 64)
	if err != nil {
		return time.Minute, true
	}
	return max(time.Unix(n, 0).Sub(now), 0) + time.Second, true
}

func message(b []byte) string {
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &m) == nil && m.Message != "" {
		return m.Message
	}
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// RepoFromRemote reads owner and name from a github.com remote URL, or from a
// bare "owner/name" such as GITHUB_REPOSITORY.
func RepoFromRemote(raw string) (owner, name string, ok bool) {
	raw = strings.TrimSpace(raw)
	path := ""
	switch {
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil || !isGitHub(u.Hostname()) {
			return "", "", false
		}
		path = u.Path
	case strings.Contains(raw, "@"):
		host, p, found := strings.Cut(raw[strings.LastIndexByte(raw, '@')+1:], ":")
		if !found || !isGitHub(host) {
			return "", "", false
		}
		path = p
	default:
		path = raw
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func isGitHub(host string) bool {
	host = strings.ToLower(host)
	return host == "github.com" || host == "www.github.com"
}

// TokenFrom finds a GitHub token: GITHUB_TOKEN, then GH_TOKEN, then the
// output of `gh auth token` when gh is installed and logged in.
func TokenFrom(getenv func(string) string, run func(name string, args ...string) (string, error)) string {
	for _, k := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
	}
	out, err := run("gh", "auth", "token")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// Exec runs a command and returns its stdout; it is the run argument for TokenFrom.
func Exec(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}
