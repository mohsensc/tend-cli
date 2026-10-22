package deliver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/fetch"
)

// step is one recorded exchange: what tend must send and what GitHub answers.
type step struct {
	req    string // "METHOD /path?query"
	body   string // JSON the request body must equal, "" for none, "*" for any
	status int    // 0 means 200
	resp   string // file under testdata/github, or inline JSON
	header map[string]string
}

type stub struct {
	g     *GitHub
	warn  *bytes.Buffer
	slept []time.Duration
}

// newAPI starts a server that plays steps in order. Every deviation fails the
// test; so does leaving steps unplayed.
func newAPI(t *testing.T, steps []step) *stub {
	t.Helper()
	var mu sync.Mutex
	next := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if next >= len(steps) {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			http.Error(w, "unexpected", 500)
			return
		}
		s := steps[next]
		next++
		check(t, s, r)
		for k, v := range s.header {
			w.Header().Set(k, v)
		}
		status := s.status
		if status == 0 {
			status = 200
		}
		w.WriteHeader(status)
		_, _ = w.Write(response(t, s.resp))
	}))
	t.Cleanup(func() {
		srv.Close()
		if next != len(steps) {
			t.Errorf("%d of %d expected requests were never made; next was %s", len(steps)-next, len(steps), steps[next].req)
		}
	})
	res := &stub{warn: &bytes.Buffer{}}
	g := NewGitHub(fetch.New("", "test"), srv.Client(), "tok", "acme", "shop").(*GitHub)
	g.api, g.warn = srv.URL, res.warn
	g.sleep = func(_ context.Context, d time.Duration) error { res.slept = append(res.slept, d); return nil }
	g.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	res.g = g
	return res
}

func check(t *testing.T, s step, r *http.Request) {
	t.Helper()
	method, target, _ := strings.Cut(s.req, " ")
	want, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	if r.Method != method || r.URL.Path != want.Path || !reflect.DeepEqual(r.URL.Query(), want.Query()) {
		t.Errorf("request = %s %s, want %s", r.Method, r.URL.RequestURI(), s.req)
	}
	for k, v := range map[string]string{
		"Authorization":        "Bearer tok",
		"Accept":               "application/vnd.github+json",
		"X-Github-Api-Version": "2022-11-28",
	} {
		if got := r.Header.Get(k); got != v {
			t.Errorf("%s: header %s = %q, want %q", s.req, k, got, v)
		}
	}
	got, _ := readAll(r)
	if s.body == "*" {
		if len(got) == 0 {
			t.Errorf("%s: no body", s.req)
		}
		return
	}
	if s.body == "" {
		if len(got) != 0 {
			t.Errorf("%s: unexpected body %s", s.req, got)
		}
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("%s: content type %q", s.req, ct)
	}
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Errorf("%s: body is not JSON: %s", s.req, got)
		return
	}
	if err := json.Unmarshal([]byte(s.body), &b); err != nil {
		t.Fatalf("bad expected body in test: %v", err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("%s: body = %s, want %s", s.req, got, s.body)
	}
}

func readAll(r *http.Request) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r.Body)
	return buf.Bytes(), err
}

