package core

import "context"

// Model is a text completion. Providers live in internal/model; nil means "no model".
type Model interface {
	Name() string
	Complete(ctx context.Context, system, prompt string) (string, error)
}
