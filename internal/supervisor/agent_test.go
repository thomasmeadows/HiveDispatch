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
	m := fake.New(fake.Call("c1", "echo", `{}`), fake.Call("c2", "echo", `{}`), fake.Call("c3", "echo", `{}`))
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
