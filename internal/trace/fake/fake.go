// Package fake is an in-memory trace.Exporter for tests.
package fake

import (
	"context"
	"sync"

	"github.com/thomasmeadows/hivedispatch/internal/trace"
)

// Exporter records every run it is handed.
type Exporter struct {
	mu      sync.Mutex
	started []trace.Run
	ended   []trace.Run
	closed  bool
}

var _ trace.Exporter = (*Exporter)(nil)

// Start implements trace.Exporter.
func (e *Exporter) Start(r trace.Run) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.started = append(e.started, r)
}

// End implements trace.Exporter.
func (e *Exporter) End(r trace.Run) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ended = append(e.ended, r)
}

// Close implements trace.Exporter.
func (e *Exporter) Close(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	return nil
}

// Started returns the runs started so far, in order.
func (e *Exporter) Started() []trace.Run {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]trace.Run(nil), e.started...)
}

// Ended returns the runs ended so far, in the order they ended.
func (e *Exporter) Ended() []trace.Run {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]trace.Run(nil), e.ended...)
}

// Closed reports whether Close was called.
func (e *Exporter) Closed() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closed
}

// Named returns the ended runs called name.
func (e *Exporter) Named(name string) []trace.Run {
	var out []trace.Run
	for _, r := range e.Ended() {
		if r.Name == name {
			out = append(out, r)
		}
	}
	return out
}

// Children returns the ended runs whose parent is id, in the order they
// ended.
func (e *Exporter) Children(id string) []trace.Run {
	var out []trace.Run
	for _, r := range e.Ended() {
		if r.ParentID == id {
			out = append(out, r)
		}
	}
	return out
}
