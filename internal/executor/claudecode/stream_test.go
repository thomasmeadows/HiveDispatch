package claudecode

import (
	"bufio"
	"os"
	"testing"
	"time"
)

func parseFixture(t *testing.T, name, cwd string, onToolUse func(int)) transcript {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	p := newStreamParser(cwd, onToolUse)
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
	p := newStreamParser("/w", nil)
	p.Line([]byte("Warning: no stdin data received"))
	p.Line([]byte(""))
	p.Line([]byte(`{"type":"unknown_future_event"}`))
	if tr := p.Transcript(); tr.Result != nil || tr.Lines != 3 {
		t.Errorf("transcript = %+v", tr)
	}
}
