# Phase 2: Dispatcher Loop With Fakes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A single-worker dispatcher that polls, claims with read-back, runs an executor with heartbeat/timeout/step budget, and leaves every ticket in a documented state with an explanatory comment — verified with a two-worker race test and a manual run of two `hivedispatch run` processes against one real Jira ticket, using a fake executor.

**Architecture:** `internal/dispatch` is the deterministic control loop. It depends only on interfaces: `tracker.Tracker` (Phase 1), plus five new ones defined here with fakes — `executor.Executor`, `triage.Triager`, `gitops.Workspaces`, `githost.GitHost`, `state.RunStore`. `internal/schedule` evaluates run windows. `internal/prompt` renders tickets into prompts. Phases 3–5 replace the fakes with real implementations without touching the dispatcher.

**Tech Stack:** Go 1.27 stdlib (`context`, `log/slog`, `sync`, `os/signal`, `encoding/json`), `gopkg.in/yaml.v3`.

**Spec:** `docs/design-spec.md` (Lifecycle and claiming, Failure handling, Budget and scheduling, Build order step 2) and `docs/decisions.md`.

## Global Constraints

- Module path `github.com/thomasmeadows/hivedispatch`; no new external dependencies.
- Every dispatcher comment starts with the marker `[HiveDispatch]` so the dispatcher can tell its own comments from human replies.
- Every terminal path posts a comment and releases the claim. No ticket is ever left claimed by a finished run.
- The dispatcher never inspects git artifacts to decide an outcome; the executor's `Result` decides, artifacts only appear in the comment text.
- `go vet ./... && go test -race ./... && golangci-lint run` green before every commit.
- Commit messages end with `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.

---

## File Structure

```
internal/executor/executor.go          Executor, Task, Footprint, Result, Status, Cause, Cause.Describe
internal/executor/fake/fake.go         scripted results, Block for timeout/kill tests, call recording
internal/executor/fake/fake_test.go
internal/triage/triage.go              Triager, Input, Decision, Kind
internal/triage/passthrough/passthrough.go  always-dispatch triager using prompt.Render
internal/triage/fake/fake.go           scripted decisions
internal/prompt/prompt.go              Render(ticket, repo, branch) and RenderResume(ticket, since)
internal/prompt/prompt_test.go
internal/gitops/gitops.go              Workspace, Workspaces interface, BranchName
internal/gitops/fake/fake.go           temp-dir workspaces, Changed map drives Finalize
internal/githost/githost.go            PR, Request, GitHost interface
internal/githost/fake/fake.go          in-memory PRs
internal/state/state.go                Run, Phase, LogEntry, RunStore
internal/state/localdir/localdir.go    runs/<KEY>.json, logs/<KEY>/events.jsonl, logs/<KEY>/<name>.log
internal/state/localdir/localdir_test.go
internal/schedule/schedule.go          Parse(config.RunWindows), Open(t)
internal/schedule/schedule_test.go
internal/config/config.go              + RunTimeout, StepBudget, MaxAttempts, PollJitter, RunWindows
internal/dispatch/dispatch.go          Dispatcher, Config, Outcome, Once, Run
internal/dispatch/handle.go            Handle: claim → triage/resume → execute → report
internal/dispatch/report.go            comment templates
internal/dispatch/report_test.go
internal/dispatch/dispatch_test.go     harness + behaviour tests
internal/dispatch/race_test.go         two-worker race, stale reclaim, heartbeat loss
internal/tracker/fake/fake.go          + exported OverwriteClaim, PollCount
cmd/hivedispatch/main.go               + run [-once]
```

---

### Task 1: Executor contract and fake

**Files:**
- Create: `internal/executor/executor.go`, `internal/executor/fake/fake.go`, `internal/executor/fake/fake_test.go`

**Interfaces:**
- Produces:

```go
type Status string   // StatusCompleted "completed" | StatusNeedsInput "needs_input" | StatusFailed "failed"
type Cause string    // CauseNone "" | CauseBudget "budget" | CauseTimeout "timeout" | CauseStepBudget "step_budget" | CauseOverlap "overlap" | CauseError "error" | CauseKilled "killed"
func (c Cause) Describe() string
type Task struct { TicketKey, Prompt, Workspace, ResumeToken string; StepBudget int }
type Footprint struct { Files []string }
type Result struct { Status Status; StopCause Cause; Summary, Question, ResumeToken string; ChangedFiles []string; Log string }
type Executor interface { Name() string; Plan(ctx, Task) (Footprint, error); Run(ctx, Task) (Result, error) }
// fake
func New() *Executor                      // Default = Completed, Summary "Fake executor: would have worked on this ticket."
Results map[string]executor.Result        // by ticket key
Block   map[string]bool                   // Run waits for ctx.Done, returns Failed with Timeout or Killed
Err     map[string]error                  // Run returns this error
func (e *Executor) Calls() []executor.Task
```

- [ ] **Step 1: Write the failing test**

`internal/executor/fake/fake_test.go`:

```go
package fake

import (
	"context"
	"errors"
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/executor/...`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Write `internal/executor/executor.go`**

```go
// Package executor defines the boundary between the orchestrator and a
// coding-agent CLI.
//
// The orchestrator guarantees the adapter a prepared workspace, a rendered
// prompt, and bounded resources. The adapter guarantees back a terminal
// status and a human-readable summary, always — even on failure.
package executor

import "context"

// Status is the terminal status of a run.
type Status string

// Terminal statuses.
const (
	StatusCompleted  Status = "completed"
	StatusNeedsInput Status = "needs_input"
	StatusFailed     Status = "failed"
)

// Cause records why a run stopped.
type Cause string

// Stop causes. CauseNone means the run reached its own conclusion.
const (
	CauseNone       Cause = ""
	CauseBudget     Cause = "budget"
	CauseTimeout    Cause = "timeout"
	CauseStepBudget Cause = "step_budget"
	CauseOverlap    Cause = "overlap"
	CauseError      Cause = "error"
	CauseKilled     Cause = "killed"
)

// Describe returns a short human explanation for ticket comments.
func (c Cause) Describe() string {
	switch c {
	case CauseBudget:
		return "provider budget or rate limit reached"
	case CauseTimeout:
		return "wall-clock timeout reached"
	case CauseStepBudget:
		return "step budget exhausted (the ticket may be underspecified)"
	case CauseOverlap:
		return "file overlap with another in-flight ticket"
	case CauseError:
		return "the executor reported an error"
	case CauseKilled:
		return "the run was interrupted"
	}
	return ""
}

// Task is one unit of work handed to an executor.
type Task struct {
	TicketKey   string
	Prompt      string // ticket body + comment thread, rendered
	Workspace   string // path to the prepared worktree
	ResumeToken string // opaque; stored, never interpreted
	StepBudget  int
}

// Footprint is the set of files a planned run expects to touch.
type Footprint struct {
	Files []string
}

// Result is what an executor returns. Status is always set.
type Result struct {
	Status       Status
	StopCause    Cause
	Summary      string // posted to the ticket
	Question     string // when NeedsInput
	ResumeToken  string // opaque; persisted for the next run
	ChangedFiles []string
	Log          string
}

// Executor wraps one coding-agent CLI. Timeouts ride on the context.
type Executor interface {
	Name() string
	Plan(ctx context.Context, t Task) (Footprint, error)
	Run(ctx context.Context, t Task) (Result, error)
}
```

- [ ] **Step 4: Write `internal/executor/fake/fake.go`**

```go
// Package fake is a scripted executor.Executor for tests and for proving
// the coordination loop before a real agent is wired in.
package fake

import (
	"context"
	"errors"
	"sync"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
)

// DefaultSummary is what the fake reports when nothing is scripted.
const DefaultSummary = "Fake executor: would have worked on this ticket."

// Executor returns scripted results keyed by ticket.
type Executor struct {
	Results map[string]executor.Result
	Block   map[string]bool
	Err     map[string]error
	Default executor.Result

	mu    sync.Mutex
	calls []executor.Task
}

var _ executor.Executor = (*Executor)(nil)

// New returns a fake whose default result is a successful no-change run.
func New() *Executor {
	return &Executor{
		Results: map[string]executor.Result{},
		Block:   map[string]bool{},
		Err:     map[string]error{},
		Default: executor.Result{Status: executor.StatusCompleted, Summary: DefaultSummary},
	}
}

// Name implements executor.Executor.
func (e *Executor) Name() string { return "fake" }

// Plan implements executor.Executor with an empty footprint.
func (e *Executor) Plan(_ context.Context, _ executor.Task) (executor.Footprint, error) {
	return executor.Footprint{}, nil
}

// Run records the task and returns the scripted outcome.
func (e *Executor) Run(ctx context.Context, t executor.Task) (executor.Result, error) {
	e.mu.Lock()
	e.calls = append(e.calls, t)
	block := e.Block[t.TicketKey]
	err := e.Err[t.TicketKey]
	res, ok := e.Results[t.TicketKey]
	if !ok {
		res = e.Default
	}
	e.mu.Unlock()

	if err != nil {
		return executor.Result{}, err
	}
	if block {
		<-ctx.Done()
		cause := executor.CauseKilled
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			cause = executor.CauseTimeout
		}
		return executor.Result{Status: executor.StatusFailed, StopCause: cause, Summary: "interrupted"}, nil
	}
	return res, nil
}

// Calls returns every task Run has received.
func (e *Executor) Calls() []executor.Task {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]executor.Task(nil), e.calls...)
}
```

- [ ] **Step 5: Run tests, lint, commit**

Run: `go vet ./... && go test -race ./internal/executor/... && ~/go/bin/golangci-lint run ./...`
Expected: PASS, 0 issues.

```bash
git add internal/executor
git commit -m "feat(executor): Executor contract and scripted fake

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 2: Prompt rendering and triage contract

**Files:**
- Create: `internal/prompt/prompt.go`, `internal/prompt/prompt_test.go`, `internal/triage/triage.go`, `internal/triage/passthrough/passthrough.go`, `internal/triage/fake/fake.go`

