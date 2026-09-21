# Phase 8: Supervisor Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `hivedispatch supervisor` — HiveDispatch's own interactive agent, with its own loop, tools and memory, that helps an operator configure and run HiveDispatch: reads the embedded docs, edits the worker config (with confirmation), runs the allowlisted `hivedispatch` subcommands, and remembers notes between sessions.

**Architecture:** Everything lives under `internal/supervisor`. `internal/supervisor/model` is the provider boundary (`Model` interface; `fake`, `openai`, `anthropic` sub-packages, stdlib `net/http`). `internal/supervisor` holds the agent loop (`agent.go`), tools (`tools*.go`), memory (`memory.go`), prompt (`prompt.go`), config (`config.go`), REPL (`repl.go`) and the wiring (`New` in `supervisor.go`). The command is `cmd/hivedispatch/supervisor.go`. The worker `config` package, starter config, dispatcher and executors are not modified.

**Tech Stack:** Go 1.27 stdlib + `gopkg.in/yaml.v3` (already a dependency). OpenAI `chat/completions` wire format (Hugging Face router, Ollama, OpenAI); Anthropic Messages API `2023-06-01`.

**Spec:** `docs/superpowers/specs/2026-09-20-supervisor-agent-design.md` — the plan argues from it; read it first.

## Global Constraints

- Go 1.27, stdlib plus `gopkg.in/yaml.v3` only. No new dependencies.
- Every new file lives under `internal/supervisor/`, except: `docs/docs.go` (embed), `cmd/hivedispatch/supervisor.go`, one `case` + one usage line in `cmd/hivedispatch/main.go`, and the docs.
- Unit tests never touch the network: `httptest.Server` for adapters, `model/fake` for the loop.
- Errors are never discarded (`errcheck` includes `Close`); write `_ = f()` when an error is deliberately ignored.
- Every tool that mutates anything outside the notes file asks `[y/N]` in the terminal; non-tty answers no.
- Before every commit: `go vet ./... && go test -race ./... && golangci-lint run ./...` green. Conventional commit messages ending with `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.
- Decisions are append-only in `docs/decisions.md`.

---

## File Structure

```
docs/docs.go                                    package docs; //go:embed *.md → FS
internal/supervisor/model/model.go              Role, Message, ToolCall, ToolDef, Request, Response, Usage, Model, HTTPError, RateLimited, ReadError
internal/supervisor/model/model_test.go         ReadError
internal/supervisor/model/fake/fake.go          scripted Model
internal/supervisor/model/openai/openai.go      chat/completions adapter
internal/supervisor/model/openai/openai_test.go httptest round trips
internal/supervisor/model/anthropic/anthropic.go     Messages API adapter
internal/supervisor/model/anthropic/anthropic_test.go
internal/supervisor/config.go                   Config, ConfigPath, LoadConfig, presets, NewModel
internal/supervisor/config_test.go
internal/supervisor/agent.go                    Tool, Event, Agent, Turn
internal/supervisor/agent_test.go
internal/supervisor/diff.go                     Diff(a, b string) string
internal/supervisor/diff_test.go
internal/supervisor/tools.go                    read_config, write_config, read_doc, read_repo_file, remember
internal/supervisor/tools_test.go
internal/supervisor/tools_run.go                run_hivedispatch (allowlist, re-exec)
internal/supervisor/tools_run_test.go
internal/supervisor/memory.go                   Memory: notes + sessions
internal/supervisor/memory_test.go
internal/supervisor/prompt.go                   BuildSystem, CheckOutput
internal/supervisor/prompt_test.go
internal/supervisor/repl.go                     REPL, Confirm, slash commands, signals, spinner
internal/supervisor/repl_test.go
internal/supervisor/supervisor.go               Options, New — wires everything
cmd/hivedispatch/supervisor.go                  runSupervisor
cmd/hivedispatch/main.go                        + case "supervisor", usage line
cmd/hivedispatch/main_test.go                   + TestSupervisorOneShotFake
docs/config.md, docs/setup.md, README.md, docs/design-spec.md, docs/decisions.md
```

---

### Task 1: Embedded docs and the model types

**Files:**
- Create: `docs/docs.go`, `internal/supervisor/model/model.go`, `internal/supervisor/model/model_test.go`, `internal/supervisor/model/fake/fake.go`

**Interfaces:**
- Produces:

```go
// package docs
var FS embed.FS                       // setup.md, config.md, design-spec.md, decisions.md

// package model
type Role string
const ( RoleUser Role = "user"; RoleAssistant Role = "assistant"; RoleTool Role = "tool" )
type Message struct { Role Role; Content string; ToolCalls []ToolCall; ToolCallID string; IsError bool }
type ToolCall struct { ID, Name string; Args json.RawMessage }
type ToolDef struct { Name, Description string; Schema json.RawMessage }
type Request struct { System string; Messages []Message; Tools []ToolDef; MaxTokens int }
type Usage struct { InputTokens, OutputTokens int }
type Response struct { Message Message; StopReason string; Usage Usage } // "end_turn" | "tool_use" | "max_tokens"
type Model interface { Name() string; Chat(ctx context.Context, req Request) (Response, error) }
type HTTPError struct { Status int; Message string }         // Error(): "provider: HTTP 401: <message>"
type RateLimited struct { Err *HTTPError; RetryAfter time.Duration } // Error(), Unwrap()
func ReadError(resp *http.Response) error                    // non-2xx → *HTTPError, 429/529 → *RateLimited

// package fake
type Model struct { Responses []model.Response; Fail error; Calls []model.Request } // + mutex
func New(responses ...model.Response) *Model
```

- [ ] **Step 1: `docs/docs.go`**

```go
// Package docs embeds the operator documentation so the supervisor can
// read it at runtime without a checkout.
package docs

import "embed"

// FS holds every markdown file in this directory.
//
//go:embed *.md
var FS embed.FS
```

- [ ] **Step 2: Failing test for `ReadError`** — `internal/supervisor/model/model_test.go`:

```go
package model

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func fetch(t *testing.T, status int, body string, hdr map[string]string) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	return ReadError(resp)
}

func TestReadErrorNilOn2xx(t *testing.T) {
	if err := fetch(t, 200, `{}`, nil); err != nil {
		t.Fatal(err)
	}
}

func TestReadErrorExtractsMessage(t *testing.T) {
	err := fetch(t, 401, `{"error":{"type":"authentication_error","message":"invalid x-api-key"}}`, nil)
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 401 || he.Message != "invalid x-api-key" {
		t.Fatalf("got %#v", err)
	}
	err = fetch(t, 400, `{"error":"model not found"}`, nil)
	if !errors.As(err, &he) || he.Message != "model not found" {
		t.Fatalf("got %#v", err)
	}
	err = fetch(t, 500, `<html>oops</html>`, nil)
	if !errors.As(err, &he) || he.Message != "<html>oops</html>" {
		t.Fatalf("got %#v", err)
	}
}

func TestReadErrorRateLimited(t *testing.T) {
	err := fetch(t, 429, `{"error":{"message":"slow down"}}`, map[string]string{"Retry-After": "30"})
	var rl *RateLimited
	if !errors.As(err, &rl) || rl.RetryAfter != 30*time.Second || rl.Err.Message != "slow down" {
		t.Fatalf("got %#v", err)
	}
	var he *HTTPError
	if !errors.As(err, &he) {
		t.Fatal("RateLimited must unwrap to HTTPError")
	}
}
```

- [ ] **Step 3: Run** `go test ./internal/supervisor/model/` → FAIL (package does not exist).

- [ ] **Step 4: `model.go`**

```go
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
	Role       Role
	Content    string
	ToolCalls  []ToolCall // assistant only
	ToolCallID string     // tool results only
	IsError    bool       // tool results only: the tool failed
}

// ToolCall is the model asking for a tool to run.
type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
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
```

Add JSON tags so session transcripts are readable: on `Message` — `json:"role"`, `json:"content,omitempty"`, `json:"tool_calls,omitempty"`, `json:"tool_call_id,omitempty"`, `json:"is_error,omitempty"`; on `ToolCall` — `json:"id"`, `json:"name"`, `json:"args"`.

- [ ] **Step 5: `fake/fake.go`**

```go
// Package fake is a scripted model.Model for tests.
package fake

import (
	"context"
	"sync"

	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model"
)

// Model returns Responses in order and records every Request. When the
// script is exhausted it returns an empty end_turn so a runaway loop
// terminates. Fail, when set, is returned once by the next Chat.
type Model struct {
	mu        sync.Mutex
	Responses []model.Response
	Fail      error
	Calls     []model.Request
}

var _ model.Model = (*Model)(nil)

// New returns a Model scripted with responses.
func New(responses ...model.Response) *Model {
	return &Model{Responses: responses}
}

// Name implements model.Model.
func (m *Model) Name() string { return "fake" }

// Chat implements model.Model.
func (m *Model) Chat(_ context.Context, req model.Request) (model.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, req)
	if m.Fail != nil {
		err := m.Fail
		m.Fail = nil
		return model.Response{}, err
	}
	if len(m.Responses) == 0 {
		return model.Response{Message: model.Message{Role: model.RoleAssistant}, StopReason: model.StopEndTurn}, nil
	}
	r := m.Responses[0]
	m.Responses = m.Responses[1:]
	return r, nil
}

// Text is a convenience: an end_turn response with only text.
func Text(s string) model.Response {
	return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: s}, StopReason: model.StopEndTurn}
}

// Call is a convenience: a tool_use response with one call.
func Call(id, name, args string) model.Response {
	return model.Response{
		Message:    model.Message{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: id, Name: name, Args: []byte(args)}}},
		StopReason: model.StopToolUse,
	}
}
```

- [ ] **Step 6: Verify** `go vet ./docs/ ./internal/supervisor/... && go test -race ./internal/supervisor/... && golangci-lint run ./docs/ ./internal/supervisor/...` → PASS.

- [ ] **Step 7: Commit** `feat(supervisor): model interface, error types, scripted fake; embed docs`.

---

### Task 2: OpenAI-compatible adapter

**Files:**
- Create: `internal/supervisor/model/openai/openai.go`, `openai_test.go`

**Interfaces:**
- Produces:

```go
type Config struct { BaseURL, Model, APIKey, Vendor string; HTTPClient *http.Client }
func New(c Config) (*Client, error)   // BaseURL and Model required; trailing "/" trimmed
func (c *Client) Name() string        // "<vendor>/<model>"
func (c *Client) Chat(ctx, req) (model.Response, error)
```

- [ ] **Step 1: Failing tests** — `openai_test.go`:

```go
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model"
)

