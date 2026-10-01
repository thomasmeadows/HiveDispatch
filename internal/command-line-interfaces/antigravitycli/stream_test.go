package antigravitycli

import (
	"strings"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
)

func TestStreamDeduplicatesStepsAndUsage(t *testing.T) {
	count := 0
	p := NewParser(func(n int) { count = n })
	lines := []string{
		`{"event":"init","conversation_id":"c1","init":{"model":"gemini-test"}}`,
		`{"event":"step_update","step_update":{"conversation_id":"c1","step_index":4,"state":"ACTIVE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"go test ./..."}}}}`,
		`{"event":"step_update","step_update":{"conversation_id":"c1","step_index":4,"state":"DONE","step_type":"tool","tool_info":{"name":"run_command","output":"ok"}}}`,
		`{"event":"step_update","step_update":{"conversation_id":"c1","step_index":5,"state":"ACTIVE","step_type":"agent_response","text_delta":"All "}}`,
		`{"event":"step_update","step_update":{"conversation_id":"c1","step_index":5,"state":"DONE","step_type":"agent_response","text_delta":"done.","usage":{"input_tokens":10,"cache_read_tokens":20,"output_tokens":5,"thinking_tokens":3}}}`,
		`{"event":"step_update","step_update":{"conversation_id":"c1","step_index":5,"state":"DONE","step_type":"agent_response","usage":{"input_tokens":10,"cache_read_tokens":20,"output_tokens":5}}}`,
		`{"event":"step_update","step_update":{"conversation_id":"c1","step_index":6,"state":"DONE","step_type":"checkpoint","usage":{"input_tokens":2,"output_tokens":1}}}`,
		`{"event":"result","result":{"conversation_id":"c1","status":"SUCCESS","response":"All done.","usage":{"input_tokens":9999,"output_tokens":9999}}}`,
	}
	for _, line := range lines {
		if err := p.Line([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	tr := p.Transcript()
	if count != 1 || tr.SessionID != "c1" || tr.Model != "gemini-test" || tr.Result == nil || tr.Result.Response != "All done." {
		t.Fatalf("transcript: %+v, count %d", tr, count)
	}
	if len(tr.Steps) != 2 || tr.Steps[0].Name != "run_command" || !strings.Contains(tr.Steps[0].Input, "go test") || tr.Steps[0].Output != "ok" || tr.Steps[0].End.IsZero() || tr.Steps[1].Output != "All done." {
		t.Fatalf("steps: %+v", tr.Steps)
	}
	if !tr.HasStepUsage || tr.Usage.InputTokens != 32 || tr.Usage.OutputTokens != 6 {
		t.Fatalf("usage: %+v", tr.Usage)
	}
}

func TestStreamToolFailureAndConversationIdentity(t *testing.T) {
	p := NewParser(nil)
	for _, line := range []string{
		`{"event":"init","conversation_id":"main"}`,
		`{"event":"step_update","step_update":{"conversation_id":"main","step_index":1,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"error":{"type":"permission_denied","message":"Command needs approval"}}}}`,
		`{"event":"step_update","step_update":{"conversation_id":"child","step_index":1,"state":"DONE","step_type":"tool","tool_name":"read_file"}}`,
	} {
		if err := p.Line([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	tr := p.Transcript()
	if tr.ToolUses != 2 || len(tr.Steps) != 2 || !tr.Steps[0].IsError || !strings.Contains(tr.Steps[0].Output, "Command needs approval") || tr.SessionID != "main" {
		t.Fatalf("transcript: %+v", tr)
	}
}

func TestStreamRejectsMalformedAndToleratesUnknownEvents(t *testing.T) {
	p := NewParser(nil)
	if err := p.Line([]byte(`{"event":"future"}`)); err != nil {
		t.Fatal(err)
	}
	if err := p.Line([]byte(`{"event":"result","result":`)); err == nil {
		t.Fatal("malformed JSON accepted")
	}
}

func TestStreamCapsOutput(t *testing.T) {
	p := NewParser(nil)
	line := `{"event":"step_update","step_update":{"conversation_id":"c","step_index":1,"state":"ACTIVE","step_type":"agent_response","text_delta":"` + strings.Repeat("a", executor.MaxStepOutput) + `"}}`
	for range 3 {
		if err := p.Line([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.Transcript().Steps[0].Output) > executor.MaxStepOutput+32 {
		t.Fatal("unbounded output")
	}
}
