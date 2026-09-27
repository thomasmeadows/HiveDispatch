// Package fake is a scripted executor.Executor for tests and for proving
// the coordination loop before a real agent is wired in.
package fake

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	// Placeholder writes a file into the workspace so the git path is exercised.
	Placeholder bool
	// Gate, when set, holds every run until it is closed (or the run's
	// context ends), so tests can observe runs in flight.
	Gate chan struct{}

	mu           sync.Mutex
	calls        []executor.Task
	active, peak int
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
	e.active++
	e.peak = max(e.peak, e.active)
	gate := e.Gate
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.active--
		e.mu.Unlock()
	}()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
		}
	}

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
	if e.Placeholder && t.Workspace != "" {
		name := "HIVEDISPATCH_PLACEHOLDER.md"
		body := fmt.Sprintf("# %s\n\nPlaceholder written by the fake executor.\n", t.TicketKey)
		if err := os.WriteFile(filepath.Join(t.Workspace, name), []byte(body), 0o644); err != nil {
			return executor.Result{}, err
		}
		res.ChangedFiles = append(res.ChangedFiles, name)
	}
	return res, nil
}

// Active is how many runs are in flight now.
func (e *Executor) Active() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.active
}

// Peak is the most runs that were ever in flight at once.
func (e *Executor) Peak() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.peak
}

// Calls returns every task Run has received.
func (e *Executor) Calls() []executor.Task {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]executor.Task(nil), e.calls...)
}
