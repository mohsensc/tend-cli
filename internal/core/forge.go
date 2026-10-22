package core

import (
	"context"
	"time"
)

type RepoInfo struct {
	Owner   string
	Name    string
	Default string // default branch
}

type PR struct {
	Number int
	NodeID string
	URL    string
	Branch string
	Base   string
	Title  string
	Body   string
	State  string // open|closed|merged
	Draft  bool
	Head   string // the commit the branch points at, when known
}

type PRInput struct {
	Title     string
	Body      string
	Branch    string
	Base      string
	Draft     bool
	Labels    []string
	Reviewers []string
}

type Comment struct {
	ID     int64
	Author string
	Role   string // OWNER|MEMBER|COLLABORATOR|CONTRIBUTOR|NONE
	Body   string
	At     time.Time
}

type Forge interface {
	Repo(ctx context.Context) (RepoInfo, error)
	// FindPR returns the most recent PR with this head branch in any state, nil if none.
	FindPR(ctx context.Context, branch string) (*PR, error)
	// OpenPRs lists open PRs whose head branch starts with prefix.
	OpenPRs(ctx context.Context, prefix string) ([]PR, error)
	// HadPR reports whether a PR whose head branch starts with prefix, with
	// tend's marker in its body, was ever opened, whatever became of it.
	HadPR(ctx context.Context, prefix string) (bool, error)
	CreatePR(ctx context.Context, in PRInput) (*PR, error)
	UpdatePR(ctx context.Context, number int, in PRInput) error
	ClosePR(ctx context.Context, number int) error
	Comments(ctx context.Context, number int) ([]Comment, error)
	Comment(ctx context.Context, number int, body string) error
	AutoMerge(ctx context.Context, pr *PR, method string) error // method: merge|squash|rebase
}

// CI is what a repository's own checks said about a commit.
type CI struct {
	State  string   // none|pending|success|failure
	Failed []string // names of the checks that failed
}

// Checks is a forge that reports the CI result of a commit.
type Checks interface {
	Checks(ctx context.Context, sha string) (CI, error)
}

type Issue struct {
	Number int
	Title  string
	Body   string
	Open   bool
}

// Tracker is a forge that keeps tend's one tracking issue.
type Tracker interface {
	// FindIssue returns the newest issue, open or closed, whose body holds
	// marker, nil if none.
	FindIssue(ctx context.Context, marker string) (*Issue, error)
	CreateIssue(ctx context.Context, title, body string, labels []string) (*Issue, error)
	// EditIssue sets the body and opens or closes the issue.
	EditIssue(ctx context.Context, number int, body string, open bool) error
}
