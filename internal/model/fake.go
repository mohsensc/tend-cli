package model

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/mohsensc/tend-cli/internal/core"
)

type Call struct{ System, Prompt string }

// Script is a core.Model for tests. The nth call returns Errs[n] when it is set,
// else Replies[n]; running past the end of the script is an error.
type Script struct {
	Replies []string
	Errs    []error
	Calls   []Call

	mu sync.Mutex
}

var _ core.Model = (*Script)(nil)

func (s *Script) Name() string { return "script" }

func (s *Script) Complete(ctx context.Context, system, prompt string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.Calls)
	s.Calls = append(s.Calls, Call{System: system, Prompt: prompt})
	if n < len(s.Errs) && s.Errs[n] != nil {
		return "", s.Errs[n]
	}
	if n >= len(s.Replies) {
		return "", fmt.Errorf("script: no reply for call %d", n+1)
	}
	return s.Replies[n], nil
}

// Steps is an Agent for tests: the nth Chat returns Replies[n], and the
// conversation it was given is kept. Complete answers from the embedded Script.
type Steps struct {
	Script
	Replies []Reply
	Chats   [][]Turn

	mu sync.Mutex
}

var _ Agent = (*Steps)(nil)

func (s *Steps) Chat(ctx context.Context, system string, turns []Turn, tools []Tool) (*Reply, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.Chats)
	s.Chats = append(s.Chats, slices.Clone(turns))
	if n >= len(s.Replies) {
		return nil, fmt.Errorf("steps: no reply for turn %d", n+1)
	}
	r := s.Replies[n]
	return &r, nil
}
