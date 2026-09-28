package dispatch

import (
	"context"
	"strings"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

var (
	planner  = config.Agent{Name: "planner", Role: config.RolePlanning, Executor: "claude"}
	coder    = config.Agent{Name: "coder", Role: config.RoleCoding, Executor: "claude"}
	reviewer = config.Agent{Name: "reviewer", Role: config.RoleReview, Executor: "claude"}
)

// once runs one poll of every stage and fails the test on error.
func (h *harness) once(t *testing.T) int {
	t.Helper()
	n, err := h.d.Once(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (h *harness) status(t *testing.T) string {
	t.Helper()
	return h.ticket(t).Status
}

func (h *harness) lastComment(t *testing.T) string {
	t.Helper()
	cs := h.tr.Comments("HIVE-1")
	if len(cs) == 0 {
		t.Fatal("no comments")
	}
	return cs[len(cs)-1]
}

func TestPlanningAgentPlansThenACoderSkipsTriage(t *testing.T) {
	h := newHarness(t)
	h.setAgents(planner, coder)
	h.tr.Add(tracker.Ticket{Key: "HIVE-1", Summary: "one", Status: string(tracker.StatePlanning)})

	if n := h.once(t); n != 1 {
		t.Fatalf("handled = %d", n)
	}
	advice := h.ex.AdviceCalls()
	if len(advice) != 1 || advice[0].Kind != executor.AdvicePlan || !strings.Contains(advice[0].Prompt, "one") || advice[0].Schema == "" {
		t.Fatalf("advice = %+v", advice)
	}
	if c := h.lastComment(t); !strings.HasPrefix(c, Marker) || !strings.Contains(c, "Fake plan") || !strings.Contains(c, "planner") {
		t.Errorf("plan comment = %q", c)
	}
	if h.status(t) != string(tracker.StateReady) || !h.run(t).Planned {
		t.Fatalf("status = %s planned = %v", h.status(t), h.run(t).Planned)
	}
	if len(h.ex.Calls()) != 0 {
		t.Fatal("the planner must not write code")
	}

	h.once(t)
	if len(h.tri.Calls()) != 0 {
		t.Error("a planned ticket skips triage")
	}
	if calls := h.ex.Calls(); len(calls) != 1 || !strings.Contains(calls[0].Prompt, "Fake plan") {
		t.Errorf("coding task = %+v, want the plan in its prompt", calls)
	}
	if h.status(t) != string(tracker.StateInReview) {
		t.Errorf("status = %s", h.status(t))
	}
}

func TestPlanningAgentAsksWhenUnclear(t *testing.T) {
	h := newHarness(t)
	h.setAgents(planner, coder)
	h.tr.Add(tracker.Ticket{Key: "HIVE-1", Summary: "one", Status: string(tracker.StatePlanning)})
	h.ex.Answers["HIVE-1"] = `{"decision":"needs_info","question":"Postgres or SQLite?"}`
	h.once(t)
	if h.status(t) != string(tracker.StateNeedsInfo) || !strings.Contains(h.lastComment(t), "Postgres or SQLite?") {
		t.Errorf("status = %s comment = %q", h.status(t), h.lastComment(t))
	}
	if h.run(t).Planned {
		t.Error("an unanswered question is not a plan")
	}
}

func TestStagesWithoutAgentsAreLeftAlone(t *testing.T) {
	h := newHarness(t) // one default coding agent
	h.tr.Add(tracker.Ticket{Key: "HIVE-1", Summary: "one", Status: string(tracker.StatePlanning)})
	if n := h.once(t); n != 0 || len(h.ex.AdviceCalls()) != 0 {
		t.Errorf("handled %d, advice %d: nobody plans without a planning agent", n, len(h.ex.AdviceCalls()))
	}
	if h.tr.PollCount() != 1 {
		t.Errorf("polls = %d, want only the Ready stage polled", h.tr.PollCount())
	}
}

func TestReviewAgentApprovesOncePerVersion(t *testing.T) {
	h := newHarness(t)
	h.ws.Changed["HIVE-1"] = true // the coder pushes, so a PR opens
	h.setAgents(coder, reviewer)
	h.once(t) // the coder opens a PR; the ticket goes to In Review
	if h.status(t) != string(tracker.StateInReview) {
		t.Fatalf("status = %s", h.status(t))
	}
	h.once(t) // the reviewer reviews it
	reviews := h.host.Reviews()
	if len(reviews) != 1 || !strings.Contains(reviews[0], "Approved") || !strings.Contains(reviews[0], "reviewer") {
		t.Fatalf("reviews = %v", reviews)
	}
	if h.status(t) != string(tracker.StateInReview) || !strings.Contains(h.lastComment(t), "merge") {
		t.Errorf("status = %s comment = %q: an approved PR waits for a human to merge", h.status(t), h.lastComment(t))
	}
	if a := h.ex.AdviceCalls(); len(a) != 1 || a[0].Kind != executor.AdviceReview || !strings.Contains(a[0].Prompt, "diff --git") {
		t.Errorf("review advice = %+v, want the diff in the prompt", a)
	}
	h.once(t)
	if len(h.host.Reviews()) != 1 {
		t.Error("the same version must not be reviewed twice")
	}
}

func TestReviewSendsBackThenCoderFixesThenReviewPasses(t *testing.T) {
	h := newHarness(t)
	h.ws.Changed["HIVE-1"] = true // the coder pushes, so a PR opens
	h.setAgents(coder, reviewer)
	h.ex.Results["HIVE-1"] = executor.Result{Status: executor.StatusCompleted, Summary: "done", ResumeToken: "sess-1"}
	h.once(t) // coded, PR at sha-1
	h.ex.Answers["HIVE-1"] = `{"verdict":"request_changes","summary":"Missing a test for the empty case."}`
	h.once(t) // review: changes requested
	if h.status(t) != string(tracker.StateReady) || !strings.Contains(h.lastComment(t), "Missing a test") {
		t.Fatalf("status = %s comment = %q", h.status(t), h.lastComment(t))
	}
	if r := h.run(t); r.ReviewRounds != 1 || !r.ReviewFix {
		t.Fatalf("run = %+v", r)
	}
	h.once(t) // the coder addresses the review
	calls := h.ex.Calls()
	if len(calls) != 2 || !strings.Contains(calls[1].Prompt, "Missing a test") || calls[1].ResumeToken != "sess-1" {
		t.Fatalf("fix task = %+v, want the review in a resumed session", calls[len(calls)-1])
	}
	if len(h.tri.Calls()) != 1 {
		t.Errorf("triage calls = %d: a review fix skips triage", len(h.tri.Calls()))
	}
	if h.run(t).ReviewFix {
		t.Error("the fix was made; the flag must clear")
	}
	h.host.SetHead("o/r", "hive/hive-1-one", "sha-2")
	delete(h.ex.Answers, "HIVE-1")
	h.once(t) // the new version is reviewed and passes
	if reviews := h.host.Reviews(); len(reviews) != 2 || !strings.Contains(reviews[1], "Approved") {
		t.Errorf("reviews = %v", reviews)
	}
}

func TestReviewRoundsRunOutToAHuman(t *testing.T) {
	h := newHarness(t)
	h.ws.Changed["HIVE-1"] = true // the coder pushes, so a PR opens
	h.d.Cfg.MaxReviewRounds = 1
	h.setAgents(coder, reviewer)
	h.ex.Answers["HIVE-1"] = `{"verdict":"request_changes","summary":"Still wrong."}`
	h.once(t) // code
	h.once(t) // review 1: back to Ready
	h.once(t) // fix
	h.host.SetHead("o/r", "hive/hive-1-one", "sha-2")
	h.once(t) // review 2: out of rounds
	if h.status(t) != string(tracker.StateNeedsHuman) || !strings.Contains(h.lastComment(t), "human") {
		t.Errorf("status = %s comment = %q", h.status(t), h.lastComment(t))
	}
}

func TestPinOnlyBindsWithinItsRole(t *testing.T) {
	h := newHarness(t)
	h.ws.Changed["HIVE-1"] = true // the coder pushes, so a PR opens
	h.setAgents(coder, reviewer)
	h.tr.Add(tracker.Ticket{Key: "HIVE-1", Summary: "one", Labels: []string{"hive:agent:reviewer"}})
	h.once(t)
	if r := h.run(t); r.Agent != "worker-a/coder" {
		t.Errorf("coded by %q: a pin to the reviewer does not stop the coder", r.Agent)
	}
	h.once(t)
	if reviews := h.host.Reviews(); len(reviews) != 1 || !strings.Contains(reviews[0], "reviewer") {
		t.Errorf("reviews = %v", reviews)
	}
}
