package dispatch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/executor"
	exfake "github.com/thomasmeadows/hivedispatch/internal/executor/fake"
	"github.com/thomasmeadows/hivedispatch/internal/state"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

func (h *harness) setAgents(agents ...config.Agent) {
	h.d.Cfg.Repos[0].Agents = agents
}

func TestAgentsArePoolSlots(t *testing.T) {
	h := newHarness(t)
	h.setAgents(config.Agent{Name: "a", Executor: "claude"}, config.Agent{Name: "b", Executor: "claude"})
	h.d.Cfg.MaxConcurrent = 5
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
	waitFor(t, "both agents busy", func() bool { return h.ex.Active() == 2 })
	time.Sleep(20 * time.Millisecond) // a third ticket has no free agent
	if h.ex.Active() != 2 {
		t.Fatalf("active = %d, want 2 (one per agent)", h.ex.Active())
	}
	close(gate)
	if n := <-done; n != 3 {
		t.Errorf("handled = %d, want 3", n)
	}
	if p := h.ex.Peak(); p != 2 {
		t.Errorf("peak = %d, want 2", p)
	}
}

func TestPinnedTicketRunsOnItsAgent(t *testing.T) {
	h := newHarness(t)
	h.setAgents(config.Agent{Name: "a", Executor: "claude", Model: "ma"}, config.Agent{Name: "Big", Executor: "claude", Model: "mb"})
	h.tr.Add(tracker.Ticket{Key: "HIVE-1", Summary: "one", Labels: []string{"hive:ready", "hive:agent:big"}})
	if out := h.handle(t); out != OutcomeCompleted {
		t.Fatalf("out = %v", out)
	}
	if calls := h.ex.Calls(); len(calls) != 1 || calls[0].Model != "mb" {
		t.Errorf("task = %+v, want agent Big's model", calls)
	}
	if r := h.run(t); r.Agent != "worker-a/Big" || r.Executor != "claude" {
		t.Errorf("run agent = %q executor = %q", r.Agent, r.Executor)
	}
}

func TestPinnedToUnknownAgentIsSkipped(t *testing.T) {
	h := newHarness(t)
	h.tr.Add(tracker.Ticket{Key: "HIVE-1", Summary: "one", Labels: []string{"hive:agent:nobody"}})
	n, err := h.d.Once(context.Background())
	if err != nil || n != 0 || len(h.ex.Calls()) != 0 {
		t.Errorf("n=%d err=%v calls=%d", n, err, len(h.ex.Calls()))
	}
	if !strings.Contains(h.logs.String(), "nobody") {
		t.Errorf("log does not name the missing agent: %s", h.logs.String())
	}
	if out := h.handle(t); out != OutcomeSkipped {
		t.Errorf("Handle = %v, want skipped", out)
	}
}

func TestAgentExecutorIsChosenByKind(t *testing.T) {
	h := newHarness(t)
	codex := exfake.New()
	h.d.Executors = map[string]executor.Executor{"codex": codex}
	h.setAgents(config.Agent{Name: "cx", Executor: "codex"})
	if out := h.handle(t); out != OutcomeCompleted {
		t.Fatalf("out = %v", out)
	}
	if len(codex.Calls()) != 1 || len(h.ex.Calls()) != 0 {
		t.Errorf("codex calls = %d, fallback calls = %d", len(codex.Calls()), len(h.ex.Calls()))
	}
}

func TestResumeNeedsTheSameExecutor(t *testing.T) {
	h := newHarness(t)
	seedNeedsInput(t, h, true)
	r, err := h.store.Load(context.Background(), "HIVE-1")
	if err != nil {
		t.Fatal(err)
	}
	r.Executor = "codex" // the question came from a Codex session
	if err := h.store.Save(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if out := h.handle(t); out != OutcomeCompleted {
		t.Fatalf("out = %v", out)
	}
	if len(h.tri.Calls()) != 1 {
		t.Error("a Claude agent cannot resume a Codex session: it must triage afresh")
	}
	if calls := h.ex.Calls(); len(calls) != 1 || calls[0].ResumeToken != "" {
		t.Errorf("task = %+v, want no resume token", calls)
	}
}

func TestResumeOnSameExecutorKeepsToken(t *testing.T) {
	h := newHarness(t)
	seedNeedsInput(t, h, true)
	r, err := h.store.Load(context.Background(), "HIVE-1")
	if err != nil {
		t.Fatal(err)
	}
	r.Executor = "claude"
	if err := h.store.Save(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	h.handle(t)
	if calls := h.ex.Calls(); len(calls) != 1 || calls[0].ResumeToken != "sess-9" {
		t.Errorf("task = %+v", calls)
	}
	if got := h.run(t); got.Phase != state.PhaseDone {
		t.Errorf("phase = %v", got.Phase)
	}
}