func response(t *testing.T, resp string) []byte {
	t.Helper()
	if resp == "" {
		return []byte("{}")
	}
	if strings.HasPrefix(resp, "{") || strings.HasPrefix(resp, "[") {
		return []byte(resp)
	}
	b, err := os.ReadFile(filepath.Join("testdata", "github", resp))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const pulls = "/repos/acme/shop/pulls"

func TestRepo(t *testing.T) {
	a := newAPI(t, []step{{req: "GET /repos/acme/shop", resp: "repo.json"}})
	got, err := a.g.Repo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != (core.RepoInfo{Owner: "acme", Name: "shop", Default: "trunk"}) {
		t.Errorf("Repo = %+v", got)
	}
}

func TestFindPR(t *testing.T) {
	head := "?head=acme%3Atend%2Fnpm-stripe-16.0.1&state=all&sort=created&direction=desc&per_page=10"
	tests := []struct {
		name string
		resp string
		want *core.PR
	}{
		{"open", "[" + string(response(t, "pull_open.json")) + "]", &core.PR{
			Number: 12, NodeID: "PR_kwDOABCDE85xyz", URL: "https://github.com/acme/shop/pull/12", Branch: "tend/npm-stripe-16.0.1",
			Base: "main", Title: "Bump stripe to 16.0.1 (fixes GHSA-xxxx-yyyy-zzzz)", State: "open", Head: "1111111111111111111111111111111111111111",
			Body: "## stripe 14.2.0 → 16.0.1\n\n<!-- tend id=\"npm/stripe@16.0.1\" base=\"abc1234\" rev=\"1\" -->\n"}},
		{"merged", "pulls_find_merged.json", &core.PR{
			Number: 9, NodeID: "PR_kwDOABCDE85old", URL: "https://github.com/acme/shop/pull/9", Branch: "tend/npm-stripe-15.0.0",
			Base: "main", Title: "Bump stripe to 15.0.0", Body: "old body", State: "merged"}},
		{"closed draft", "pulls_find_closed.json", &core.PR{
			Number: 10, NodeID: "PR_kwDOABCDE85cls", URL: "https://github.com/acme/shop/pull/10", Branch: "tend/npm-left-pad-1.3.0",
			Base: "main", Title: "Bump left-pad to 1.3.0", State: "closed", Draft: true}},
		{"reopened older PR", `[{"number": 14, "state": "closed", "merged_at": null}, {"number": 13, "state": "open"}]`, &core.PR{Number: 13, State: "open"}},
		{"none", "[]", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := newAPI(t, []step{{req: "GET " + pulls + head, resp: tc.resp}})
			got, err := a.g.FindPR(context.Background(), "tend/npm-stripe-16.0.1")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("FindPR = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestOpenPRs(t *testing.T) {
	a := newAPI(t, []step{{req: "GET " + pulls + "?state=open&per_page=100", resp: "pulls_open.json"}})
	got, err := a.g.OpenPRs(context.Background(), "tend/")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Number != 12 || got[1].Number != 11 || !got[0].Draft || got[1].Base != "develop" {
		t.Errorf("OpenPRs = %+v", got)
	}
}

func TestOpenPRsPages(t *testing.T) {
	var first strings.Builder
	first.WriteString("[")
	for i := 0; i < 100; i++ {
		if i > 0 {
			first.WriteString(",")
		}
		fmt.Fprintf(&first, `{"number": %d, "state": "open", "head": {"ref": "feature/%d"}, "base": {"ref": "main"}}`, 100+i, i)
	}
	first.WriteString("]")
	a := newAPI(t, []step{
		{req: "GET " + pulls + "?state=open&per_page=100", resp: first.String()},
		{req: "GET " + pulls + "?state=open&per_page=100&page=2", resp: `[{"number": 7, "state": "open", "body": "<!-- tend id=\"npm/a@1.0.0\" base=\"abc\" rev=\"1\" -->", "head": {"ref": "tend/npm-a-1.0.0", "repo": {"full_name": "acme/shop"}}, "base": {"ref": "main"}}]`},
	})
	got, err := a.g.OpenPRs(context.Background(), "tend/")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Number != 7 {
		t.Errorf("OpenPRs = %+v", got)
	}
}

func TestCreatePR(t *testing.T) {
	create := step{
		req:    "POST " + pulls,
		body:   `{"title": "Bump stripe to 16.0.1", "head": "tend/npm-stripe-16.0.1", "base": "main", "body": "the body", "draft": true}`,
		status: 201, resp: "pull_draft.json",
	}
	in := core.PRInput{Title: "Bump stripe to 16.0.1", Body: "the body", Branch: "tend/npm-stripe-16.0.1", Base: "main", Draft: true}
	want := &core.PR{Number: 12, NodeID: "PR_kwDOABCDE85xyz", URL: "https://github.com/acme/shop/pull/12", Branch: "tend/npm-stripe-16.0.1", Base: "main", Title: "Bump stripe to 16.0.1", State: "open", Draft: true}

	tests := []struct {
		name  string
		in    core.PRInput
		steps []step
		warn  string
	}{
		{"no labels, no reviewers", in, []step{create}, ""},
		{"label exists", withLabels(in, "tend"), []step{
			create,
			{req: "GET /repos/acme/shop/labels/tend", resp: "label.json"},
			{req: "POST /repos/acme/shop/issues/12/labels", body: `{"labels": ["tend"]}`, resp: "[]"},
		}, ""},
		{"label created when missing", withLabels(in, "tend", "good first issue"), []step{
			create,
			{req: "GET /repos/acme/shop/labels/tend", status: 404, resp: "label_missing.json"},
			{req: "POST /repos/acme/shop/labels", body: `{"name": "tend", "color": "0e8a16"}`, status: 201, resp: "label.json"},
			{req: "GET /repos/acme/shop/labels/good%20first%20issue", resp: "label.json"},
			{req: "POST /repos/acme/shop/issues/12/labels", body: `{"labels": ["tend", "good first issue"]}`, resp: "[]"},
		}, ""},
		{"label created by someone else meanwhile", withLabels(in, "tend"), []step{
			create,
			{req: "GET /repos/acme/shop/labels/tend", status: 404, resp: "label_missing.json"},
			{req: "POST /repos/acme/shop/labels", body: `{"name": "tend", "color": "0e8a16"}`, status: 422, resp: `{"message": "Validation Failed"}`},
			{req: "POST /repos/acme/shop/issues/12/labels", body: `{"labels": ["tend"]}`, resp: "[]"},
		}, ""},
		{"reviewers", withReviewers(in, "alice", "acme/core"), []step{
			create,
			{req: "POST /repos/acme/shop/pulls/12/requested_reviewers", body: `{"reviewers": ["alice"], "team_reviewers": ["core"]}`, status: 201, resp: "{}"},
		}, ""},
		{"invalid reviewer is a warning", withReviewers(in, "author"), []step{
			create,
			{req: "POST /repos/acme/shop/pulls/12/requested_reviewers", body: `{"reviewers": ["author"]}`, status: 422, resp: "reviewers_invalid.json"},
		}, "could not request reviewers on #12: github: POST /repos/acme/shop/pulls/12/requested_reviewers: 422 Review cannot be requested"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := newAPI(t, tc.steps)
			got, err := a.g.CreatePR(context.Background(), tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("CreatePR = %+v", got)
			}
			if tc.warn == "" && a.warn.Len() > 0 {
				t.Errorf("unexpected warning %q", a.warn)
			}
			if !strings.Contains(a.warn.String(), tc.warn) {
				t.Errorf("warning = %q, want it to contain %q", a.warn, tc.warn)
			}
		})
	}
}

func withLabels(in core.PRInput, l ...string) core.PRInput    { in.Labels = l; return in }
func withReviewers(in core.PRInput, r ...string) core.PRInput { in.Reviewers = r; return in }

func TestCreatePRErrors(t *testing.T) {
	in := core.PRInput{Title: "t", Branch: "b", Base: "main"}
	a := newAPI(t, []step{{req: "POST " + pulls, body: `{"title": "t", "head": "b", "base": "main", "body": "", "draft": false}`, status: 403, resp: "forbidden.json"}})
	_, err := a.g.CreatePR(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "403 Resource not accessible by integration") {
		t.Fatalf("err = %v", err)
	}
	if len(a.slept) != 0 {
		t.Error("a permission error was treated as a rate limit")
	}

	a = newAPI(t, []step{{req: "POST " + pulls, body: `{"title": "t", "head": "b", "base": "main", "body": "", "draft": false}`, status: 403,
		resp: `{"message": "GitHub Actions is not permitted to create or approve pull requests."}`}})
	_, err = a.g.CreatePR(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), `turn on "Allow GitHub Actions to create and approve pull requests"`) {
		t.Errorf("err = %v", err)
	}

	in.Labels = []string{"tend"}
	a = newAPI(t, []step{
		{req: "POST " + pulls, body: `{"title": "t", "head": "b", "base": "main", "body": "", "draft": false}`, status: 201, resp: "pull_draft.json"},
		{req: "GET /repos/acme/shop/labels/tend", status: 401, resp: `{"message": "Bad credentials"}`},
	})
	pr, err := a.g.CreatePR(context.Background(), in)
	if err == nil || pr == nil || pr.Number != 12 {
		t.Fatalf("a failed label call must still return the created PR: %+v, %v", pr, err)
	}
}

func TestCreatePRReviewerErrors(t *testing.T) {
	in := withReviewers(core.PRInput{Title: "t", Branch: "b", Base: "main"}, "alice")
	a := newAPI(t, []step{
		{req: "POST " + pulls, body: `{"title": "t", "head": "b", "base": "main", "body": "", "draft": false}`, status: 201, resp: "pull_draft.json"},
		{req: "POST /repos/acme/shop/pulls/12/requested_reviewers", body: "*", status: 404, resp: `{"message": "Not Found"}`},
	})
	pr, err := a.g.CreatePR(context.Background(), in)
	if err == nil || pr == nil || pr.Number != 12 || !strings.Contains(err.Error(), "404") {
		t.Fatalf("pr = %+v, err = %v", pr, err)
	}
}

func TestWritesTimeOut(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	g := NewGitHub(fetch.New("", "test"), srv.Client(), "tok", "acme", "shop").(*GitHub)
	g.api = srv.URL
	old := writeTimeout
	writeTimeout = 50 * time.Millisecond
	defer func() { writeTimeout = old }()
	if err := g.Comment(context.Background(), 1, "x"); err == nil {
		t.Fatal("a hung server did not fail the write")
	}
}

func TestUpdatePR(t *testing.T) {
	patch := step{req: "PATCH " + pulls + "/12", body: `{"title": "New title", "body": "New body"}`}
	toDraft := `mutation($id: ID!) { convertPullRequestToDraft(input: {pullRequestId: $id}) { pullRequest { id } } }`
	toReady := `mutation($id: ID!) { markPullRequestReadyForReview(input: {pullRequestId: $id}) { pullRequest { id } } }`
	gql := func(q string) step {
		return step{req: "POST /graphql", body: fmt.Sprintf(`{"query": %q, "variables": {"id": "PR_kwDOABCDE85xyz"}}`, q), resp: `{"data": {}}`}
	}
	get := func(resp string) step { return step{req: "GET " + pulls + "/12", resp: resp} }
	in := core.PRInput{Title: "New title", Body: "New body"}

	tests := []struct {
		name  string
		draft bool
		steps []step
	}{
		{"ready stays ready", false, []step{get("pull_open.json"), patch}},
		{"draft stays draft", true, []step{get("pull_draft.json"), patch}},
		{"ready becomes draft", true, []step{get("pull_open.json"), patch, gql(toDraft)}},
		{"draft becomes ready", false, []step{get("pull_draft.json"), patch, gql(toReady)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := newAPI(t, tc.steps)
			in := in
			in.Draft = tc.draft
			if err := a.g.UpdatePR(context.Background(), 12, in); err != nil {
				t.Fatal(err)
			}
		})
	}

	t.Run("graphql errors surface", func(t *testing.T) {
		a := newAPI(t, []step{get("pull_open.json"), patch, {req: "POST /graphql", body: "*", resp: `{"errors": [{"message": "Could not resolve"}]}`}})
		in := in
		in.Draft = true
		err := a.g.UpdatePR(context.Background(), 12, in)
		if err == nil || !strings.Contains(err.Error(), "Could not resolve") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestClosePR(t *testing.T) {
	a := newAPI(t, []step{{req: "PATCH " + pulls + "/12", body: `{"state": "closed"}`}})
	if err := a.g.ClosePR(context.Background(), 12); err != nil {
		t.Fatal(err)
	}
}

func TestClosedBy(t *testing.T) {
	tests := []struct {
		name, resp, want string
	}{
		{"a person", `{"number": 12, "closed_by": {"login": "ada", "type": "User"}}`, "ada"},
		{"an app", `{"number": 12, "closed_by": {"login": "stale[bot]", "type": "Bot"}}`, "stale[bot]"},
		{"a bot account without the suffix", `{"number": 12, "closed_by": {"login": "closer", "type": "Bot"}}`, "closer[bot]"},
		{"not said", `{"number": 12, "closed_by": null}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newAPI(t, []step{{req: "GET /repos/acme/shop/issues/12", resp: tt.resp}})
			got, err := a.g.ClosedBy(context.Background(), 12)
			if err != nil || got != tt.want {
				t.Errorf("ClosedBy = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestComments(t *testing.T) {
	a := newAPI(t, []step{{req: "GET /repos/acme/shop/issues/12/comments?per_page=100", resp: "comments.json"}})
	got, err := a.g.Comments(context.Background(), 12)
	if err != nil {
		t.Fatal(err)
	}
	roles := []string{"OWNER", "MEMBER", "NONE", "NONE", "COLLABORATOR", "CONTRIBUTOR"}
	if len(got) != len(roles) {
		t.Fatalf("got %d comments", len(got))
	}
	for i, c := range got {
		if c.Role != roles[i] {
			t.Errorf("comment %d (%s) role = %s, want %s", c.ID, c.Author, c.Role, roles[i])
		}
	}
	first := core.Comment{ID: 501, Author: "maintainer", Role: "OWNER", Body: "tend apply 2", At: time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)}
	if got[0] != first {
		t.Errorf("first = %+v", got[0])
	}
}

func TestComment(t *testing.T) {
	a := newAPI(t, []step{{req: "POST /repos/acme/shop/issues/12/comments", body: `{"body": "Closing."}`, status: 201}})
	if err := a.g.Comment(context.Background(), 12, "Closing."); err != nil {
		t.Fatal(err)
	}
}

func TestAutoMerge(t *testing.T) {
	q := `mutation($id: ID!, $method: PullRequestMergeMethod!) { enablePullRequestAutoMerge(input: {pullRequestId: $id, mergeMethod: $method}) { pullRequest { id } } }`
	gql := func(method, resp string) step {
		return step{req: "POST /graphql", body: fmt.Sprintf(`{"query": %q, "variables": {"id": "PR_kwDOABCDE85xyz", "method": %q}}`, q, method), resp: resp}
	}
	pr := &core.PR{Number: 12, NodeID: "PR_kwDOABCDE85xyz"}

	for method, want := range map[string]string{"merge": "MERGE", "squash": "SQUASH", "rebase": "REBASE"} {
		t.Run(method, func(t *testing.T) {
			a := newAPI(t, []step{gql(want, "graphql_ok.json")})
			if err := a.g.AutoMerge(context.Background(), pr, method); err != nil {
				t.Fatal(err)
			}
		})
	}

	t.Run("repository setting is off", func(t *testing.T) {
		a := newAPI(t, []step{gql("SQUASH", "graphql_automerge_off.json")})
		err := a.g.AutoMerge(context.Background(), pr, "squash")
		if err == nil || !strings.Contains(err.Error(), "auto-merge: Pull request Auto merge is not allowed for this repository") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("node id is looked up when missing", func(t *testing.T) {
		a := newAPI(t, []step{{req: "GET " + pulls + "/12", resp: "pull_open.json"}, gql("SQUASH", "graphql_ok.json")})
		if err := a.g.AutoMerge(context.Background(), &core.PR{Number: 12}, "squash"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("unknown method sends nothing", func(t *testing.T) {
		a := newAPI(t, nil)
		if err := a.g.AutoMerge(context.Background(), pr, "fast-forward"); err == nil {
			t.Fatal("accepted a merge method GitHub does not have")
		}
	})
	t.Run("http failure is wrapped", func(t *testing.T) {
		a := newAPI(t, []step{{req: "POST /graphql", body: "*", status: 502, resp: `{"message": "bad gateway"}`}})
		err := a.g.AutoMerge(context.Background(), pr, "squash")
		if err == nil || !strings.HasPrefix(err.Error(), "auto-merge: github: POST /graphql: 502") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRateLimit(t *testing.T) {
	comment := step{req: "POST /repos/acme/shop/issues/12/comments", body: `{"body": "x"}`}
	limited := func(h map[string]string) step {
		s := comment
		s.status, s.resp, s.header = 403, "rate_limit.json", h
		return s
	}
	ok := comment
	ok.status = 201

	t.Run("retry-after is honored once", func(t *testing.T) {
		a := newAPI(t, []step{limited(map[string]string{"Retry-After": "7"}), ok})
		if err := a.g.Comment(context.Background(), 12, "x"); err != nil {
			t.Fatal(err)
		}
		if len(a.slept) != 1 || a.slept[0] != 7*time.Second {
			t.Errorf("slept %v", a.slept)
		}
		if !strings.Contains(a.warn.String(), "rate limit") {
			t.Errorf("no note on stderr: %q", a.warn)
		}
	})
	t.Run("reset time is honored", func(t *testing.T) {
		reset := fmt.Sprint(time.Unix(1_800_000_000, 0).Add(20 * time.Second).Unix())
		a := newAPI(t, []step{limited(map[string]string{"X-Ratelimit-Remaining": "0", "X-Ratelimit-Reset": reset}), ok})
		if err := a.g.Comment(context.Background(), 12, "x"); err != nil {
			t.Fatal(err)
		}
		if len(a.slept) != 1 || a.slept[0] != 21*time.Second {
			t.Errorf("slept %v", a.slept)
		}
	})
	t.Run("429 without hints waits a minute", func(t *testing.T) {
		s := limited(nil)
		s.status = 429
		a := newAPI(t, []step{s, ok})
		if err := a.g.Comment(context.Background(), 12, "x"); err != nil {
			t.Fatal(err)
		}
		if len(a.slept) != 1 || a.slept[0] != time.Minute {
			t.Errorf("slept %v", a.slept)
		}
	})
	t.Run("a second limit is an error", func(t *testing.T) {
		a := newAPI(t, []step{limited(map[string]string{"Retry-After": "1"}), limited(map[string]string{"Retry-After": "1"})})
		err := a.g.Comment(context.Background(), 12, "x")
		if err == nil || !strings.Contains(err.Error(), "still rate limited") {
			t.Fatalf("err = %v", err)
		}
		if len(a.slept) != 1 {
			t.Errorf("slept %d times", len(a.slept))
		}
	})
	t.Run("a long wait is refused", func(t *testing.T) {
		reset := fmt.Sprint(time.Unix(1_800_000_000, 0).Add(50 * time.Minute).Unix())
		a := newAPI(t, []step{limited(map[string]string{"X-Ratelimit-Remaining": "0", "X-Ratelimit-Reset": reset})})
		err := a.g.Comment(context.Background(), 12, "x")
		if err == nil || !strings.Contains(err.Error(), "rate limited until") {
			t.Fatalf("err = %v", err)
		}
		if len(a.slept) != 0 {
			t.Error("slept through a 50 minute limit")
		}
	})
	t.Run("reads are limited too", func(t *testing.T) {
		get := step{req: "GET /repos/acme/shop", status: 403, resp: "rate_limit.json", header: map[string]string{"Retry-After": "3"}}
		a := newAPI(t, []step{get, {req: "GET /repos/acme/shop", resp: "repo.json"}})
		if _, err := a.g.Repo(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(a.slept) != 1 {
			t.Errorf("slept %v", a.slept)
		}
	})
}

func TestRepoFromRemote(t *testing.T) {
	tests := []struct {
		in    string
		owner string
		name  string
		ok    bool
	}{
		{"git@github.com:acme/shop.git", "acme", "shop", true},
		{"git@github.com:acme/shop", "acme", "shop", true},
		{"ssh://git@github.com/acme/shop", "acme", "shop", true},
		{"ssh://git@github.com/acme/shop.git", "acme", "shop", true},
		{"ssh://git@github.com:22/acme/shop.git", "acme", "shop", true},
		{"https://github.com/acme/shop", "acme", "shop", true},
		{"https://github.com/acme/shop.git", "acme", "shop", true},
		{"https://github.com/acme/shop/", "acme", "shop", true},
		{"https://x-access-token:abc@github.com/acme/shop.git", "acme", "shop", true},
		{"HTTPS://GitHub.com/Acme/Shop.git", "Acme", "Shop", true},
		{"git://github.com/acme/shop.git", "acme", "shop", true},
		{"acme/shop", "acme", "shop", true},
		{"  acme/shop\n", "acme", "shop", true},
		{"my.repo/with.dots", "my.repo", "with.dots", true},
		{"git@gitlab.com:acme/shop.git", "", "", false},
		{"https://gitlab.com/acme/shop", "", "", false},
		{"https://github.com/acme", "", "", false},
		{"https://github.com/acme/shop/pull/3", "", "", false},
		{"git@github.com:shop.git", "", "", false},
		{"shop", "", "", false},
		{"", "", "", false},
		{"/", "", "", false},
		{"a/b/c", "", "", false},
		{"https://github.com.evil.test/acme/shop", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			owner, name, ok := RepoFromRemote(tc.in)
			if owner != tc.owner || name != tc.name || ok != tc.ok {
				t.Errorf("RepoFromRemote(%q) = %q %q %v", tc.in, owner, name, ok)
			}
		})
	}
}

func TestTokenFrom(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	gh := func(out string, err error) func(string, ...string) (string, error) {
		return func(name string, args ...string) (string, error) {
			if name != "gh" || strings.Join(args, " ") != "auth token" {
				t.Errorf("ran %s %v", name, args)
			}
			return out, err
		}
	}
	tests := []struct {
		name string
		env  map[string]string
		run  func(string, ...string) (string, error)
		want string
	}{
		{"GITHUB_TOKEN wins", map[string]string{"GITHUB_TOKEN": "a", "GH_TOKEN": "b"}, gh("c\n", nil), "a"},
		{"GH_TOKEN next", map[string]string{"GH_TOKEN": "b"}, gh("c\n", nil), "b"},
		{"blank is unset", map[string]string{"GITHUB_TOKEN": "  ", "GH_TOKEN": ""}, gh("c\n", nil), "c"},
		{"gh output is trimmed", nil, gh(" ghp_xyz\n", nil), "ghp_xyz"},
		{"gh missing", nil, gh("", fmt.Errorf("executable file not found")), ""},
		{"gh logged out", nil, gh("\n", nil), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TokenFrom(env(tc.env), tc.run); got != tc.want {
				t.Errorf("TokenFrom = %q, want %q", got, tc.want)
			}
		})
	}
	called := false
	TokenFrom(env(map[string]string{"GITHUB_TOKEN": "a"}), func(string, ...string) (string, error) { called = true; return "", nil })
	if called {
		t.Error("ran gh although GITHUB_TOKEN was set")
	}
}
