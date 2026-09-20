package dispatch

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/executor"
	exfake "github.com/thomasmeadows/hivedispatch/internal/executor/fake"
	"github.com/thomasmeadows/hivedispatch/internal/githost"
	hostfake "github.com/thomasmeadows/hivedispatch/internal/githost/fake"
	gitfake "github.com/thomasmeadows/hivedispatch/internal/gitops/fake"
	"github.com/thomasmeadows/hivedispatch/internal/schedule"
	"github.com/thomasmeadows/hivedispatch/internal/state"
	"github.com/thomasmeadows/hivedispatch/internal/state/localdir"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
	trfake "github.com/thomasmeadows/hivedispatch/internal/tracker/fake"
	"github.com/thomasmeadows/hivedispatch/internal/triage"
	trifake "github.com/thomasmeadows/hivedispatch/internal/triage/fake"
)

type harness struct {
	tr    *trfake.Tracker
	ex    *exfake.Executor
	tri   *trifake.Triager
	ws    *gitfake.Workspaces
	host  *hostfake.Host
	store *localdir.Store
	logs  *syncBuffer
	d     *Dispatcher
}

// syncBuffer is a bytes.Buffer safe for the heartbeat goroutine to log to.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		tr:    trfake.New(),
		ex:    exfake.New(),
		tri:   trifake.New(),
		ws:    gitfake.New(t.TempDir()),
		host:  hostfake.New(),
		store: localdir.New(t.TempDir()),
		logs:  &syncBuffer{},
	}
	h.tr.Now = func() time.Time { return now }
	h.d = &Dispatcher{
		Cfg: Config{
			AgentID: "worker-a", ClaimTimeout: time.Hour, HeartbeatInterval: time.Hour,
			RunTimeout: time.Second, StepBudget: 50, MaxAttempts: 3,
			Repos: []config.RepoConfig{{Name: "o/r", URL: "git@x:o/r.git", DefaultBranch: "main", Project: "HIVE"}},
		},
		Tracker: h.tr, Triager: h.tri, Executor: h.ex, Workspaces: h.ws, Host: h.host, Store: h.store,
		Now: func() time.Time { return now },
		Log: slog.New(slog.NewTextHandler(h.logs, nil)),
	}
	h.tr.Add(tracker.Ticket{Key: "HIVE-1", Summary: "one"})
	return h
}

func (h *harness) handle(t *testing.T) Outcome {
	t.Helper()
	out, err := h.d.Handle(context.Background(), h.ticket(t))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	return out
}