**Interfaces:**
- Consumes: `tracker.Ticket`, `config.RepoConfig`.
- Produces:

```go
// prompt
func Render(t tracker.Ticket, repo config.RepoConfig, branch string) string
func RenderResume(t tracker.Ticket, since time.Time, isOurs func(tracker.Comment) bool) string
// triage
type Kind string   // KindDispatch "dispatch" | KindNeedsInfo "needs_info" | KindReject "reject"
type Decision struct { Kind Kind; Reason, Prompt, Question string; Complexity int }
type Input struct { Ticket tracker.Ticket; Repo config.RepoConfig; Branch, RepoPath string; Attempts int; LastStopCause string }
type Triager interface { Decide(ctx, Input) (Decision, error) }
// passthrough.Triager{} — always KindDispatch with prompt.Render
// fake: Decisions map[string]triage.Decision; Default triage.Decision; Err error; Calls() []triage.Input
```

- [ ] **Step 1: Write the failing test**

`internal/prompt/prompt_test.go`:

```go
package prompt

import (
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

var base = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

func ticket() tracker.Ticket {
	return tracker.Ticket{
		Key: "HIVE-7", Summary: "Add --version", Description: "Print the version.",
		URL: "https://x/browse/HIVE-7",
		Comments: []tracker.Comment{
			{Author: "Thomas", Body: "Also bump the changelog.", Created: base.Add(-2 * time.Hour)},
			{Author: "Thomas", Body: "[HiveDispatch] Which format?", Created: base.Add(-time.Hour)},
			{Author: "Thomas", Body: "semver please", Created: base.Add(-30 * time.Minute)},
		},
	}
}

func TestRenderIncludesEverything(t *testing.T) {
	got := Render(ticket(), config.RepoConfig{Name: "o/r", DefaultBranch: "main"}, "hive/HIVE-7")
	for _, want := range []string{"HIVE-7", "Add --version", "Print the version.", "Also bump the changelog.", "hive/HIVE-7", "o/r", "HIVE_NEEDS_INPUT:"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
}

func TestRenderResumeOnlyNewHumanComments(t *testing.T) {
	isOurs := func(c tracker.Comment) bool { return strings.HasPrefix(c.Body, "[HiveDispatch]") }
	got := RenderResume(ticket(), base.Add(-time.Hour), isOurs)
	if !strings.Contains(got, "semver please") {
		t.Errorf("missing reply:\n%s", got)
	}
	if strings.Contains(got, "Also bump") || strings.Contains(got, "Which format?") {
		t.Errorf("included old or own comments:\n%s", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/prompt/`
Expected: FAIL — `undefined: Render`.

- [ ] **Step 3: Write `internal/prompt/prompt.go`**

```go
// Package prompt renders tickets into executor prompts.
package prompt

import (
	"fmt"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// NeedsInputMarker is the line prefix an executor uses to ask a question.
const NeedsInputMarker = "HIVE_NEEDS_INPUT:"

// Render builds the initial prompt for a ticket.
func Render(t tracker.Ticket, repo config.RepoConfig, branch string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Ticket %s: %s\n", t.Key, t.Summary)
	if t.URL != "" {
		fmt.Fprintf(&sb, "%s\n", t.URL)
	}
	sb.WriteString("\n## Description\n\n")
	if strings.TrimSpace(t.Description) == "" {
		sb.WriteString("(no description)\n")
	} else {
		sb.WriteString(t.Description + "\n")
	}
	if len(t.Comments) > 0 {
		sb.WriteString("\n## Discussion\n\n")
		writeComments(&sb, t.Comments)
	}
	sb.WriteString(instructions(repo, branch))
	return sb.String()
}

// RenderResume builds the prompt for continuing after a human replied.
// Only comments created after since that are not the orchestrator's own
// are included.
func RenderResume(t tracker.Ticket, since time.Time, isOurs func(tracker.Comment) bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Resuming ticket %s: %s\n\n", t.Key, t.Summary)
	sb.WriteString("You previously asked for input. New replies:\n\n")
	var fresh []tracker.Comment
	for _, c := range t.Comments {
		if c.Created.After(since) && !isOurs(c) {
			fresh = append(fresh, c)
		}
	}
	writeComments(&sb, fresh)
	sb.WriteString("\nContinue the work with this information. The same rules as before apply.\n")
	return sb.String()
}

func writeComments(sb *strings.Builder, cs []tracker.Comment) {
	for _, c := range cs {
		fmt.Fprintf(sb, "- [%s] %s: %s\n", c.Created.UTC().Format("2006-01-02 15:04"), c.Author, strings.ReplaceAll(c.Body, "\n", "\n  "))
	}
}

func instructions(repo config.RepoConfig, branch string) string {
	return fmt.Sprintf(`
## Instructions

You are working in the repository %s on branch %s (based on %s).
Implement what the ticket asks and nothing more.
Commit as you go with clear messages; do not leave work uncommitted.
Run the project's tests before you finish.
Do not merge, do not push, and do not touch other branches.
If you cannot proceed without information only a human has, stop and end your
reply with a single line starting with %s followed by the question.
When you are done, end your reply with a short summary of what changed.
`, repo.Name, branch, repo.DefaultBranch, NeedsInputMarker)
}
```

- [ ] **Step 4: Write `internal/triage/triage.go`**

```go
// Package triage defines the decision layer in front of dispatch.
//
// A Triager can only decide, never act: it returns a Decision and the
// dispatcher does the rest.
package triage

import (
	"context"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// Kind is the triage verdict.
type Kind string

// Verdicts.
const (
	KindDispatch  Kind = "dispatch"
	KindNeedsInfo Kind = "needs_info"
	KindReject    Kind = "reject"
)

// Decision is what a Triager returns.
type Decision struct {
	Kind       Kind
	Reason     string // why; posted on reject
	Prompt     string // rendered prompt when dispatching
	Question   string // posted when needs_info
	Complexity int    // 0 = unknown
}

// Input is everything a Triager may consider.
type Input struct {
	Ticket        tracker.Ticket
	Repo          config.RepoConfig
	Branch        string
	RepoPath      string // read-only checkout, may be empty
	Attempts      int
	LastStopCause string
}

// Triager decides whether and how to attempt a ticket.
type Triager interface {
	Decide(ctx context.Context, in Input) (Decision, error)
}
```

- [ ] **Step 5: Write `internal/triage/passthrough/passthrough.go`**

```go
// Package passthrough is a Triager that dispatches every ticket. It is the
// rules-based baseline the model-backed triager is measured against.
package passthrough

import (
	"context"

	"github.com/thomasmeadows/hivedispatch/internal/prompt"
	"github.com/thomasmeadows/hivedispatch/internal/triage"
)

// Triager dispatches unconditionally.
type Triager struct{}

var _ triage.Triager = Triager{}

// Decide implements triage.Triager.
func (Triager) Decide(_ context.Context, in triage.Input) (triage.Decision, error) {
	return triage.Decision{
		Kind:   triage.KindDispatch,
		Reason: "passthrough",
		Prompt: prompt.Render(in.Ticket, in.Repo, in.Branch),
	}, nil
}
```

- [ ] **Step 6: Write `internal/triage/fake/fake.go`**

```go
// Package fake is a scripted triage.Triager for tests.
package fake

import (
	"context"
	"sync"

	"github.com/thomasmeadows/hivedispatch/internal/triage"
)

// Triager returns scripted decisions keyed by ticket.
type Triager struct {
	Decisions map[string]triage.Decision
	Default   triage.Decision
	Err       error

	mu    sync.Mutex
	calls []triage.Input
}

var _ triage.Triager = (*Triager)(nil)

// New returns a fake that dispatches by default with a fixed prompt.
func New() *Triager {
	return &Triager{
		Decisions: map[string]triage.Decision{},
		Default:   triage.Decision{Kind: triage.KindDispatch, Reason: "fake", Prompt: "do the thing"},
	}
}

// Decide records the input and returns the scripted decision.
func (f *Triager) Decide(_ context.Context, in triage.Input) (triage.Decision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	if f.Err != nil {
		return triage.Decision{}, f.Err
	}
	if d, ok := f.Decisions[in.Ticket.Key]; ok {
		return d, nil
	}
	return f.Default, nil
}

// Calls returns every input Decide has received.
func (f *Triager) Calls() []triage.Input {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]triage.Input(nil), f.calls...)
}
```

- [ ] **Step 7: Run tests, lint, commit**

Run: `go vet ./... && go test -race ./internal/prompt/ ./internal/triage/... && ~/go/bin/golangci-lint run ./...`
Expected: PASS, 0 issues.

```bash
git add internal/prompt internal/triage
git commit -m "feat(triage): Triager contract, passthrough and fake; prompt rendering

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 3: Workspace and git-host contracts with fakes

**Files:**
- Create: `internal/gitops/gitops.go`, `internal/gitops/fake/fake.go`, `internal/githost/githost.go`, `internal/githost/fake/fake.go`, `internal/githost/fake/fake_test.go`

**Interfaces:**
- Produces:

```go
// gitops
type Workspace struct { Path, Branch string }
type Workspaces interface {
    Prepare(ctx, repo config.RepoConfig, ticketKey string) (Workspace, error)
    Finalize(ctx, ws Workspace, message string) (pushed bool, err error)   // safety-commit dirty tree; push if branch has commits
}
func BranchName(ticketKey string) string   // "hive/" + key
// gitops/fake: New(root string) *Workspaces; Changed map[string]bool (by ticket key) → Finalize returns it; Finalized() []string
// githost
type PR struct { URL string; Number int; Draft bool }
type Request struct { Title, Body, Head, Base string; Draft bool }
type GitHost interface { FindPR(ctx, repo, head string) (*PR, error); OpenPR(ctx, repo string, req Request) (*PR, error) }
// githost/fake: New() *Host; Opened() []Request; Err error
```

- [ ] **Step 1: Write the failing test**

`internal/githost/fake/fake_test.go`:

```go
package fake

