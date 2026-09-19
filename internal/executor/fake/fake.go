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
