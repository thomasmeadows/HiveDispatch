package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
)

func fakeExec(t *testing.T, mode string) (*Executor, string) {
	t.Helper()
	bin, err := filepath.Abs("testdata/fakeclaude.sh")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_CLAUDE_MODE", mode)
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_CLAUDE_ARGS_FILE", args)
	t.Setenv("FAKE_CLAUDE_STDIN_FILE", filepath.Join(t.TempDir(), "stdin"))
	return New(Config{Binary: bin}), args
}

func task(t *testing.T) executor.Task {
	t.Helper()
	return executor.Task{TicketKey: "HIVE-1", Prompt: "do it", Workspace: t.TempDir(), StepBudget: 10}
}

func TestRunSuccess(t *testing.T) {
	e, argsFile := fakeExec(t, "success")
	res, err := e.Run(context.Background(), task(t))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != executor.StatusCompleted || res.Summary != "hello" || res.ResumeToken != "5b89d684-8de0-4b4e-99b8-a7f283ffd611" {
		t.Errorf("res = %+v", res)
	}
	if !strings.Contains(res.Log, `"type": "result"`) {
		t.Error("log should contain the raw stream")
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "stream-json") || strings.Contains(string(args), "--resume") {
		t.Errorf("args = %s", args)
	}
	stdin, _ := os.ReadFile(os.Getenv("FAKE_CLAUDE_STDIN_FILE"))
	if string(stdin) != "do it" {
		t.Errorf("stdin = %q", stdin)
	}
}

func TestRunPassesResumeAndGuidance(t *testing.T) {
	e, argsFile := fakeExec(t, "success")
	tk := task(t)
	tk.ResumeToken = "old-session"
	if err := os.WriteFile(filepath.Join(tk.Workspace, ".hivedispatch.yaml"), []byte("guidance: Always run make test.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), tk); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--resume\nold-session") {
		t.Errorf("args = %s", args)
	}
	stdin, _ := os.ReadFile(os.Getenv("FAKE_CLAUDE_STDIN_FILE"))
	if !strings.Contains(string(stdin), "Always run make test.") {
		t.Errorf("guidance missing from prompt: %q", stdin)
	}
}

func TestRunNeedsInput(t *testing.T) {
	e, _ := fakeExec(t, "needsinput")
	res, err := e.Run(context.Background(), task(t))
	if err != nil || res.Status != executor.StatusNeedsInput || !strings.HasPrefix(res.Question, "Should the flag") {
		t.Errorf("res=%+v err=%v", res, err)
	}
}

func TestRunErrorTranscript(t *testing.T) {
	e, _ := fakeExec(t, "error")
	res, err := e.Run(context.Background(), task(t))
	if err != nil || res.Status != executor.StatusFailed || res.StopCause != executor.CauseBudget {
		t.Errorf("res=%+v err=%v", res, err)
	}
	if len(res.ChangedFiles) != 1 || res.ChangedFiles[0] != "cmd/app/main.go" {
		t.Errorf("changed = %v", res.ChangedFiles)
	}
}

func TestRunCrashWithoutResult(t *testing.T) {
	e, _ := fakeExec(t, "crash")
	res, err := e.Run(context.Background(), task(t))
	if err != nil || res.Status != executor.StatusFailed || res.StopCause != executor.CauseError || !strings.Contains(res.Summary, "segfault") {
		t.Errorf("res=%+v err=%v", res, err)
	}
}

func TestRunTimeoutKillsProcessGroup(t *testing.T) {
	e, _ := fakeExec(t, "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	res, err := e.Run(ctx, task(t))
	if err != nil || res.StopCause != executor.CauseTimeout {
		t.Errorf("res=%+v err=%v", res, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("hung child was not killed promptly")
	}
	if res.ResumeToken == "" {
		t.Error("session id from init should survive a timeout")
	}
}

func TestRunStepBudget(t *testing.T) {
	e, _ := fakeExec(t, "manysteps")
	tk := task(t)
	tk.StepBudget = 5
	start := time.Now()
	res, err := e.Run(context.Background(), tk)
	if err != nil || res.StopCause != executor.CauseStepBudget {
		t.Errorf("res=%+v err=%v", res, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("step budget did not stop the run promptly")
	}
}

func TestPlanUsesStructuredOutput(t *testing.T) {
	e, argsFile := fakeExec(t, "plan")
	fp, err := e.Plan(context.Background(), task(t))
	if err != nil || len(fp.Files) != 2 || fp.Files[1] != "b.go" {
		t.Errorf("fp=%+v err=%v", fp, err)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--json-schema") || !strings.Contains(string(args), "plan") {
		t.Errorf("args = %s", args)
	}
}
