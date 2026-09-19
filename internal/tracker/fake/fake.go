// Package fake is an in-memory tracker.Tracker for tests.
//
// BeforeReadBack lets a test inject a competing write between Claim's write
// and its read-back, which is how the claim race is exercised without a
// real tracker.
package fake

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// Tracker is an in-memory tracker.Tracker.
type Tracker struct {
	// BeforeReadBack, if set, runs between Claim's write and read-back.
	BeforeReadBack func(key string)
	// Now returns the current time; defaults to time.Now.
	Now func() time.Time

	mu          sync.Mutex
	tickets     map[string]*tracker.Ticket
	comments    map[string][]string
	transitions map[string][]tracker.State
	nextID      int
}

var _ tracker.Tracker = (*Tracker)(nil)

// New returns an empty fake tracker.
func New() *Tracker {
	return &Tracker{
		Now:         time.Now,
		tickets:     map[string]*tracker.Ticket{},
		comments:    map[string][]string{},
		transitions: map[string][]tracker.State{},
	}
}

// Add seeds a ticket. An empty Status defaults to "ready".
func (f *Tracker) Add(t tracker.Ticket) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t.Status == "" {
		t.Status = string(tracker.StateReady)
	}
	c := t
	f.tickets[t.Key] = &c
}

// Comments returns the bodies posted to key, in order.
func (f *Tracker) Comments(key string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.comments[key]...)
}

// Transitions returns the states key was transitioned to, in order.
func (f *Tracker) Transitions(key string) []tracker.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]tracker.State(nil), f.transitions[key]...)
}

// overwriteClaim sets a claim unconditionally; used by race tests.
func (f *Tracker) overwriteClaim(key, agentID string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.tickets[key]; ok {
		t.Claim = &tracker.Claim{AgentID: agentID, At: at}
	}
}

func copyTicket(t *tracker.Ticket) tracker.Ticket {
	c := *t
	c.Labels = append([]string(nil), t.Labels...)
	c.Comments = append([]tracker.Comment(nil), t.Comments...)
	if t.Claim != nil {
		cl := *t.Claim
		c.Claim = &cl
	}
	return c
}

// Poll returns tickets in the ready state.
func (f *Tracker) Poll(_ context.Context) ([]tracker.Ticket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tracker.Ticket
	for _, t := range f.tickets {
		if t.Status == string(tracker.StateReady) {
			out = append(out, copyTicket(t))
		}
	}
	return out, nil
}

// Get returns a copy of the ticket.
func (f *Tracker) Get(_ context.Context, key string) (tracker.Ticket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tickets[key]
	if !ok {
		return tracker.Ticket{}, tracker.ErrNotFound
	}
	return copyTicket(t), nil
}

// Claim writes the claim, runs BeforeReadBack, then re-reads.
func (f *Tracker) Claim(_ context.Context, key, agentID string, at time.Time) (bool, error) {
	f.mu.Lock()
	t, ok := f.tickets[key]
	if !ok {
		f.mu.Unlock()
		return false, tracker.ErrNotFound
	}
	t.Claim = &tracker.Claim{AgentID: agentID, At: at}
	f.mu.Unlock()

	if f.BeforeReadBack != nil {
		f.BeforeReadBack(key)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	return t.Claim != nil && t.Claim.AgentID == agentID, nil
}

func (f *Tracker) holder(key, agentID string) (*tracker.Ticket, error) {
	t, ok := f.tickets[key]
	if !ok {
		return nil, tracker.ErrNotFound
	}
	if t.Claim == nil || t.Claim.AgentID != agentID {
		return nil, tracker.ErrNotClaimHolder
	}
	return t, nil
}

// Heartbeat refreshes the claim timestamp for the holder.
func (f *Tracker) Heartbeat(_ context.Context, key, agentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, err := f.holder(key, agentID)
	if err != nil {
		return err
	}
	t.Claim.At = f.Now()
	return nil
}

// Release clears the claim for the holder.
func (f *Tracker) Release(_ context.Context, key, agentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, err := f.holder(key, agentID)
	if err != nil {
		return err
	}
	t.Claim = nil
	return nil
}

// Comment appends body to the ticket thread.
func (f *Tracker) Comment(_ context.Context, key, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tickets[key]
	if !ok {
		return tracker.ErrNotFound
	}
	f.nextID++
	t.Comments = append(t.Comments, tracker.Comment{
		ID:      fmt.Sprint(f.nextID),
		Author:  "fake",
		Body:    body,
		Created: f.Now(),
	})
	f.comments[key] = append(f.comments[key], body)
	return nil
}

// Transition sets the ticket status to the state's string form.
func (f *Tracker) Transition(_ context.Context, key string, to tracker.State) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tickets[key]
	if !ok {
		return tracker.ErrNotFound
	}
	if !to.Valid() {
		return fmt.Errorf("fake: invalid state %q", to)
	}
	t.Status = string(to)
	f.transitions[key] = append(f.transitions[key], to)
	return nil
}
