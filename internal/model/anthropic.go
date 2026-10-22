package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mohsensc/tend-cli/internal/config"
)

const anthropicURL = "https://api.anthropic.com/v1/messages"

type anthropic struct {
	poster
	model string
	max   int
}

func newAnthropic(cfg config.Model, key string) (*anthropic, error) {
	if cfg.Model == "" {
		return nil, errors.New("model: provider anthropic needs model: <id>")
	}
	h := map[string]string{"x-api-key": key, "anthropic-version": "2023-06-01"}
	return &anthropic{poster: newPoster(anthropicURL, h), model: cfg.Model, max: replyMax}, nil
}

func (a *anthropic) Name() string { return "anthropic/" + a.model }

type anthropicMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (a *anthropic) Complete(ctx context.Context, system, prompt string) (string, error) {
	body := struct {
		Model     string         `json:"model"`
		MaxTokens int            `json:"max_tokens"`
		System    string         `json:"system,omitempty"`
		Messages  []anthropicMsg `json:"messages"`
	}{a.model, a.max, system, []anthropicMsg{{"user", prompt}}}
	data, err := a.post(ctx, body)
	if err != nil {
		return "", fmt.Errorf("model: %s: %w", a.Name(), err)
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("model: %s: %w", a.Name(), err)
	}
	var text strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	switch {
	case out.StopReason == "max_tokens":
		return "", fmt.Errorf("model: %s: reply cut off at %d tokens", a.Name(), a.max)
	case out.StopReason == "refusal":
		return "", fmt.Errorf("model: %s: declined to answer", a.Name())
	case text.Len() == 0:
		return "", fmt.Errorf("model: %s: reply has no text", a.Name())
	}
	return text.String(), nil
}

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type anthropicTurn struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// Chat sends the conversation with the tools. Tool answers travel in the
// user's turn, so a run of them becomes one turn.
func (a *anthropic) Chat(ctx context.Context, system string, turns []Turn, tools []Tool) (*Reply, error) {
	var msgs []anthropicTurn
	for _, t := range turns {
		switch t.Role {
		case "assistant":
			var blocks []anthropicBlock
			if t.Text != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: t.Text})
			}
			for _, c := range t.Calls {
				blocks = append(blocks, anthropicBlock{Type: "tool_use", ID: c.ID, Name: c.Name, Input: object(c.Args)})
			}
			msgs = append(msgs, anthropicTurn{Role: "assistant", Content: blocks})
		case "tool":
			b := anthropicBlock{Type: "tool_result", ToolUseID: t.CallID, Content: t.Text}
			if n := len(msgs); n > 0 && msgs[n-1].Role == "user" && msgs[n-1].Content[0].Type == "tool_result" {
				msgs[n-1].Content = append(msgs[n-1].Content, b)
				continue
			}
			msgs = append(msgs, anthropicTurn{Role: "user", Content: []anthropicBlock{b}})
		default:
			msgs = append(msgs, anthropicTurn{Role: "user", Content: []anthropicBlock{{Type: "text", Text: t.Text}}})
		}
	}
	var defs []anthropicTool
	for _, t := range tools {
		defs = append(defs, anthropicTool{t.Name, t.Description, t.Params})
	}
	body := struct {
		Model     string          `json:"model"`
		MaxTokens int             `json:"max_tokens"`
		System    string          `json:"system,omitempty"`
		Tools     []anthropicTool `json:"tools,omitempty"`
		Messages  []anthropicTurn `json:"messages"`
	}{a.model, a.max, system, defs, msgs}
	data, err := a.post(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("model: %s: %w", a.Name(), err)
	}
	var out struct {
		Content    []anthropicBlock `json:"content"`
		StopReason string           `json:"stop_reason"`
		Usage      struct {
			In  int `json:"input_tokens"`
			Out int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("model: %s: %w", a.Name(), err)
	}
	switch out.StopReason {
	case "max_tokens":
		return nil, fmt.Errorf("model: %s: reply cut off at %d tokens", a.Name(), a.max)
	case "refusal":
		return nil, fmt.Errorf("model: %s: declined to answer", a.Name())
	}
	res := &Reply{Tokens: out.Usage.In + out.Usage.Out}
	var text strings.Builder
	for _, b := range out.Content {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "tool_use":
			res.Calls = append(res.Calls, ToolCall{ID: b.ID, Name: b.Name, Args: object(b.Input)})
		}
	}
	res.Text = text.String()
	return res, nil
}

// object is the arguments as a JSON object, {} when the model sent none.
func object(raw json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(raw))) == 0 || string(raw) == "null" {
		return json.RawMessage("{}")
	}
	return raw
}
