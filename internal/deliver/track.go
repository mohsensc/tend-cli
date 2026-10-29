package deliver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
)

var (
	_ core.Checks  = (*GitHub)(nil)
	_ core.Tracker = (*GitHub)(nil)
)

// Checks reads what the repository's CI made of a commit: its check runs
// and its commit statuses together. A cancelled or skipped check says
// nothing either way.
func (g *GitHub) Checks(ctx context.Context, sha string) (core.CI, error) {
	var runs struct {
		Runs []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if err := g.do(ctx, "GET", g.repoPath("/commits/"+sha+"/check-runs?per_page=100"), nil, &runs); err != nil {
		return core.CI{}, err
	}
	var status struct {
		Statuses []struct {
			Context string `json:"context"`
			State   string `json:"state"`
		} `json:"statuses"`
	}
	if err := g.do(ctx, "GET", g.repoPath("/commits/"+sha+"/status"), nil, &status); err != nil {
		return core.CI{}, err
	}
	var res []ciCheck
	for _, r := range runs.Runs {
		c := ciCheck{r.Name, "success"}
		switch {
		case r.Status != "completed":
			c.state = "pending"
		case slices.Contains([]string{"failure", "timed_out", "action_required", "startup_failure"}, r.Conclusion):
			c.state = "failure"
		case r.Conclusion == "cancelled" || r.Conclusion == "skipped" || r.Conclusion == "stale":
			continue
		}
		res = append(res, c)
	}
	for _, s := range status.Statuses {
		c := ciCheck{s.Context, s.State}
		if s.State == "error" {
			c.state = "failure"
		}
		res = append(res, c)
	}
	return verdict(res), nil
}

type ciCheck struct{ name, state string }

// verdict sums up checks: a failure, else one still running, else success,
// or none when there are no checks at all.
func verdict(cs []ciCheck) core.CI {
	res := core.CI{State: "none"}
	for _, c := range cs {
		switch {
		case c.state == "failure":
			res.State = "failure"
			res.Failed = append(res.Failed, c.name)
		case c.state == "pending" && res.State != "failure":
			res.State = "pending"
		case res.State == "none":
			res.State = "success"
		}
	}
	slices.Sort(res.Failed)
	res.Failed = slices.Compact(res.Failed)
	return res
}

type issue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	Role   string `json:"author_association"`
	User   struct {
		Type string `json:"type"`
	} `json:"user"`
	Pull *struct {
		URL string `json:"url"`
	} `json:"pull_request"`
}

// trusted reports whether i was opened by an app, as tend in Actions is,
// or by someone with write access, whose token tend may run with. Anyone
// else can open an issue with tend's marker in it.
func (i issue) trusted() bool {
	return strings.EqualFold(i.User.Type, "Bot") || i.Role == "OWNER" || i.Role == "MEMBER" || i.Role == "COLLABORATOR"
}

func (i issue) issue() *core.Issue {
	return &core.Issue{Number: i.Number, Title: i.Title, Body: i.Body, Open: i.State == "open"}
}

// FindIssue looks for the issue whose body holds marker, newest first,
// among those an app or someone with write access opened. Pull requests,
// which the issues API lists too, never count.
func (g *GitHub) FindIssue(ctx context.Context, marker string) (*core.Issue, error) {
	all, err := getAll[issue](ctx, g, g.repoPath("/issues?state=all&sort=created&direction=desc"))
	if err != nil {
		return nil, err
	}
	for _, i := range all {
		if i.Pull == nil && i.trusted() && strings.Contains(i.Body, marker) {
			return i.issue(), nil
		}
	}
	return nil, nil
}

func (g *GitHub) CreateIssue(ctx context.Context, title, body string, labels []string) (*core.Issue, error) {
	for _, n := range labels {
		if err := g.ensureLabel(ctx, n); err != nil {
			return nil, err
		}
	}
	var res issue
	in := map[string]any{"title": title, "body": body}
	if len(labels) > 0 {
		in["labels"] = labels
	}
	if err := g.do(ctx, "POST", g.repoPath("/issues"), in, &res); err != nil {
		return nil, err
	}
	return res.issue(), nil
}

func (g *GitHub) EditIssue(ctx context.Context, number int, body string, open bool) error {
	state := "closed"
	if open {
		state = "open"
	}
	err := g.do(ctx, "PATCH", g.repoPath("/issues/"+strconv.Itoa(number)), map[string]any{"body": body, "state": state}, nil)
	if isStatus(err, http.StatusNotFound) || isStatus(err, http.StatusGone) || isStatus(err, http.StatusMovedPermanently) {
		return fmt.Errorf("%w: %w", ErrGone, err)
	}
	return err
}

// ErrGone is an issue that was deleted, or moved to another repo.
var ErrGone = errors.New("the issue is gone")
