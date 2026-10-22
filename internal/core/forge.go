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