import (
	"context"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/githost"
)

func TestOpenThenFind(t *testing.T) {
	h := New()
	ctx := context.Background()
	if pr, err := h.FindPR(ctx, "o/r", "hive/HIVE-1"); err != nil || pr != nil {
		t.Fatalf("find before open: pr=%v err=%v", pr, err)
	}
	pr, err := h.OpenPR(ctx, "o/r", githost.Request{Title: "t", Head: "hive/HIVE-1", Base: "main"})
	if err != nil || pr == nil || pr.URL == "" || pr.Number != 1 {
		t.Fatalf("open: pr=%+v err=%v", pr, err)
	}
	again, _ := h.FindPR(ctx, "o/r", "hive/HIVE-1")
	if again == nil || again.URL != pr.URL {
		t.Errorf("find after open = %+v", again)
	}
	if len(h.Opened()) != 1 {
		t.Errorf("opened = %+v", h.Opened())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/githost/...`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Write `internal/gitops/gitops.go`**

```go
// Package gitops defines how the dispatcher obtains and finalizes a
// per-ticket workspace. The real implementation (Phase 3) uses git
// worktrees; the fake uses plain directories.
package gitops

import (
	"context"

	"github.com/thomasmeadows/hivedispatch/internal/config"
)

// Workspace is a prepared checkout for one ticket.
type Workspace struct {
	Path   string
	Branch string
}

// Workspaces prepares and finalizes ticket workspaces.
type Workspaces interface {
	// Prepare returns a workspace on the ticket's branch, creating it from
	// the repo's default branch if needed. Calling it again for the same
	// ticket returns the same workspace (resume).
	Prepare(ctx context.Context, repo config.RepoConfig, ticketKey string) (Workspace, error)
	// Finalize commits any uncommitted changes with message and pushes the
	// branch if it has commits beyond the default branch. It reports
	// whether anything was pushed.
	Finalize(ctx context.Context, ws Workspace, message string) (pushed bool, err error)
}

// BranchName derives the work branch from the ticket key.
func BranchName(ticketKey string) string {
	return "hive/" + ticketKey
}
```

- [ ] **Step 4: Write `internal/gitops/fake/fake.go`**

```go
// Package fake is a directory-backed gitops.Workspaces for tests.
package fake

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/gitops"
)

// Workspaces creates one directory per ticket under Root.
type Workspaces struct {
	Root    string
	Changed map[string]bool // ticket key → Finalize reports pushed

	mu        sync.Mutex
	finalized []string
}

var _ gitops.Workspaces = (*Workspaces)(nil)

// New returns a fake rooted at root.
func New(root string) *Workspaces {
	return &Workspaces{Root: root, Changed: map[string]bool{}}
}

// Prepare creates <root>/<key> and returns it.
func (w *Workspaces) Prepare(_ context.Context, _ config.RepoConfig, key string) (gitops.Workspace, error) {
	dir := filepath.Join(w.Root, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return gitops.Workspace{}, err
	}
	return gitops.Workspace{Path: dir, Branch: gitops.BranchName(key)}, nil
}

// Finalize records the call and reports Changed for the ticket.
func (w *Workspaces) Finalize(_ context.Context, ws gitops.Workspace, message string) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	key := strings.TrimPrefix(ws.Branch, "hive/")
	w.finalized = append(w.finalized, key+": "+message)
	return w.Changed[key], nil
}

// Finalized returns "<key>: <message>" for every Finalize call.
func (w *Workspaces) Finalized() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.finalized...)
}
```

- [ ] **Step 5: Write `internal/githost/githost.go`**

```go
// Package githost defines pull-request operations on a git hosting service.
package githost

import "context"

// PR is an open pull request.
type PR struct {
	URL    string
	Number int
	Draft  bool
}

// Request describes a pull request to open.
type Request struct {
	Title string
	Body  string
	Head  string // branch with the changes
	Base  string // target branch
	Draft bool
}

// GitHost finds and opens pull requests. repo is "owner/name".
type GitHost interface {
	// FindPR returns the open PR whose head is head, or nil, nil.
	FindPR(ctx context.Context, repo, head string) (*PR, error)
	OpenPR(ctx context.Context, repo string, req Request) (*PR, error)
}
```

- [ ] **Step 6: Write `internal/githost/fake/fake.go`**

```go
// Package fake is an in-memory githost.GitHost for tests.
package fake

import (
	"context"
	"fmt"
	"sync"

	"github.com/thomasmeadows/hivedispatch/internal/githost"
)

// Host stores PRs in memory.
type Host struct {
	Err error // returned by every call when set

	mu     sync.Mutex
	prs    map[string]*githost.PR // repo + "#" + head
	opened []githost.Request
}

var _ githost.GitHost = (*Host)(nil)

// New returns an empty host.
func New() *Host {
	return &Host{prs: map[string]*githost.PR{}}
}

// FindPR implements githost.GitHost.
func (h *Host) FindPR(_ context.Context, repo, head string) (*githost.PR, error) {
	if h.Err != nil {
		return nil, h.Err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if pr, ok := h.prs[repo+"#"+head]; ok {
		c := *pr
		return &c, nil
	}
	return nil, nil
}

// OpenPR implements githost.GitHost.
func (h *Host) OpenPR(_ context.Context, repo string, req githost.Request) (*githost.PR, error) {
	if h.Err != nil {
		return nil, h.Err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.opened = append(h.opened, req)
	n := len(h.prs) + 1
	pr := &githost.PR{URL: fmt.Sprintf("https://example.test/%s/pull/%d", repo, n), Number: n, Draft: req.Draft}
	h.prs[repo+"#"+req.Head] = pr
	c := *pr
	return &c, nil
}

// Opened returns every request OpenPR has received.
func (h *Host) Opened() []githost.Request {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]githost.Request(nil), h.opened...)
}
```

- [ ] **Step 7: Run tests, lint, commit**

Run: `go vet ./... && go test -race ./internal/gitops/... ./internal/githost/... && ~/go/bin/golangci-lint run ./...`
Expected: PASS, 0 issues.

```bash
git add internal/gitops internal/githost
git commit -m "feat: Workspaces and GitHost contracts with fakes

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 4: Run state and local-directory store

**Files:**
- Create: `internal/state/state.go`, `internal/state/localdir/localdir.go`, `internal/state/localdir/localdir_test.go`

**Interfaces:**
- Produces:

```go
type Phase string  // PhaseClaimed "claimed" | PhasePlanned "planned" | PhaseWorking "working" | PhasePushed "pushed" | PhasePROpened "pr_opened" | PhaseDone "done" | PhaseBlocked "blocked"
type Run struct {
    Ticket, Agent, Branch string; Attempts int; Phase Phase
    LastStatus, StopCause, ResumeToken, PRURL string
    QuestionAt, UpdatedAt time.Time
}
type LogEntry struct { Time time.Time; Agent, Event, Phase, Cause, Message string }
type RunStore interface {
    Load(ctx, key string) (*Run, error)            // &Run{Ticket: key} when none
    Save(ctx, run *Run) error                       // sets UpdatedAt
    AppendLog(ctx, key string, e LogEntry) error
    WriteLog(ctx, key, name, content string) (path string, err error)
}
// localdir: New(dir string) *Store
```

- [ ] **Step 1: Write the failing test**

`internal/state/localdir/localdir_test.go`:

```go
package localdir

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/state"
)

func TestLoadMissingReturnsEmptyRun(t *testing.T) {
	s := New(t.TempDir())
	run, err := s.Load(context.Background(), "HIVE-1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Ticket != "HIVE-1" || run.Attempts != 0 || run.Phase != "" {
		t.Errorf("run = %+v", run)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	ctx := context.Background()
	in := &state.Run{Ticket: "HIVE-1", Agent: "w", Branch: "hive/HIVE-1", Attempts: 2, Phase: state.PhasePushed, ResumeToken: "tok", QuestionAt: time.Date(2026, 9, 19, 1, 2, 3, 0, time.UTC)}
	if err := s.Save(ctx, in); err != nil {
		t.Fatal(err)
	}
	if in.UpdatedAt.IsZero() {
		t.Error("Save must set UpdatedAt")
	}
	out, err := s.Load(ctx, "HIVE-1")
	if err != nil {
		t.Fatal(err)
	}
	if out.Attempts != 2 || out.Phase != state.PhasePushed || out.ResumeToken != "tok" || !out.QuestionAt.Equal(in.QuestionAt) {
		t.Errorf("out = %+v", out)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "runs", "HIVE-1.json")); err != nil {
		t.Error("run file not at runs/HIVE-1.json")
	}
}

func TestAppendLogAndWriteLog(t *testing.T) {
	s := New(t.TempDir())
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := s.AppendLog(ctx, "HIVE-1", state.LogEntry{Agent: "w", Event: "claimed"}); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(s.Dir, "logs", "HIVE-1", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(raw)), "\n") + 1; lines != 2 {
		t.Errorf("lines = %d", lines)
	}
	p, err := s.WriteLog(ctx, "HIVE-1", "run-1", "executor output")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); string(got) != "executor output" {
		t.Errorf("log = %q", got)
	}
	if !strings.HasSuffix(p, filepath.Join("logs", "HIVE-1", "run-1.log")) {
		t.Errorf("path = %s", p)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/state/...`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Write `internal/state/state.go`**

