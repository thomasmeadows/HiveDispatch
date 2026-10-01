package supervisor

import (
	"context"
	"errors"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model"
	"github.com/thomasmeadows/hivedispatch/internal/supervisor/model/fake"
	"github.com/thomasmeadows/hivedispatch/internal/trace"
	tracefake "github.com/thomasmeadows/hivedispatch/internal/trace/fake"
)

func TestTurnTraces(t *testing.T) {
	call := fake.Call("c1", "echo", `{"x":1}`)
	call.Usage = model.Usage{InputTokens: 11, OutputTokens: 2}
	m := fake.New(call, fake.Text("done"))
	a, _ := newAgent(m, echoTool{})
	rec := &tracefake.Exporter{}
	a.Tracer = trace.New(rec, trace.Options{})

	if _, err := a.Turn(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	turns := rec.Named("supervisor turn")
	if len(turns) != 1 {
		t.Fatalf("turn spans: %+v", rec.Ended())
	}
	turn := turns[0]
	if turn.Kind != trace.KindChain || turn.ParentID != "" || turn.Inputs["message"] != "go" || turn.Outputs["reply"] != "done" {
		t.Fatalf("turn span %+v", turn)
	}
	kids := rec.Children(turn.ID)
	if len(kids) != 3 {
		t.Fatalf("children %+v", kids)
	}
	llm, tool, llm2 := kids[0], kids[1], kids[2]
	if llm.Kind != trace.KindLLM || llm.Name != "fake" || llm2.Kind != trace.KindLLM {
		t.Fatalf("llm spans %+v %+v", llm, llm2)
	}
	if llm.Metadata["ls_model_name"] != "fake" {
		t.Fatalf("llm metadata %v", llm.Metadata)
	}
	u, _ := llm.Outputs["usage_metadata"].(map[string]any)
	if u["input_tokens"] != 11 || u["output_tokens"] != 2 {
		t.Fatalf("usage %v", llm.Outputs)
	}
	msgs, _ := llm.Inputs["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("llm input messages %v", llm.Inputs["messages"])
	}
	if sys := msgs[0].(map[string]any); sys["role"] != "system" || sys["content"] != "SYS" {
		t.Fatalf("system message %v", sys)
	}
	if tool.Kind != trace.KindTool || tool.Name != "echo" || tool.Outputs["output"] != `echo:{"x":1}` || tool.Error != "" {
		t.Fatalf("tool span %+v", tool)
	}
	if args, _ := tool.Inputs["args"].(map[string]any); args["x"] != float64(1) {
		t.Fatalf("tool args %v", tool.Inputs)
	}
}

func TestTurnTracesErrors(t *testing.T) {
	m := fake.New(fake.Call("c1", "echo", `{}`), fake.Text("ok"))
	a, _ := newAgent(m, echoTool{fail: true})
	rec := &tracefake.Exporter{}
	a.Tracer = trace.New(rec, trace.Options{})
	if _, err := a.Turn(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if tools := rec.Named("echo"); len(tools) != 1 || tools[0].Error != "echo broke" {
		t.Fatalf("tool span %+v", tools)
	}

	m2 := fake.New()
	m2.Fail = errors.New("provider down")
	a2, _ := newAgent(m2)
	rec2 := &tracefake.Exporter{}
	a2.Tracer = trace.New(rec2, trace.Options{})
	if _, err := a2.Turn(context.Background(), "go"); err == nil {
		t.Fatal("want an error")
	}
	if turn := rec2.Named("supervisor turn"); len(turn) != 1 || turn[0].Error != "provider down" {
		t.Fatalf("turn span %+v", turn)
	}
	if llm := rec2.Named("fake"); len(llm) != 1 || llm[0].Error != "provider down" {
		t.Fatalf("llm span %+v", llm)
	}
}

func TestTurnTracesCancelledCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := fake.New(model.Response{
		Message: model.Message{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{
			{ID: "c1", Name: "cancel", Args: []byte(`{}`)},
			{ID: "c2", Name: "cancel", Args: []byte(`{}`)},
		}},
		StopReason: model.StopToolUse,
	})
	invoked := 0
	a, _ := newAgent(m, cancelOnCallTool{cancel: cancel, invoked: &invoked})
	rec := &tracefake.Exporter{}
	a.Tracer = trace.New(rec, trace.Options{})
	if _, err := a.Turn(ctx, "go"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	tools := rec.Named("cancel")
	if len(tools) != 2 || tools[1].Error != "cancelled" {
		t.Fatalf("tool spans %+v", tools)
	}
	if turn := rec.Named("supervisor turn"); len(turn) != 1 || turn[0].Error == "" {
		t.Fatalf("turn span %+v", turn)
	}
}