// server replies with body and captures the request JSON.
func server(t *testing.T, status int, body string) (*httptest.Server, *map[string]any) {
	t.Helper()
	got := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if a := r.Header.Get("Authorization"); a != "Bearer k" {
			t.Errorf("auth = %q", a)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Errorf("request not JSON: %v", err)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func client(t *testing.T, url string) *Client {
	t.Helper()
	c, err := New(Config{BaseURL: url + "/v1/", Model: "m", APIKey: "k", Vendor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestChatText(t *testing.T) {
	srv, got := server(t, 200, `{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`)
	c := client(t, srv.URL)
	resp, err := c.Chat(context.Background(), model.Request{System: "sys", Messages: []model.Message{{Role: model.RoleUser, Content: "hello"}}, MaxTokens: 10})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "hi" || resp.StopReason != model.StopEndTurn || resp.Usage.InputTokens != 5 {
		t.Fatalf("resp = %+v", resp)
	}
	msgs := (*got)["messages"].([]any)
	if first := msgs[0].(map[string]any); first["role"] != "system" || first["content"] != "sys" {
		t.Errorf("system message = %v", first)
	}
	if (*got)["model"] != "m" || (*got)["max_tokens"] != float64(10) {
		t.Errorf("request = %v", *got)
	}
	if c.Name() != "test/m" {
		t.Errorf("name = %s", c.Name())
	}
}

func TestChatToolCall(t *testing.T) {
	srv, _ := server(t, 200, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"read_doc","arguments":"{\"name\":\"setup\"}"}}]},"finish_reason":"tool_calls"}]}`)
	c := client(t, srv.URL)
	resp, err := c.Chat(context.Background(), model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != model.StopToolUse || len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
	tc := resp.Message.ToolCalls[0]
	if tc.ID != "c1" || tc.Name != "read_doc" || string(tc.Args) != `{"name":"setup"}` {
		t.Errorf("call = %+v", tc)
	}
}

func TestChatSendsToolsAndResults(t *testing.T) {
	srv, got := server(t, 200, `{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)
	c := client(t, srv.URL)
	_, err := c.Chat(context.Background(), model.Request{
		Tools: []model.ToolDef{{Name: "read_doc", Description: "d", Schema: []byte(`{"type":"object"}`)}},
		Messages: []model.Message{
			{Role: model.RoleUser, Content: "x"},
			{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "c1", Name: "read_doc", Args: []byte(`{"name":"setup"}`)}}},
			{Role: model.RoleTool, ToolCallID: "c1", Content: "# Setup"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools := (*got)["tools"].([]any)
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "read_doc" || fn["parameters"].(map[string]any)["type"] != "object" {
		t.Errorf("tools = %v", tools)
	}
	msgs := (*got)["messages"].([]any)
	asst := msgs[1].(map[string]any)
	call := asst["tool_calls"].([]any)[0].(map[string]any)
	if call["id"] != "c1" || call["function"].(map[string]any)["arguments"] != `{"name":"setup"}` {
		t.Errorf("assistant = %v", asst)
	}
	res := msgs[2].(map[string]any)
	if res["role"] != "tool" || res["tool_call_id"] != "c1" || res["content"] != "# Setup" {
		t.Errorf("tool result = %v", res)
	}
}

func TestChatErrors(t *testing.T) {
	srv, _ := server(t, 401, `{"error":{"message":"bad key"}}`)
	_, err := client(t, srv.URL).Chat(context.Background(), model.Request{})
	var he *model.HTTPError
	if !errors.As(err, &he) || he.Status != 401 || he.Message != "bad key" {
		t.Fatalf("err = %v", err)
	}
	srv2, _ := server(t, 429, `{"error":{"message":"slow"}}`)
	_, err = client(t, srv2.URL).Chat(context.Background(), model.Request{})
	var rl *model.RateLimited
	if !errors.As(err, &rl) {
		t.Fatalf("err = %v", err)
	}
}

func TestNewRequiresFields(t *testing.T) {
	if _, err := New(Config{Model: "m"}); err == nil {
		t.Error("missing base_url accepted")
	}
	if _, err := New(Config{BaseURL: "http://x"}); err == nil {
		t.Error("missing model accepted")
	}
}
```

- [ ] **Step 2: Run** → FAIL (no package).

- [ ] **Step 3: `openai.go`**

```go
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
```

- [ ] **Step 4: Run** `go test -race ./internal/supervisor/model/...` → PASS. Lint.

- [ ] **Step 5: Commit** `feat(supervisor): openai-compatible chat/completions adapter`.

---

### Task 3: Anthropic adapter

**Files:**
- Create: `internal/supervisor/model/anthropic/anthropic.go`, `anthropic_test.go`

**Interfaces:**
- Produces:

```go
type Config struct { Model, APIKey, BaseURL string; HTTPClient *http.Client } // BaseURL default https://api.anthropic.com
func New(c Config) (*Client, error)   // Model and APIKey required
func (c *Client) Name() string        // "anthropic/<model>"
```

Rules: `system` top-level; assistant content is `text` + `tool_use` blocks; tool results are `tool_result` blocks inside a `user` message; **consecutive user-role messages (tool results, then a user text) are merged into one message** so the roles alternate as the API requires; `max_tokens` defaults to 4096 when the request says 0 (the API requires it).

- [ ] **Step 1: Failing tests** — `anthropic_test.go`:

```go
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model"
)

