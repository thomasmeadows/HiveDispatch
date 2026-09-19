package dispatch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

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
	b := *h.d
	b.Cfg.AgentID = "worker-b"
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
