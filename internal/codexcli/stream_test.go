package codexcli

import (
	"bufio"
	"os"
	"testing"
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
