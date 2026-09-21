// Package anthropic talks to the Anthropic Messages API.
package anthropic

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

const (
	defaultBaseURL   = "https://api.anthropic.com"
	apiVersion       = "2023-06-01"
	defaultMaxTokens = 4096
)

// Config selects the model and key.
type Config struct {
	Model      string // required
	APIKey     string // required
	BaseURL    string // default https://api.anthropic.com
	HTTPClient *http.Client
}

// Client implements model.Model.
type Client struct {
	cfg Config
}

var _ model.Model = (*Client)(nil)

// New validates the config.
func New(c Config) (*Client, error) {
	if c.Model == "" {
		return nil, errors.New("anthropic: model is required")
	}
	if c.APIKey == "" {
		return nil, errors.New("anthropic: api key is required")
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 5 * time.Minute}
	}
	return &Client{cfg: c}, nil
}

// Name implements model.Model.
func (c *Client) Name() string { return "anthropic/" + c.cfg.Model }

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`          // tool_use
	Name      string          `json:"name,omitempty"`        // tool_use
	Input     json.RawMessage `json:"input,omitempty"`       // tool_use
	ToolUseID string          `json:"tool_use_id,omitempty"` // tool_result
	Content   string          `json:"content,omitempty"`     // tool_result
	IsError   bool            `json:"is_error,omitempty"`    // tool_result
}

type wireMessage struct {
	Role    string  `json:"role"`
	Content []block `json:"content"`
}

type wireTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type wireRequest struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	System    string        `json:"system,omitempty"`
	Messages  []wireMessage `json:"messages"`
	Tools     []wireTool    `json:"tools,omitempty"`
}

type wireResponse struct {
	Content    []block `json:"content"`
	StopReason string  `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// toWire converts messages, folding tool results into user messages and
// merging consecutive same-role messages so roles alternate.
func toWire(req model.Request) wireRequest {
	w := wireRequest{System: req.System, MaxTokens: req.MaxTokens}
	if w.MaxTokens == 0 {
		w.MaxTokens = defaultMaxTokens
	}
	push := func(role string, blocks ...block) {
		if n := len(w.Messages); n > 0 && w.Messages[n-1].Role == role {
			w.Messages[n-1].Content = append(w.Messages[n-1].Content, blocks...)
			return
		}
		w.Messages = append(w.Messages, wireMessage{Role: role, Content: blocks})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case model.RoleTool:
			push("user", block{Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Content, IsError: m.IsError})
		case model.RoleAssistant:
			var blocks []block
			if m.Content != "" {
				blocks = append(blocks, block{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				input := tc.Args
				if len(input) == 0 {
					input = json.RawMessage(`{}`)
				}
				blocks = append(blocks, block{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: input})
			}
			push("assistant", blocks...)
		default:
			push("user", block{Type: "text", Text: m.Content})
		}
	}
	for _, t := range req.Tools {
		w.Tools = append(w.Tools, wireTool{Name: t.Name, Description: t.Description, InputSchema: t.Schema})
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
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return model.Response{}, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("x-api-key", c.cfg.APIKey)
	hr.Header.Set("anthropic-version", apiVersion)
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
	out := model.Response{
		Message:    model.Message{Role: model.RoleAssistant},
		StopReason: wr.StopReason,
		Usage:      model.Usage{InputTokens: wr.Usage.InputTokens, OutputTokens: wr.Usage.OutputTokens},
	}
	var text []string
	for _, b := range wr.Content {
		switch b.Type {
		case "text":
			text = append(text, b.Text)
		case "tool_use":
			out.Message.ToolCalls = append(out.Message.ToolCalls, model.ToolCall{ID: b.ID, Name: b.Name, Args: b.Input})
		}
	}
	out.Message.Content = strings.Join(text, "\n")
	if out.StopReason == "" {
		out.StopReason = model.StopEndTurn
	}
	return out, nil
}