func server(t *testing.T, status int, body string) (*httptest.Server, *map[string]any) {
	t.Helper()
	got := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "k" || r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("headers = %v", r.Header)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Errorf("request not JSON: %v", err)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func client(t *testing.T, url string) *Client {
	t.Helper()
	c, err := New(Config{Model: "claude-x", APIKey: "k", BaseURL: url})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestChatText(t *testing.T) {
	srv, got := server(t, 200, `{"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":2}}`)
	c := client(t, srv.URL)
	resp, err := c.Chat(context.Background(), model.Request{System: "sys", Messages: []model.Message{{Role: model.RoleUser, Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "hi" || resp.StopReason != model.StopEndTurn || resp.Usage.InputTokens != 7 {
		t.Fatalf("resp = %+v", resp)
	}
	if (*got)["system"] != "sys" || (*got)["max_tokens"] != float64(4096) || (*got)["model"] != "claude-x" {
		t.Errorf("request = %v", *got)
	}
	if c.Name() != "anthropic/claude-x" {
		t.Errorf("name = %s", c.Name())
	}
}

func TestChatToolUse(t *testing.T) {
	srv, _ := server(t, 200, `{"content":[{"type":"text","text":"let me look"},{"type":"tool_use","id":"t1","name":"read_doc","input":{"name":"setup"}}],"stop_reason":"tool_use"}`)
	resp, err := client(t, srv.URL).Chat(context.Background(), model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != model.StopToolUse || resp.Message.Content != "let me look" || len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
	if tc := resp.Message.ToolCalls[0]; tc.ID != "t1" || tc.Name != "read_doc" || string(tc.Args) != `{"name":"setup"}` {
		t.Errorf("call = %+v", tc)
	}
}

func TestChatSendsToolsAndMergesResults(t *testing.T) {
	srv, got := server(t, 200, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`)
	_, err := client(t, srv.URL).Chat(context.Background(), model.Request{
		Tools: []model.ToolDef{{Name: "read_doc", Description: "d", Schema: []byte(`{"type":"object"}`)}},
		Messages: []model.Message{
			{Role: model.RoleUser, Content: "x"},
			{Role: model.RoleAssistant, Content: "looking", ToolCalls: []model.ToolCall{{ID: "t1", Name: "read_doc", Args: []byte(`{"name":"setup"}`)}, {ID: "t2", Name: "read_config", Args: []byte(`{}`)}}},
			{Role: model.RoleTool, ToolCallID: "t1", Content: "# Setup"},
			{Role: model.RoleTool, ToolCallID: "t2", Content: "boom", IsError: true},
			{Role: model.RoleUser, Content: "and then?"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools := (*got)["tools"].([]any)[0].(map[string]any)
	if tools["name"] != "read_doc" || tools["input_schema"].(map[string]any)["type"] != "object" {
		t.Errorf("tools = %v", tools)
	}
	msgs := (*got)["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages (user, assistant, merged user), got %d: %v", len(msgs), msgs)
	}
	asst := msgs[1].(map[string]any)["content"].([]any)
	if asst[0].(map[string]any)["type"] != "text" || asst[1].(map[string]any)["type"] != "tool_use" || asst[1].(map[string]any)["id"] != "t1" {
		t.Errorf("assistant = %v", asst)
	}
	merged := msgs[2].(map[string]any)
	blocks := merged["content"].([]any)
	if merged["role"] != "user" || len(blocks) != 3 {
		t.Fatalf("merged = %v", merged)
	}
	if b := blocks[1].(map[string]any); b["type"] != "tool_result" || b["tool_use_id"] != "t2" || b["is_error"] != true {
		t.Errorf("error result = %v", b)
	}
	if b := blocks[2].(map[string]any); b["type"] != "text" || b["text"] != "and then?" {
		t.Errorf("trailing text = %v", b)
	}
}

func TestChatErrors(t *testing.T) {
	srv, _ := server(t, 401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	_, err := client(t, srv.URL).Chat(context.Background(), model.Request{})
	var he *model.HTTPError
	if !errors.As(err, &he) || he.Message != "invalid x-api-key" {
		t.Fatalf("err = %v", err)
	}
	srv2, _ := server(t, 529, `{"error":{"message":"overloaded"}}`)
	_, err = client(t, srv2.URL).Chat(context.Background(), model.Request{})
	var rl *model.RateLimited
	if !errors.As(err, &rl) {
		t.Fatalf("err = %v", err)
	}
}

func TestNewRequiresFields(t *testing.T) {
	if _, err := New(Config{Model: "m"}); err == nil {
		t.Error("missing key accepted")
	}
	if _, err := New(Config{APIKey: "k"}); err == nil {
		t.Error("missing model accepted")
	}
}
```

- [ ] **Step 2: Run** → FAIL.

- [ ] **Step 3: `anthropic.go`**

```go
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
```

- [ ] **Step 4: Run** tests + lint → PASS.

- [ ] **Step 5: Commit** `feat(supervisor): anthropic messages adapter`.

---

### Task 4: Supervisor config and the model factory

**Files:**
- Create: `internal/supervisor/config.go`, `config_test.go`

**Interfaces:**
- Produces:

```go
type Config struct {
    Provider   string `yaml:"provider"`    // anthropic | openai | huggingface | ollama | (fake: flag only)
    Model      string `yaml:"model"`
    BaseURL    string `yaml:"base_url"`
    APIKeyEnv  string `yaml:"api_key_env"`
    MaxTokens  int    `yaml:"max_tokens"`  // default 4096
    StepBudget int    `yaml:"step_budget"` // default 20
    FromEnv    bool   `yaml:"-"`           // provider was chosen from the environment
}
func Dir(workerConfigPath string) string           // filepath.Join(filepath.Dir(workerConfigPath), "supervisor")
func ConfigPath(workerConfigPath string) string    // Dir(...)/config.yaml
func LoadConfig(path string, getenv func(string) string) (Config, error) // missing file → defaults
func (c *Config) Override(provider, modelName string) // flags; re-applies presets
func NewModel(c Config, getenv func(string) string) (model.Model, error)
func ProbeOllama(ctx context.Context, baseURL string) error // GET base_url/models
```

Presets:

| provider | model | base_url | api_key_env |
|---|---|---|---|
| anthropic | `claude-sonnet-5` | `https://api.anthropic.com` | `ANTHROPIC_API_KEY` |
| openai | `gpt-5-mini` | `https://api.openai.com/v1` | `OPENAI_API_KEY` |
| huggingface | `Qwen/Qwen3-32B` | `https://router.huggingface.co/v1` | `HF_TOKEN` |
| ollama | `qwen3` | `http://localhost:11434/v1` | *(none)* |

Provider from env when empty: `ANTHROPIC_API_KEY` → anthropic; `OPENAI_API_KEY` → openai; `HF_TOKEN` → huggingface; else ollama (`FromEnv = true` in all four cases).

- [ ] **Step 1: Failing tests** — `config_test.go`:

```go
package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfigMissingFileDefaultsFromEnv(t *testing.T) {
	c, err := LoadConfig(filepath.Join(t.TempDir(), "nope.yaml"), env(map[string]string{"ANTHROPIC_API_KEY": "k"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Provider != "anthropic" || c.Model != "claude-sonnet-5" || c.APIKeyEnv != "ANTHROPIC_API_KEY" || !c.FromEnv {
		t.Errorf("config = %+v", c)
	}
	if c.MaxTokens != 4096 || c.StepBudget != 20 {
		t.Errorf("budgets = %+v", c)
	}
}

func TestProviderFromEnvOrder(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"ANTHROPIC_API_KEY": "a", "OPENAI_API_KEY": "o", "HF_TOKEN": "h"}, "anthropic"},
		{map[string]string{"OPENAI_API_KEY": "o", "HF_TOKEN": "h"}, "openai"},
		{map[string]string{"HF_TOKEN": "h"}, "huggingface"},
		{map[string]string{}, "ollama"},
	}
	for _, tc := range cases {
		c, err := LoadConfig(filepath.Join(t.TempDir(), "nope.yaml"), env(tc.env))
		if err != nil {
			t.Fatal(err)
		}
		if c.Provider != tc.want {
			t.Errorf("env %v: provider = %s, want %s", tc.env, c.Provider, tc.want)
		}
	}
}

func TestLoadConfigFileWinsAndPresetsFill(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte("provider: huggingface\nmodel: meta-llama/Llama-3.3-70B-Instruct\nstep_budget: 5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(p, env(map[string]string{"ANTHROPIC_API_KEY": "a"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Provider != "huggingface" || c.FromEnv || c.BaseURL != "https://router.huggingface.co/v1" || c.APIKeyEnv != "HF_TOKEN" || c.StepBudget != 5 || c.Model != "meta-llama/Llama-3.3-70B-Instruct" {
		t.Errorf("config = %+v", c)
	}
}

func TestLoadConfigRejects(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte("provider: bard\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(p, env(nil)); err == nil || !strings.Contains(err.Error(), "provider") {
		t.Errorf("err = %v", err)
	}
	if err := os.WriteFile(p, []byte("step_budget: -1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(p, env(nil)); err == nil || !strings.Contains(err.Error(), "step_budget") {
		t.Errorf("err = %v", err)
	}
}

func TestOverride(t *testing.T) {
	c, _ := LoadConfig(filepath.Join(t.TempDir(), "nope.yaml"), env(nil))
	c.Override("openai", "")
	if c.Provider != "openai" || c.Model != "gpt-5-mini" || c.BaseURL != "https://api.openai.com/v1" || c.FromEnv {
		t.Errorf("config = %+v", c)
	}
	c.Override("", "gpt-5")
	if c.Provider != "openai" || c.Model != "gpt-5" {
		t.Errorf("config = %+v", c)
	}
}

func TestNewModel(t *testing.T) {
	c, _ := LoadConfig(filepath.Join(t.TempDir(), "nope.yaml"), env(map[string]string{"ANTHROPIC_API_KEY": "k"}))
	m, err := NewModel(c, env(map[string]string{"ANTHROPIC_API_KEY": "k"}))
	if err != nil || m.Name() != "anthropic/claude-sonnet-5" {
		t.Fatalf("model = %v, err = %v", m, err)
	}
	if _, err := NewModel(c, env(nil)); err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY is not set") {
		t.Errorf("err = %v", err)
	}
	c.Override("ollama", "")
	if m, err := NewModel(c, env(nil)); err != nil || m.Name() != "ollama/qwen3" {
		t.Errorf("ollama: %v %v", m, err)
	}
	c.Override("fake", "")
	if m, err := NewModel(c, env(nil)); err != nil || m.Name() != "fake" {
		t.Errorf("fake: %v %v", m, err)
	}
}

func TestPaths(t *testing.T) {
	if got := ConfigPath("/home/u/.config/hivedispatch/config.yaml"); got != "/home/u/.config/hivedispatch/supervisor/config.yaml" {
		t.Errorf("ConfigPath = %s", got)
	}
}
```

- [ ] **Step 2: Run** → FAIL.

- [ ] **Step 3: `config.go`**

```go
// Package supervisor is HiveDispatch's own agent: a loop over a chat model
// with tools that read the docs, edit the worker config and run the
// allowlisted hivedispatch subcommands, plus a notes file it remembers
// between sessions. It helps an operator get HiveDispatch running.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model"
	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model/anthropic"
	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model/fake"
	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model/openai"
)

// Config is the supervisor's own settings, kept in its own file so nothing
// about the supervisor lives in the worker config it helps to write.
type Config struct {
	Provider   string `yaml:"provider"`
	Model      string `yaml:"model"`
	BaseURL    string `yaml:"base_url"`
	APIKeyEnv  string `yaml:"api_key_env"`
	MaxTokens  int    `yaml:"max_tokens"`
	StepBudget int    `yaml:"step_budget"`
	FromEnv    bool   `yaml:"-"` // provider was chosen from the environment, not the file or a flag
}

type preset struct {
	model, baseURL, keyEnv string
}

var presets = map[string]preset{
	"anthropic":   {"claude-sonnet-5", "https://api.anthropic.com", "ANTHROPIC_API_KEY"},
	"openai":      {"gpt-5-mini", "https://api.openai.com/v1", "OPENAI_API_KEY"},
	"huggingface": {"Qwen/Qwen3-32B", "https://router.huggingface.co/v1", "HF_TOKEN"},
	"ollama":      {"qwen3", "http://localhost:11434/v1", ""},
	"fake":        {"fake", "", ""},
}

// Providers lists the configurable providers, for messages.
const Providers = "anthropic, openai, huggingface or ollama"

// Dir is the supervisor's directory beside the worker config.
func Dir(workerConfigPath string) string {
	return filepath.Join(filepath.Dir(workerConfigPath), "supervisor")
}

// ConfigPath is the supervisor config file beside the worker config.
func ConfigPath(workerConfigPath string) string {
	return filepath.Join(Dir(workerConfigPath), "config.yaml")
}

// LoadConfig reads the file, or returns defaults when it is missing.
func LoadConfig(path string, getenv func(string) string) (Config, error) {
	var c Config
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return c, fmt.Errorf("read supervisor config: %w", err)
	default:
		if err := yaml.Unmarshal(raw, &c); err != nil {
			return c, fmt.Errorf("parse supervisor config %s: %w", path, err)
		}
	}
	if c.Provider == "fake" {
		return c, fmt.Errorf("supervisor config %s: provider fake is for tests; pass -provider fake on the command line instead", path)
	}
	if c.Provider == "" {
		c.Provider = providerFromEnv(getenv)
		c.FromEnv = true
	}
	if err := c.validate(); err != nil {
		return c, fmt.Errorf("supervisor config %s: %w", path, err)
	}
	c.applyPreset()
	return c, nil
}

func providerFromEnv(getenv func(string) string) string {
	switch {
	case getenv("ANTHROPIC_API_KEY") != "":
		return "anthropic"
	case getenv("OPENAI_API_KEY") != "":
		return "openai"
	case getenv("HF_TOKEN") != "":
		return "huggingface"
	default:
		return "ollama"
	}
}

func (c *Config) validate() error {
	if _, ok := presets[c.Provider]; !ok {
		return fmt.Errorf("provider: want %s, got %q", Providers, c.Provider)
	}
	if c.MaxTokens < 0 {
		return errors.New("max_tokens must be positive")
	}
	if c.StepBudget < 0 {
		return errors.New("step_budget must be positive")
	}
	return nil
}

// applyPreset fills empty fields from the provider's preset.
func (c *Config) applyPreset() {
	p := presets[c.Provider]
	if c.Model == "" {
		c.Model = p.model
	}
	if c.BaseURL == "" {
		c.BaseURL = p.baseURL
	}
	if c.APIKeyEnv == "" {
		c.APIKeyEnv = p.keyEnv
	}
	if c.MaxTokens == 0 {
		c.MaxTokens = 4096
	}
	if c.StepBudget == 0 {
		c.StepBudget = 20
	}
}

// Override applies -provider and -model. A new provider resets the model,
// base URL and key env so its presets apply.
func (c *Config) Override(provider, modelName string) {
	if provider != "" && provider != c.Provider {
		c.Provider = provider
		c.Model, c.BaseURL, c.APIKeyEnv = "", "", ""
		c.FromEnv = false
	}
	if modelName != "" {
		c.Model = modelName
	}
	c.applyPreset()
}

// NewModel builds the provider's client. A missing key is reported here,
// naming the variable, never on the first call.
func NewModel(c Config, getenv func(string) string) (model.Model, error) {
	key := ""
	if c.APIKeyEnv != "" {
		key = getenv(c.APIKeyEnv)
		if key == "" {
			return nil, fmt.Errorf("%s is not set (supervisor provider %s; change api_key_env in the supervisor config to use another variable)", c.APIKeyEnv, c.Provider)
		}
	}
	switch c.Provider {
	case "anthropic":
		return anthropic.New(anthropic.Config{Model: c.Model, APIKey: key, BaseURL: c.BaseURL})
	case "openai", "huggingface", "ollama":
		return openai.New(openai.Config{BaseURL: c.BaseURL, Model: c.Model, APIKey: key, Vendor: c.Provider})
	case "fake":
		return fake.New(fake.Text("(fake supervisor: no model configured)")), nil
	default:
		return nil, fmt.Errorf("provider: want %s, got %q", Providers, c.Provider)
	}
}

// ProbeOllama checks that an Ollama server answers at baseURL, so a
// defaulted provider fails with a useful message instead of a timeout on
// the first turn.
func ProbeOllama(ctx context.Context, baseURL string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, req.URL)
	}
	return nil
}
```

- [ ] **Step 4: Run** tests + lint → PASS.

- [ ] **Step 5: Commit** `feat(supervisor): own config file with provider presets and env-based default`.

---

### Task 5: Agent loop

**Files:**
- Create: `internal/supervisor/agent.go`, `agent_test.go`

**Interfaces:**
- Produces:

```go
type Tool interface { Def() model.ToolDef; Call(ctx context.Context, args json.RawMessage) (string, error) }
type Event struct { Kind string; Tool string; Args json.RawMessage; Result string; Err error } // Kind: "tool_start" | "tool_done" | "budget"
type Agent struct { Model model.Model; Tools []Tool; System func() string; StepBudget int; MaxTokens int; Events func(Event) }
func (a *Agent) Turn(ctx context.Context, user string) (string, error)
func (a *Agent) History() []model.Message
func (a *Agent) SetHistory(h []model.Message)
```

Rules (from the spec): a tool error is a tool result with `IsError` and the message; an unknown tool name is the same; the budget counts tool calls per turn and when reached the loop stops after answering every pending call, returning the last assistant text plus `"\n\n(step budget of N tool calls reached — say \"continue\" to keep going)"`; a model error or cancellation leaves the history as it was before that call (the user message is kept).

- [ ] **Step 1: Failing tests** — `agent_test.go`:

```go
package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model"
	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model/fake"
)

type echoTool struct{ fail bool }

func (echoTool) Def() model.ToolDef {
	return model.ToolDef{Name: "echo", Description: "echoes", Schema: []byte(`{"type":"object"}`)}
}

func (e echoTool) Call(_ context.Context, args json.RawMessage) (string, error) {
	if e.fail {
		return "", errors.New("echo broke")
	}
	return "echo:" + string(args), nil
}

func newAgent(m model.Model, tools ...Tool) (*Agent, *[]Event) {
	var events []Event
	a := &Agent{Model: m, Tools: tools, System: func() string { return "SYS" }, StepBudget: 3, MaxTokens: 100,
		Events: func(e Event) { events = append(events, e) }}
	return a, &events
}

func TestTurnTextOnly(t *testing.T) {
	m := fake.New(fake.Text("hello back"))
	a, _ := newAgent(m)
	out, err := a.Turn(context.Background(), "hello")
	if err != nil || out != "hello back" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	req := m.Calls[0]
	if req.System != "SYS" || req.MaxTokens != 100 || len(req.Messages) != 1 || req.Messages[0].Content != "hello" {
		t.Errorf("request = %+v", req)
	}
	h := a.History()
	if len(h) != 2 || h[1].Role != model.RoleAssistant || h[1].Content != "hello back" {
		t.Errorf("history = %+v", h)
	}
}

func TestTurnToolRoundTrip(t *testing.T) {
	m := fake.New(fake.Call("c1", "echo", `{"x":1}`), fake.Text("done"))
	a, events := newAgent(m, echoTool{})
	out, err := a.Turn(context.Background(), "go")
	if err != nil || out != "done" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	h := a.History()
	if len(h) != 4 || h[2].Role != model.RoleTool || h[2].ToolCallID != "c1" || h[2].Content != `echo:{"x":1}` || h[2].IsError {
		t.Fatalf("history = %+v", h)
	}
	if len(m.Calls[1].Tools) != 1 || m.Calls[1].Tools[0].Name != "echo" {
		t.Errorf("tools not sent: %+v", m.Calls[1].Tools)
	}
	if len(*events) != 2 || (*events)[0].Kind != "tool_start" || (*events)[1].Kind != "tool_done" || (*events)[1].Result != `echo:{"x":1}` {
		t.Errorf("events = %+v", *events)
	}
}

func TestTurnToolErrorIsFedBack(t *testing.T) {
	m := fake.New(fake.Call("c1", "echo", `{}`), fake.Call("c2", "nope", `{}`), fake.Text("ok"))
	a, _ := newAgent(m, echoTool{fail: true})
	if _, err := a.Turn(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	h := a.History()
	if !h[2].IsError || h[2].Content != "echo broke" {
		t.Errorf("tool error result = %+v", h[2])
	}
	if !h[4].IsError || !strings.Contains(h[4].Content, `unknown tool "nope"`) {
		t.Errorf("unknown tool result = %+v", h[4])
	}
}

func TestTurnStepBudget(t *testing.T) {
	m := fake.New(fake.Call("c1", "echo", `{}`), fake.Call("c2", "echo", `{}`), fake.Call("c3", "echo", `{}`), fake.Call("c4", "echo", `{}`), fake.Text("never"))
	a, events := newAgent(m, echoTool{})
	out, err := a.Turn(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "step budget of 3") {
		t.Errorf("out = %q", out)
	}
	if len(m.Calls) != 3 {
		t.Errorf("model called %d times, want 3", len(m.Calls))
	}
	h := a.History()
	if last := h[len(h)-1]; last.Role != model.RoleTool {
		t.Errorf("history must end with the answered call, got %+v", last)
	}
	if k := (*events)[len(*events)-1].Kind; k != "budget" {
		t.Errorf("last event = %s", k)
	}
	// The next turn still works: every call was answered.
	m.Responses = append(m.Responses, fake.Text("continued"))
	if out, err := a.Turn(context.Background(), "continue"); err != nil || out != "continued" {
		t.Errorf("next turn: %q %v", out, err)
	}
}

func TestTurnModelErrorLeavesHistory(t *testing.T) {
	m := fake.New()
	m.Fail = errors.New("boom")
	a, _ := newAgent(m)
	if _, err := a.Turn(context.Background(), "hi"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if h := a.History(); len(h) != 1 || h[0].Content != "hi" {
		t.Errorf("history = %+v", h)
	}
}

func TestTurnEmptyReply(t *testing.T) {
	m := fake.New(model.Response{Message: model.Message{Role: model.RoleAssistant}, StopReason: model.StopEndTurn})
	a, _ := newAgent(m)
	out, err := a.Turn(context.Background(), "hi")
	if err != nil || out != "(no reply)" {
		t.Errorf("out = %q, err = %v", out, err)
	}
}
```

- [ ] **Step 2: Run** → FAIL.

- [ ] **Step 3: `agent.go`**

```go
package supervisor

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model"
)

// Tool is something the model may call.
type Tool interface {
	Def() model.ToolDef
	Call(ctx context.Context, args json.RawMessage) (string, error)
}

// Event is what the loop reports as it goes, for the REPL to print.
type Event struct {
	Kind   string // "tool_start" | "tool_done" | "budget"
	Tool   string
	Args   json.RawMessage
	Result string
	Err    error
}

// Agent is the loop: model call, tool calls, repeat until the model
// answers in text or the step budget runs out.
type Agent struct {
	Model      model.Model
	Tools      []Tool
	System     func() string // rebuilt every turn so config and check state are fresh
	StepBudget int           // tool calls per user turn
	MaxTokens  int
	Events     func(Event)

	history []model.Message
}

// History returns the conversation so far, without the system prompt.
func (a *Agent) History() []model.Message {
	return append([]model.Message(nil), a.history...)
}

// SetHistory replaces the conversation (resume).
func (a *Agent) SetHistory(h []model.Message) {
	a.history = append([]model.Message(nil), h...)
}

func (a *Agent) emit(e Event) {
	if a.Events != nil {
		a.Events(e)
	}
}

func (a *Agent) defs() []model.ToolDef {
	defs := make([]model.ToolDef, 0, len(a.Tools))
	for _, t := range a.Tools {
		defs = append(defs, t.Def())
	}
	return defs
}

func (a *Agent) tool(name string) Tool {
	for _, t := range a.Tools {
		if t.Def().Name == name {
			return t
		}
	}
	return nil
}

// Turn sends one user message and runs tool calls until the model replies
// in text. On a model error the history is exactly as it was before the
// failing call, so the caller can retry or carry on.
func (a *Agent) Turn(ctx context.Context, user string) (string, error) {
	a.history = append(a.history, model.Message{Role: model.RoleUser, Content: user})
	steps := 0
	lastText := ""
	for {
		resp, err := a.Model.Chat(ctx, model.Request{
			System: a.System(), Messages: a.history, Tools: a.defs(), MaxTokens: a.MaxTokens,
		})
		if err != nil {
			return "", err
		}
		msg := resp.Message
		msg.Role = model.RoleAssistant
		a.history = append(a.history, msg)
		if msg.Content != "" {
			lastText = msg.Content
		}
		if len(msg.ToolCalls) == 0 {
			if lastText == "" {
				return "(no reply)", nil
			}
			return lastText, nil
		}
		for _, tc := range msg.ToolCalls {
			steps++
			a.history = append(a.history, a.call(ctx, tc))
		}
		if steps >= a.StepBudget {
			a.emit(Event{Kind: "budget"})
			return fmt.Sprintf("%s\n\n(step budget of %d tool calls reached — say \"continue\" to keep going)", lastText, a.StepBudget), nil
		}
	}
}

func (a *Agent) call(ctx context.Context, tc model.ToolCall) model.Message {
	a.emit(Event{Kind: "tool_start", Tool: tc.Name, Args: tc.Args})
	res := model.Message{Role: model.RoleTool, ToolCallID: tc.ID}
	t := a.tool(tc.Name)
	var out string
	var err error
	if t == nil {
		err = fmt.Errorf("unknown tool %q", tc.Name)
	} else {
		out, err = t.Call(ctx, tc.Args)
	}
	if err != nil {
		res.IsError = true
		res.Content = err.Error()
	} else {
		res.Content = out
	}
	a.emit(Event{Kind: "tool_done", Tool: tc.Name, Args: tc.Args, Result: res.Content, Err: err})
	return res
}
```

Note the budget check happens after answering the calls, so `TestTurnStepBudget` sees three model calls (call, call, call → budget hit at 3) and a consistent history.

- [ ] **Step 4: Run** tests + lint → PASS.

- [ ] **Step 5: Commit** `feat(supervisor): agent loop with tool calls and a per-turn step budget`.

---

### Task 6: Diff and the file tools

**Files:**
- Create: `internal/supervisor/diff.go`, `diff_test.go`, `internal/supervisor/tools.go`, `tools_test.go`

**Interfaces:**
- Produces:

```go
func Diff(name, a, b string) string  // unified-ish, line based: "--- name\n+++ name\n" then " / - / + lines
func NewReadConfig(path string) Tool
func NewWriteConfig(path string, confirm func(prompt string) bool) Tool
func NewReadDoc() Tool
func NewReadRepoFile(workerConfigPath string) Tool
func NewRemember(mem *Memory) Tool   // Memory from Task 7 — implement the tool in Task 7's step, declare here
```

`write_config` rules: `content` that does not parse as YAML → error, nothing written; a config that parses but fails `config.Load` validation is still offered — the diff **and** the validation problems are shown in the confirm prompt and returned to the model (the starter config fails validation too; being able to save an intermediate state is the point). Validation is done by writing `content` to `path + ".tmp"`, calling `config.Load` on it, and renaming it into place on yes (after copying the old file to `path + ".bak"`); on no or on parse error the temp file is removed.

- [ ] **Step 1: Failing diff test** — `diff_test.go`:

```go
package supervisor

import "testing"

func TestDiff(t *testing.T) {
	got := Diff("c.yaml", "a\nb\nc\n", "a\nB\nc\nd\n")
	want := "--- c.yaml\n+++ c.yaml\n a\n-b\n+B\n c\n+d\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := Diff("x", "", "one\n"); got != "--- x (missing)\n+++ x\n+one\n" {
		t.Errorf("new file diff = %q", got)
	}
}
```

- [ ] **Step 2: `diff.go`**

```go
package supervisor

import "strings"

// Diff is a line-based diff for showing an operator what write_config is
// about to change. It is for humans; its exact shape is not a contract.
func Diff(name, a, b string) string {
	al, bl := splitLines(a), splitLines(b)
	// Longest common subsequence table.
	n, m := len(al), len(bl)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if al[i] == bl[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var sb strings.Builder
	if a == "" {
		sb.WriteString("--- " + name + " (missing)\n")
	} else {
		sb.WriteString("--- " + name + "\n")
	}
	sb.WriteString("+++ " + name + "\n")
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case al[i] == bl[j]:
			sb.WriteString(" " + al[i] + "\n")
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			sb.WriteString("-" + al[i] + "\n")
			i++
		default:
			sb.WriteString("+" + bl[j] + "\n")
			j++
		}
	}
	for ; i < n; i++ {
		sb.WriteString("-" + al[i] + "\n")
	}
	for ; j < m; j++ {
		sb.WriteString("+" + bl[j] + "\n")
	}
	return sb.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
```

- [ ] **Step 3: Failing tool tests** — `tools_test.go`:

```go
package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func call(t *testing.T, tool Tool, args string) (string, error) {
	t.Helper()
	return tool.Call(context.Background(), json.RawMessage(args))
}

const validWorkerConfig = `agent_id: w
tracker: github
repos:
  - {name: o/r, url: git@github.com:o/r.git, project: X}
`

func TestReadConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	out, err := call(t, NewReadConfig(p), `{}`)
	if err != nil || !strings.Contains(out, "no config at "+p) {
		t.Fatalf("missing: %q %v", out, err)
	}
	if err := os.WriteFile(p, []byte("agent_id: w\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, _ := call(t, NewReadConfig(p), `{}`); out != "agent_id: w\n" {
		t.Errorf("out = %q", out)
	}
}

func TestWriteConfigRejectsUnparseable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	asked := false
	tool := NewWriteConfig(p, func(string) bool { asked = true; return true })
	_, err := call(t, tool, `{"content":"agent_id: [oops\n"}`)
	if err == nil || asked {
		t.Fatalf("err = %v, asked = %v", err, asked)
	}
	if _, statErr := os.Stat(p); statErr == nil {
		t.Error("file was written")
	}
	if _, statErr := os.Stat(p + ".tmp"); statErr == nil {
		t.Error("temp file left behind")
	}
}

func TestWriteConfigAsksShowsDiffAndBacksUp(t *testing.T) {
	t.Setenv("HIVE_GITHUB_TOKEN", "gh")
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("agent_id: old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var prompt string
	tool := NewWriteConfig(p, func(s string) bool { prompt = s; return true })
	body, _ := json.Marshal(map[string]string{"content": validWorkerConfig})
	out, err := call(t, tool, string(body))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "-agent_id: old") || !strings.Contains(prompt, "+agent_id: w") {
		t.Errorf("prompt = %q", prompt)
	}
	if !strings.Contains(out, "wrote "+p) {
		t.Errorf("out = %q", out)
	}
	got, _ := os.ReadFile(p)
	if string(got) != validWorkerConfig {
		t.Errorf("file = %q", got)
	}
	bak, _ := os.ReadFile(p + ".bak")
	if string(bak) != "agent_id: old\n" {
		t.Errorf("bak = %q", bak)
	}
}

func TestWriteConfigDeclined(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	tool := NewWriteConfig(p, func(string) bool { return false })
	out, err := call(t, tool, `{"content":"agent_id: w\n"}`)
	if err != nil || !strings.Contains(out, "declined by user") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if _, statErr := os.Stat(p); statErr == nil {
		t.Error("file was written")
	}
}

func TestWriteConfigReportsValidationProblems(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	var prompt string
	tool := NewWriteConfig(p, func(s string) bool { prompt = s; return true })
	out, err := call(t, tool, `{"content":"agent_id: w\n"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "repos must list") || !strings.Contains(prompt, "repos must list") {
		t.Errorf("problems not reported: out=%q prompt=%q", out, prompt)
	}
}

func TestReadDoc(t *testing.T) {
	out, err := call(t, NewReadDoc(), `{"name":"setup"}`)
	if err != nil || !strings.HasPrefix(out, "# Setup") {
		t.Fatalf("out = %.40q, err = %v", out, err)
	}
	if _, err := call(t, NewReadDoc(), `{"name":"../go.mod"}`); err == nil || !strings.Contains(err.Error(), "setup, config, design, decisions") {
		t.Errorf("err = %v", err)
	}
}

func TestReadRepoFile(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	root := filepath.Join(dir, "work")
	if err := os.WriteFile(cfg, []byte("workroot: "+root+"\nrepos:\n  - {name: o/r, url: u, project: X}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repos", "o__r", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".hivedispatch.yaml"), []byte("executor: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := NewReadRepoFile(cfg)
	if out, err := call(t, tool, `{"repo":"o/r","name":".hivedispatch.yaml"}`); err != nil || out != "executor: {}\n" {
		t.Errorf("out = %q, err = %v", out, err)
	}
	if _, err := call(t, tool, `{"repo":"o/r","name":"AGENTS.md"}`); err == nil || !strings.Contains(err.Error(), "no AGENTS.md") {
		t.Errorf("missing file: %v", err)
	}
	if _, err := call(t, tool, `{"repo":"o/r","name":"../secret"}`); err == nil || !strings.Contains(err.Error(), ".hivedispatch.yaml or AGENTS.md") {
		t.Errorf("bad name: %v", err)
	}
	if _, err := call(t, tool, `{"repo":"x/y","name":"AGENTS.md"}`); err == nil || !strings.Contains(err.Error(), "not in repos") {
		t.Errorf("unknown repo: %v", err)
	}
	if _, err := call(t, NewReadRepoFile(filepath.Join(dir, "nope.yaml")), `{"repo":"o/r","name":"AGENTS.md"}`); err == nil || !strings.Contains(err.Error(), "no config") {
		t.Errorf("no config: %v", err)
	}
}
```

- [ ] **Step 4: `tools.go`**

```go
package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/thomasmeadows/hivedispatch/docs"
	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model"
)

const noArgs = `{"type":"object","properties":{}}`

func decode(args json.RawMessage, into any) error {
	if len(args) == 0 {
		return nil
	}
	if err := json.Unmarshal(args, into); err != nil {
		return fmt.Errorf("bad arguments: %w", err)
	}
	return nil
}

// --- read_config ---

type readConfig struct{ path string }

// NewReadConfig returns the tool that reads the worker config.
func NewReadConfig(path string) Tool { return readConfig{path: path} }

func (r readConfig) Def() model.ToolDef {
	return model.ToolDef{Name: "read_config", Description: "Read the worker config file (" + r.path + "). Returns its YAML, or says it is missing.", Schema: []byte(noArgs)}
}

func (r readConfig) Call(context.Context, json.RawMessage) (string, error) {
	raw, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return "no config at " + r.path + " (write_config creates it)", nil
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// --- write_config ---

type writeConfig struct {
	path    string
	confirm func(string) bool
}

// NewWriteConfig returns the tool that replaces the worker config after
// the operator confirms the diff.
func NewWriteConfig(path string, confirm func(string) bool) Tool {
	return writeConfig{path: path, confirm: confirm}
}

func (w writeConfig) Def() model.ToolDef {
	return model.ToolDef{
		Name:        "write_config",
		Description: "Replace the whole worker config with new YAML. The operator sees a diff and must approve. Unparseable YAML is refused; a config that parses but is incomplete is written and the remaining problems are returned so you can tell the operator.",
		Schema:      []byte(`{"type":"object","properties":{"content":{"type":"string","description":"the complete new config.yaml"}},"required":["content"]}`),
	}
}

func (w writeConfig) Call(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Content string `json:"content"`
	}
	if err := decode(args, &in); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Content) == "" {
		return "", errors.New("content is empty")
	}
	var probe map[string]any
	if err := yaml.Unmarshal([]byte(in.Content), &probe); err != nil {
		return "", fmt.Errorf("not valid YAML, nothing written: %w", err)
	}
	old, err := os.ReadFile(w.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(w.path), 0o755); err != nil {
		return "", err
	}
	tmp := w.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(in.Content), 0o600); err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp) }()
	problems := ""
	if _, err := config.Load(tmp); err != nil {
		problems = strings.ReplaceAll(err.Error(), tmp, w.path)
	}
	prompt := Diff(filepath.Base(w.path), string(old), in.Content)
	if problems != "" {
		prompt += "\nThe config still has problems:\n" + problems + "\n"
	}
	prompt += "\nApply to " + w.path + "?"
	if !w.confirm(prompt) {
		return "declined by user; the config is unchanged", nil
	}
	if old != nil {
		if err := os.WriteFile(w.path+".bak", old, 0o600); err != nil {
			return "", err
		}
	}
	if err := os.Rename(tmp, w.path); err != nil {
		return "", err
	}
	out := "wrote " + w.path
	if problems != "" {
		out += "\n\nIt is not complete yet:\n" + problems
	}
	return out, nil
}

// --- read_doc ---

var docFiles = map[string]string{
	"setup": "setup.md", "config": "config.md", "design": "design-spec.md", "decisions": "decisions.md",
}

const docNames = "setup, config, design, decisions"

type readDoc struct{}

// NewReadDoc returns the tool that reads the embedded operator docs.
func NewReadDoc() Tool { return readDoc{} }

func (readDoc) Def() model.ToolDef {
	return model.ToolDef{
		Name:        "read_doc",
		Description: "Read one of HiveDispatch's docs: setup (step-by-step setup for Jira and GitHub Issues), config (every config key), design (architecture), decisions (design decisions and why).",
		Schema:      []byte(`{"type":"object","properties":{"name":{"type":"string","enum":["setup","config","design","decisions"]}},"required":["name"]}`),
	}
}

func (readDoc) Call(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(args, &in); err != nil {
		return "", err
	}
	file, ok := docFiles[in.Name]
	if !ok {
		return "", fmt.Errorf("unknown doc %q: want one of %s", in.Name, docNames)
	}
	raw, err := docs.FS.ReadFile(file)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// --- read_repo_file ---

type readRepoFile struct{ workerConfigPath string }

// NewReadRepoFile returns the tool that reads a governed repo's policy
// file or AGENTS.md from its base checkout under workroot.
func NewReadRepoFile(workerConfigPath string) Tool {
	return readRepoFile{workerConfigPath: workerConfigPath}
}

func (readRepoFile) Def() model.ToolDef {
	return model.ToolDef{
		Name:        "read_repo_file",
		Description: "Read .hivedispatch.yaml (the repo policy the executor obeys) or AGENTS.md from a configured repository's checkout. The checkout exists only after a run has cloned it.",
		Schema:      []byte(`{"type":"object","properties":{"repo":{"type":"string","description":"owner/repo as in repos[].name"},"name":{"type":"string","enum":[".hivedispatch.yaml","AGENTS.md"]}},"required":["repo","name"]}`),
	}
}

// workerRepos reads only workroot and repos[].name from the worker config,
// without validating the rest, so this works on a half-written config.
func workerRepos(path string) (workroot string, names []string, err error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil, errors.New("no config at " + path)
	}
	if err != nil {
		return "", nil, err
	}
	var c struct {
		Workroot string `yaml:"workroot"`
		Repos    []struct {
			Name string `yaml:"name"`
		} `yaml:"repos"`
	}
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return "", nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, r := range c.Repos {
		names = append(names, r.Name)
	}
	workroot = c.Workroot
	if home := os.Getenv("HOME"); strings.HasPrefix(workroot, "~/") && home != "" {
		workroot = filepath.Join(home, workroot[2:])
	}
	if workroot == "" {
		workroot = filepath.Join(os.Getenv("HOME"), ".local", "share", "hivedispatch")
	}
	return workroot, names, nil
}

func (r readRepoFile) Call(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Repo string `json:"repo"`
		Name string `json:"name"`
	}
	if err := decode(args, &in); err != nil {
		return "", err
	}
	if in.Name != ".hivedispatch.yaml" && in.Name != "AGENTS.md" {
		return "", fmt.Errorf("name must be .hivedispatch.yaml or AGENTS.md, got %q", in.Name)
	}
	workroot, names, err := workerRepos(r.workerConfigPath)
	if err != nil {
		return "", err
	}
	known := false
	for _, n := range names {
		if strings.EqualFold(n, in.Repo) {
			known = true
		}
	}
	if !known {
		return "", fmt.Errorf("%q is not in repos[] of %s (configured: %s)", in.Repo, r.workerConfigPath, strings.Join(names, ", "))
	}
	base := filepath.Join(workroot, "repos", strings.ReplaceAll(in.Repo, "/", "__"), "repo")
	raw, err := os.ReadFile(filepath.Join(base, in.Name))
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("no %s in %s (the checkout appears after the first run clones the repo)", in.Name, base)
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
```

- [ ] **Step 5: Run** tests + lint → PASS (the `remember` tool comes with Task 7).

- [ ] **Step 6: Commit** `feat(supervisor): config, doc and repo-file tools with a confirmed diff on write`.

---

### Task 7: Memory — notes and sessions — plus the `remember` tool

**Files:**
- Create: `internal/supervisor/memory.go`, `memory_test.go`; append to `tools.go`, `tools_test.go`

**Interfaces:**
- Produces:

```go
type Memory struct { Dir string } // <cfgdir>/supervisor
func NewMemory(dir string) *Memory
func (m *Memory) NotesPath() string
func (m *Memory) Notes() (string, error)              // whole file, "" when missing
func (m *Memory) NotesForPrompt() (string, int)       // capped at 16 KiB keeping the newest lines; returns text and total line count
func (m *Memory) Append(now time.Time, note string) error // "- 2026-09-20: note\n"
func (m *Memory) NewSessionName(now time.Time) string // "20260920T143000Z.json"
func (m *Memory) SaveSession(name string, h []model.Message) error // atomic: tmp + rename
func (m *Memory) LoadSession(name string) ([]model.Message, error)
func (m *Memory) NewestSession() (string, error)     // "" when none
func NewRemember(mem *Memory, now func() time.Time) Tool
```

- [ ] **Step 1: Failing tests** — `memory_test.go`:

```go
package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model"
)

var at = time.Date(2026, 9, 20, 14, 30, 0, 0, time.UTC)

func TestNotesAppendAndRead(t *testing.T) {
	m := NewMemory(filepath.Join(t.TempDir(), "supervisor"))
	if n, err := m.Notes(); err != nil || n != "" {
		t.Fatalf("empty: %q %v", n, err)
	}
	if err := m.Append(at, "tracker is github"); err != nil {
		t.Fatal(err)
	}
	if err := m.Append(at, "token comes from gh"); err != nil {
		t.Fatal(err)
	}
	n, _ := m.Notes()
	if n != "- 2026-09-20: tracker is github\n- 2026-09-20: token comes from gh\n" {
		t.Errorf("notes = %q", n)
	}
	text, lines := m.NotesForPrompt()
	if lines != 2 || text != n {
		t.Errorf("prompt notes = %q (%d)", text, lines)
	}
}

func TestNotesForPromptCapKeepsNewest(t *testing.T) {
	m := NewMemory(filepath.Join(t.TempDir(), "supervisor"))
	for i := 0; i < 2000; i++ {
		if err := m.Append(at, strings.Repeat("x", 20)+" "+string(rune('a'+i%26))); err != nil {
			t.Fatal(err)
		}
	}
	text, lines := m.NotesForPrompt()
	if lines != 2000 || len(text) > 16<<10 || !strings.HasSuffix(text, string(rune('a'+1999%26))+"\n") {
		t.Errorf("len=%d lines=%d tail=%q", len(text), lines, text[len(text)-30:])
	}
	if !strings.HasPrefix(text, "- ") {
		t.Errorf("cap must cut on a line boundary: %q", text[:20])
	}
	raw, _ := os.ReadFile(m.NotesPath())
	if len(strings.Split(strings.TrimSpace(string(raw)), "\n")) != 2000 {
		t.Error("file was truncated")
	}
}

func TestSessions(t *testing.T) {
	m := NewMemory(filepath.Join(t.TempDir(), "supervisor"))
	if name, err := m.NewestSession(); err != nil || name != "" {
		t.Fatalf("no sessions: %q %v", name, err)
	}
	h := []model.Message{{Role: model.RoleUser, Content: "hi"}, {Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "c1", Name: "read_doc", Args: []byte(`{"name":"setup"}`)}}}, {Role: model.RoleTool, ToolCallID: "c1", Content: "# Setup", IsError: true}}
	first := m.NewSessionName(at)
	if first != "20260920T143000Z.json" {
		t.Errorf("name = %s", first)
	}
	if err := m.SaveSession(first, h); err != nil {
		t.Fatal(err)
	}
	second := m.NewSessionName(at.Add(time.Minute))
	if err := m.SaveSession(second, h[:1]); err != nil {
		t.Fatal(err)
	}
	if name, _ := m.NewestSession(); name != second {
		t.Errorf("newest = %s", name)
	}
	got, err := m.LoadSession(first)
	if err != nil || len(got) != 3 || got[1].ToolCalls[0].Name != "read_doc" || string(got[1].ToolCalls[0].Args) != `{"name":"setup"}` || !got[2].IsError {
		t.Errorf("loaded = %+v, err = %v", got, err)
	}
	if _, err := m.LoadSession("nope.json"); err == nil {
		t.Error("missing session loaded")
	}
}

func TestRememberTool(t *testing.T) {
	m := NewMemory(filepath.Join(t.TempDir(), "supervisor"))
	out, err := call(t, NewRemember(m, func() time.Time { return at }), `{"note":"jira site is x.atlassian.net"}`)
	if err != nil || !strings.Contains(out, "remembered") {
		t.Fatalf("out = %q err = %v", out, err)
	}
	if n, _ := m.Notes(); n != "- 2026-09-20: jira site is x.atlassian.net\n" {
		t.Errorf("notes = %q", n)
	}
	if _, err := call(t, NewRemember(m, func() time.Time { return at }), `{"note":"  "}`); err == nil {
		t.Error("empty note accepted")
	}
}
```

- [ ] **Step 2: Run** → FAIL.

- [ ] **Step 3: `memory.go`**

```go
package supervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model"
)

const notesPromptCap = 16 << 10

// Memory is what the supervisor keeps between sessions: a notes file it
// appends to, and one transcript per session.
type Memory struct {
	Dir string
}

// NewMemory returns a Memory rooted at dir (created on first write).
func NewMemory(dir string) *Memory { return &Memory{Dir: dir} }

// NotesPath is the notes file.
func (m *Memory) NotesPath() string { return filepath.Join(m.Dir, "memory.md") }

func (m *Memory) sessionsDir() string { return filepath.Join(m.Dir, "sessions") }

// Notes returns the whole notes file, or "" when there is none.
func (m *Memory) Notes() (string, error) {
	raw, err := os.ReadFile(m.NotesPath())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(raw), err
}

// NotesForPrompt returns the notes capped for the system prompt, keeping
// the newest lines, and the total number of lines in the file.
func (m *Memory) NotesForPrompt() (string, int) {
	n, err := m.Notes()
	if err != nil || n == "" {
		return "", 0
	}
	lines := strings.Split(strings.TrimSuffix(n, "\n"), "\n")
	total := len(lines)
	for len(n) > notesPromptCap && len(lines) > 1 {
		lines = lines[1:]
		n = strings.Join(lines, "\n") + "\n"
	}
	return n, total
}

// Append adds one dated note.
func (m *Memory) Append(now time.Time, note string) error {
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(m.NotesPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	line := fmt.Sprintf("- %s: %s\n", now.UTC().Format("2006-01-02"), strings.TrimSpace(note))
	if _, err := f.WriteString(line); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// NewSessionName names a session file by its start time.
func (m *Memory) NewSessionName(now time.Time) string {
	return now.UTC().Format("20060102T150405Z") + ".json"
}

// SaveSession writes the transcript atomically.
func (m *Memory) SaveSession(name string, h []model.Message) error {
	if err := os.MkdirAll(m.sessionsDir(), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(m.sessionsDir(), name)
	if err := os.WriteFile(p+".tmp", raw, 0o600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

// LoadSession reads a transcript by name, or by path when name contains a
// separator.
func (m *Memory) LoadSession(name string) ([]model.Message, error) {
	p := name
	if !strings.ContainsRune(name, filepath.Separator) {
		p = filepath.Join(m.sessionsDir(), name)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("load session: %w", err)
	}
	var h []model.Message
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, fmt.Errorf("parse session %s: %w", p, err)
	}
	return h, nil
}

// NewestSession returns the most recent session file name, or "".
func (m *Memory) NewestSession() (string, error) {
	entries, err := os.ReadDir(m.sessionsDir())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", nil
	}
	sort.Strings(names)
	return names[len(names)-1], nil
}
```

- [ ] **Step 4: `remember` in `tools.go`**

```go
// --- remember ---

type remember struct {
	mem *Memory
	now func() time.Time
}

// NewRemember returns the tool that appends to the notes file.
func NewRemember(mem *Memory, now func() time.Time) Tool { return remember{mem: mem, now: now} }

func (remember) Def() model.ToolDef {
	return model.ToolDef{
		Name:        "remember",
		Description: "Append a short note to your persistent memory, shown to you at the start of every session. Use it for facts about this operator's setup that will matter next time (which tracker, where the token comes from, what was fixed).",
		Schema:      []byte(`{"type":"object","properties":{"note":{"type":"string"}},"required":["note"]}`),
	}
}

func (r remember) Call(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Note string `json:"note"`
	}
	if err := decode(args, &in); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Note) == "" {
		return "", errors.New("note is empty")
	}
	if err := r.mem.Append(r.now(), in.Note); err != nil {
		return "", err
	}
	return "remembered in " + r.mem.NotesPath(), nil
}
```

- [ ] **Step 5: Run** tests + lint → PASS.

- [ ] **Step 6: Commit** `feat(supervisor): notes file and session transcripts; remember tool`.

---

### Task 8: `run_hivedispatch` tool

**Files:**
- Create: `internal/supervisor/tools_run.go`, `tools_run_test.go`

**Interfaces:**
- Produces:

```go
func NewRunHivedispatch(exe, workerConfigPath string, confirm func(string) bool) Tool
func Allowed(args []string) (canonical string, mutating bool, ok bool) // exported for the prompt
var Allowlist = []string{ "version", "check", "check -live", "init", "init -jira", "init -github", "status", "status -json", "run -once -executor fake", "run -once -executor fake -placeholder" }
```

Rules: `args` is a string array; the first element is the subcommand, the rest flags, matched as a set (order-free) against the allowlist; `-config PATH` is inserted right after the subcommand on exec; mutating shapes (`init*`, `run …`) ask `Run \`hivedispatch <canonical>\`? It <effect>.` before running; timeout 5 minutes; stdout and stderr captured separately, each keeping the last 32 KiB; result text is `exit <code>\n--- stdout ---\n…\n--- stderr ---\n…`. `HIVE_*` and the rest of the environment are inherited. A non-zero exit is **not** a tool error (the model must read the output); only a refused argv, a declined confirmation, or a failure to start is.

- [ ] **Step 1: Failing tests** — `tools_run_test.go`:

```go
package supervisor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAllowed(t *testing.T) {
	cases := []struct {
		args     []string
		want     string
		mutating bool
		ok       bool
	}{
		{[]string{"version"}, "version", false, true},
		{[]string{"check", "-live"}, "check -live", false, true},
		{[]string{"init", "-github"}, "init -github", true, true},
		{[]string{"run", "-executor", "fake", "-once"}, "run -once -executor fake", true, true},
		{[]string{"run", "-once", "-placeholder", "-executor", "fake"}, "run -once -executor fake -placeholder", true, true},
		{[]string{"run"}, "", false, false},
		{[]string{"run", "-once"}, "", false, false},
		{[]string{"run", "-once", "-executor", "claude"}, "", false, false},
		{[]string{"once", "X-1"}, "", false, false},
		{[]string{"check", "-config", "/etc/passwd"}, "", false, false},
		{nil, "", false, false},
	}
	for _, tc := range cases {
		got, mut, ok := Allowed(tc.args)
		if got != tc.want || mut != tc.mutating || ok != tc.ok {
			t.Errorf("Allowed(%v) = %q %v %v, want %q %v %v", tc.args, got, mut, ok, tc.want, tc.mutating, tc.ok)
		}
	}
}

// fakeExe writes a script that prints its argv and exits with $HIVE_TEST_EXIT.
func fakeExe(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	p := filepath.Join(t.TempDir(), "hivedispatch")
	script := "#!/bin/sh\necho \"args: $*\"\necho \"err line\" >&2\nexit ${HIVE_TEST_EXIT:-0}\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunHivedispatchInsertsConfigAndCaptures(t *testing.T) {
	t.Setenv("HIVE_TEST_EXIT", "3")
	tool := NewRunHivedispatch(fakeExe(t), "/c/config.yaml", func(string) bool { t.Error("check must not ask"); return false })
	out, err := call(t, tool, `{"args":["check","-live"]}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"exit 3", "args: check -config /c/config.yaml -live", "err line"} {
		if !strings.Contains(out, want) {
			t.Errorf("out %q lacks %q", out, want)
		}
	}
}

