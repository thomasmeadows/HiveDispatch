// Package executor defines the boundary between the orchestrator and a
// coding-agent CLI.
//
// The orchestrator guarantees the adapter a prepared workspace, a rendered
// prompt, and bounded resources. The adapter guarantees back a terminal
// status and a human-readable summary, always — even on failure.
package executor

import (
	"context"
	"encoding/json"
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
	Model       string // the agent's model; wins over the repository policy's
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
	// Steps is what the CLI did, in order, for tracing; may be empty.
	Steps []Step
	// Usage is the run's model and token totals, when the CLI reports them.
	Usage Usage
}

// StepKind is what a Step was.
type StepKind string

// Step kinds.
const (
	StepTool    StepKind = "tool"    // the agent acted: a tool call, command or file change
	StepMessage StepKind = "message" // the agent said or reasoned something
)

// Step is one thing a coding-agent CLI did during a run, timed by when its
// events arrived on the stream (the streams carry no timestamps of their
// own). A step whose result never arrived has a zero End.
type Step struct {
	Kind    StepKind
	Name    string // the tool, or "assistant" / "reasoning" for a message
	Input   string // the tool's input: JSON or a command line
	Output  string // the tool's result or the message text, capped at MaxStepOutput
	IsError bool
	Start   time.Time
	End     time.Time
}

// MaxStepOutput caps a Step's Output, so a run that reads large files does
// not hold them all in memory for its trace.
const MaxStepOutput = 16 << 10

// MaxSteps caps how many Steps a run records; later steps are not kept.
const MaxSteps = 2000

// CapOutput truncates s to MaxStepOutput.
func CapOutput(s string) string {
	if len(s) <= MaxStepOutput {
		return s
	}
	return s[:MaxStepOutput] + "…[truncated]"
}

// Usage is a run's model and token totals as the CLI reported them.
type Usage struct {
	Model        string
	InputTokens  int
	OutputTokens int
	CostUSD      float64 // 0 when the CLI does not say
}

// AdviceKind is what a read-only run is for.
type AdviceKind string

// The read-only runs planning and review agents make.
const (
	AdvicePlan   AdviceKind = "plan"   // a planning agent planning a ticket
	AdviceReview AdviceKind = "review" // a review agent reviewing a pull request
)

// Advice is a read-only run: the agent may inspect Workspace but changes
// nothing, and answers with JSON matching Schema.
type Advice struct {
	Kind      AdviceKind
	TicketKey string
	Prompt    string
	Workspace string
	Schema    string // JSON Schema of the answer
	Model     string // the agent's model; wins over the repository policy's
}

// Executor wraps one coding-agent CLI. Timeouts ride on the context.
type Executor interface {
	Name() string
	Plan(ctx context.Context, t Task) (Footprint, error)
	Run(ctx context.Context, t Task) (Result, error)
	// Advise makes a read-only run and returns its JSON answer.
	Advise(ctx context.Context, a Advice) (json.RawMessage, error)
}
