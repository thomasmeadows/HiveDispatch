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
