package fake

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
)

func TestDefaultResult(t *testing.T) {
	e := New()
	res, err := e.Run(context.Background(), executor.Task{TicketKey: "HIVE-1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != executor.StatusCompleted || res.Summary == "" {
		t.Errorf("res = %+v", res)
	}
	if calls := e.Calls(); len(calls) != 1 || calls[0].TicketKey != "HIVE-1" {
		t.Errorf("calls = %+v", calls)
	}
}

func TestScriptedResultAndError(t *testing.T) {
	e := New()
	e.Results["HIVE-1"] = executor.Result{Status: executor.StatusNeedsInput, Question: "which db?"}
	e.Err["HIVE-2"] = errors.New("boom")
	res, err := e.Run(context.Background(), executor.Task{TicketKey: "HIVE-1"})
	if err != nil || res.Question != "which db?" {
		t.Errorf("res=%+v err=%v", res, err)
	}
	if _, err := e.Run(context.Background(), executor.Task{TicketKey: "HIVE-2"}); err == nil {
		t.Error("expected error")
	}
}

func TestBlockReturnsTimeoutOrKilled(t *testing.T) {
	e := New()
	e.Block["HIVE-1"] = true
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	res, _ := e.Run(ctx, executor.Task{TicketKey: "HIVE-1"})
	if res.Status != executor.StatusFailed || res.StopCause != executor.CauseTimeout {
		t.Errorf("timeout res = %+v", res)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	go func() { time.Sleep(5 * time.Millisecond); cancel2() }()
	res, _ = e.Run(ctx2, executor.Task{TicketKey: "HIVE-1"})
	if res.StopCause != executor.CauseKilled {
		t.Errorf("killed res = %+v", res)
	}
}

func TestCauseDescribe(t *testing.T) {
	if executor.CauseNone.Describe() != "" {
		t.Error("none should be empty")
	}
	for _, c := range []executor.Cause{executor.CauseBudget, executor.CauseTimeout, executor.CauseStepBudget, executor.CauseOverlap, executor.CauseError, executor.CauseKilled} {
		if c.Describe() == "" {
			t.Errorf("%q has no description", c)
		}
	}
}

func TestPlaceholderWritesFile(t *testing.T) {
	e := New()
	e.Placeholder = true
	dir := t.TempDir()
	res, err := e.Run(context.Background(), executor.Task{TicketKey: "HIVE-1", Workspace: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "HIVEDISPATCH_PLACEHOLDER.md")); err != nil {
		t.Error("placeholder file not written")
	}
	if len(res.ChangedFiles) != 1 {
		t.Errorf("changed = %v", res.ChangedFiles)
	}
}
