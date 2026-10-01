package graphcli

import (
	"testing"
	"time"
)

func TestParserEvents(t *testing.T) {
	var counts []int
	p := NewParser(func(n int) { counts = append(counts, n) })
	for _, l := range []string{
		`not json`,
		`{"type":"node","name":"plan","start":"2026-10-01T12:00:00Z","end":"2026-10-01T12:00:02Z"}`,
		`{"type":"step","kind":"tool","name":"Bash","input":"{}","output":"ok","is_error":false,"start":"2026-10-01T12:00:03Z","end":"2026-10-01T12:00:05Z"}`,
		`{"type":"step","kind":"message","name":"assistant","output":"hi","start":"2026-10-01T12:00:06Z","end":"2026-10-01T12:00:06Z"}`,
		`{"type":"usage","model":"claude-opus-5-5","input_tokens":10,"output_tokens":2,"cost_usd":0.1}`,
		`{"type":"usage","model":"claude-opus-5-5","input_tokens":5,"output_tokens":1,"cost_usd":0.05}`,
		`{"type":"result","status":"completed","stop_cause":"","summary":"done","resume_token":"th-1","changed_files":["a.go"],"steps_traced":true}`,
	} {
		p.Line([]byte(l))
	}
	tr := p.Transcript()
	if len(tr.Steps) != 2 || tr.Steps[0].Name != "Bash" || tr.Steps[0].End.Sub(tr.Steps[0].Start) != 2*time.Second || tr.Steps[1].Kind != "message" {
		t.Fatalf("steps %+v", tr.Steps)
	}
	if tr.Usage.InputTokens != 15 || tr.Usage.OutputTokens != 3 || tr.Usage.CostUSD < 0.149 || tr.Usage.Model != "claude-opus-5-5" {
		t.Fatalf("usage %+v", tr.Usage)
	}
	if len(tr.Nodes) != 1 || tr.Nodes[0] != "plan" {
		t.Fatalf("nodes %v", tr.Nodes)
	}
	if r := tr.Result; r == nil || r.Status != "completed" || r.ResumeToken != "th-1" || !r.StepsTraced || len(r.ChangedFiles) != 1 {
		t.Fatalf("result %+v", tr.Result)
	}
	if len(counts) != 1 || counts[0] != 1 {
		t.Fatalf("only tool steps count toward the budget: %v", counts)
	}
}