```go
// Package state defines per-ticket run state and where it is stored.
//
// One run record per ticket, written only by the worker holding the claim.
// The phase is written before each next action starts, so phase plus a
// stale heartbeat says exactly where a run stopped.
package state

import (
	"context"
	"time"
)

// Phase is where a run is in its lifecycle.
type Phase string

// Run phases.
const (
	PhaseClaimed  Phase = "claimed"
	PhasePlanned  Phase = "planned"
	PhaseWorking  Phase = "working"
	PhasePushed   Phase = "pushed"
	PhasePROpened Phase = "pr_opened"
	PhaseDone     Phase = "done"
	PhaseBlocked  Phase = "blocked"
)

// Run is the per-ticket record.
type Run struct {
	Ticket      string    `json:"ticket"`
	Agent       string    `json:"agent"`
	Branch      string    `json:"branch"`
	Attempts    int       `json:"attempts"`
	Phase       Phase     `json:"phase"`
	LastStatus  string    `json:"lastStatus,omitempty"`
	StopCause   string    `json:"stopCause,omitempty"`
	ResumeToken string    `json:"resumeToken,omitempty"`
	PRURL       string    `json:"prUrl,omitempty"`
	QuestionAt  time.Time `json:"questionAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// LogEntry is one structured line in a ticket's event log.
type LogEntry struct {
	Time    time.Time `json:"time"`
	Agent   string    `json:"agent"`
	Event   string    `json:"event"`
	Phase   string    `json:"phase,omitempty"`
	Cause   string    `json:"cause,omitempty"`
	Message string    `json:"message,omitempty"`
}

// RunStore persists runs and logs.
type RunStore interface {
	// Load returns the run for key, or an empty Run{Ticket: key}.
	Load(ctx context.Context, key string) (*Run, error)
	// Save persists run, setting UpdatedAt.
	Save(ctx context.Context, run *Run) error
	// AppendLog adds a structured event.
	AppendLog(ctx context.Context, key string, e LogEntry) error
	// WriteLog stores a raw log under name and returns its path.
	WriteLog(ctx context.Context, key, name, content string) (string, error)
}
```

- [ ] **Step 4: Write `internal/state/localdir/localdir.go`**

```go
// Package localdir stores run state in a plain directory. It is the test
// store and the fallback when no state branch is configured.
package localdir

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/state"
)

// Store writes runs/<KEY>.json and logs/<KEY>/... under Dir.
type Store struct {
	Dir string
	Now func() time.Time

	mu sync.Mutex
}

var _ state.RunStore = (*Store)(nil)

// New returns a store rooted at dir.
func New(dir string) *Store {
	return &Store{Dir: dir, Now: time.Now}
}

func (s *Store) runPath(key string) string {
	return filepath.Join(s.Dir, "runs", key+".json")
}

// Load implements state.RunStore.
func (s *Store) Load(_ context.Context, key string) (*state.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.runPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return &state.Run{Ticket: key}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("state: read %s: %w", key, err)
	}
	var run state.Run
	if err := json.Unmarshal(raw, &run); err != nil {
		return nil, fmt.Errorf("state: parse %s: %w", key, err)
	}
	return &run, nil
}

// Save implements state.RunStore with an atomic write.
func (s *Store) Save(_ context.Context, run *state.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run.UpdatedAt = s.Now().UTC()
	p := s.runPath(run.Ticket)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// AppendLog implements state.RunStore.
func (s *Store) AppendLog(_ context.Context, key string, e state.LogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Time.IsZero() {
		e.Time = s.Now().UTC()
	}
	dir := filepath.Join(s.Dir, "logs", key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(raw, '\n'))
	return err
}

// WriteLog implements state.RunStore.
func (s *Store) WriteLog(_ context.Context, key, name, content string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.Dir, "logs", key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, name+".log")
	return p, os.WriteFile(p, []byte(content), 0o644)
}
```

- [ ] **Step 5: Run tests, lint, commit**

Run: `go vet ./... && go test -race ./internal/state/... && ~/go/bin/golangci-lint run ./...`
Expected: PASS, 0 issues.

```bash
git add internal/state
git commit -m "feat(state): Run record, RunStore contract, local-directory store

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 5: Run windows and config additions

**Files:**
- Create: `internal/schedule/schedule.go`, `internal/schedule/schedule_test.go`
- Modify: `internal/config/config.go` (new fields + defaults + validation), `internal/config/config_test.go`

**Interfaces:**
- Produces:

```go
// config additions
RunTimeout time.Duration `yaml:"run_timeout"`      // default 45m
StepBudget int           `yaml:"step_budget"`      // default 200
MaxAttempts int          `yaml:"max_attempts"`     // default 3
PollJitter time.Duration `yaml:"poll_jitter"`      // default 10s
RunWindows RunWindows    `yaml:"run_windows"`
type RunWindows struct { Timezone string `yaml:"timezone"`; Windows []WindowConfig `yaml:"windows"` }
type WindowConfig struct { Days []string `yaml:"days"`; Start string `yaml:"start"`; End string `yaml:"end"` }
// schedule
func Parse(cfg config.RunWindows) (*Schedule, error)
func (s *Schedule) Open(t time.Time) bool   // no windows → always true; nil receiver → true
```

- [ ] **Step 1: Write the failing tests**

Append to `internal/config/config_test.go`:

```go
func TestLoadRunDefaultsAndWindows(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	body := validYAML + `
run_windows:
  timezone: America/New_York
  windows:
    - days: [mon, tue]
      start: "22:00"
      end: "06:00"
`
	cfg, err := Load(writeTemp(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RunTimeout != 45*time.Minute || cfg.StepBudget != 200 || cfg.MaxAttempts != 3 || cfg.PollJitter != 10*time.Second {
		t.Errorf("defaults: %+v", cfg)
	}
	if cfg.RunWindows.Timezone != "America/New_York" || len(cfg.RunWindows.Windows) != 1 || cfg.RunWindows.Windows[0].End != "06:00" {
		t.Errorf("windows = %+v", cfg.RunWindows)
	}
}

func TestValidateRejectsBadWindow(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	body := validYAML + "run_windows:\n  timezone: Mars/Olympus\n  windows:\n    - {start: \"25:00\", end: \"06:00\"}\n"
	_, err := Load(writeTemp(t, body))
	if err == nil || !strings.Contains(err.Error(), "timezone") || !strings.Contains(err.Error(), "start") {
		t.Fatalf("err = %v", err)
	}
}
```

`internal/schedule/schedule_test.go`:

```go
package schedule

import (
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
)

func at(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse("2006-01-02 15:04 MST", s)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func TestEmptyIsAlwaysOpen(t *testing.T) {
	s, err := Parse(config.RunWindows{})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Open(time.Now()) {
		t.Error("empty schedule should be open")
	}
	var nilS *Schedule
	if !nilS.Open(time.Now()) {
		t.Error("nil schedule should be open")
	}
}

func TestDaytimeWindow(t *testing.T) {
	s, err := Parse(config.RunWindows{Timezone: "UTC", Windows: []config.WindowConfig{{Days: []string{"mon", "tue"}, Start: "09:00", End: "17:00"}}})
	if err != nil {
		t.Fatal(err)
	}
	// 2026-09-21 is a Monday.
	if !s.Open(at(t, "2026-09-21 12:00 UTC")) {
		t.Error("Monday noon should be open")
	}
	if s.Open(at(t, "2026-09-21 17:00 UTC")) {
		t.Error("end is exclusive")
	}
	if s.Open(at(t, "2026-09-23 12:00 UTC")) {
		t.Error("Wednesday should be closed")
	}
}

func TestOvernightWindowSpansMidnight(t *testing.T) {
	s, err := Parse(config.RunWindows{Timezone: "UTC", Windows: []config.WindowConfig{{Days: []string{"friday"}, Start: "22:00", End: "06:00"}}})
	if err != nil {
		t.Fatal(err)
	}
	// 2026-09-25 is a Friday.
	if !s.Open(at(t, "2026-09-25 23:00 UTC")) {
		t.Error("Friday 23:00 should be open")
	}
	if !s.Open(at(t, "2026-09-26 03:00 UTC")) {
		t.Error("Saturday 03:00 belongs to Friday's window")
	}
	if s.Open(at(t, "2026-09-26 23:00 UTC")) {
		t.Error("Saturday 23:00 should be closed")
	}
	if s.Open(at(t, "2026-09-25 12:00 UTC")) {
		t.Error("Friday noon should be closed")
	}
}

func TestTimezoneApplied(t *testing.T) {
	s, err := Parse(config.RunWindows{Timezone: "America/New_York", Windows: []config.WindowConfig{{Start: "22:00", End: "23:00"}}})
	if err != nil {
		t.Fatal(err)
	}
	// 02:30 UTC on 2026-09-20 is 22:30 EDT on 2026-09-19.
	if !s.Open(at(t, "2026-09-20 02:30 UTC")) {
		t.Error("should be open in New York evening")
	}
}

func TestParseErrors(t *testing.T) {
	cases := []config.RunWindows{
		{Timezone: "Nope/Nowhere"},
		{Windows: []config.WindowConfig{{Start: "9", End: "17:00"}}},
		{Windows: []config.WindowConfig{{Days: []string{"funday"}, Start: "09:00", End: "17:00"}}},
	}
	for i, c := range cases {
		if _, err := Parse(c); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ ./internal/schedule/`
Expected: FAIL — `cfg.RunTimeout undefined`, `undefined: Parse`.

- [ ] **Step 3: Extend `internal/config/config.go`**

Add fields to `Config` after `ClaimTimeout`:

```go
	RunTimeout        time.Duration `yaml:"run_timeout"`
	StepBudget        int           `yaml:"step_budget"`
	MaxAttempts       int           `yaml:"max_attempts"`
	PollJitter        time.Duration `yaml:"poll_jitter"`
	RunWindows        RunWindows    `yaml:"run_windows"`
```

Add types after `RepoConfig`:

```go
// RunWindows restricts when the poller claims new work. Empty means always.
type RunWindows struct {
	Timezone string         `yaml:"timezone"` // IANA name; default "Local"
	Windows  []WindowConfig `yaml:"windows"`
}

// WindowConfig is one daily window. Start after End spans midnight.
type WindowConfig struct {
	Days  []string `yaml:"days"` // mon..sun or full names; empty = every day
	Start string   `yaml:"start"` // HH:MM
	End   string   `yaml:"end"`   // HH:MM, exclusive
}
```

