package model

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mohsensc/tend-cli/internal/config"
)

const openaiURL = "https://api.openai.com/v1"

// openai speaks the chat completions API, which many providers and local
// servers offer at their own base URL.
type openai struct {
	poster
	name  string
	model string
	max   int
}

func newOpenAI(cfg config.Model, key string) (*openai, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("model: provider %s needs model: <id>", cfg.Provider)
	}
	h := map[string]string{}
	if key != "" {
		h["Authorization"] = "Bearer " + key
	}
	base := strings.TrimSuffix(cmp.Or(cfg.BaseURL, openaiURL), "/")
	return &openai{poster: newPoster(base+"/chat/completions", h), name: cmp.Or(cfg.Provider, "openai"), model: cfg.Model, max: replyMax}, nil
}

func (o *openai) Name() string { return o.name + "/" + o.model }

type openaiMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (o *openai) Complete(ctx context.Context, system, prompt string) (string, error) {
	msgs := []openaiMsg{{"user", prompt}}
	if system != "" {
		msgs = append([]openaiMsg{{"system", system}}, msgs...)
	}
	body := struct {
		Model     string      `json:"model"`
		MaxTokens int         `json:"max_completion_tokens"`
		Messages  []openaiMsg `json:"messages"`
	}{o.model, o.max, msgs}
	data, err := o.post(ctx, body)
	if err != nil {
		return "", fmt.Errorf("model: %s: %w", o.Name(), err)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("model: %s: %w", o.Name(), err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("model: %s: reply has no choices", o.Name())
	}
	c := out.Choices[0]
	switch {
	case c.FinishReason == "length":
		return "", fmt.Errorf("model: %s: reply cut off at %d tokens", o.Name(), o.max)
	case c.Message.Refusal != "":
		return "", fmt.Errorf("model: %s: declined: %s", o.Name(), c.Message.Refusal)
	case c.Message.Content == "":
		return "", fmt.Errorf("model: %s: reply has no text", o.Name())
	}
	return c.Message.Content, nil
}

type openaiCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openaiTurn struct {
	Role       string       `json:"role"`
	Content    *string      `json:"content"`
	ToolCalls  []openaiCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type openaiTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

// Chat sends the conversation with the tools as functions.
func (o *openai) Chat(ctx context.Context, system string, turns []Turn, tools []Tool) (*Reply, error) {
	text := func(s string) *string { return &s }
	var msgs []openaiTurn
	if system != "" {
		msgs = append(msgs, openaiTurn{Role: "system", Content: text(system)})
	}
	for _, t := range turns {
		m := openaiTurn{Role: t.Role, Content: text(t.Text), ToolCallID: t.CallID}
		switch {
		case t.Role != "assistant" || t.Text != "":
		case len(t.Calls) > 0:
			m.Content = nil
		default:
			m.Content = text(noReply)
		}
		for _, c := range t.Calls {
			var call openaiCall
			call.ID, call.Type = c.ID, "function"
			call.Function.Name, call.Function.Arguments = c.Name, string(object(c.Args))
			m.ToolCalls = append(m.ToolCalls, call)
		}
		msgs = append(msgs, m)
	}
	var defs []openaiTool
	for _, t := range tools {
		var d openaiTool
		d.Type = "function"
		d.Function.Name, d.Function.Description, d.Function.Parameters = t.Name, t.Description, t.Params
		defs = append(defs, d)
	}
	body := struct {
		Model     string       `json:"model"`
		MaxTokens int          `json:"max_completion_tokens"`
		Messages  []openaiTurn `json:"messages"`
		Tools     []openaiTool `json:"tools,omitempty"`
	}{o.model, o.max, msgs, defs}
	data, err := o.post(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("model: %s: %w", o.Name(), err)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content   string       `json:"content"`
				Refusal   string       `json:"refusal"`
				ToolCalls []openaiCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			Total int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("model: %s: %w", o.Name(), err)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("model: %s: reply has no choices", o.Name())
	}
	c := out.Choices[0]
	switch {
	case c.FinishReason == "length":
		return nil, fmt.Errorf("model: %s: reply cut off at %d tokens", o.Name(), o.max)
	case c.Message.Refusal != "":
		return nil, fmt.Errorf("model: %s: declined: %s", o.Name(), c.Message.Refusal)
	}
	res := &Reply{Text: c.Message.Content, Tokens: out.Usage.Total}
	for _, tc := range c.Message.ToolCalls {
		res.Calls = append(res.Calls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: object(json.RawMessage(tc.Function.Arguments))})
	}
	return res, nil
}
