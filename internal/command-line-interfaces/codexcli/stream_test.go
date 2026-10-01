package codexcli

import (
	"bufio"
	"os"
	"testing"
	"time"
)

func parseFixture(t *testing.T, name, cwd string, onToolUse func(int)) Transcript {
	t.Helper()
	f, err := os.Open("codextest/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	p := NewParser(cwd, onToolUse)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		p.Line(sc.Bytes())
	}
	return p.Transcript()
}

func TestParseSuccessTranscript(t *testing.T) {
	var counts []int
	tr := parseFixture(t, "exec_success.jsonl", "/work/HIVE-1", func(n int) { counts = append(counts, n) })
	if tr.ThreadID != "01a0bc3f-24b5-79e1-92de-a5550233345e" {
		t.Errorf("thread = %q", tr.ThreadID)
	}
	// One command and one file change; the completed events for items already
	// counted at item.started must not count twice.
	if tr.ToolUses != 2 || len(counts) != 2 || counts[1] != 2 {
		t.Errorf("tool uses = %d, callbacks = %v", tr.ToolUses, counts)
	}
	if len(tr.EditedFiles) != 1 || tr.EditedFiles[0] != "cmd/app/main.go" {
		t.Errorf("edited = %v", tr.EditedFiles)
	}
	if tr.LastMessage != "hello" || !tr.TurnCompleted || tr.Error != "" {
		t.Errorf("transcript = %+v", tr)
	}
}

func TestParseErrorTranscriptCollectsEditsAndError(t *testing.T) {
	tr := parseFixture(t, "exec_error.jsonl", "/work/HIVE-2", nil)
	if tr.ToolUses != 2 {
		t.Errorf("tool uses = %d", tr.ToolUses)
	}
	if len(tr.EditedFiles) != 2 || tr.EditedFiles[0] != "cmd/app/main.go" || tr.EditedFiles[1] != "README.md" {
		t.Errorf("edited = %v", tr.EditedFiles)
	}
	if tr.TurnCompleted || tr.Error != "You've hit your usage limit. Try again later." {
		t.Errorf("transcript = %+v", tr)
	}
	if !LooksLikeBudget(tr) {
		t.Error("a usage-limit error is a budget stop")
	}
	if LooksLikeBudget(Transcript{Error: "model not found"}) {
		t.Error("an ordinary error is not a budget stop")
	}
}

func TestParseIgnoresGarbage(t *testing.T) {
	p := NewParser("/w", nil)
	p.Line([]byte("Reading prompt from stdin..."))
	p.Line([]byte(""))
	p.Line([]byte(`{"type":"unknown_future_event"}`))
	p.Line([]byte(`{"type":"item.completed","item":{"id":"x","type":"todo_list","items":[]}}`))
	if tr := p.Transcript(); tr.TurnCompleted || tr.ToolUses != 0 || tr.Lines != 4 {
		t.Errorf("Transcript = %+v", tr)
	}
}

func TestParseCountsCompletedItemsNeverStarted(t *testing.T) {
	p := NewParser("/w", nil)
	p.Line([]byte(`{"type":"item.completed","item":{"id":"a","type":"mcp_tool_call","server":"s","tool":"t","status":"completed"}}`))
	p.Line([]byte(`{"type":"item.completed","item":{"id":"b","type":"web_search","query":"q"}}`))
	p.Line([]byte(`{"type":"item.completed","item":{"id":"c","type":"agent_message","text":"first"}}`))
	p.Line([]byte(`{"type":"item.completed","item":{"id":"d","type":"agent_message","text":"last"}}`))
	tr := p.Transcript()
	if tr.ToolUses != 2 || tr.LastMessage != "last" {
		t.Errorf("Transcript = %+v", tr)
	}
}

func stepClock() func() time.Time {
	t := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	return func() time.Time {
		t = t.Add(time.Second)
		return t
	}
}

func TestParseRecordsSteps(t *testing.T) {
	p := NewParser("/w", nil)
	p.Now = stepClock()
	for _, l := range []string{
		`{"type":"thread.started","thread_id":"th"}`,
		`{"type":"item.started","item":{"id":"i0","type":"command_execution","command":"bash -lc 'cat a'","aggregated_output":"","exit_code":null,"status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"i0","type":"command_execution","command":"bash -lc 'cat a'","aggregated_output":"hello\n","exit_code":0,"status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"i1","type":"reasoning","text":"Think."}}`,
		`{"type":"item.started","item":{"id":"i2","type":"command_execution","command":"false","status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"i2","type":"command_execution","command":"false","aggregated_output":"","exit_code":1,"status":"failed"}}`,
		`{"type":"item.completed","item":{"id":"i3","type":"file_change","changes":[{"path":"/w/b.go","kind":"update"}],"status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"i4","type":"agent_message","text":"done"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1200,"cached_input_tokens":800,"output_tokens":90}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":0,"output_tokens":5}}`,
	} {
		p.Line([]byte(l))
	}
	tr := p.Transcript()
	if tr.InputTokens != 1210 || tr.OutputTokens != 95 {
		t.Errorf("tokens = %d in, %d out", tr.InputTokens, tr.OutputTokens)
	}
	s := tr.Steps
	if len(s) != 5 {
		t.Fatalf("steps = %+v", s)
	}
	if s[0].Kind != "tool" || s[0].Name != "command_execution" || s[0].Input != "bash -lc 'cat a'" || s[0].Output != "hello\n" || s[0].IsError || s[0].End.Sub(s[0].Start) != time.Second {
		t.Errorf("command step = %+v", s[0])
	}
	if s[1].Kind != "message" || s[1].Name != "reasoning" || s[1].Output != "Think." {
		t.Errorf("reasoning step = %+v", s[1])
	}
	if !s[2].IsError || s[2].Output != "exit code 1" {
		t.Errorf("failed command step = %+v", s[2])
	}
	if s[3].Name != "file_change" || s[3].Output != "update b.go" || !s[3].Start.Equal(s[3].End) {
		t.Errorf("file change step = %+v", s[3])
	}
	if s[4].Kind != "message" || s[4].Name != "assistant" || s[4].Output != "done" {
		t.Errorf("message step = %+v", s[4])
	}
}
