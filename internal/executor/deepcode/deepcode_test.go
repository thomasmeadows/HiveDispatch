package deepcode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/command-line-interfaces/deepcodecli"
	"github.com/thomasmeadows/hivedispatch/internal/executor"
)

func TestMapOutcome(t *testing.T) {
	exitErr := errors.New("exit status 1")
	cases := []struct {
		name     string
		tr       deepcodecli.Transcript
		exit     deepcodecli.Exit
		status   executor.Status
		cause    executor.Cause
		contains string
	}{
		{"completed", deepcodecli.Transcript{Status: "completed", Reply: "Done."}, deepcodecli.Exit{Stdout: "Done.\n"}, executor.StatusCompleted, executor.CauseNone, "Done."},
		{"marker", deepcodecli.Transcript{Status: "completed"}, deepcodecli.Exit{Stdout: "I looked.\nHIVE_NEEDS_INPUT: Which DB?\n"}, executor.StatusNeedsInput, executor.CauseNone, "Which DB?"},
		{"waiting", deepcodecli.Transcript{Status: "waiting_for_user", Reply: "Postgres or SQLite?"}, deepcodecli.Exit{ExitErr: exitErr, Stderr: "Execution requires user input, which is unavailable in --exec mode."}, executor.StatusNeedsInput, executor.CauseNone, "Postgres or SQLite?"},
		{"permission", deepcodecli.Transcript{Status: "ask_permission"}, deepcodecli.Exit{ExitErr: exitErr, Stderr: "Execution requires permission confirmation, which is unavailable in --exec mode.\n- Tool: bash"}, executor.StatusFailed, executor.CauseError, "~/.deepcode/settings.json"},
		{"ratelimit", deepcodecli.Transcript{Status: "failed", FailReason: "429 Too Many Requests"}, deepcodecli.Exit{ExitErr: exitErr, Stderr: "Execution failed: 429 Too Many Requests"}, executor.StatusFailed, executor.CauseBudget, "429"},
		{"failed", deepcodecli.Transcript{Status: "failed", FailReason: "boom"}, deepcodecli.Exit{ExitErr: exitErr, Stderr: "Execution failed: boom"}, executor.StatusFailed, executor.CauseError, "boom"},
		{"nosession", deepcodecli.Transcript{}, deepcodecli.Exit{ExitErr: exitErr, Stderr: "Execution failed: API key not found"}, executor.StatusFailed, executor.CauseError, "API key not found"},
		{"timeout", deepcodecli.Transcript{}, deepcodecli.Exit{CtxErr: context.DeadlineExceeded}, executor.StatusFailed, executor.CauseTimeout, ""},
		{"steps", deepcodecli.Transcript{}, deepcodecli.Exit{StepTripped: true}, executor.StatusFailed, executor.CauseStepBudget, ""},
		{"killed", deepcodecli.Transcript{}, deepcodecli.Exit{CtxErr: context.Canceled}, executor.StatusFailed, executor.CauseKilled, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := mapOutcome(c.tr, c.exit)
			if res.Status != c.status || res.StopCause != c.cause {
				t.Fatalf("got %s/%s, want %s/%s: %+v", res.Status, res.StopCause, c.status, c.cause, res)
			}
			text := res.Summary + "\n" + res.Question
			if c.contains != "" && !strings.Contains(text, c.contains) {
				t.Fatalf("%q does not mention %q", text, c.contains)
			}
		})
	}
}

func TestMapOutcomeCarriesSessionData(t *testing.T) {
	steps := []executor.Step{{Kind: executor.StepTool, Name: "edit"}}
	tr := deepcodecli.Transcript{SessionID: "s1", Status: "completed", Reply: "ok", Steps: steps, EditedFiles: []string{"a.go"},
		Usage: executor.Usage{Model: "deepseek-flash", InputTokens: 5}}
	res := mapOutcome(tr, deepcodecli.Exit{Stdout: "ok"})
	if res.ResumeToken != "s1" || len(res.Steps) != 1 || res.ChangedFiles[0] != "a.go" || res.Usage.Model != "deepseek-flash" {
		t.Fatalf("res %+v", res)
	}
}

func TestRunAddsGuidanceAndPath(t *testing.T) {
	bin, err := filepath.Abs("../../command-line-interfaces/deepcodecli/deepcodetest/fakedeepcode.sh")
	if err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".hive-dispatch"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".hive-dispatch", "policy.yaml"), []byte("guidance: run go test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_DEEPCODE_MODE", "ok")
	t.Setenv("FAKE_DEEPCODE_ARGS_FILE", argsFile)
	t.Setenv("HOME", home)
	res, err := New(Config{Binary: bin}).Run(context.Background(), executor.Task{Prompt: "do it", Workspace: ws})
	if err != nil || res.Status != executor.StatusCompleted || res.ResumeToken == "" || res.Log == "" {
		t.Fatalf("res %+v err %v", res, err)
	}
	raw, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(raw), "do it") || !strings.Contains(string(raw), "run go test") {
		t.Fatalf("argv %q", raw)
	}
}

func TestPlanAndAdviseAreRefused(t *testing.T) {
	e := New(Config{})
	if _, err := e.Advise(context.Background(), executor.Advice{}); err == nil || !strings.Contains(err.Error(), "coding") {
		t.Fatalf("advise err %v", err)
	}
	if _, err := e.Plan(context.Background(), executor.Task{}); err == nil || !strings.Contains(err.Error(), "coding") {
		t.Fatalf("plan err %v", err)
	}
	if e.Name() != "deepcode" {
		t.Fatal(e.Name())
	}
}
