package antigravity

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
)

func fakeCLI(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agy")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nset -eu\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunProtocol(t *testing.T) {
	ws := t.TempDir()
	if err := os.Mkdir(filepath.Join(ws, ".hive-dispatch"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".hive-dispatch/policy.yaml"), []byte("guidance: Keep tests green\nexecutor:\n  path: [/opt/agy-test]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(t.TempDir(), "stdin")
	t.Setenv("CAPTURE", capture)
	t.Setenv("WORKSPACE", ws)
	bin := fakeCLI(t, `
[ "$PWD" = "$WORKSPACE" ]
case "$PATH" in /opt/agy-test:*) ;; *) exit 3;; esac
[ "$#" = 10 ]
while [ "$#" -gt 0 ]; do
 case "$1" in
 --input-format|--output-format) [ "$2" = stream-json ];;
 --conversation) [ "$2" = previous ];;
 --model) [ "$2" = gemini-test ];;
 --print-timeout) [ "$2" != 5m ];;
 *) exit 4;;
 esac
 shift 2
done
cat > "$CAPTURE"
cat <<'JSON'
{"event":"init","conversation_id":"previous","init":{"model":"gemini-test"}}
{"event":"step_update","step_update":{"conversation_id":"previous","step_index":10,"state":"DONE","step_type":"agent_response","text_delta":"Fixed.","usage":{"input_tokens":2,"cache_read_tokens":4,"output_tokens":3}}}
{"event":"result","result":{"conversation_id":"previous","status":"SUCCESS","response":"Fixed.","usage":{"input_tokens":1000,"output_tokens":900}}}
JSON
`)
	r, err := New(Config{Binary: bin}).Run(context.Background(), executor.Task{Workspace: ws, Prompt: "Fix \"this\"\nnow", ResumeToken: "previous", Model: "gemini-test"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != executor.StatusCompleted || r.Summary != "Fixed." || r.ResumeToken != "previous" || r.Usage.Model != "gemini-test" || r.Usage.InputTokens != 6 || r.Usage.OutputTokens != 3 {
		t.Fatalf("result: %+v", r)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Event   string
		Message struct{ Content string }
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	if in.Event != "user" || !strings.Contains(in.Message.Content, "Fix \"this\"\nnow") || !strings.Contains(in.Message.Content, "Keep tests green") {
		t.Fatalf("input: %+v", in)
	}
}

func TestRunOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, status, response, detail, after string
		want                                  executor.Status
		cause                                 executor.Cause
	}{
		{"success", "SUCCESS", "Done", "", "", executor.StatusCompleted, executor.CauseNone},
		{"question", "SUCCESS", "HIVE_NEEDS_INPUT: Which DB?", "", "", executor.StatusNeedsInput, executor.CauseNone},
		{"waiting", "WAITING", "Which DB?", "", "", executor.StatusNeedsInput, executor.CauseNone},
		{"quota", "ERROR", "", "429 Resource exhausted", "exit 1", executor.StatusFailed, executor.CauseBudget},
		{"error", "ERROR", "", "authentication required", "exit 1", executor.StatusFailed, executor.CauseError},
		{"invalid", "INVALID", "", "", "", executor.StatusFailed, executor.CauseError},
		{"running", "RUNNING", "", "", "", executor.StatusFailed, executor.CauseError},
		{"canceled", "CANCELED", "", "", "", executor.StatusFailed, executor.CauseKilled},
		{"interrupted", "INTERRUPTED", "", "", "", executor.StatusFailed, executor.CauseKilled},
		{"exit", "SUCCESS", "Done", "", "exit 1", executor.StatusFailed, executor.CauseError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{"event": "result", "result": map[string]any{"conversation_id": "s1", "status": tc.status, "response": tc.response, "error": tc.detail}})
			if err != nil {
				t.Fatal(err)
			}
			bin := fakeCLI(t, "cat >/dev/null\ncat <<'JSON'\n"+string(raw)+"\nJSON\n"+tc.after+"\n")
			r, err := New(Config{Binary: bin}).Run(context.Background(), executor.Task{Workspace: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			if r.Status != tc.want || r.StopCause != tc.cause || r.ResumeToken != "s1" || r.Summary == "" {
				t.Fatalf("result: %+v", r)
			}
			if tc.want == executor.StatusNeedsInput && r.Question == "" {
				t.Fatal("missing question")
			}
			if tc.detail != "" && !strings.Contains(r.Summary, tc.detail) {
				t.Fatalf("missing error: %+v", r)
			}
		})
	}
}

func TestRunCancellationAndBudget(t *testing.T) {
	for _, budget := range []int{0, 1} {
		t.Run(string(rune('0'+budget)), func(t *testing.T) {
			bin := fakeCLI(t, `cat >/dev/null
echo '{"event":"init","conversation_id":"saved"}'
echo '{"event":"step_update","step_update":{"step_index":1,"step_type":"tool","state":"ACTIVE","tool_name":"read_file"}}'
echo '{"event":"step_update","step_update":{"step_index":1,"step_type":"tool","state":"DONE","tool_name":"read_file"}}'
echo '{"event":"step_update","step_update":{"step_index":2,"step_type":"tool","state":"ACTIVE","tool_name":"read_file"}}'
sleep 30
`)
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			r, err := New(Config{Binary: bin}).Run(ctx, executor.Task{Workspace: t.TempDir(), StepBudget: budget})
			if err != nil {
				t.Fatal(err)
			}
			want := executor.CauseTimeout
			if budget > 0 {
				want = executor.CauseStepBudget
			}
			if r.StopCause != want || r.ResumeToken != "saved" {
				t.Fatalf("result: %+v", r)
			}
		})
	}
}

func TestReadOnlyRejected(t *testing.T) {
	e := New(Config{})
	if _, err := e.Plan(context.Background(), executor.Task{}); err == nil {
		t.Fatal("plan accepted")
	}
	if _, err := e.Advise(context.Background(), executor.Advice{}); err == nil {
		t.Fatal("review accepted")
	}
}

func TestResultOnlyUsageOnResume(t *testing.T) {
	bin := fakeCLI(t, `cat >/dev/null
echo '{"event":"result","result":{"status":"SUCCESS","response":"Done","usage":{"input_tokens":10,"cache_read_tokens":20,"output_tokens":5}}}'
`)
	for _, resume := range []string{"", "old"} {
		r, err := New(Config{Binary: bin}).Run(context.Background(), executor.Task{Workspace: t.TempDir(), ResumeToken: resume})
		if err != nil {
			t.Fatal(err)
		}
		want := 30
		if resume != "" {
			want = 0
		}
		if r.Usage.InputTokens != want {
			t.Fatalf("resume %q usage %+v", resume, r.Usage)
		}
	}
}

func TestMissingAndMalformedResults(t *testing.T) {
	for _, output := range []string{`{"event":"init","conversation_id":"s1"}`, `{"event":"result"}`, `{"event":"result","result":`} {
		bin := fakeCLI(t, "cat >/dev/null\ncat <<'JSON'\n"+output+"\nJSON\n")
		r, err := New(Config{Binary: bin}).Run(context.Background(), executor.Task{Workspace: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if r.Status != executor.StatusFailed || r.StopCause != executor.CauseError {
			t.Fatalf("result: %+v", r)
		}
	}
}

func TestMissingBinary(t *testing.T) {
	_, err := New(Config{Binary: filepath.Join(t.TempDir(), "missing")}).Run(context.Background(), executor.Task{Workspace: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "antigravity.binary") {
		t.Fatalf("error: %v", err)
	}
}

func TestCLITimeout(t *testing.T) {
	bin := fakeCLI(t, `cat >/dev/null
echo '{"event":"result","result":{"status":"ERROR","error":"context deadline exceeded"}}'
exit 1
`)
	r, err := New(Config{Binary: bin}).Run(context.Background(), executor.Task{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if r.StopCause != executor.CauseTimeout {
		t.Fatalf("result: %+v", r)
	}
}
