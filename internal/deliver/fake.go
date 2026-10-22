package deliver

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
)

// FakeForge is an in-memory core.Forge for tests. PR numbers start at 1. Tests
// play the human side with Merge, CloseByHuman and Say.
type FakeForge struct {
	Info      core.RepoInfo
	Now       func() time.Time
	Err       error // returned by every call when set
	Labels    map[int][]string
	Reviewers map[int][]string
	Merges    map[int]string // auto-merge method requested per PR
	CreateErr error          // returned by the next CreatePR, once
	Peak      int            // the most PRs open at once

	mu       sync.Mutex
	prs      []*core.PR
	comments map[int][]core.Comment
	closer   map[int]string
	nextID   int64
}

var (
	_ core.Forge = (*FakeForge)(nil)
	_ Closer     = (*FakeForge)(nil)
)

// tendLogin is the account tend writes as on the fake forge.
const tendLogin = "tend[bot]"

func NewFake(owner, name, base string) *FakeForge {
	return &FakeForge{
		Info:      core.RepoInfo{Owner: owner, Name: name, Default: base},
		Labels:    map[int][]string{},
		Reviewers: map[int][]string{},
		Merges:    map[int]string{},
		comments:  map[int][]core.Comment{},
		closer:    map[int]string{},
	}
}

func (f *FakeForge) Repo(ctx context.Context) (core.RepoInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Info, f.Err
}

func (f *FakeForge) FindPR(ctx context.Context, branch string) (*core.PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	for i := len(f.prs) - 1; i >= 0; i-- {
		if f.prs[i].Branch == branch {
			pr := *f.prs[i]
			return &pr, nil
		}
	}
	return nil, nil
}

func (f *FakeForge) OpenPRs(ctx context.Context, prefix string) ([]core.PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	var res []core.PR
	for _, pr := range f.prs {
		_, _, _, ok := ParseMarker(pr.Body)
		if ok && pr.State == "open" && strings.HasPrefix(pr.Branch, prefix) {
			res = append(res, *pr)
		}
	}
	return res, nil
}

func (f *FakeForge) HadPR(ctx context.Context, prefix string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return false, f.Err
	}
	for _, pr := range f.prs {
		if _, _, _, ok := ParseMarker(pr.Body); ok && strings.HasPrefix(pr.Branch, prefix) {
			return true, nil
		}
	}
	return false, nil
}

func (f *FakeForge) CreatePR(ctx context.Context, in core.PRInput) (*core.PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	if err := f.CreateErr; err != nil {
		f.CreateErr = nil
		return nil, err
	}
	open := 1
	for _, pr := range f.prs {
		if pr.State == "open" {
			open++
		}
		if pr.State == "open" && pr.Branch == in.Branch {
			return nil, fmt.Errorf("fake forge: a pull request already exists for %s (#%d)", in.Branch, pr.Number)
		}
	}
	n := len(f.prs) + 1
	pr := &core.PR{
		Number: n,
		NodeID: fmt.Sprintf("PR_%d", n),
		URL:    fmt.Sprintf("https://github.com/%s/%s/pull/%d", f.Info.Owner, f.Info.Name, n),
		Branch: in.Branch,
		Base:   in.Base,
		Title:  in.Title,
		Body:   in.Body,
		State:  "open",
		Draft:  in.Draft,
	}
	f.prs = append(f.prs, pr)
	f.Peak = max(f.Peak, open)
	f.Labels[n] = slices.Clone(in.Labels)
	f.Reviewers[n] = slices.Clone(in.Reviewers)
	res := *pr
	return &res, nil
}

func (f *FakeForge) UpdatePR(ctx context.Context, number int, in core.PRInput) error {
	return f.edit(number, func(pr *core.PR) error {
		pr.Title, pr.Body, pr.Draft = in.Title, in.Body, in.Draft
		return nil
	})
}

func (f *FakeForge) ClosePR(ctx context.Context, number int) error {
	return f.edit(number, func(pr *core.PR) error {
		pr.State = "closed"
		f.closer[number] = tendLogin
		return nil
	})
}

func (f *FakeForge) ClosedBy(ctx context.Context, number int) (string, error) {
	var res string
	err := f.edit(number, func(*core.PR) error {
		res = f.closer[number]
		return nil
	})
	return res, err
}

func (f *FakeForge) Comments(ctx context.Context, number int) ([]core.Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	if f.find(number) == nil {
		return nil, fmt.Errorf("fake forge: no PR #%d", number)
	}
	return slices.Clone(f.comments[number]), nil
}

func (f *FakeForge) Comment(ctx context.Context, number int, body string) error {
	return f.Say(number, tendLogin, "NONE", body)
}

func (f *FakeForge) AutoMerge(ctx context.Context, pr *core.PR, method string) error {
	return f.edit(pr.Number, func(*core.PR) error {
		f.Merges[pr.Number] = method
		return nil
	})
}

// PR returns a copy of PR n, or nil.
func (f *FakeForge) PR(n int) *core.PR {
	f.mu.Lock()
	defer f.mu.Unlock()
	pr := f.find(n)
	if pr == nil {
		return nil
	}
	res := *pr
	return &res
}

// All returns copies of every PR in creation order.
func (f *FakeForge) All() []core.PR {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]core.PR, len(f.prs))
	for i, pr := range f.prs {
		res[i] = *pr
	}
	return res
}

// Merge marks an open PR merged, as a maintainer would.
func (f *FakeForge) Merge(n int) error {
	return f.edit(n, func(pr *core.PR) error {
		if pr.State != "open" {
			return fmt.Errorf("fake forge: #%d is %s", n, pr.State)
		}
		pr.State = "merged"
		return nil
	})
}

// CloseByHuman closes an open PR without merging it, as a maintainer would.
func (f *FakeForge) CloseByHuman(n int) error { return f.CloseAs(n, "maintainer") }

// CloseAs closes an open PR without merging it, on login's account.
func (f *FakeForge) CloseAs(n int, login string) error {
	return f.edit(n, func(pr *core.PR) error {
		if pr.State != "open" {
			return fmt.Errorf("fake forge: #%d is %s", n, pr.State)
		}
		pr.State = "closed"
		f.closer[n] = login
		return nil
	})
}

// Reopen opens a closed PR again.
func (f *FakeForge) Reopen(n int) error {
	return f.edit(n, func(pr *core.PR) error {
		if pr.State != "closed" {
			return fmt.Errorf("fake forge: #%d is %s", n, pr.State)
		}
		pr.State = "open"
		delete(f.closer, n)
		return nil
	})
}

// Say adds a comment to PR n by author with the given role (OWNER, MEMBER, ...).
func (f *FakeForge) Say(n int, author, role, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return f.Err
	}
	if f.find(n) == nil {
		return fmt.Errorf("fake forge: no PR #%d", n)
	}
	f.nextID++
	var at time.Time
	if f.Now != nil {
		at = f.Now()
	}
	f.comments[n] = append(f.comments[n], core.Comment{ID: f.nextID, Author: author, Role: role, Body: body, At: at})
	return nil
}

func (f *FakeForge) edit(n int, fn func(*core.PR) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return f.Err
	}
	pr := f.find(n)
	if pr == nil {
		return fmt.Errorf("fake forge: no PR #%d", n)
	}
	return fn(pr)
}

func (f *FakeForge) find(n int) *core.PR {
	if n < 1 || n > len(f.prs) {
		return nil
	}
	return f.prs[n-1]
}
