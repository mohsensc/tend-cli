package deliver

import (
	"context"
	"reflect"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

// CI is read from check runs and commit statuses together: a failure
// anywhere wins, then one still running; cancelled and skipped runs say
// nothing; no checks at all is none.
func TestChecks(t *testing.T) {
	tests := []struct {
		name, runs, status string
		want               core.CI
	}{
		{"none", `{"check_runs": []}`, `{"statuses": []}`, core.CI{State: "none"}},
		{"passed", `{"check_runs": [{"name": "test", "status": "completed", "conclusion": "success"}]}`, `{"statuses": []}`, core.CI{State: "success"}},
		{"running", `{"check_runs": [{"name": "test", "status": "in_progress"}, {"name": "lint", "status": "completed", "conclusion": "success"}]}`, `{"statuses": []}`, core.CI{State: "pending"}},
		{"failed", `{"check_runs": [{"name": "test", "status": "completed", "conclusion": "failure"}, {"name": "lint", "status": "in_progress"}]}`, `{"statuses": [{"context": "ci/legacy", "state": "error"}]}`,
			core.CI{State: "failure", Failed: []string{"ci/legacy", "test"}}},
		{"cancelled only", `{"check_runs": [{"name": "test", "status": "completed", "conclusion": "cancelled"}]}`, `{"statuses": []}`, core.CI{State: "none"}},
		{"a status pending", `{"check_runs": []}`, `{"statuses": [{"context": "ci/legacy", "state": "pending"}]}`, core.CI{State: "pending"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newAPI(t, []step{
				{req: "GET /repos/acme/shop/commits/abc123/check-runs?per_page=100", resp: tt.runs},
				{req: "GET /repos/acme/shop/commits/abc123/status", resp: tt.status},
			})
			got, err := api.g.Checks(context.Background(), "abc123")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// The tracking issue is found by its marker among issues, never among the
// PRs the issues API lists too, and is created with tend's labels and
// edited in place.
func TestTrackingIssue(t *testing.T) {
	api := newAPI(t, []step{
		{req: "GET /repos/acme/shop/issues?state=all&sort=created&direction=desc&per_page=100", resp: `[
			{"number": 9, "title": "a PR", "body": "<!-- tend tracking -->", "state": "open", "pull_request": {"url": "x"}},
			{"number": 7, "title": "tend: what it could not do on its own", "body": "list\n<!-- tend tracking -->", "state": "closed"}
		]`},
		{req: "GET /repos/acme/shop/labels/tend", resp: `{"name": "tend"}`},
		{req: "POST /repos/acme/shop/issues", body: `{"body":"b","labels":["tend"],"title":"t"}`, status: 201, resp: `{"number": 12, "title": "t", "body": "b", "state": "open"}`},
		{req: "PATCH /repos/acme/shop/issues/12", body: `{"body":"c","state":"closed"}`, resp: `{}`},
	})
	ctx := context.Background()
	is, err := api.g.FindIssue(ctx, "<!-- tend tracking -->")
	if err != nil || is == nil || is.Number != 7 || is.Open {
		t.Fatalf("found %+v, %v", is, err)
	}
	made, err := api.g.CreateIssue(ctx, "t", "b", []string{"tend"})
	if err != nil || made.Number != 12 || !made.Open {
		t.Fatalf("made %+v, %v", made, err)
	}
	if err := api.g.EditIssue(ctx, 12, "c", false); err != nil {
		t.Fatal(err)
	}
}