func TestRunHivedispatchRefusesAndAsks(t *testing.T) {
	tool := NewRunHivedispatch(fakeExe(t), "/c/config.yaml", func(string) bool { return false })
	if _, err := call(t, tool, `{"args":["once","X-1"]}`); err == nil || !strings.Contains(err.Error(), "run -once -executor fake") {
		t.Errorf("refusal must list the allowlist: %v", err)
	}
	out, err := call(t, tool, `{"args":["init","-github"]}`)
	if err != nil || !strings.Contains(out, "declined by user") {
		t.Errorf("declined: %q %v", out, err)
	}
	var prompt string
	tool = NewRunHivedispatch(fakeExe(t), "/c/config.yaml", func(s string) bool { prompt = s; return true })
	if out, err := call(t, tool, `{"args":["run","-once","-executor","fake"]}`); err != nil || !strings.Contains(out, "args: run -config /c/config.yaml -once -executor fake") {
		t.Errorf("approved: %q %v", out, err)
	}
	if !strings.Contains(prompt, "hivedispatch run -once -executor fake") {
		t.Errorf("prompt = %q", prompt)
	}
}
```

- [ ] **Step 2: Run** → FAIL.

- [ ] **Step 3: `tools_run.go`**

```go
package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model"
)

const (
	runTimeout  = 5 * time.Minute
	runMaxBytes = 32 << 10
)

