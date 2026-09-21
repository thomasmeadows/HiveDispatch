// Package model is the supervisor's boundary to a chat model: one request
// carrying the system prompt, the conversation and the tool definitions,
// one response carrying the assistant turn. Providers implement Model;
// the fake next to it scripts responses for tests.
package model

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Role is who a message is from.
type Role string

// The three roles the loop uses. The system prompt is a Request field, not
// a message, because providers disagree about where it goes.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one turn in the conversation.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // assistant only
	ToolCallID string     `json:"tool_call_id,omitempty"` // tool results only
	IsError    bool       `json:"is_error,omitempty"`     // tool results only: the tool failed
}

// ToolCall is the model asking for a tool to run.
type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// ToolDef describes a tool to the model.
type ToolDef struct {
	Name        string
	Description string
	Schema      json.RawMessage // JSON Schema for Args
}

// Request is one call to the model.
type Request struct {
	System    string
	Messages  []Message
	Tools     []ToolDef
	MaxTokens int
}

// Usage is the token count the provider reported.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// Response is the assistant turn.
type Response struct {
	Message    Message
	StopReason string // "end_turn" | "tool_use" | "max_tokens"
	Usage      Usage
}

// Stop reasons, normalised across providers.
const (
	StopEndTurn   = "end_turn"
	StopToolUse   = "tool_use"
	StopMaxTokens = "max_tokens"
)

// Model is a chat model that can call tools.
type Model interface {
	Name() string
	Chat(ctx context.Context, req Request) (Response, error)
}

// HTTPError is a non-2xx response from a provider.
type HTTPError struct {
	Status  int
	Message string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("provider: HTTP %d: %s", e.Status, e.Message)
}

// RateLimited is a 429 or 529, so the REPL can say so and when to retry.
type RateLimited struct {
	Err        *HTTPError
	RetryAfter time.Duration // 0 when the provider did not say
}

func (e *RateLimited) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("rate limited, retry after %s: %s", e.RetryAfter, e.Err.Message)
	}
	return "rate limited: " + e.Err.Message
}

func (e *RateLimited) Unwrap() error { return e.Err }

const maxErrBody = 4 << 10

// ReadError returns nil for a 2xx response and an *HTTPError (or
// *RateLimited) otherwise, with the provider's error text pulled from the
// JSON body when it has one: {"error":{"message":...}} or {"error":"..."}.
func ReadError(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
	he := &HTTPError{Status: resp.StatusCode, Message: errorMessage(body)}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == 529 {
		rl := &RateLimited{Err: he}
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			rl.RetryAfter = time.Duration(s) * time.Second
		}
		return rl
	}
	return he
}

func errorMessage(body []byte) string {
	var obj struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &obj) == nil && len(obj.Error) > 0 {
		var s string
		if json.Unmarshal(obj.Error, &s) == nil && s != "" {
			return s
		}
		var nested struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(obj.Error, &nested) == nil && nested.Message != "" {
			return nested.Message
		}
	}
	if s := strings.TrimSpace(string(body)); s != "" {
		return s
	}
	return "no body"
}