func (h *harness) ticket(t *testing.T) tracker.Ticket {
	t.Helper()
	tk, err := h.tr.Get(context.Background(), "HIVE-1")
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func (h *harness) run(t *testing.T) *state.Run {
	t.Helper()
	r, err := h.store.Load(context.Background(), "HIVE-1")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (h *harness) assertTransitions(t *testing.T, want ...tracker.State) {
	t.Helper()
	got := h.tr.Transitions("HIVE-1")
	if len(got) != len(want) {
		t.Fatalf("transitions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("transitions = %v, want %v", got, want)
		}
	}
}

func (h *harness) assertLastComment(t *testing.T, wants ...string) {
	t.Helper()
	cs := h.tr.Comments("HIVE-1")
	if len(cs) == 0 {
		t.Fatal("no comments posted")
	}
	last := cs[len(cs)-1]
	for _, w := range wants {
		if !strings.Contains(last, w) {
			t.Errorf("last comment missing %q:\n%s", w, last)
		}
	}
}

func (h *harness) assertReleased(t *testing.T) {
	t.Helper()
	if c := h.ticket(t).Claim; c != nil {
		t.Errorf("claim not released: %+v", c)
	}
}

func TestHandleSkipsUnknownProject(t *testing.T) {
	h := newHarness(t)
	h.tr.Add(tracker.Ticket{Key: "OTHER-1"})
	tk, _ := h.tr.Get(context.Background(), "OTHER-1")
	out, err := h.d.Handle(context.Background(), tk)
	if err != nil || out != OutcomeSkipped {
		t.Fatalf("out=%v err=%v", out, err)
	}
	if got, _ := h.tr.Get(context.Background(), "OTHER-1"); got.Claim != nil {
		t.Error("must not claim a ticket with no repo")
	}
}

func TestHandleSkipsFreshForeignClaim(t *testing.T) {
	h := newHarness(t)
	h.tr.OverwriteClaim("HIVE-1", "worker-b", now.Add(-time.Minute))
	if out := h.handle(t); out != OutcomeSkipped {
		t.Fatalf("out = %v", out)
	}
	if c := h.ticket(t).Claim; c == nil || c.AgentID != "worker-b" {
		t.Errorf("foreign claim disturbed: %+v", c)
	}
	if len(h.ex.Calls()) != 0 {
		t.Error("executor must not run")
	}
}

func TestHandleReclaimsStaleClaim(t *testing.T) {
	h := newHarness(t)
	h.tr.OverwriteClaim("HIVE-1", "worker-b", now.Add(-2*time.Hour))
	if out := h.handle(t); out != OutcomeCompleted {
		t.Fatalf("out = %v", out)
	}
	if len(h.ex.Calls()) != 1 {
		t.Error("executor should have run after reclaiming a stale claim")
	}
	h.assertReleased(t)
}

func TestHandleClaimLost(t *testing.T) {
	h := newHarness(t)
	h.tr.BeforeReadBack = func(key string) { h.tr.OverwriteClaim(key, "worker-b", now.Add(time.Second)) }
	if out := h.handle(t); out != OutcomeClaimLost {
		t.Fatalf("out = %v", out)
	}
	if len(h.ex.Calls()) != 0 || len(h.tr.Comments("HIVE-1")) != 0 {
		t.Error("loser must do nothing")
	}
}

func TestTriageNeedsInfo(t *testing.T) {
	h := newHarness(t)
	h.tri.Decisions["HIVE-1"] = triage.Decision{Kind: triage.KindNeedsInfo, Question: "Which DB?"}
	if out := h.handle(t); out != OutcomeNeedsInfo {
		t.Fatalf("out = %v", out)
	}
	h.assertTransitions(t, tracker.StateNeedsInfo)
	h.assertLastComment(t, Marker, "Which DB?")
	h.assertReleased(t)
	if len(h.ex.Calls()) != 0 {
		t.Error("executor must not run")
	}
	if r := h.run(t); r.LastStatus != string(executor.StatusNeedsInput) || r.QuestionAt.IsZero() || r.Phase != state.PhaseBlocked {
		t.Errorf("run = %+v", r)
	}
}

func TestTriageReject(t *testing.T) {
	h := newHarness(t)
	h.tri.Decisions["HIVE-1"] = triage.Decision{Kind: triage.KindReject, Reason: "cross-cutting"}
	if out := h.handle(t); out != OutcomeRejected {
		t.Fatalf("out = %v", out)
	}
	h.assertTransitions(t, tracker.StateNeedsHuman)
	h.assertLastComment(t, "cross-cutting")
	h.assertReleased(t)
	if r := h.run(t); r.Phase != state.PhaseDone || r.LastStatus != "rejected" {
		t.Errorf("rejected run must be terminal: %+v", r)
	}
}

func TestDispatchCompletedWithPR(t *testing.T) {
	h := newHarness(t)
	h.ws.Changed["HIVE-1"] = true
	h.ex.Results["HIVE-1"] = executor.Result{Status: executor.StatusCompleted, Summary: "added flag", ResumeToken: "sess-1"}
	if out := h.handle(t); out != OutcomeCompleted {
		t.Fatalf("out = %v", out)
	}
	h.assertTransitions(t, tracker.StateInProgress, tracker.StateInReview)
	opened := h.host.Opened()
	if len(opened) != 1 || opened[0].Head != "hive/HIVE-1" || opened[0].Base != "main" || !strings.Contains(opened[0].Title, "HIVE-1") {
		t.Fatalf("opened = %+v", opened)
	}
	h.assertLastComment(t, "pull/1", "added flag")
	r := h.run(t)
	if r.Phase != state.PhaseDone || r.PRURL == "" || r.Attempts != 1 || r.ResumeToken != "sess-1" || r.Branch != "hive/HIVE-1" {
		t.Errorf("run = %+v", r)
	}
	calls := h.ex.Calls()
	if len(calls) != 1 || calls[0].Prompt != "do the thing" || calls[0].StepBudget != 50 || calls[0].Workspace == "" {
		t.Errorf("task = %+v", calls)
	}
	if tri := h.tri.Calls(); len(tri) != 1 || tri[0].RepoPath == "" {
		t.Errorf("triage must receive the workspace path: %+v", tri)
	}
	h.assertReleased(t)
}

func TestDispatchCompletedNoChanges(t *testing.T) {
	h := newHarness(t)
	if out := h.handle(t); out != OutcomeCompleted {
		t.Fatalf("out = %v", out)
	}
	h.assertTransitions(t, tracker.StateInProgress, tracker.StateInReview)
	if len(h.host.Opened()) != 0 {
		t.Error("no PR without changes")
	}
	h.assertLastComment(t, "no code changes", exfake.DefaultSummary)
	h.assertReleased(t)
}

func TestExecutorNeedsInput(t *testing.T) {
	h := newHarness(t)
	h.ex.Results["HIVE-1"] = executor.Result{Status: executor.StatusNeedsInput, Question: "Postgres or SQLite?", ResumeToken: "sess-2"}
	if out := h.handle(t); out != OutcomeNeedsInput {
		t.Fatalf("out = %v", out)
	}
	h.assertTransitions(t, tracker.StateInProgress, tracker.StateNeedsInfo)
	h.assertLastComment(t, "Postgres or SQLite?")
	r := h.run(t)
	if r.ResumeToken != "sess-2" || r.QuestionAt.IsZero() || r.LastStatus != string(executor.StatusNeedsInput) {
		t.Errorf("run = %+v", r)
	}
	h.assertReleased(t)
}

func TestExecutorFailedRetries(t *testing.T) {
	for _, cause := range []executor.Cause{executor.CauseBudget, executor.CauseStepBudget, executor.CauseError, executor.CauseKilled} {
		t.Run(string(cause), func(t *testing.T) {
			h := newHarness(t)
			h.ws.Changed["HIVE-1"] = true
			h.ex.Results["HIVE-1"] = executor.Result{Status: executor.StatusFailed, StopCause: cause, Summary: "partial"}
			if out := h.handle(t); out != OutcomeFailed {
				t.Fatalf("out = %v", out)
			}
			h.assertTransitions(t, tracker.StateInProgress, tracker.StateReady)
			h.assertLastComment(t, cause.Describe(), "1 of 3", "hive/HIVE-1", "partial")
			if len(h.host.Opened()) != 0 {
				t.Error("never open a PR from a failed run")
			}
			r := h.run(t)
			if r.Attempts != 1 || r.StopCause != string(cause) || r.Phase != state.PhasePushed {
				t.Errorf("run = %+v", r)
			}
			h.assertReleased(t)
		})
	}
}

func TestExecutorFailedExhaustsAttempts(t *testing.T) {
	h := newHarness(t)
	if err := h.store.Save(context.Background(), &state.Run{Ticket: "HIVE-1", Attempts: 2}); err != nil {
		t.Fatal(err)
	}
	h.ex.Results["HIVE-1"] = executor.Result{Status: executor.StatusFailed, StopCause: executor.CauseError}
	if out := h.handle(t); out != OutcomeFailed {
		t.Fatalf("out = %v", out)
	}
	h.assertTransitions(t, tracker.StateInProgress, tracker.StateNeedsHuman)
	h.assertLastComment(t, "3 of 3", "human")
}

func TestExecutorErrorIsFailure(t *testing.T) {
	h := newHarness(t)
	h.ex.Err["HIVE-1"] = context.Canceled
	if out := h.handle(t); out != OutcomeFailed {
		t.Fatalf("out = %v", out)
	}
	h.assertLastComment(t, executor.CauseError.Describe())
	if r := h.run(t); r.StopCause != string(executor.CauseError) {
		t.Errorf("run = %+v", r)
	}
}

func TestExecutorTimeout(t *testing.T) {
	h := newHarness(t)
	h.d.Cfg.RunTimeout = 20 * time.Millisecond
	h.ex.Block["HIVE-1"] = true
	if out := h.handle(t); out != OutcomeFailed {
		t.Fatalf("out = %v", out)
	}
	h.assertLastComment(t, executor.CauseTimeout.Describe())
	h.assertReleased(t)
}

func TestPROpenFailureReturnsToReady(t *testing.T) {
	h := newHarness(t)
	h.ws.Changed["HIVE-1"] = true
	h.host.Err = context.DeadlineExceeded
	out, err := h.d.Handle(context.Background(), h.ticket(t))
	if err == nil || out != OutcomeFailed {
		t.Fatalf("out=%v err=%v", out, err)
	}
	h.assertTransitions(t, tracker.StateInProgress, tracker.StateReady)
	h.assertLastComment(t, "opening the PR failed", "hive/HIVE-1")
	h.assertReleased(t)
}

func TestTriageErrorLeavesTicketReady(t *testing.T) {
	h := newHarness(t)
	h.tri.Err = context.DeadlineExceeded
	if _, err := h.d.Handle(context.Background(), h.ticket(t)); err == nil {
		t.Fatal("expected error")
	}
	h.assertTransitions(t)
	h.assertReleased(t)
}

func TestCompletedWithoutPRHostStillReportsBranch(t *testing.T) {
	h := newHarness(t)
	h.ws.Changed["HIVE-1"] = true
	h.d.Host = nonePRHost{}
	if out := h.handle(t); out != OutcomeCompleted {
		t.Fatalf("out = %v", out)
	}
	h.assertTransitions(t, tracker.StateInProgress, tracker.StateInReview)
	h.assertLastComment(t, "hive/HIVE-1", "could not open a PR")
	if r := h.run(t); r.PRURL != "" || r.Phase != state.PhaseDone {
		t.Errorf("run = %+v", r)
	}
}

// assertLogged checks that one log line carries every fragment, in order of
// the lines written. Fragments are matched against the slog text format.
func (h *harness) assertLogged(t *testing.T, fragments ...string) {
	t.Helper()
	for _, line := range strings.Split(h.logs.String(), "\n") {
		ok := true
		for _, f := range fragments {
			if !strings.Contains(line, f) {
				ok = false
				break
			}
		}
		if ok {
			return
		}
	}
	t.Errorf("no log line contains all of %q:\n%s", fragments, h.logs.String())
}

func (h *harness) assertNotLogged(t *testing.T, fragment string) {
	t.Helper()
	if strings.Contains(h.logs.String(), fragment) {
		t.Errorf("log should not contain %q:\n%s", fragment, h.logs.String())
	}
}

func TestLogsPickedUpStartedAndFinished(t *testing.T) {
	h := newHarness(t)
	h.ws.Changed["HIVE-1"] = true
	h.ex.Results["HIVE-1"] = executor.Result{Status: executor.StatusCompleted, Summary: "added flag"}
	h.handle(t)
	h.assertLogged(t, `msg="picked up ticket"`, "ticket=HIVE-1", "summary=one")
	h.assertLogged(t, `msg="work started"`, "ticket=HIVE-1", "attempt=1/3")
	h.assertLogged(t, `msg="work finished"`, "ticket=HIVE-1", `result="added flag"`, "pull/1")
}

func TestLogsNeedsInfoFromTriage(t *testing.T) {
	h := newHarness(t)
	h.tri.Decisions["HIVE-1"] = triage.Decision{Kind: triage.KindNeedsInfo, Question: "Which DB?"}
	h.handle(t)
	h.assertLogged(t, `msg="picked up ticket"`, "ticket=HIVE-1")
	h.assertLogged(t, `msg="work needs info"`, "ticket=HIVE-1", `result="Which DB?"`)
	h.assertNotLogged(t, `msg="work started"`)
}

func TestLogsNeedsInfoFromExecutor(t *testing.T) {
	h := newHarness(t)
	h.ex.Results["HIVE-1"] = executor.Result{Status: executor.StatusNeedsInput, Question: "Postgres or SQLite?"}
	h.handle(t)
	h.assertLogged(t, `msg="work started"`, "ticket=HIVE-1")
	h.assertLogged(t, `msg="work needs info"`, "ticket=HIVE-1", `result="Postgres or SQLite?"`)
}

func TestLogsNeedsHumanOnReject(t *testing.T) {
	h := newHarness(t)
	h.tri.Decisions["HIVE-1"] = triage.Decision{Kind: triage.KindReject, Reason: "cross-cutting"}
	h.handle(t)
	h.assertLogged(t, `msg="work needs human"`, "ticket=HIVE-1", "result=cross-cutting")
}

func TestLogsFailedThenNeedsHumanWhenExhausted(t *testing.T) {
	h := newHarness(t)
	h.ex.Results["HIVE-1"] = executor.Result{Status: executor.StatusFailed, StopCause: executor.CauseBudget, Summary: "partial"}
	h.handle(t)
	h.assertLogged(t, `msg="work failed"`, "ticket=HIVE-1", "cause=budget", "result=partial", "attempt=1/3")
	h.assertNotLogged(t, `msg="work needs human"`)

	h = newHarness(t)
	if err := h.store.Save(context.Background(), &state.Run{Ticket: "HIVE-1", Attempts: 2}); err != nil {
		t.Fatal(err)
	}
	h.ex.Results["HIVE-1"] = executor.Result{Status: executor.StatusFailed, StopCause: executor.CauseError, Summary: "boom"}
	h.handle(t)
	h.assertLogged(t, `msg="work needs human"`, "ticket=HIVE-1", "cause=error", "result=boom", "attempt=3/3")
}

func TestSkippedTicketsAreNotLoggedAtInfo(t *testing.T) {
	h := newHarness(t)
	h.tr.OverwriteClaim("HIVE-1", "worker-b", now.Add(-time.Minute))
	if _, err := h.d.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.assertNotLogged(t, "level=INFO")
}

func TestOncePollsAndReportsPollTime(t *testing.T) {
	h := newHarness(t)
	var polled []time.Time
	h.d.Polled = func(at time.Time) { polled = append(polled, at) }
	if _, err := h.d.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(polled) != 1 || !polled[0].Equal(now) {
		t.Fatalf("polled = %v, want [%v]", polled, now)
	}
}

func TestOnceOutsideWindowDoesNotReportPoll(t *testing.T) {
	h := newHarness(t)
	sched, err := schedule.Parse(config.RunWindows{Timezone: "UTC", Windows: []config.WindowConfig{{Start: "01:00", End: "02:00"}}})
	if err != nil {
		t.Fatal(err)
	}
	h.d.Schedule = sched
	h.d.Now = func() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) }
	h.d.Polled = func(time.Time) { t.Error("Polled must not fire when the window is closed") }
	if _, err := h.d.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// nonePRHost mirrors githost/none: pushes happen, PRs do not.
type nonePRHost struct{}

func (nonePRHost) FindPR(context.Context, string, string) (*githost.PR, error) { return nil, nil }
func (nonePRHost) OpenPR(context.Context, string, githost.Request) (*githost.PR, error) {
	return nil, nil
}
