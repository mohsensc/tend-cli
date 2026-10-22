package model

import (
	"context"
	"encoding/json"

	"github.com/mohsensc/tend-cli/internal/core"
)

// Tool is a function the model may call. Params is the JSON schema of its
// arguments object.
type Tool struct {
	Name, Description string
	Params            map[string]any
}

// ToolCall is the model asking for one tool to run.
type ToolCall struct {
	ID, Name string
	Args     json.RawMessage
}

// Turn is one entry of a conversation: the user's text, the model's reply and
// the tools it called, or a tool's answer to one call.
type Turn struct {
	Role   string // user | assistant | tool
	Text   string
	Calls  []ToolCall // assistant
	CallID string     // tool: the call this answers
}

// Reply is what the model sent back, and the tokens the exchange cost.
type Reply struct {
	Text   string
	Calls  []ToolCall
	Tokens int
}

// Agent is a model that can call tools, through its API's own function
// calling. The command provider is not one.
type Agent interface {
	core.Model
	Chat(ctx context.Context, system string, turns []Turn, tools []Tool) (*Reply, error)
}
