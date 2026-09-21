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