// Allowlist is every argv shape run_hivedispatch may execute, in canonical
// flag order.
var Allowlist = []string{
	"version",
	"check", "check -live",
	"init", "init -jira", "init -github",
	"status", "status -json",
	"run -once -executor fake", "run -once -executor fake -placeholder",
}

// effects explains what a mutating shape does, for the confirmation.
var effects = map[string]string{
	"init":                                 "writes a starter worker config (never overwrites one)",
	"init -jira":                           "creates the two claim custom fields in Jira",
	"init -github":                         "creates the hive:* labels in every configured repository",
	"run -once -executor fake":             "polls the tracker once and, for a ready ticket, claims it, branches and reports — with no coding agent",
	"run -once -executor fake -placeholder": "polls the tracker once and, for a ready ticket, claims it, writes a placeholder file, pushes a branch and opens a pull request",
}

// Allowed matches args against the allowlist regardless of flag order.
func Allowed(args []string) (canonical string, mutating bool, ok bool) {
	if len(args) == 0 {
		return "", false, false
	}
	key := args[0] + " " + sortedFlags(args[1:])
	for _, a := range Allowlist {
		parts := strings.Fields(a)
		if parts[0]+" "+sortedFlags(parts[1:]) == key {
			_, mutating = effects[a]
			return a, mutating, true
		}
	}
	return "", false, false
}

