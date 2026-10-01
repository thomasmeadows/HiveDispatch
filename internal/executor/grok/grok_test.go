package grok

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
)

func fakeCLI(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "grok")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nset -eu\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRun(t *testing.T) {
	ws := t.TempDir()
	if err := os.Mkdir(filepath.Join(ws, ".hive-dispatch"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".hive-dispatch/policy.yaml"), []byte("guidance: Keep tests green\nexecutor:\n  path: [/opt/grok-test]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := fakeCLI(t, `
[ "$PWD" = "$WORKSPACE" ]
case "$PATH" in /opt/grok-test:*) ;; *) exit 2;; esac
seen=0
while [ "$#" -gt 0 ]; do
 seen=$((seen + 1))
 case "$1" in
 --prompt-file) printf '%s' "$2" > "$PROMPT_PATH"; prompt=$(cat "$2"); case "$prompt" in *"Fix the bug"*"Keep tests green"*) ;; *) exit 3;; esac; shift 2;;
 --model) [ "$2" = "grok-test" ]; shift 2;;
 --resume) [ "$2" = "session-old" ]; shift 2;;
 --output-format) [ "$2" = "streaming-messages-json" ]; shift 2;;
 --max-turns) [ "$2" = "5" ]; shift 2;;
 --no-auto-update) shift;;
 *) exit 4;;
 esac
done
[ "$seen" = 6 ]
cat <<'JSON'
{"type":"system","subtype":"init","session_id":"session-new","model":"grok-test"}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"call1","name":"search_replace","input":{"file_path":"main.go"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"call1","content":"Edited","is_error":false}]}}
{"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","result":"Fixed.","session_id":"session-new","usage":{"input_tokens":10,"cache_read_input_tokens":20,"cache_creation_input_tokens":3,"output_tokens":4},"total_cost_usd":0.1}
JSON
`)
	t.Setenv("WORKSPACE", ws)
	capture := filepath.Join(t.TempDir(), "prompt-path")
	t.Setenv("PROMPT_PATH", capture)
	r, err := New(Config{Binary: bin}).Run(context.Background(), executor.Task{Workspace: ws, Prompt: "Fix the bug", Model: "grok-test", ResumeToken: "session-old", StepBudget: 5})
	if err != nil {
		t.Fatal(err)
	}
	raw, readErr := os.ReadFile(capture)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if _, statErr := os.Stat(string(raw)); !os.IsNotExist(statErr) {
		t.Fatalf("temporary prompt not removed: %v", statErr)
	}
	if r.Status != executor.StatusCompleted || r.Summary != "Fixed." || r.ResumeToken != "session-new" {
		t.Fatalf("result: %+v", r)
	}
	if len(r.ChangedFiles) != 1 || r.ChangedFiles[0] != "main.go" {
		t.Fatalf("files: %v", r.ChangedFiles)
	}
	if r.Usage.InputTokens != 33 || r.Usage.OutputTokens != 4 || r.Usage.CostUSD != 0.1 || r.Usage.Model != "grok-test" {
		t.Fatalf("usage: %+v", r.Usage)
	}
	if len(r.Steps) != 1 || r.Steps[0].Output != "Edited" || r.Steps[0].End.IsZero() {
		t.Fatalf("steps: %+v", r.Steps)
	}
}

func TestRunOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, line, after string
		status            executor.Status
		cause             executor.Cause
		contains          string
	}{
		{"question", `{"type":"result","subtype":"success","stop_reason":"end_turn","result":"HIVE_NEEDS_INPUT: Which DB?"}`, "", executor.StatusNeedsInput, executor.CauseNone, "Which DB?"},
		{"turns", `{"type":"result","subtype":"error_max_turns","is_error":true,"stop_reason":"max_turn_requests"}`, "", executor.StatusFailed, executor.CauseStepBudget, "step budget"},
		{"quota", `{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["429 Too Many Requests"]}`, "exit 1", executor.StatusFailed, executor.CauseBudget, "429"},
		{"error", `{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["Authentication failed"]}`, "exit 1", executor.StatusFailed, executor.CauseError, "Authentication failed"},
		{"truncated", `{"type":"result","subtype":"success","stop_reason":"max_tokens","result":"Partial"}`, "", executor.StatusFailed, executor.CauseError, "max_tokens"},
		{"refusal", `{"type":"result","subtype":"success","stop_reason":"refusal"}`, "", executor.StatusFailed, executor.CauseError, "refusal"},
		{"exit", `{"type":"result","subtype":"success","stop_reason":"end_turn","result":"Done"}`, "exit 1", executor.StatusFailed, executor.CauseError, "exit status 1"},
		{"missing", `{}`, "", executor.StatusFailed, executor.CauseError, "without a result"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := fakeCLI(t, "cat <<'JSON'\n"+tc.line+"\nJSON\n"+tc.after+"\n")
			r, err := New(Config{Binary: bin}).Run(context.Background(), executor.Task{Workspace: t.TempDir(), Prompt: "task"})
			if err != nil {
				t.Fatal(err)
			}
			if r.Status != tc.status || r.StopCause != tc.cause || !strings.Contains(r.Summary, tc.contains) {
				t.Fatalf("result: %+v", r)
			}
		})
	}
}

func TestRunCancellationAndBudget(t *testing.T) {
	for _, budget := range []int{0, 1} {
		t.Run(string(rune('0'+budget)), func(t *testing.T) {
			bin := fakeCLI(t, `
echo '{"type":"system","subtype":"init","session_id":"saved"}'
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"a","name":"read_file","input":{}},{"type":"tool_use","id":"b","name":"read_file","input":{}}]}}'
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

func TestCodingOnly(t *testing.T) {
	e := New(Config{})
	if _, err := e.Plan(context.Background(), executor.Task{}); err == nil {
		t.Fatal("plan accepted")
	}
	if _, err := e.Advise(context.Background(), executor.Advice{}); err == nil {
		t.Fatal("review accepted")
	}
}

func TestRunStartFailure(t *testing.T) {
	_, err := New(Config{Binary: filepath.Join(t.TempDir(), "missing")}).Run(context.Background(), executor.Task{Workspace: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "grok.binary") {
		t.Fatalf("error: %v", err)
	}
}

func TestRunCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	bin := fakeCLI(t, "echo '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"saved\"}'\nsleep 30\n")
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()
	defer cancel()
	r, err := New(Config{Binary: bin}).Run(ctx, executor.Task{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if r.StopCause != executor.CauseKilled {
		t.Fatalf("result: %+v", r)
	}
}
