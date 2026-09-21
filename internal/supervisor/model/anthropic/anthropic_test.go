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