func sortedFlags(flags []string) string {
	// Pair each flag with its value so "-executor fake" stays together.
	var items []string
	for i := 0; i < len(flags); i++ {
		f := flags[i]
		if i+1 < len(flags) && !strings.HasPrefix(flags[i+1], "-") {
			f += " " + flags[i+1]
			i++
		}
		items = append(items, f)
	}
	sort.Strings(items)
	return strings.Join(items, " ")
}

type runHivedispatch struct {
	exe, configPath string
	confirm         func(string) bool
}

// NewRunHivedispatch returns the tool that re-executes this binary with an
// allowlisted argv.
func NewRunHivedispatch(exe, workerConfigPath string, confirm func(string) bool) Tool {
	return runHivedispatch{exe: exe, configPath: workerConfigPath, confirm: confirm}
}

func (runHivedispatch) Def() model.ToolDef {
	return model.ToolDef{
		Name: "run_hivedispatch",
		Description: "Run a hivedispatch subcommand and get its exit code and output. Allowed: " + strings.Join(Allowlist, "; ") +
			". -config is added for you. init and run ask the operator first. A non-zero exit is normal — read the output and explain it.",
		Schema: []byte(`{"type":"object","properties":{"args":{"type":"array","items":{"type":"string"},"description":"subcommand and flags, e.g. [\"check\",\"-live\"]"}},"required":["args"]}`),
	}
}

func (r runHivedispatch) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Args []string `json:"args"`
	}
	if err := decode(args, &in); err != nil {
		return "", err
	}
	canonical, mutating, ok := Allowed(in.Args)
	if !ok {
		return "", fmt.Errorf("refused: %q is not allowed; allowed: %s", strings.Join(in.Args, " "), strings.Join(Allowlist, "; "))
	}
	if mutating && !r.confirm(fmt.Sprintf("Run `hivedispatch %s`? It %s.", canonical, effects[canonical])) {
		return "declined by user; nothing was run", nil
	}
	argv := append([]string{in.Args[0], "-config", r.configPath}, in.Args[1:]...)
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.exe, argv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	case err != nil:
		return "", fmt.Errorf("start hivedispatch %s: %w", canonical, err)
	}
	if ctx.Err() != nil {
		return "", fmt.Errorf("hivedispatch %s: timed out after %s", canonical, runTimeout)
	}
	return fmt.Sprintf("exit %d\n--- stdout ---\n%s\n--- stderr ---\n%s", code, tail(stdout.String()), tail(stderr.String())), nil
}

func tail(s string) string {
	if len(s) <= runMaxBytes {
		return s
	}
	return "…(truncated)…\n" + s[len(s)-runMaxBytes:]
}
```

- [ ] **Step 4: Run** tests + lint → PASS.

- [ ] **Step 5: Commit** `feat(supervisor): run_hivedispatch tool — allowlisted re-exec with confirmation for init and run`.

---

### Task 9: System prompt

**Files:**
- Create: `internal/supervisor/prompt.go`, `prompt_test.go`

**Interfaces:**
- Produces:

```go
type PromptInput struct { ConfigPath string; ConfigExists bool; ModelName string; Notes string; NoteLines int; CheckOutput string }
func BuildSystem(in PromptInput) string
func CheckOutput(ctx context.Context, exe, workerConfigPath string) string // "hivedispatch check" output, capped; "check could not run: …" on failure
```

- [ ] **Step 1: Failing test** — `prompt_test.go`:

```go
package supervisor

import (
	"context"
	"strings"
	"testing"
)

