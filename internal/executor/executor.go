// Package executor defines the boundary between the orchestrator and a
// coding-agent CLI.
//
// The orchestrator guarantees the adapter a prepared workspace, a rendered
// prompt, and bounded resources. The adapter guarantees back a terminal
// status and a human-readable summary, always — even on failure.
package executor

import (
	"context"
	"time"
)

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
	// RetryAfter is when a Budget stop is expected to clear; zero if unknown.
	RetryAfter time.Time
}

// Executor wraps one coding-agent CLI. Timeouts ride on the context.
type Executor interface {
	Name() string
	Plan(ctx context.Context, t Task) (Footprint, error)
	Run(ctx context.Context, t Task) (Result, error)
}