In `applyDefaults` add:

```go
	if c.RunTimeout == 0 {
		c.RunTimeout = 45 * time.Minute
	}
	if c.StepBudget == 0 {
		c.StepBudget = 200
	}
	if c.MaxAttempts == 0 {
		c.MaxAttempts = 3
	}
	if c.PollJitter == 0 {
		c.PollJitter = 10 * time.Second
	}
```

In `Validate`, before the `if len(problems) == 0` check, add:

```go
	if tz := c.RunWindows.Timezone; tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			problems = append(problems, "run_windows.timezone: unknown timezone "+tz)
		}
	}
	for i, w := range c.RunWindows.Windows {
		for _, pair := range [][2]string{{"start", w.Start}, {"end", w.End}} {
			if _, err := time.Parse("15:04", pair[1]); err != nil {
				problems = append(problems, fmt.Sprintf("run_windows.windows[%d].%s: want HH:MM, got %q", i, pair[0], pair[1]))
			}
		}
	}
```

- [ ] **Step 4: Write `internal/schedule/schedule.go`**

```go
// Package schedule evaluates configured run windows.
//
// Windows gate only the start of new work: a run in flight when a window
// closes finishes rather than aborting, because a safe stop beats a punctual
// one.
package schedule

import (
	"fmt"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
)

type window struct {
	days  map[time.Weekday]bool // nil = every day
	start int                   // minutes since midnight
	end   int                   // exclusive
}

// Schedule is a parsed set of run windows.
type Schedule struct {
	loc     *time.Location
	windows []window
}

var dayNames = map[string]time.Weekday{
	"sun": time.Sunday, "sunday": time.Sunday,
	"mon": time.Monday, "monday": time.Monday,
	"tue": time.Tuesday, "tuesday": time.Tuesday,
	"wed": time.Wednesday, "wednesday": time.Wednesday,
	"thu": time.Thursday, "thursday": time.Thursday,
	"fri": time.Friday, "friday": time.Friday,
	"sat": time.Saturday, "saturday": time.Saturday,
}

// Parse validates and compiles cfg.
func Parse(cfg config.RunWindows) (*Schedule, error) {
	loc := time.Local
	if cfg.Timezone != "" {
		l, err := time.LoadLocation(cfg.Timezone)
		if err != nil {
			return nil, fmt.Errorf("schedule: timezone %q: %w", cfg.Timezone, err)
		}
		loc = l
	}
	s := &Schedule{loc: loc}
	for i, w := range cfg.Windows {
		start, err := minutes(w.Start)
		if err != nil {
			return nil, fmt.Errorf("schedule: window %d start: %w", i, err)
		}
		end, err := minutes(w.End)
		if err != nil {
			return nil, fmt.Errorf("schedule: window %d end: %w", i, err)
		}
		var days map[time.Weekday]bool
		if len(w.Days) > 0 {
			days = map[time.Weekday]bool{}
			for _, d := range w.Days {
				wd, ok := dayNames[strings.ToLower(strings.TrimSpace(d))]
				if !ok {
					return nil, fmt.Errorf("schedule: window %d: unknown day %q", i, d)
				}
				days[wd] = true
			}
		}
		s.windows = append(s.windows, window{days: days, start: start, end: end})
	}
	return s, nil
}

func minutes(hhmm string) (int, error) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return 0, fmt.Errorf("want HH:MM, got %q", hhmm)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// Open reports whether new work may start at t. A nil or empty schedule is
// always open.
func (s *Schedule) Open(t time.Time) bool {
	if s == nil || len(s.windows) == 0 {
		return true
	}
	lt := t.In(s.loc)
	m := lt.Hour()*60 + lt.Minute()
	today := lt.Weekday()
	yesterday := (today + 6) % 7
	for _, w := range s.windows {
		if w.start <= w.end {
			if w.on(today) && m >= w.start && m < w.end {
				return true
			}
			continue
		}
		// Spans midnight: the evening part belongs to today, the morning
		// part to the window that started yesterday.
		if w.on(today) && m >= w.start {
			return true
		}
		if w.on(yesterday) && m < w.end {
			return true
		}
	}
	return false
}

func (w window) on(d time.Weekday) bool {
	return w.days == nil || w.days[d]
}
```

- [ ] **Step 5: Run tests, lint, commit**

Run: `go vet ./... && go test -race ./internal/config/ ./internal/schedule/ && ~/go/bin/golangci-lint run ./...`
Expected: PASS, 0 issues.

```bash
git add internal/config internal/schedule
git commit -m "feat(schedule): run windows with timezone and overnight spans; run config defaults

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 6: Comment templates

**Files:**
- Create: `internal/dispatch/report.go`, `internal/dispatch/report_test.go`

**Interfaces:**
- Produces (all unexported, used by `handle.go`):

```go
const Marker = "[HiveDispatch]"           // exported: the CLI docs reference it
func isOurs(c tracker.Comment) bool
func reportTriageNeedsInfo(question string) string
func reportRejected(reason string) string
func reportCompleted(res executor.Result, pr *githost.PR, branch string, pushed bool) string
func reportNeedsInput(question string) string
func reportFailed(res executor.Result, attempts, maxAttempts int, branch string, pushed, exhausted bool, now time.Time) string
func reportPRFailed(err error, branch string) string
```

- [ ] **Step 1: Write the failing test**

`internal/dispatch/report_test.go`:

```go
package dispatch

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/githost"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

var now = time.Date(2026, 9, 19, 14, 2, 0, 0, time.UTC)