func TestBuildSystem(t *testing.T) {
	s := BuildSystem(PromptInput{ConfigPath: "/c/config.yaml", ConfigExists: false, ModelName: "fake", Notes: "- 2026-09-20: uses github\n", NoteLines: 1, CheckOutput: "read config: open /c/config.yaml: no such file"})
	for _, want := range []string{
		"HiveDispatch supervisor",
		"ask before changing anything",
		"read_doc",
		"run -once -executor fake -placeholder",
		"# Notes from earlier sessions",
		"- 2026-09-20: uses github",
		"/c/config.yaml (missing)",
		"# Current `hivedispatch check` output",
		"no such file",
		"never in the config",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if len(s) > 12<<10 {
		t.Errorf("base prompt is %d bytes; keep it small, docs are fetched on demand", len(s))
	}
	if s2 := BuildSystem(PromptInput{ConfigPath: "/c/config.yaml", ConfigExists: true, ModelName: "fake"}); !strings.Contains(s2, "/c/config.yaml (exists)") || strings.Contains(s2, "# Notes from earlier sessions") {
		t.Errorf("exists/no-notes variant: %q", s2)
	}
}

func TestCheckOutput(t *testing.T) {
	out := CheckOutput(context.Background(), fakeExe(t), "/c/config.yaml")
	if !strings.Contains(out, "args: check -config /c/config.yaml") {
		t.Errorf("out = %q", out)
	}
	if out := CheckOutput(context.Background(), "/nonexistent/hivedispatch", "/c"); !strings.Contains(out, "check could not run") {
		t.Errorf("out = %q", out)
	}
}
```

- [ ] **Step 2: `prompt.go`**

```go
package supervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// PromptInput is the state the system prompt is built from, gathered
// fresh every turn.
type PromptInput struct {
	ConfigPath   string
	ConfigExists bool
	ModelName    string
	Notes        string
	NoteLines    int
	CheckOutput  string
}

// BuildSystem renders the system prompt.
func BuildSystem(in PromptInput) string {
	var sb strings.Builder
	sb.WriteString(`You are the HiveDispatch supervisor: an assistant built into the hivedispatch binary that helps an operator get HiveDispatch configured and running.

HiveDispatch turns tickets into pull requests. A worker polls an issue tracker (Jira Cloud or GitHub Issues), triages and claims a ticket, creates a git worktree on a hive/<KEY> branch, runs a coding-agent CLI (Claude Code or Codex) in it, commits, pushes, opens a pull request and reports back on the ticket. Everything is configured in one worker config file; secrets are environment variables (HIVE_JIRA_TOKEN, HIVE_GITHUB_TOKEN) and never in the config.

# Rules

- Always ask before changing anything. The tools enforce this (write_config, init and run show the operator a diff or the command and wait for y/N); your job is to explain what you are about to do and why before you call them.
- Prefer read_doc over guessing. The config reference (read_doc config) lists every key; the setup guide (read_doc setup) has the step-by-step for each tracker. Cite the section you relied on.
- Tokens live in environment variables, never in the config. If a check reports a missing token, tell the operator which variable to export and where the value comes from.
- Never run the real executor. run_hivedispatch only allows a fake-executor dry run; "hivedispatch run" for real is something the operator starts themselves.
- Keep replies short and concrete: what is wrong, what you will do, what the operator must do. One step at a time.
- Use remember for facts about this operator's setup that will matter next session.

# Tools

read_config, write_config (whole file, confirmed diff), read_doc (setup, config, design, decisions), read_repo_file (.hivedispatch.yaml or AGENTS.md from a configured repo), remember, and run_hivedispatch with exactly these argv shapes:
`)
	for _, a := range Allowlist {
		sb.WriteString("  - " + a + "\n")
	}
	if in.Notes != "" {
		fmt.Fprintf(&sb, "\n# Notes from earlier sessions (%d lines in %s)\n\n%s", in.NoteLines, "memory.md", in.Notes)
	}
	state := "missing"
	if in.ConfigExists {
		state = "exists"
	}
	fmt.Fprintf(&sb, "\n# Current state\n\n- Worker config: %s (%s)\n- Model: %s\n- Date: %s\n", in.ConfigPath, state, in.ModelName, time.Now().UTC().Format("2006-01-02"))
	if in.CheckOutput != "" {
		sb.WriteString("\n# Current `hivedispatch check` output\n\n```\n" + strings.TrimSpace(in.CheckOutput) + "\n```\n")
	}
	return sb.String()
}

// CheckOutput runs `hivedispatch check` and returns what it printed, so
// the first reply already knows what is wrong.
func CheckOutput(ctx context.Context, exe, workerConfigPath string) string {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "check", "-config", workerConfigPath)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return "check could not run: " + err.Error()
	}
	return tail(out.String())
}
```

- [ ] **Step 3: Run** tests + lint → PASS.

- [ ] **Step 4: Commit** `feat(supervisor): system prompt with rules, notes and live check output`.

---

### Task 10: REPL and wiring

**Files:**
- Create: `internal/supervisor/repl.go`, `repl_test.go`, `internal/supervisor/supervisor.go`

**Interfaces:**
- Produces:

```go
type Options struct {
    WorkerConfigPath string
    Provider, Model  string        // flag overrides
    Resume           bool          // newest session
    Session          string        // a specific session file
    Exe              string        // os.Executable()
    Stdin io.Reader; Stdout, Stderr io.Writer
    Interactive      bool          // stdin is a terminal
    Getenv           func(string) string
    Now              func() time.Time
}
func New(ctx context.Context, o Options) (*REPL, error)
type REPL struct { /* agent, memory, session name, io, model name, config path */ }
func (r *REPL) Run(ctx context.Context) error   // interactive loop, or one-shot when !Interactive
func (r *REPL) Confirm(prompt string) bool      // reads a line from stdin; false when !Interactive
func (r *REPL) Banner() string
```

REPL behaviour (spec § REPL): banner; `> ` prompt; slash commands `/help /quit /notes /model /reset`; each `tool_start` event prints `⋯ name(summary)` to stderr where summary is the args JSON cut to 60 chars; the reply prints to stdout followed by a blank line; after each turn the session is saved; provider errors print `error: <msg>` and the loop continues; Ctrl-C during a turn cancels it (`interrupted` printed), Ctrl-C at the prompt exits; Ctrl-D exits; non-interactive reads all stdin as one message, prints the answer, returns (an error → non-nil return). Lines are read by a goroutine into a channel so signals can be selected against a blocked read. The spinner writes `thinking…` and clears with `\r\033[K` on stderr only when `Interactive`.

- [ ] **Step 1: Failing tests** — `repl_test.go` (drive the REPL with a fake model through `New` by `Provider: "fake"`; the fake returns one canned reply, so assertions are on flow, not content):

```go
package supervisor

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func opts(t *testing.T, stdin string, interactive bool) (Options, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	var out, errb bytes.Buffer
	return Options{
		WorkerConfigPath: filepath.Join(dir, "config.yaml"),
		Provider:         "fake",
		Exe:              fakeExe(t),
		Stdin:            strings.NewReader(stdin),
		Stdout:           &out, Stderr: &errb,
		Interactive:      interactive,
		Getenv:           func(string) string { return "" },
		Now:              func() time.Time { return at },
	}, &out, &errb
}

func TestOneShot(t *testing.T) {
	o, out, _ := opts(t, "why does check fail?\n", false)
	r, err := New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "(fake supervisor") {
		t.Errorf("stdout = %q", out.String())
	}
	if r.Confirm("x?") {
		t.Error("non-interactive confirm must be false")
	}
	name, _ := NewMemory(Dir(o.WorkerConfigPath)).NewestSession()
	if name == "" {
		t.Error("session not saved")
	}
}

func TestInteractiveCommandsAndQuit(t *testing.T) {
	o, out, _ := opts(t, "/model\n/notes\nhello\n/quit\nnever sent\n", true)
	r, err := New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "fake") || !strings.Contains(s, "(no notes yet)") || !strings.Contains(s, "(fake supervisor") {
		t.Errorf("stdout = %q", s)
	}
	if strings.Count(s, "(fake supervisor") != 1 {
		t.Errorf("/quit did not stop the loop: %q", s)
	}
	if !strings.Contains(r.Banner(), "config "+o.WorkerConfigPath+" (missing)") {
		t.Errorf("banner = %q", r.Banner())
	}
}

func TestResumeLoadsNewest(t *testing.T) {
	o, _, _ := opts(t, "", false)
	mem := NewMemory(Dir(o.WorkerConfigPath))
	if err := mem.SaveSession(mem.NewSessionName(at), historyOf("earlier")); err != nil {
		t.Fatal(err)
	}
	o.Resume = true
	r, err := New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if h := r.agent.History(); len(h) != 1 || h[0].Content != "earlier" {
		t.Errorf("history = %+v", h)
	}
	o.Resume = false
	o.Session = "missing.json"
	if _, err := New(context.Background(), o); err == nil {
		t.Error("missing session accepted")
	}
}

func TestConfirmReadsLine(t *testing.T) {
	o, _, errb := opts(t, "y\nn\n", true)
	r, err := New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Confirm("Apply?") || r.Confirm("Apply?") {
		t.Error("confirm should read y then n")
	}
	if !strings.Contains(errb.String(), "Apply? [y/N] ") {
		t.Errorf("stderr = %q", errb.String())
	}
	if _, statErr := os.Stat(o.WorkerConfigPath); statErr == nil {
		t.Error("New must not create the worker config")
	}
}
```

Also in `repl_test.go` (import `github.com/thomasmeadows/hivedispatch/internal/supervisor/model`):

```go
func historyOf(user string) []model.Message {
	return []model.Message{{Role: model.RoleUser, Content: user}}
}
```

- [ ] **Step 2: Run** → FAIL.

- [ ] **Step 3: `supervisor.go` (wiring)**

```go
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// Options wires a supervisor session.
type Options struct {
	WorkerConfigPath string
	Provider, Model  string // flag overrides
	Resume           bool   // resume the newest session
	Session          string // resume this session file
	Exe              string // the hivedispatch binary to re-exec
	Stdin            io.Reader
	Stdout, Stderr   io.Writer
	Interactive      bool // stdin is a terminal: prompts, confirmations, spinner
	Getenv           func(string) string
	Now              func() time.Time
}

// New loads the supervisor config, builds the model, tools and agent, and
// resumes a session when asked.
func New(ctx context.Context, o Options) (*REPL, error) {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	cfg, err := LoadConfig(ConfigPath(o.WorkerConfigPath), o.Getenv)
	if err != nil {
		return nil, err
	}
	cfg.Override(o.Provider, o.Model)
	if cfg.Provider == "ollama" && cfg.FromEnv {
		if err := ProbeOllama(ctx, cfg.BaseURL); err != nil {
			return nil, fmt.Errorf("no model configured: set ANTHROPIC_API_KEY, OPENAI_API_KEY or HF_TOKEN, run Ollama (tried %s: %v), or write %s", cfg.BaseURL, err, ConfigPath(o.WorkerConfigPath))
		}
	}
	m, err := NewModel(cfg, o.Getenv)
	if err != nil {
		return nil, err
	}
	mem := NewMemory(Dir(o.WorkerConfigPath))
	r := &REPL{mem: mem, modelName: m.Name(), configPath: o.WorkerConfigPath, exe: o.Exe,
		stdin: o.Stdin, stdout: o.Stdout, stderr: o.Stderr, interactive: o.Interactive, now: o.Now}
	r.lines = newLineReader(o.Stdin)
	tools := []Tool{
		NewReadConfig(o.WorkerConfigPath),
		NewWriteConfig(o.WorkerConfigPath, r.Confirm),
		NewReadDoc(),
		NewReadRepoFile(o.WorkerConfigPath),
		NewRunHivedispatch(o.Exe, o.WorkerConfigPath, r.Confirm),
		NewRemember(mem, o.Now),
	}
	r.agent = &Agent{Model: m, Tools: tools, System: r.system, StepBudget: cfg.StepBudget, MaxTokens: cfg.MaxTokens, Events: r.onEvent}
	switch {
	case o.Session != "":
		h, err := mem.LoadSession(o.Session)
		if err != nil {
			return nil, err
		}
		r.agent.SetHistory(h)
		r.session = o.Session
	case o.Resume:
		name, err := mem.NewestSession()
		if err != nil {
			return nil, err
		}
		if name == "" {
			return nil, errors.New("no earlier session to resume")
		}
		h, err := mem.LoadSession(name)
		if err != nil {
			return nil, err
		}
		r.agent.SetHistory(h)
		r.session = name
	default:
		r.session = mem.NewSessionName(o.Now())
	}
	return r, nil
}
```

- [ ] **Step 4: `repl.go`**

```go
package supervisor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"
)

// REPL is the terminal front end of the agent.
type REPL struct {
	agent       *Agent
	mem         *Memory
	session     string
	modelName   string
	configPath  string
	exe         string
	stdin       io.Reader
	stdout      io.Writer
	stderr      io.Writer
	interactive bool
	now         func() time.Time
	lines       *lineReader
}

// lineReader reads stdin on a goroutine so the loop can select between a
// line and a signal. Confirm and the prompt share it.
type lineReader struct {
	once sync.Once
	ch   chan string
	done chan struct{}
	r    io.Reader
}

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{ch: make(chan string), done: make(chan struct{}), r: r}
}

func (l *lineReader) start() {
	l.once.Do(func() {
		go func() {
			sc := bufio.NewScanner(l.r)
			sc.Buffer(make([]byte, 64<<10), 1<<20)
			for sc.Scan() {
				l.ch <- sc.Text()
			}
			close(l.done)
		}()
	})
}

// next returns the next line, or ok=false at EOF.
func (l *lineReader) next() (string, bool) {
	l.start()
	select {
	case s := <-l.ch:
		return s, true
	case <-l.done:
		return "", false
	}
}

// Banner is the first line printed.
func (r *REPL) Banner() string {
	state := "missing"
	if _, err := os.Stat(r.configPath); err == nil {
		state = "exists"
	}
	_, n := r.mem.NotesForPrompt()
	return fmt.Sprintf("supervisor: %s · config %s (%s) · notes %d lines", r.modelName, r.configPath, state, n)
}

