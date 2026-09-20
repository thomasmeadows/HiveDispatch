package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/claudecli"
	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/state"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

func seedNeedsInput(t *testing.T, h *harness, withReply bool) {
	t.Helper()
	if err := h.store.Save(context.Background(), &state.Run{
		Ticket: "HIVE-1", Attempts: 1, LastStatus: string(executor.StatusNeedsInput),
		ResumeToken: "sess-9", QuestionAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	tk := tracker.Ticket{Key: "HIVE-1", Summary: "one", Comments: []tracker.Comment{
		{Author: "T", Body: Marker + " I need input", Created: now.Add(-time.Hour)},
	}}
	if withReply {
		tk.Comments = append(tk.Comments, tracker.Comment{Author: "T", Body: "use sqlite", Created: now.Add(-30 * time.Minute)})
	}
	h.tr.Add(tk)
}

func TestResumeAfterHumanReply(t *testing.T) {
	h := newHarness(t)
	seedNeedsInput(t, h, true)
	if out := h.handle(t); out != OutcomeCompleted {
		t.Fatalf("out = %v", out)
	}
	if len(h.tri.Calls()) != 0 {
		t.Error("resume must skip triage")
	}
	calls := h.ex.Calls()
	if len(calls) != 1 || calls[0].ResumeToken != "sess-9" || !strings.Contains(calls[0].Prompt, "use sqlite") {
		t.Errorf("task = %+v", calls)
	}
	if r := h.run(t); r.Attempts != 2 {
		t.Errorf("attempts = %d", r.Attempts)
	}
}

func TestNoReplyTriagesAgain(t *testing.T) {
	h := newHarness(t)
	seedNeedsInput(t, h, false)
	h.handle(t)
	if len(h.tri.Calls()) != 1 {
		t.Error("without a reply the ticket is triaged again")
	}
}

func TestHeartbeatLossCancelsRun(t *testing.T) {
	h := newHarness(t)
	h.d.Cfg.HeartbeatInterval = 5 * time.Millisecond
	h.d.Cfg.RunTimeout = 5 * time.Second
	h.ex.Block["HIVE-1"] = true
	go func() {
		time.Sleep(30 * time.Millisecond)
		h.tr.OverwriteClaim("HIVE-1", "worker-b", now)
	}()
	start := time.Now()
	out := h.handle(t)
	if out != OutcomeFailed {
		t.Fatalf("out = %v", out)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("run was not cancelled promptly after losing the claim")
	}
	if r := h.run(t); r.StopCause != string(executor.CauseKilled) {
		t.Errorf("run = %+v", r)
	}
	if c := h.ticket(t).Claim; c == nil || c.AgentID != "worker-b" {
		t.Errorf("the thief's claim must survive our release: %+v", c)
	}
}

func TestTwoWorkersOneTicketExactlyOneWins(t *testing.T) {
	h := newHarness(t)
	cfgB := h.d.Cfg
	cfgB.AgentID = "worker-b"
	b := Dispatcher{
		Cfg: cfgB, Tracker: h.d.Tracker, Triager: h.d.Triager, Executor: h.d.Executor,
		Workspaces: h.d.Workspaces, Host: h.d.Host, Store: h.d.Store, Now: h.d.Now, Log: h.d.Log,
	}
	// Both workers write before either reads back, which is the interleaving
	// the read-back protocol exists to resolve.
	var barrier sync.WaitGroup
	barrier.Add(2)
	h.tr.BeforeReadBack = func(string) { barrier.Done(); barrier.Wait() }

	tk := h.ticket(t)
	var wg sync.WaitGroup
	outcomes := make([]Outcome, 2)
	for i, d := range []*Dispatcher{h.d, &b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outcomes[i], _ = d.Handle(context.Background(), tk)
		}()
	}
	wg.Wait()

	wins, losses := 0, 0
	for _, o := range outcomes {
		switch o {
		case OutcomeCompleted:
			wins++
		case OutcomeClaimLost:
			losses++
		}
	}
	if wins != 1 || losses != 1 {
		t.Fatalf("outcomes = %v", outcomes)
	}
	if len(h.ex.Calls()) != 1 {
		t.Errorf("executor ran %d times, want 1", len(h.ex.Calls()))
	}
	if len(h.tr.Comments("HIVE-1")) != 1 {
		t.Errorf("comments = %v", h.tr.Comments("HIVE-1"))
	}
	h.assertReleased(t)
}

func TestOnceRespectsScheduleAndCountsHandled(t *testing.T) {
	h := newHarness(t)
	h.tr.Add(tracker.Ticket{Key: "HIVE-2", Summary: "two"})
	n, err := h.d.Once(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if h.tr.PollCount() != 1 {
		t.Errorf("polls = %d", h.tr.PollCount())
	}
}

func TestRunStopsOnLoopCancel(t *testing.T) {
	h := newHarness(t)
	h.d.Cfg.PollInterval = 5 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	err := h.d.Run(ctx, context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
	if h.tr.PollCount() < 2 {
		t.Errorf("polls = %d, want repeated polling", h.tr.PollCount())
	}
}

func TestBudgetStopPausesPolling(t *testing.T) {
	h := newHarness(t)
	reset := now.Add(45 * time.Minute)
	h.ex.Results["HIVE-1"] = executor.Result{Status: executor.StatusFailed, StopCause: executor.CauseBudget, Summary: "limit", RetryAfter: reset}
	if out := h.handle(t); out != OutcomeFailed {
		t.Fatalf("out = %v", out)
	}
	if until := h.d.PausedUntil(); !until.Equal(reset.Add(pauseMargin)) {
		t.Errorf("paused until %v, want reset+margin %v", until, reset.Add(pauseMargin))
	}
	// While paused, Once does not poll.
	before := h.tr.PollCount()
	if n, err := h.d.Once(context.Background()); err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if h.tr.PollCount() != before {
		t.Error("must not poll while paused")
	}
	// After the reset, polling resumes.
	h.d.Now = func() time.Time { return reset.Add(2 * time.Minute) }
	_, _ = h.d.Once(context.Background())
	if h.tr.PollCount() != before+1 {
		t.Error("should poll again after the reset")
	}
}

func TestTriageBudgetErrorPausesWithDefaultBackoff(t *testing.T) {
	h := newHarness(t)
	h.tri.Err = fmt.Errorf("triage: %w", &claudecli.BudgetError{Message: "session limit"})
	if _, err := h.d.Handle(context.Background(), h.ticket(t)); err == nil {
		t.Fatal("expected error")
	}
	if until := h.d.PausedUntil(); !until.Equal(now.Add(defaultBudgetBackoff)) {
		t.Errorf("paused until %v, want default backoff", until)
	}
	h.assertReleased(t)
}

func TestPushedWithoutPRJustOpensThePR(t *testing.T) {
	h := newHarness(t)
	// A previous run completed and pushed but the PR failed to open.
	if err := h.store.Save(context.Background(), &state.Run{
		Ticket: "HIVE-1", Attempts: 1, Phase: state.PhasePushed, LastStatus: string(executor.StatusCompleted), ResumeToken: "sess", Branch: "hive/HIVE-1",
	}); err != nil {
		t.Fatal(err)
	}
	h.ws.Changed["HIVE-1"] = true
	if out := h.handle(t); out != OutcomeCompleted {
		t.Fatalf("out = %v", out)
	}
	if len(h.tri.Calls()) != 0 || len(h.ex.Calls()) != 0 {
		t.Error("finishing a pushed run must not triage or run the agent again")
	}
	if len(h.host.Opened()) != 1 {
		t.Errorf("opened = %+v", h.host.Opened())
	}
	h.assertTransitions(t, tracker.StateInReview)
	if r := h.run(t); r.Phase != state.PhaseDone || r.PRURL == "" || r.Attempts != 1 {
		t.Errorf("run = %+v", r)
	}
}