func mustContain(t *testing.T, got string, wants ...string) {
	t.Helper()
	if !strings.HasPrefix(got, Marker) {
		t.Errorf("missing marker prefix: %q", got)
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
}

func TestIsOurs(t *testing.T) {
	if !isOurs(tracker.Comment{Body: Marker + " hi"}) || isOurs(tracker.Comment{Body: "hi"}) {
		t.Error("isOurs wrong")
	}
}

func TestReports(t *testing.T) {
	mustContain(t, reportTriageNeedsInfo("Which DB?"), "Which DB?", "Ready")
	mustContain(t, reportRejected("too big"), "too big")
	mustContain(t, reportNeedsInput("Which DB?"), "Which DB?", "Ready")
	mustContain(t, reportPRFailed(errors.New("403"), "hive/HIVE-1"), "403", "hive/HIVE-1")

	pr := &githost.PR{URL: "https://x/pull/1"}
	mustContain(t, reportCompleted(executor.Result{Summary: "did it"}, pr, "hive/HIVE-1", true), "https://x/pull/1", "did it")
	got := reportCompleted(executor.Result{Summary: "nothing to do"}, nil, "hive/HIVE-1", false)
	mustContain(t, got, "no code changes", "nothing to do")

	res := executor.Result{Status: executor.StatusFailed, StopCause: executor.CauseTimeout, Summary: "got halfway"}
	got = reportFailed(res, 1, 3, "hive/HIVE-1", true, false, now)
	mustContain(t, got, "14:02 UTC", executor.CauseTimeout.Describe(), "1 of 3", "hive/HIVE-1", "got halfway", "retry")
	got = reportFailed(res, 3, 3, "hive/HIVE-1", false, true, now)
	mustContain(t, got, "3 of 3", "No changes were made", "human")
	if strings.Contains(got, "retry") {
		t.Error("exhausted report must not promise a retry")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dispatch/`
Expected: FAIL — `undefined: Marker`.

- [ ] **Step 3: Write `internal/dispatch/report.go`**

```go
package dispatch

import (
	"fmt"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/githost"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// Marker prefixes every comment the dispatcher posts so it can recognise
// its own comments when looking for human replies.
const Marker = "[HiveDispatch]"

func isOurs(c tracker.Comment) bool {
	return strings.HasPrefix(c.Body, Marker)
}

const resumeHint = "Reply in this thread, then move the ticket back to Ready to resume."

func reportTriageNeedsInfo(question string) string {
	return fmt.Sprintf("%s Before starting, I need more information:\n\n%s\n\n%s", Marker, question, resumeHint)
}

func reportRejected(reason string) string {
	return fmt.Sprintf("%s Not attempting this ticket automatically: %s\n\nA human should pick this up.", Marker, reason)
}

func reportNeedsInput(question string) string {
	return fmt.Sprintf("%s I need input before continuing:\n\n%s\n\n%s", Marker, question, resumeHint)
}

func reportCompleted(res executor.Result, pr *githost.PR, branch string, pushed bool) string {
	var sb strings.Builder
	switch {
	case pr != nil:
		fmt.Fprintf(&sb, "%s Opened %s from branch `%s`.", Marker, pr.URL, branch)
	case pushed:
		fmt.Fprintf(&sb, "%s Pushed branch `%s` but could not open a PR.", Marker, branch)
	default:
		fmt.Fprintf(&sb, "%s Finished with no code changes.", Marker)
	}
	if s := strings.TrimSpace(res.Summary); s != "" {
		sb.WriteString("\n\n" + s)
	}
	return sb.String()
}

func reportFailed(res executor.Result, attempts, maxAttempts int, branch string, pushed, exhausted bool, now time.Time) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s Stopped at %s — %s. Attempt %d of %d.", Marker, now.UTC().Format("15:04 UTC"), res.StopCause.Describe(), attempts, maxAttempts)
	if pushed {
		fmt.Fprintf(&sb, "\n\nWork so far is committed to `%s`. No PR was opened.", branch)
	} else {
		sb.WriteString("\n\nNo changes were made.")
	}
	if s := strings.TrimSpace(res.Summary); s != "" {
		sb.WriteString("\n\n" + s)
	}
	if exhausted {
		sb.WriteString("\n\nAttempts exhausted; a human should look at this.")
	} else {
		sb.WriteString("\n\nWill retry on the next poll.")
	}
	return sb.String()
}

func reportPRFailed(err error, branch string) string {
	return fmt.Sprintf("%s Pushed branch `%s` but opening the PR failed: %v\n\nWill retry on the next poll.", Marker, branch, err)
}
```

- [ ] **Step 4: Run tests, lint, commit**

Run: `go vet ./... && go test -race ./internal/dispatch/ && ~/go/bin/golangci-lint run ./...`
Expected: PASS, 0 issues.

```bash
git add internal/dispatch
git commit -m "feat(dispatch): ticket comment templates

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 7: Dispatcher core — claim, triage, execute, report

This is the largest task. The test harness is written first and reused by Tasks 8–9.

**Files:**
- Create: `internal/dispatch/dispatch.go`, `internal/dispatch/handle.go`, `internal/dispatch/dispatch_test.go`
- Modify: `internal/tracker/fake/fake.go` — rename `overwriteClaim` to exported `OverwriteClaim` (update `fake_test.go` call site)

**Interfaces:**
- Produces:

```go
type Config struct { AgentID string; ClaimTimeout, HeartbeatInterval, RunTimeout, PollInterval, PollJitter time.Duration; StepBudget, MaxAttempts int; Repos []config.RepoConfig }
func ConfigFrom(c *config.Config) Config
type Dispatcher struct { Cfg Config; Tracker tracker.Tracker; Triager triage.Triager; Executor executor.Executor; Workspaces gitops.Workspaces; Host githost.GitHost; Store state.RunStore; Schedule *schedule.Schedule; Now func() time.Time; Log *slog.Logger }
type Outcome string  // OutcomeSkipped | OutcomeClaimLost | OutcomeNeedsInfo | OutcomeRejected | OutcomeCompleted | OutcomeNeedsInput | OutcomeFailed
func (d *Dispatcher) Handle(ctx, t tracker.Ticket) (Outcome, error)
func (d *Dispatcher) Once(ctx) (handled int, err error)
func (d *Dispatcher) Run(loopCtx, runCtx context.Context) error   // Task 9
```

- [ ] **Step 1: Export the fake tracker's claim override**

In `internal/tracker/fake/fake.go` rename `overwriteClaim` → `OverwriteClaim` with doc comment `// OverwriteClaim sets a claim unconditionally, simulating another worker.` Update the call in `fake_test.go`.

- [ ] **Step 2: Write the harness and first tests**

`internal/dispatch/dispatch_test.go`:

```go
package dispatch

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/executor"
	exfake "github.com/thomasmeadows/hivedispatch/internal/executor/fake"
	gitfake "github.com/thomasmeadows/hivedispatch/internal/gitops/fake"
	hostfake "github.com/thomasmeadows/hivedispatch/internal/githost/fake"
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
	d     *Dispatcher
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
	}
	h.tr.Now = func() time.Time { return now }
	h.d = &Dispatcher{
		Cfg: Config{
			AgentID: "worker-a", ClaimTimeout: time.Hour, HeartbeatInterval: time.Hour,
			RunTimeout: time.Second, StepBudget: 50, MaxAttempts: 3,
			Repos: []config.RepoConfig{{Name: "o/r", URL: "git@x:o/r.git", DefaultBranch: "main", JiraProject: "HIVE"}},
		},
		Tracker: h.tr, Triager: h.tri, Executor: h.ex, Workspaces: h.ws, Host: h.host, Store: h.store,
		Now: func() time.Time { return now },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h.tr.Add(tracker.Ticket{Key: "HIVE-1", Summary: "one"})
	return h
}

func (h *harness) handle(t *testing.T) Outcome {
	t.Helper()
	tk, err := h.tr.Get(context.Background(), "HIVE-1")
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.d.Handle(context.Background(), tk)
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
	if r := h.run(t); r.LastStatus != string(executor.StatusNeedsInput) || r.QuestionAt.IsZero() {
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
	tk := h.ticket(t)
	out, err := h.d.Handle(context.Background(), tk)
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
	tk := h.ticket(t)
	if _, err := h.d.Handle(context.Background(), tk); err == nil {
		t.Fatal("expected error")
	}
	h.assertTransitions(t)
	h.assertReleased(t)
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/dispatch/`
Expected: FAIL — `undefined: Dispatcher`.

- [ ] **Step 4: Write `internal/dispatch/dispatch.go`**

```go
// Package dispatch is the deterministic control loop: poll, claim, triage,
// execute, report. It decides nothing about code and nothing about which
// tickets are worth attempting; those decisions come from the Triager and
// the Executor and this package carries them out.
package dispatch

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/githost"
	"github.com/thomasmeadows/hivedispatch/internal/gitops"
	"github.com/thomasmeadows/hivedispatch/internal/schedule"
	"github.com/thomasmeadows/hivedispatch/internal/state"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
	"github.com/thomasmeadows/hivedispatch/internal/triage"
)

// Config is the subset of worker config the dispatcher needs.
type Config struct {
	AgentID           string
	ClaimTimeout      time.Duration
	HeartbeatInterval time.Duration
	RunTimeout        time.Duration
	PollInterval      time.Duration
	PollJitter        time.Duration
	StepBudget        int
	MaxAttempts       int
	Repos             []config.RepoConfig
}

// ConfigFrom extracts the dispatcher config from the worker config.
func ConfigFrom(c *config.Config) Config {
	return Config{
		AgentID:           c.AgentID,
		ClaimTimeout:      c.ClaimTimeout,
		HeartbeatInterval: c.HeartbeatInterval,
		RunTimeout:        c.RunTimeout,
		PollInterval:      c.PollInterval,
		PollJitter:        c.PollJitter,
		StepBudget:        c.StepBudget,
		MaxAttempts:       c.MaxAttempts,
		Repos:             c.Repos,
	}
}

// Dispatcher runs the control loop for one worker.
type Dispatcher struct {
	Cfg        Config
	Tracker    tracker.Tracker
	Triager    triage.Triager
	Executor   executor.Executor
	Workspaces gitops.Workspaces
	Host       githost.GitHost
	Store      state.RunStore
	Schedule   *schedule.Schedule // nil = always open
	Now        func() time.Time   // nil = time.Now
	Log        *slog.Logger       // nil = slog.Default()
}

// Outcome summarises what Handle did with a ticket.
type Outcome string

// Outcomes.
const (
	OutcomeSkipped    Outcome = "skipped"
	OutcomeClaimLost  Outcome = "claim_lost"
	OutcomeNeedsInfo  Outcome = "needs_info"
	OutcomeRejected   Outcome = "rejected"
	OutcomeCompleted  Outcome = "completed"
	OutcomeNeedsInput Outcome = "needs_input"
	OutcomeFailed     Outcome = "failed"
)

func (d *Dispatcher) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Dispatcher) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

// repoFor maps a ticket key to the repo whose project prefix matches.
func (d *Dispatcher) repoFor(key string) (config.RepoConfig, bool) {
	project, _, ok := strings.Cut(key, "-")
	if !ok {
		return config.RepoConfig{}, false
	}
	for _, r := range d.Cfg.Repos {
		if strings.EqualFold(r.JiraProject, project) {
			return r, true
		}
	}
	return config.RepoConfig{}, false
}

// Once polls and handles every ticket, unless the schedule is closed.
// It returns how many tickets were actually worked (not skipped or lost).
func (d *Dispatcher) Once(ctx context.Context) (int, error) {
	if !d.Schedule.Open(d.now()) {
		d.log().Debug("outside run window; not polling")
		return 0, nil
	}
	tickets, err := d.Tracker.Poll(ctx)
	if err != nil {
		return 0, err
	}
	handled := 0
	for _, t := range tickets {
		if ctx.Err() != nil {
			break
		}
		out, err := d.Handle(ctx, t)
		if err != nil {
			d.log().Error("handle failed", "ticket", t.Key, "outcome", out, "err", err)
		} else {
			d.log().Info("handled", "ticket", t.Key, "outcome", out)
		}
		if out != OutcomeSkipped && out != OutcomeClaimLost {
			handled++
		}
	}
	return handled, nil
}

// Run polls on the configured interval until loopCtx is cancelled. Runs in
// flight use runCtx, so a caller can stop polling (drain) before it hard-
// cancels work. Pass the same context for both to stop immediately.
func (d *Dispatcher) Run(loopCtx, runCtx context.Context) error {
	for {
		if _, err := d.Once(runCtx); err != nil {
			d.log().Error("poll failed", "err", err)
		}
		wait := d.Cfg.PollInterval
		if d.Cfg.PollJitter > 0 {
			wait += time.Duration(rand.Int64N(int64(d.Cfg.PollJitter)))
		}
		select {
		case <-loopCtx.Done():
			return loopCtx.Err()
		case <-time.After(wait):
		}
	}
}
```

- [ ] **Step 5: Write `internal/dispatch/handle.go`**

```go
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/githost"
	"github.com/thomasmeadows/hivedispatch/internal/gitops"
	"github.com/thomasmeadows/hivedispatch/internal/prompt"
	"github.com/thomasmeadows/hivedispatch/internal/state"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
	"github.com/thomasmeadows/hivedispatch/internal/triage"
)

// releaseTimeout bounds the cleanup calls made after ctx may be cancelled.
const releaseTimeout = 30 * time.Second

// Handle takes one ticket from polled to a terminal state. Every path that
// claims the ticket also releases it and leaves a comment explaining what
// happened.
func (d *Dispatcher) Handle(ctx context.Context, t tracker.Ticket) (Outcome, error) {
	now := d.now()
	repo, ok := d.repoFor(t.Key)
	if !ok {
		d.log().Warn("no repo configured for ticket", "ticket", t.Key)
		return OutcomeSkipped, nil
	}
	if t.Claim.Fresh(now, d.Cfg.ClaimTimeout) && t.Claim.AgentID != d.Cfg.AgentID {
		return OutcomeSkipped, nil
	}
	won, err := d.Tracker.Claim(ctx, t.Key, d.Cfg.AgentID, now)
	if err != nil {
		return OutcomeSkipped, fmt.Errorf("claim %s: %w", t.Key, err)
	}
	if !won {
		return OutcomeClaimLost, nil
	}
	defer d.release(ctx, t.Key)

	run, err := d.Store.Load(ctx, t.Key)
	if err != nil {
		return OutcomeSkipped, fmt.Errorf("load run %s: %w", t.Key, err)
	}
	run.Agent = d.Cfg.AgentID
	run.Branch = gitops.BranchName(t.Key)
	d.setPhase(ctx, run, state.PhaseClaimed)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go d.heartbeat(runCtx, t.Key, cancel)

	// Resume after a human reply, or triage afresh.
	var taskPrompt string
	if d.canResume(t, run) {
		taskPrompt = prompt.RenderResume(t, run.QuestionAt, isOurs)
		d.event(ctx, run, "resume", "")
	} else {
		dec, err := d.Triager.Decide(ctx, triage.Input{
			Ticket: t, Repo: repo, Branch: run.Branch,
			Attempts: run.Attempts, LastStopCause: run.StopCause,
		})
		if err != nil {
			return OutcomeSkipped, fmt.Errorf("triage %s: %w", t.Key, err)
		}
		switch dec.Kind {
		case triage.KindNeedsInfo:
			run.LastStatus = string(executor.StatusNeedsInput)
			run.QuestionAt = now
			d.save(ctx, run)
			d.comment(ctx, t.Key, reportTriageNeedsInfo(dec.Question))
			d.transition(ctx, t.Key, tracker.StateNeedsInfo)
			d.event(ctx, run, "triage_needs_info", dec.Question)
			return OutcomeNeedsInfo, nil
		case triage.KindReject:
			d.comment(ctx, t.Key, reportRejected(dec.Reason))
			d.transition(ctx, t.Key, tracker.StateNeedsHuman)
			d.event(ctx, run, "triage_reject", dec.Reason)
			return OutcomeRejected, nil
		case triage.KindDispatch:
			taskPrompt = dec.Prompt
		default:
			return OutcomeSkipped, fmt.Errorf("triage %s: unknown decision %q", t.Key, dec.Kind)
		}
	}

	d.transition(ctx, t.Key, tracker.StateInProgress)
	return d.execute(runCtx, t, repo, run, taskPrompt)
}

// canResume reports whether the last run asked a question that a human has
// since answered.
func (d *Dispatcher) canResume(t tracker.Ticket, run *state.Run) bool {
	if run.LastStatus != string(executor.StatusNeedsInput) || run.ResumeToken == "" || run.QuestionAt.IsZero() {
		return false
	}
	for _, c := range t.Comments {
		if c.Created.After(run.QuestionAt) && !isOurs(c) {
			return true
		}
	}
	return false
}

// execute runs the executor and reports the result. ctx is the run context:
// cancelled on shutdown or on losing the claim.
func (d *Dispatcher) execute(ctx context.Context, t tracker.Ticket, repo config.RepoConfig, run *state.Run, taskPrompt string) (Outcome, error) {
	ws, err := d.Workspaces.Prepare(ctx, repo, t.Key)
	if err != nil {
		res := executor.Result{Status: executor.StatusFailed, StopCause: executor.CauseError, Summary: "workspace: " + err.Error()}
		run.Attempts++
		return d.finishFailed(ctx, t, run, res, false), nil
	}
	run.Attempts++
	d.setPhase(ctx, run, state.PhaseWorking)

	execCtx, cancelExec := context.WithTimeout(ctx, d.Cfg.RunTimeout)
	res, err := d.Executor.Run(execCtx, executor.Task{
		TicketKey: t.Key, Prompt: taskPrompt, Workspace: ws.Path,
		ResumeToken: run.ResumeToken, StepBudget: d.Cfg.StepBudget,
	})
	cancelExec()
	if err != nil {
		res = executor.Result{Status: executor.StatusFailed, StopCause: executor.CauseError, Summary: err.Error()}
	}
	if res.Status == executor.StatusFailed && res.StopCause == executor.CauseNone {
		res.StopCause = executor.CauseError
	}
	if res.ResumeToken != "" {
		run.ResumeToken = res.ResumeToken
	}
	run.LastStatus = string(res.Status)
	run.StopCause = string(res.StopCause)
	if res.Log != "" {
		if _, err := d.Store.WriteLog(ctx, t.Key, d.now().UTC().Format("2006-01-02T15-04-05"), res.Log); err != nil {
			d.log().Warn("write log failed", "ticket", t.Key, "err", err)
		}
	}

	// Safety commit and push: the guarantee that every stop point is safe.
	bg := d.background(ctx)
	msg := "hive: checkpoint"
	if res.Status != executor.StatusCompleted {
		msg = fmt.Sprintf("hive: WIP (%s)", res.StopCause)
	}
	pushed, ferr := d.Workspaces.Finalize(bg, ws, msg)
	if ferr != nil {
		d.log().Error("finalize failed", "ticket", t.Key, "err", ferr)
	}
	if pushed {
		d.setPhase(bg, run, state.PhasePushed)
	}

	switch res.Status {
	case executor.StatusCompleted:
		var pr *githost.PR
		if pushed {
			pr, err = d.ensurePR(bg, repo, t, run)
			if err != nil {
				d.comment(bg, t.Key, reportPRFailed(err, run.Branch))
				d.transition(bg, t.Key, tracker.StateReady)
				d.event(bg, run, "pr_failed", err.Error())
				return OutcomeFailed, fmt.Errorf("open PR for %s: %w", t.Key, err)
			}
			run.PRURL = pr.URL
			d.setPhase(bg, run, state.PhasePROpened)
		}
		d.comment(bg, t.Key, reportCompleted(res, pr, run.Branch, pushed))
		d.transition(bg, t.Key, tracker.StateInReview)
		d.setPhase(bg, run, state.PhaseDone)
		d.event(bg, run, "completed", res.Summary)
		return OutcomeCompleted, nil
	case executor.StatusNeedsInput:
		run.QuestionAt = d.now()
		d.save(bg, run)
		d.comment(bg, t.Key, reportNeedsInput(res.Question))
		d.transition(bg, t.Key, tracker.StateNeedsInfo)
		d.event(bg, run, "needs_input", res.Question)
		return OutcomeNeedsInput, nil
	default:
		return d.finishFailed(bg, t, run, res, pushed), nil
	}
}

func (d *Dispatcher) finishFailed(ctx context.Context, t tracker.Ticket, run *state.Run, res executor.Result, pushed bool) Outcome {
	run.LastStatus = string(executor.StatusFailed)
	run.StopCause = string(res.StopCause)
	exhausted := run.Attempts >= d.Cfg.MaxAttempts
	d.save(ctx, run)
	d.comment(ctx, t.Key, reportFailed(res, run.Attempts, d.Cfg.MaxAttempts, run.Branch, pushed, exhausted, d.now()))
	if exhausted {
		d.transition(ctx, t.Key, tracker.StateNeedsHuman)
	} else {
		d.transition(ctx, t.Key, tracker.StateReady)
	}
	d.event(ctx, run, "failed", res.Summary)
	return OutcomeFailed
}

func (d *Dispatcher) ensurePR(ctx context.Context, repo config.RepoConfig, t tracker.Ticket, run *state.Run) (*githost.PR, error) {
	pr, err := d.Host.FindPR(ctx, repo.Name, run.Branch)
	if err != nil {
		return nil, err
	}
	if pr != nil {
		return pr, nil
	}
	body := fmt.Sprintf("Resolves %s.\n\n%s\n\nOpened by HiveDispatch worker %s.", t.Key, t.URL, d.Cfg.AgentID)
	return d.Host.OpenPR(ctx, repo.Name, githost.Request{
		Title: fmt.Sprintf("%s: %s", t.Key, t.Summary),
		Body:  body,
		Head:  run.Branch,
		Base:  repo.DefaultBranch,
	})
}

// heartbeat refreshes the claim until ctx ends. Losing the claim cancels
// the run: someone else now owns the ticket.
func (d *Dispatcher) heartbeat(ctx context.Context, key string, lost context.CancelFunc) {
	tick := time.NewTicker(d.Cfg.HeartbeatInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			err := d.Tracker.Heartbeat(ctx, key, d.Cfg.AgentID)
			if errors.Is(err, tracker.ErrNotClaimHolder) {
				d.log().Error("claim lost mid-run; cancelling", "ticket", key)
				lost()
				return
			}
			if err != nil && ctx.Err() == nil {
				d.log().Warn("heartbeat failed", "ticket", key, "err", err)
			}
		}
	}
}

// background returns a context that survives ctx's cancellation so cleanup
// (comments, transitions, release) still happens after a hard stop. Each
// tracker call is already bounded by the client's own request timeout.
func (d *Dispatcher) background(ctx context.Context) context.Context {
	return context.WithoutCancel(ctx)
}

func (d *Dispatcher) release(ctx context.Context, key string) {
	bg, cancel := context.WithTimeout(d.background(ctx), releaseTimeout)
	defer cancel()
	err := d.Tracker.Release(bg, key, d.Cfg.AgentID)
	if err != nil && !errors.Is(err, tracker.ErrNotClaimHolder) {
		d.log().Error("release failed", "ticket", key, "err", err)
	}
}

func (d *Dispatcher) comment(ctx context.Context, key, body string) {
	if err := d.Tracker.Comment(ctx, key, body); err != nil {
		d.log().Error("comment failed", "ticket", key, "err", err)
	}
}

func (d *Dispatcher) transition(ctx context.Context, key string, to tracker.State) {
	if err := d.Tracker.Transition(ctx, key, to); err != nil {
		d.log().Error("transition failed", "ticket", key, "to", to, "err", err)
	}
}

func (d *Dispatcher) setPhase(ctx context.Context, run *state.Run, p state.Phase) {
	run.Phase = p
	d.save(ctx, run)
}

func (d *Dispatcher) save(ctx context.Context, run *state.Run) {
	if err := d.Store.Save(ctx, run); err != nil {
		d.log().Error("save run failed", "ticket", run.Ticket, "err", err)
	}
}

func (d *Dispatcher) event(ctx context.Context, run *state.Run, ev, msg string) {
	e := state.LogEntry{Time: d.now(), Agent: d.Cfg.AgentID, Event: ev, Phase: string(run.Phase), Cause: run.StopCause, Message: msg}
	if err := d.Store.AppendLog(ctx, run.Ticket, e); err != nil {
		d.log().Error("append log failed", "ticket", run.Ticket, "err", err)
	}
}
```

- [ ] **Step 6: Run tests, lint, commit**

Run: `go vet ./... && go test -race ./... && ~/go/bin/golangci-lint run ./...`
Expected: PASS, 0 issues. If `TestPROpenFailureReturnsToReady` fails on the outcome, check that `execute` returns `OutcomeFailed` together with the error.

```bash
git add internal/dispatch internal/tracker/fake
git commit -m "feat(dispatch): Handle — claim, triage, execute, report; Once and Run loops

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 8: Resume path, heartbeat loss, and the race test

**Files:**
- Create: `internal/dispatch/race_test.go`

- [ ] **Step 1: Write the tests**

`internal/dispatch/race_test.go`:

```go
package dispatch

import (
	"context"
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
	if err != context.DeadlineExceeded {
		t.Errorf("err = %v", err)
	}
	if h.tr.PollCount() < 2 {
		t.Errorf("polls = %d, want repeated polling", h.tr.PollCount())
	}
}
```

- [ ] **Step 2: Add `PollCount` to the fake tracker**

In `internal/tracker/fake/fake.go` add field `polls int` and:

```go
// PollCount returns how many times Poll was called.
func (f *Tracker) PollCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.polls
}
```

and increment `f.polls++` inside `Poll` under the lock.

- [ ] **Step 3: Run tests**

Run: `go test -race -count=3 ./internal/dispatch/`
Expected: PASS three times in a row (the race and heartbeat tests are timing-sensitive; `-count=3` catches flakiness early).

- [ ] **Step 4: Lint and commit**

```bash
~/go/bin/golangci-lint run ./...
git add internal/dispatch internal/tracker/fake
git commit -m "test(dispatch): resume path, heartbeat loss, two-worker race, loop

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 9: `hivedispatch run` and the manual two-worker check

**Files:**
- Modify: `cmd/hivedispatch/main.go`, `cmd/hivedispatch/main_test.go`, `README.md`, `docs/decisions.md`

- [ ] **Step 1: Add a CLI test**

Append to `cmd/hivedispatch/main_test.go`:

```go
func TestRunRequiresConfig(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"run", "-config", filepath.Join(t.TempDir(), "missing.yaml")}, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1: %s", code, errb.String())
	}
}
```

- [ ] **Step 2: Add `runRun` to `cmd/hivedispatch/main.go`**

Add imports `log/slog`, `os/signal`, `path/filepath`, `syscall`, and:

```go
	"github.com/thomasmeadows/hivedispatch/internal/dispatch"
	exfake "github.com/thomasmeadows/hivedispatch/internal/executor/fake"
	gitfake "github.com/thomasmeadows/hivedispatch/internal/gitops/fake"
	hostfake "github.com/thomasmeadows/hivedispatch/internal/githost/fake"
	"github.com/thomasmeadows/hivedispatch/internal/schedule"
	"github.com/thomasmeadows/hivedispatch/internal/state/localdir"
	"github.com/thomasmeadows/hivedispatch/internal/triage/passthrough"
```

Add to the `switch`: `case "run": return runRun(args[1:], stdout, stderr)` and to `usage`:

```
  run   [-config P] [-once]   poll and dispatch (fake executor until Phase 4)
```

Then:

```go
func runRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "path to worker config")
	once := fs.Bool("once", false, "poll once and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	tr, err := jira.New(cfg.Jira)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	sched, err := schedule.Parse(cfg.RunWindows)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	d := &dispatch.Dispatcher{
		Cfg:        dispatch.ConfigFrom(cfg),
		Tracker:    tr,
		Triager:    passthrough.Triager{},
		Executor:   exfake.New(), // Phase 4 replaces this with the Claude Code adapter
		Workspaces: gitfake.New(filepath.Join(cfg.Workroot, "workspaces")),
		Host:       hostfake.New(),
		Store:      localdir.New(filepath.Join(cfg.Workroot, "state")),
		Schedule:   sched,
		Log:        logger,
	}
	logger.Info("starting", "agent", cfg.AgentID, "executor", d.Executor.Name(), "once", *once)

	// First signal drains: stop polling, let the current run finish.
	// Second signal cancels the run; cleanup still posts comments.
	loopCtx, stopLoop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopLoop()
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	go func() {
		<-loopCtx.Done()
		logger.Info("draining; press Ctrl-C again to interrupt the current run")
		stopLoop()
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		cancelRun()
	}()

	if *once {
		n, err := d.Once(runCtx)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "handled %d ticket(s)\n", n)
		return 0
	}
	if err := d.Run(loopCtx, runCtx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
```

(Add `errors` to the imports.)

- [ ] **Step 3: Run everything and build**

Run: `go vet ./... && go test -race ./... && ~/go/bin/golangci-lint run ./... && go build -o bin/hivedispatch ./cmd/hivedispatch`
Expected: PASS, 0 issues, binary built.

- [ ] **Step 4: Update README status and decisions log**

In `README.md` replace the status line with:

```markdown
**Status: pre-alpha.** Phases 0–2 of the MVP are done: config, CLI, Jira tracker, and the dispatcher loop running against a fake executor. `hivedispatch run` claims tickets, comments, and transitions them but does not yet run a coding agent.
```

Append to `docs/decisions.md`:

```markdown
## 2026-09-19 — Dispatcher comments carry a marker

Decided: every comment the dispatcher posts starts with `[HiveDispatch]`. Why: with a personal API token the dispatcher's comments are authored by the same Jira user as the human's, so author identity cannot separate questions from answers. The marker can.

## 2026-09-19 — Humans move a ticket back to Ready after answering

Decided: after `needs_info`, the ticket sits in Needs Info until a human answers and transitions it back to Ready; the next poll detects the reply and resumes. Rejected: polling Needs Info tickets for new comments. Why: an explicit transition is a deliberate "go" from a phone, and it keeps the trigger JQL the single definition of "work I may take". Automatic resume can be added later without changing the resume path.

## 2026-09-19 — Two-context shutdown: drain, then interrupt

Decided: the first Ctrl-C stops polling and lets the run in flight finish; the second cancels the run, and cleanup (safety commit, comment, release) still happens on a background context. Why: a safe stop beats a punctual one, but an operator must always be able to stop a runaway run without leaving a claimed ticket behind.

## 2026-09-19 — Dispatcher returns OutcomeFailed with an error on PR failure

Decided: if the branch pushed but the PR could not be opened, the ticket returns to Ready with a comment and the call returns an error. Why: the branch exists, so the next attempt finds or opens the PR without redoing the work; Ready is the only state the poller will pick up again.
```

- [ ] **Step 5: Commit and push**

```bash
git add -A
git commit -m "feat(cli): run command with drain-then-interrupt shutdown; fake executor wiring

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
git push
```

- [ ] **Step 6: Manual verification against Jira (needs the user's config)**

With `HIVE_JIRA_TOKEN` exported and a ticket in the trigger status:

```sh
# Terminal 1 and 2, started within a second of each other:
./bin/hivedispatch run -once
./bin/hivedispatch run -once
```

Expected: one process logs `outcome=completed`, the other `outcome=claim_lost` or `outcome=skipped`; the ticket has exactly one `[HiveDispatch] Finished with no code changes.` comment, is in `In Review`, and both claim fields are empty. Then move it back to Ready and run `./bin/hivedispatch run` (no `-once`) to watch the loop poll, handle it, and idle; Ctrl-C once to drain, confirm exit.

Record anything unexpected in `docs/decisions.md`.

---

## Self-review

**Spec coverage (Phase 2 of the MVP strategy):**
- Poll + claim with read-back, heartbeat, stale-claim reclaim — Task 7 (`Handle`, `heartbeat`), Task 8 tests. ✔
- Fake executor commenting "would have worked" — Task 1, wired in Task 9. ✔
- Two workers against one ticket — Task 8 race test + Task 9 manual check. ✔
- Every `Result.Status`/`StopCause` leaves the ticket in a documented state with a comment — Task 6 templates, Task 7 tests (`TestExecutorFailedRetries` table, needs-input, completed with/without PR, exhaustion, error, timeout). ✔
- Run windows with drain rather than abort — Task 5 `schedule`, `Once` gate, `Run(loopCtx, runCtx)` in Task 7/9. ✔
- Attempt cap → NeedsHuman — Task 7. ✔
- Safety commit on every exit, never a PR from an incomplete run — `execute` calls `Finalize` before branching on status; PR only under `StatusCompleted`. ✔
- Claim released on every path — `defer d.release` immediately after a won claim. ✔
- Interfaces for Phases 3–5: `Workspaces`, `GitHost`, `RunStore`, `Executor`, `Triager` — Tasks 1–4. ✔

**Type consistency:** `executor.Result`/`Task`/`Status`/`Cause` (Task 1) used identically in Tasks 6–8. `triage.Decision{Kind, Reason, Prompt, Question}` (Task 2) matches `handle.go`. `gitops.Workspace{Path, Branch}` and `Finalize(ctx, ws, message) (bool, error)` (Task 3) match `execute`. `state.Run` field names (Task 4) match `handle.go` and tests. `tracker/fake.OverwriteClaim` and `PollCount` are added in Tasks 7 and 8 before use. `Marker` (Task 6) is used by Tasks 7–8 tests and `prompt_test.go` hard-codes the same string.

**Placeholder scan:** none.