// Confirm asks the operator; a non-interactive session always declines.
func (r *REPL) Confirm(prompt string) bool {
	if !r.interactive {
		return false
	}
	fmt.Fprint(r.stderr, "\n"+prompt+" [y/N] ")
	line, ok := r.lines.next()
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

func (r *REPL) system() string {
	notes, n := r.mem.NotesForPrompt()
	_, statErr := os.Stat(r.configPath)
	return BuildSystem(PromptInput{
		ConfigPath: r.configPath, ConfigExists: statErr == nil, ModelName: r.modelName,
		Notes: notes, NoteLines: n, CheckOutput: CheckOutput(context.Background(), r.exe, r.configPath),
	})
}

func (r *REPL) onEvent(e Event) {
	switch e.Kind {
	case "tool_start":
		args := string(e.Args)
		if len(args) > 60 {
			args = args[:57] + "…"
		}
		fmt.Fprintf(r.stderr, "\r\033[K⋯ %s(%s)\n", e.Tool, args)
	case "budget":
		fmt.Fprintf(r.stderr, "\r\033[K(step budget reached)\n")
	}
}

const help = `commands:
  /help    this text
  /notes   print the notes file
  /model   which provider and model is answering
  /reset   start a new session (notes are kept)
  /quit    exit (Ctrl-D also exits)
Ctrl-C during a reply cancels it; at the prompt, exits.`

// Run drives the session until exit.
func (r *REPL) Run(ctx context.Context) error {
	if !r.interactive {
		raw, err := io.ReadAll(r.stdin)
		if err != nil {
			return err
		}
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			return errors.New("nothing to ask: pass a message on stdin")
		}
		return r.turn(ctx, msg)
	}
	fmt.Fprintln(r.stdout, r.Banner())
	fmt.Fprintln(r.stdout, "Type a message; /help for commands, Ctrl-D or /quit to exit.")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	for {
		fmt.Fprint(r.stdout, "\n> ")
		var line string
		var ok bool
		select {
		case <-sig:
			fmt.Fprintln(r.stdout)
			return nil
		case <-ctx.Done():
			return nil
		case res := <-r.lineOrEOF():
			line, ok = res.s, res.ok
		}
		if !ok {
			fmt.Fprintln(r.stdout)
			return nil
		}
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case line == "/quit" || line == "/exit":
			return nil
		case line == "/help":
			fmt.Fprintln(r.stdout, help)
			continue
		case line == "/model":
			fmt.Fprintln(r.stdout, r.modelName)
			continue
		case line == "/notes":
			n, err := r.mem.Notes()
			if err != nil {
				fmt.Fprintln(r.stderr, "error:", err)
			} else if n == "" {
				fmt.Fprintln(r.stdout, "(no notes yet)")
			} else {
				fmt.Fprint(r.stdout, n)
			}
			continue
		case line == "/reset":
			r.agent.SetHistory(nil)
			r.session = r.mem.NewSessionName(r.now())
			fmt.Fprintln(r.stdout, "new session")
			continue
		case strings.HasPrefix(line, "/"):
			fmt.Fprintln(r.stdout, "unknown command; /help lists them")
			continue
		}
		turnCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			select {
			case <-sig:
				cancel()
			case <-done:
			}
		}()
		err := r.turn(turnCtx, line)
		close(done)
		cancel()
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(r.stderr, "\r\033[Kinterrupted")
		} else if err != nil {
			fmt.Fprintln(r.stderr, "\r\033[Kerror:", err)
		}
	}
}

// lineOrEOF adapts the line reader to a channel of (line, ok) for select.
func (r *REPL) lineOrEOF() <-chan lineResult {
	ch := make(chan lineResult, 1)
	go func() {
		s, ok := r.lines.next()
		ch <- lineResult{s, ok}
	}()
	return ch
}

type lineResult struct {
	s  string
	ok bool
}

func (r *REPL) turn(ctx context.Context, msg string) error {
	stop := r.spinner()
	reply, err := r.agent.Turn(ctx, msg)
	stop()
	if err != nil {
		return err
	}
	fmt.Fprintln(r.stdout, reply)
	if err := r.mem.SaveSession(r.session, r.agent.History()); err != nil {
		fmt.Fprintln(r.stderr, "warning: session not saved:", err)
	}
	return nil
}

// spinner shows "thinking…" on stderr until the returned stop is called.
func (r *REPL) spinner() (stop func()) {
	if !r.interactive {
		return func() {}
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		dots := 0
		fmt.Fprint(r.stderr, "thinking")
		for {
			select {
			case <-done:
				fmt.Fprint(r.stderr, "\r\033[K")
				return
			case <-t.C:
				dots = (dots + 1) % 4
				fmt.Fprintf(r.stderr, "\r\033[Kthinking%s", strings.Repeat(".", dots))
			}
		}
	}()
	return func() { close(done); <-finished }
}
```

A line read by `lineOrEOF` while a signal wins the race is lost; acceptable because the signal case exits.

- [ ] **Step 5: Run** `go test -race ./internal/supervisor/...` + lint → PASS.

- [ ] **Step 6: Commit** `feat(supervisor): terminal REPL, one-shot mode, session resume and wiring`.

---

### Task 11: The `supervisor` command

**Files:**
- Create: `cmd/hivedispatch/supervisor.go`
- Modify: `cmd/hivedispatch/main.go` (usage + switch), `cmd/hivedispatch/main_test.go`

- [ ] **Step 1: Failing test** — append to `main_test.go`:

```go
func TestSupervisorOneShotFake(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	stdin := strings.NewReader("what is wrong?\n")
	code := runSupervisor([]string{"-config", filepath.Join(dir, "config.yaml"), "-provider", "fake"}, stdin, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "(fake supervisor") {
		t.Errorf("stdout = %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "supervisor", "sessions")); err != nil {
		t.Error("session dir not created beside the config")
	}
}

func TestUsageListsSupervisor(t *testing.T) {
	var out, errb bytes.Buffer
	run(nil, &out, &errb)
	if !strings.Contains(errb.String(), "supervisor") {
		t.Error("usage lacks supervisor")
	}
}
```

- [ ] **Step 2: `supervisor.go`**

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/supervisor"
)

// runSupervisor starts the interactive assistant, or answers one piped
// message when stdin is not a terminal.
func runSupervisor(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("supervisor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "path to worker config")
	provider := fs.String("provider", "", "override the supervisor config: "+supervisor.Providers)
	modelName := fs.String("model", "", "override the model name")
	resume := fs.Bool("resume", false, "continue the most recent session")
	session := fs.String("session", "", "continue this session file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	interactive := false
	if f, ok := stdin.(*os.File); ok {
		interactive = isTerminal(f)
	}
	ctx := context.Background()
	r, err := supervisor.New(ctx, supervisor.Options{
		WorkerConfigPath: *cfgPath, Provider: *provider, Model: *modelName,
		Resume: *resume, Session: *session, Exe: exe,
		Stdin: stdin, Stdout: stdout, Stderr: stderr, Interactive: interactive,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := r.Run(ctx); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
```

- [ ] **Step 3: `main.go`** — add to `usage` after `status`:

```
  supervisor [-config P] [-provider anthropic|openai|huggingface|ollama] [-model M] [-resume | -session FILE]
                              chat with the built-in assistant that helps configure and run HiveDispatch;
                              piped stdin asks one question and exits
```

and to the switch: `case "supervisor": return runSupervisor(args[1:], os.Stdin, stdout, stderr)`.

- [ ] **Step 4: Run** `go vet ./... && go test -race ./... && golangci-lint run ./...` → PASS.

- [ ] **Step 5: Commit** `feat(cli): supervisor command`.

---

### Task 12: Docs, decisions, and live verification

**Files:**
- Modify: `docs/config.md`, `docs/setup.md`, `README.md`, `docs/design-spec.md`, `docs/decisions.md`

- [ ] **Step 1: `docs/config.md`** — append a section:

```markdown
## Supervisor config — `~/.config/hivedispatch/supervisor/config.yaml`

Settings for `hivedispatch supervisor`, the built-in assistant. Optional: with no file, the provider is chosen from the environment (`ANTHROPIC_API_KEY` → anthropic, else `OPENAI_API_KEY` → openai, else `HF_TOKEN` → huggingface, else a local Ollama). The file lives beside the worker config, so `-config PATH` moves it to `<dir of PATH>/supervisor/config.yaml`; the assistant's notes (`memory.md`) and session transcripts (`sessions/`) are in the same directory.

| Key | Default | Meaning |
|---|---|---|
| `provider` | *(from environment)* | `anthropic`, `openai`, `huggingface` or `ollama`. The last three share the OpenAI-style `chat/completions` API |
| `model` | *(per provider)* | `claude-sonnet-5` · `gpt-5-mini` · `Qwen/Qwen3-32B` · `qwen3`. Best-effort names: check your provider's catalogue and set this explicitly |
| `base_url` | *(per provider)* | `https://api.anthropic.com` · `https://api.openai.com/v1` · `https://router.huggingface.co/v1` · `http://localhost:11434/v1`. Any OpenAI-compatible server works under `openai` |
| `api_key_env` | *(per provider)* | `ANTHROPIC_API_KEY` · `OPENAI_API_KEY` · `HF_TOKEN` · none for Ollama. The variable that holds the key; the key itself never goes in YAML |
| `max_tokens` | `4096` | Reply length limit per model call |
| `step_budget` | `20` | Tool calls the assistant may make per message before it stops and asks to continue |

`-provider` and `-model` on the command line override the file for one session.
```

- [ ] **Step 2: `docs/setup.md`** — insert after "## 0. Start here":

```markdown
## 0b. Or let the supervisor walk you through it

```sh
export ANTHROPIC_API_KEY=...     # or OPENAI_API_KEY, HF_TOKEN, or a running Ollama
hivedispatch supervisor
```

opens a chat with an assistant built into the binary. It has read these docs, sees the current `hivedispatch check` output, and can write the config for you (you approve every diff), run `init -jira` / `init -github` / `check -live` / a fake-executor dry run (you approve every one), and remembers what it learned in `~/.config/hivedispatch/supervisor/memory.md` for next time. It never runs the real executor. `echo "why does check fail?" | hivedispatch supervisor` asks one question and exits. Which model answers is in [`docs/config.md` — Supervisor config](config.md#supervisor-config--confighivedispatchsupervisorconfigyaml).
```

- [ ] **Step 3: `README.md`** — add to the quick start block after `hivedispatch init`:

```sh
hivedispatch supervisor           # or: chat with the built-in assistant, which edits the config and runs the checks for you
```

- [ ] **Step 4: `docs/design-spec.md`** — at the end of "## Supervisor tier", append:

```markdown
**2026-09-20 amendment.** The supervisor exists, starting at a narrower job than fleet decisions: an interactive assistant (`hivedispatch supervisor`) with its own agent loop, tools bounded to the worker config and the allowlisted subcommands, and a notes file. The fleet-level questions above remain the goal and the reason it has its own loop rather than wrapping a coding agent. Design: `docs/superpowers/specs/2026-09-20-supervisor-agent-design.md`.
```

- [ ] **Step 5: `docs/decisions.md`** — append six entries in the file's existing style (`## 2026-09-20 — title`, one paragraph, "Rejected: …"):

1. *The supervisor is HiveDispatch's own agent loop* — rejected wrapping `claude`/`codex` interactive mode: less code, but the point is an agent HiveDispatch owns, with memory and tools it controls, that grows into fleet decisions.
2. *OpenAI-compatible chat/completions is the second provider shape* — one adapter reaches Hugging Face's router, Ollama, OpenAI, Groq, vLLM; rejected a per-vendor adapter each.
3. *Tool subcommands re-exec the binary* — rejected moving `check`/`init` into an internal package: a `main.go` refactor with no user-visible gain; re-exec shows the operator exactly what they would see.
4. *Confirm before mutate, in code* — every tool that changes something outside the notes file asks `[y/N]` with the change shown; rejected rules-in-prompt, as with triage and the executor allowlist.
5. *Notes file plus transcripts, no compaction* — rejected replaying every transcript and model-written summaries.
6. *Segregated: one directory, its own config file* — rejected a `supervisor:` block in the worker config, which would put the assistant's settings in the file it edits.

- [ ] **Step 6: Full verification** — `go vet ./... && go test -race ./... && golangci-lint run ./...` → PASS. Commit `docs: supervisor — config reference, setup, README, design amendment, decisions`.

- [ ] **Step 7: Live verification** (report the outcome honestly; do not claim it passed if it did not):

1. `go build -o bin/hivedispatch ./cmd/hivedispatch`.
2. With `ANTHROPIC_API_KEY` set and a temp `-config /tmp/hd/config.yaml` that does not exist: `bin/hivedispatch supervisor -config /tmp/hd/config.yaml`. Ask it to set up GitHub Issues for `thomasmeadows/HiveDispatch`; approve the config diff; ask it to run `check`; confirm it reports the token state correctly; `/quit`. Confirm `/tmp/hd/supervisor/sessions/*.json` and (if it used `remember`) `memory.md` exist.
3. `bin/hivedispatch supervisor -config /tmp/hd/config.yaml -resume` and ask "where were we?" — it must reflect the earlier turn.
4. Same first flow with `-provider ollama` (local Ollama running) or `-provider huggingface` with `HF_TOKEN`: the tool-call round trip must work through the OpenAI adapter. Note any model that does not call tools — that is a model limitation to document, not a bug to fix here.
5. `echo "what does check say?" | bin/hivedispatch supervisor -config /tmp/hd/config.yaml` prints one answer and exits 0.
6. Record anything learned as a decisions entry (`## 2026-09-20 — Supervisor, live`), the way "First live end-to-end run" was recorded.

- [ ] **Step 8: Finish** — use `superpowers:finishing-a-development-branch`: push `supervisor-agent`, open the PR with the summary and the live-verification notes, never merge.
