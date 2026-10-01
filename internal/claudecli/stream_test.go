package claudecli

import (
	"bufio"
	"os"
	"testing"
	"time"
)

func parseFixture(t *testing.T, name, cwd string, onToolUse func(int)) Transcript {
	t.Helper()
	f, err := os.Open("clitest/" + name)
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
	tr := parseFixture(t, "stream_success.jsonl", "/work/HIVE-1", func(n int) { counts = append(counts, n) })
	if tr.SessionID != "5b89d684-8de0-4b4e-99b8-a7f283ffd611" {
		t.Errorf("session = %q", tr.SessionID)
	}
	if tr.ToolUses != 1 || len(counts) != 1 || counts[0] != 1 {
		t.Errorf("tool uses = %d, callbacks = %v", tr.ToolUses, counts)
	}
	if len(tr.EditedFiles) != 0 {
		t.Errorf("Read is not an edit: %v", tr.EditedFiles)
	}
	if tr.RateLimit == nil || tr.RateLimit.Status != "allowed" || !tr.RateLimit.ResetsAt.Equal(time.Unix(1789810200, 0)) {
		t.Errorf("rate limit = %+v", tr.RateLimit)
	}
	if tr.Result == nil || tr.Result.IsError || tr.Result.Result != "hello" || tr.Result.NumTurns != 2 || tr.Result.SessionID != tr.SessionID {
		t.Errorf("result = %+v", tr.Result)
	}
}

func TestParseErrorTranscriptCollectsEditsAndRateLimit(t *testing.T) {
	tr := parseFixture(t, "stream_error.jsonl", "/work/HIVE-2", nil)
	if tr.ToolUses != 2 {
		t.Errorf("tool uses = %d", tr.ToolUses)
	}
	if len(tr.EditedFiles) != 1 || tr.EditedFiles[0] != "cmd/app/main.go" {
		t.Errorf("edited = %v", tr.EditedFiles)
	}
	if tr.RateLimit == nil || tr.RateLimit.Status != "rejected" {
		t.Errorf("rate limit = %+v", tr.RateLimit)
	}
	if tr.Result == nil || !tr.Result.IsError || tr.Result.APIErrorStatus == nil || *tr.Result.APIErrorStatus != 429 {
		t.Errorf("result = %+v", tr.Result)
	}
}

func TestParseIgnoresGarbage(t *testing.T) {
	p := NewParser("/w", nil)
	p.Line([]byte("Warning: no stdin data received"))
	p.Line([]byte(""))
	p.Line([]byte(`{"type":"unknown_future_event"}`))
	if tr := p.Transcript(); tr.Result != nil || tr.Lines != 3 {
		t.Errorf("Transcript = %+v", tr)
	}
}

// stepClock ticks one second per call, from a fixed start.
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
		`{"type":"system","subtype":"init","cwd":"/w","session_id":"s","model":"claude-opus-5-5"}`,
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":""},{"type":"text","text":"Reading it."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/w/a.txt"}},{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"false"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"1\thello"}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","is_error":true,"content":[{"type":"text","text":"exit 1"}]}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t3","name":"Edit","input":{"file_path":"/w/b.go"}}]}}`,
		`{"type":"result","subtype":"success","result":"done","session_id":"s","total_cost_usd":0.25,"usage":{"input_tokens":100,"cache_read_input_tokens":50,"cache_creation_input_tokens":10,"output_tokens":20}}`,
	} {
		p.Line([]byte(l))
	}
	tr := p.Transcript()
	if tr.Model != "claude-opus-5-5" {
		t.Errorf("model = %q", tr.Model)
	}
	if u := tr.Result.Usage; u == nil || u.InputTokens != 100 || u.CacheReadInputTokens != 50 || u.CacheCreationInputTokens != 10 || u.OutputTokens != 20 {
		t.Errorf("usage = %+v", tr.Result.Usage)
	}
	s := tr.Steps
	if len(s) != 4 {
		t.Fatalf("steps = %+v", s)
	}
	if s[0].Kind != "message" || s[0].Name != "assistant" || s[0].Output != "Reading it." || !s[0].Start.Equal(s[0].End) {
		t.Errorf("message step = %+v", s[0])
	}
	if s[1].Kind != "tool" || s[1].Name != "Read" || s[1].Input != `{"file_path":"/w/a.txt"}` || s[1].Output != "1\thello" || s[1].IsError {
		t.Errorf("read step = %+v", s[1])
	}
	if got := s[1].End.Sub(s[1].Start); got != 2*time.Second {
		t.Errorf("read step took %v, want 2s (lines 3 to 4)", got)
	}
	if s[2].Name != "Bash" || !s[2].IsError || s[2].Output != "exit 1" || !s[2].End.After(s[1].End) {
		t.Errorf("bash step = %+v", s[2])
	}
	if s[3].Name != "Edit" || !s[3].End.IsZero() {
		t.Errorf("unanswered step = %+v", s[3])
	}
}
