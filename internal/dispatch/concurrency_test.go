package dispatch

import (
	"context"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// waitFor polls cond until it holds or a second passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestOnceRunsUpToMaxConcurrentTicketsAtOnce(t *testing.T) {
	h := newHarness(t)
	h.d.Cfg.MaxConcurrent = 2
	h.d.Cfg.RunTimeout = time.Minute
	h.tr.Add(tracker.Ticket{Key: "HIVE-2", Summary: "two"})
	h.tr.Add(tracker.Ticket{Key: "HIVE-3", Summary: "three"})
	gate := make(chan struct{})
	h.ex.Gate = gate
	done := make(chan int)
	go func() {
		n, err := h.d.Once(context.Background())
		if err != nil {
			t.Error(err)
		}
		done <- n
	}()
	waitFor(t, "two runs in parallel", func() bool { return h.ex.Active() == 2 })
	time.Sleep(20 * time.Millisecond) // a third must not start while both slots are held
	if h.ex.Active() != 2 {
		t.Fatalf("active = %d, want 2", h.ex.Active())
	}
	close(gate)
	if n := <-done; n != 3 {
		t.Errorf("handled = %d, want 3", n)
	}
	if p := h.ex.Peak(); p != 2 {
		t.Errorf("peak concurrency = %d, want 2", p)
	}
	// Each ticket worked in its own worktree.
	seen := map[string]bool{}
	for _, c := range h.ex.Calls() {
		if seen[c.Workspace] {
			t.Errorf("workspace %s shared", c.Workspace)
		}
		seen[c.Workspace] = true
	}
}

func TestDefaultIsOneTicketAtATime(t *testing.T) {
	h := newHarness(t)
	h.tr.Add(tracker.Ticket{Key: "HIVE-2", Summary: "two"})
	if n, err := h.d.Once(context.Background()); err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if p := h.ex.Peak(); p != 1 {
		t.Errorf("peak = %d, want 1", p)
	}
}

func TestRunKeepsPollingWhileTicketsRunAndDrainsOnStop(t *testing.T) {
	h := newHarness(t)
	h.d.Cfg.MaxConcurrent = 1
	h.d.Cfg.PollInterval = 2 * time.Millisecond
	h.d.Cfg.RunTimeout = time.Minute
	gate := make(chan struct{})
	h.ex.Gate = gate
	loopCtx, stop := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- h.d.Run(loopCtx, context.Background()) }()
	waitFor(t, "HIVE-1 to start", func() bool { return h.ex.Active() == 1 })
	h.tr.Add(tracker.Ticket{Key: "HIVE-2", Summary: "two"})
	before := h.tr.PollCount()
	waitFor(t, "polling to continue during the run", func() bool { return h.tr.PollCount() > before+2 })
	if n := len(h.ex.Calls()); n != 1 {
		t.Fatalf("calls = %d: the slot is taken, HIVE-2 must wait and HIVE-1 must not start twice", n)
	}
	stop() // drain: stop polling, let the run finish
	select {
	case <-done:
		t.Fatal("Run returned before its in-flight ticket finished")
	case <-time.After(20 * time.Millisecond):
	}
	close(gate)
	if err := <-done; err == nil {
		t.Error("Run should return the loop's cancellation")
	}
	if h.ex.Active() != 0 {
		t.Error("runs still active after Run returned")
	}
}
