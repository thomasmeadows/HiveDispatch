// Package openai talks to any chat/completions endpoint in the OpenAI
// shape: OpenAI itself, Hugging Face's router, Ollama, Groq, vLLM. Only the
// base URL and the key differ between them.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model"
)

// Config selects the endpoint and model.
type Config struct {
	BaseURL    string // e.g. https://api.openai.com/v1; required
	Model      string // required
	APIKey     string // empty allowed (Ollama)
	Vendor     string // label for Name(): "openai", "huggingface", "ollama"
	HTTPClient *http.Client
}

// Client implements model.Model.
type Client struct {
	cfg Config
}

var _ model.Model = (*Client)(nil)

// New validates the config.
func New(c Config) (*Client, error) {
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if c.BaseURL == "" {
		return nil, errors.New("openai: base_url is required")
	}
	if c.Model == "" {
		return nil, errors.New("openai: model is required")
	}
	if c.Vendor == "" {
		c.Vendor = "openai"
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 5 * time.Minute}
	}
	return &Client{cfg: c}, nil
}

// Name implements model.Model.
func (c *Client) Name() string { return c.cfg.Vendor + "/" + c.cfg.Model }

// Wire types.
type wireMessage struct {
	Role       string         `json:"role"`
	Content    *string        `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireTool struct {
	Type     string      `json:"type"`
	Function wireToolDef `json:"function"`
}

type wireToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type wireRequest struct {
	Model     string        `json:"model"`
	Messages  []wireMessage `json:"messages"`
	Tools     []wireTool    `json:"tools,omitempty"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

type wireResponse struct {
	Choices []struct {
		Message      wireMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func str(s string) *string { return &s }

func toWire(req model.Request) wireRequest {
	w := wireRequest{Model: "", MaxTokens: req.MaxTokens}
	if req.System != "" {
		w.Messages = append(w.Messages, wireMessage{Role: "system", Content: str(req.System)})
	}
	for _, m := range req.Messages {
		wm := wireMessage{Role: string(m.Role), Content: str(m.Content)}
		if m.Role == model.RoleTool {
			wm.ToolCallID = m.ToolCallID
		}
		for _, tc := range m.ToolCalls {
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{ID: tc.ID, Type: "function", Function: wireFunction{Name: tc.Name, Arguments: string(tc.Args)}})
		}
		if m.Role == model.RoleAssistant && m.Content == "" && len(m.ToolCalls) > 0 {
			wm.Content = nil
		}
		w.Messages = append(w.Messages, wm)
	}
	for _, t := range req.Tools {
		w.Tools = append(w.Tools, wireTool{Type: "function", Function: wireToolDef{Name: t.Name, Description: t.Description, Parameters: t.Schema}})
	}
	return w
}

// Chat implements model.Model.
func (c *Client) Chat(ctx context.Context, req model.Request) (model.Response, error) {
	w := toWire(req)
	w.Model = c.cfg.Model
	body, err := json.Marshal(w)
	if err != nil {
		return model.Response{}, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return model.Response{}, err
	}
	hr.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		hr.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	resp, err := c.cfg.HTTPClient.Do(hr)
	if err != nil {
		return model.Response{}, fmt.Errorf("%s: %w", c.Name(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := model.ReadError(resp); err != nil {
		return model.Response{}, err
	}
	var wr wireResponse
	if err := json.NewDecoder(resp.Body).Decode(&wr); err != nil {
		return model.Response{}, fmt.Errorf("%s: decode response: %w", c.Name(), err)
	}
	if len(wr.Choices) == 0 {
		return model.Response{}, fmt.Errorf("%s: response has no choices", c.Name())
	}
	ch := wr.Choices[0]
	out := model.Response{
		Message: model.Message{Role: model.RoleAssistant},
		Usage:   model.Usage{InputTokens: wr.Usage.PromptTokens, OutputTokens: wr.Usage.CompletionTokens},
	}
	if ch.Message.Content != nil {
		out.Message.Content = *ch.Message.Content
	}
	for _, tc := range ch.Message.ToolCalls {
		args := json.RawMessage(tc.Function.Arguments)
		if !json.Valid(args) {
			args, _ = json.Marshal(tc.Function.Arguments)
		}
		out.Message.ToolCalls = append(out.Message.ToolCalls, model.ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: args})
	}
	switch {
	case len(out.Message.ToolCalls) > 0 || ch.FinishReason == "tool_calls":
		out.StopReason = model.StopToolUse
	case ch.FinishReason == "length":
		out.StopReason = model.StopMaxTokens
	default:
		out.StopReason = model.StopEndTurn
	}
	return out, nil
}
