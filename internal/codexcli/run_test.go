package codexcli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/codexcli/codextest"
)

func TestRunSuccessCapturesTranscriptAndLog(t *testing.T) {
	bin, _, stdinFile := codextest.Setup(t, "success")
	tr, exit, log, err := Run(context.Background(), Cmd{Binary: bin, Dir: t.TempDir(), Stdin: "hi", StepBudget: 10})
	if err != nil || exit.ExitErr != nil || !tr.TurnCompleted || tr.LastMessage != "hello" {
		t.Fatalf("tr=%+v exit=%+v err=%v", tr, exit, err)
	}
	if !strings.Contains(log, `"type":"turn.completed"`) {
		t.Error("log missing stream")
	}
	if b, _ := os.ReadFile(stdinFile); string(b) != "hi" {
		t.Errorf("stdin = %q", b)
	}
}

func TestRunTimeoutKillsGroup(t *testing.T) {
	bin, _, _ := codextest.Setup(t, "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	tr, exit, _, err := Run(ctx, Cmd{Binary: bin, Dir: t.TempDir()})
	if err != nil || !errors.Is(exit.CtxErr, context.DeadlineExceeded) || tr.ThreadID == "" {
		t.Errorf("tr=%+v exit=%+v err=%v", tr, exit, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("not killed promptly")
	}
}

func TestRunStepBudgetTrips(t *testing.T) {
	bin, _, _ := codextest.Setup(t, "manysteps")
	_, exit, _, err := Run(context.Background(), Cmd{Binary: bin, Dir: t.TempDir(), StepBudget: 5})
	if err != nil || !exit.StepTripped || exit.CtxErr != nil {
		t.Errorf("exit=%+v err=%v", exit, err)
	}
}

func TestRunCrash(t *testing.T) {
	bin, _, _ := codextest.Setup(t, "crash")
	tr, exit, _, err := Run(context.Background(), Cmd{Binary: bin, Dir: t.TempDir()})
	if err != nil || exit.ExitErr == nil || tr.TurnCompleted || !strings.Contains(exit.Stderr, "segfault") {
		t.Errorf("tr=%+v exit=%+v err=%v", tr, exit, err)
	}
}
