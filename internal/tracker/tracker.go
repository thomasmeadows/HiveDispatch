// Package tracker defines the issue-tracker boundary.
//
// The tracker owns the queue and the claims. Implementations must make
// Claim an optimistic write followed by a read-back so that two workers
// racing for the same ticket resolve to exactly one winner.
package tracker

import (
	"context"
	"errors"
	"time"
)

// State is a HiveDispatch lifecycle state. Implementations map it to their
// own status vocabulary.
type State string

// Lifecycle states. Transient states (triaged, claimed, running) are not
// tracker states; they live in the run file.
const (
	StateReady      State = "ready"
	StateInProgress State = "in_progress"
	StateNeedsInfo  State = "needs_info"
	StateInReview   State = "in_review"
	StateNeedsHuman State = "needs_human"
)

// Valid reports whether s is one of the defined states.
func (s State) Valid() bool {
	switch s {
	case StateReady, StateInProgress, StateNeedsInfo, StateInReview, StateNeedsHuman:
		return true
	}
	return false
}

// Claim records which agent holds a ticket and when it last heartbeat.
type Claim struct {
	AgentID string
	At      time.Time
}

// Fresh reports whether the claim was heartbeat within timeout of now.
// A nil claim is never fresh.
func (c *Claim) Fresh(now time.Time, timeout time.Duration) bool {
	if c == nil || c.AgentID == "" {
		return false
	}
	return now.Sub(c.At) < timeout
}

// Comment is one entry in a ticket's thread, body rendered as plain text.
type Comment struct {
	ID       string
	AuthorID string
	Author   string
	Body     string
	Created  time.Time
}

// Ticket is a tracker-agnostic view of an issue.
type Ticket struct {
	Key         string
	Summary     string
	Description string // plain text
	Status      string // raw tracker status name
	URL         string
	Labels      []string
	Comments    []Comment
	Claim       *Claim // nil when unclaimed
	Updated     time.Time
}

// Sentinel errors returned by implementations.
var (
	ErrNotFound       = errors.New("tracker: ticket not found")
	ErrNotClaimHolder = errors.New("tracker: not the claim holder")
)

// Tracker is the issue-tracker boundary. All methods are safe for
// concurrent use.
type Tracker interface {
	// Poll returns every ticket matching the configured trigger query.
	Poll(ctx context.Context) ([]Ticket, error)
	// Get returns one ticket by key, or ErrNotFound.
	Get(ctx context.Context, key string) (Ticket, error)
	// Claim writes agentID and at to the ticket, then re-reads it. It
	// returns won=true only if the read-back still shows agentID.
	Claim(ctx context.Context, key, agentID string, at time.Time) (won bool, err error)
	// Heartbeat refreshes the claim timestamp. Returns ErrNotClaimHolder
	// if agentID no longer holds the claim.
	Heartbeat(ctx context.Context, key, agentID string) error
	// Release clears the claim if agentID holds it. Releasing a claim
	// held by someone else returns ErrNotClaimHolder.
	Release(ctx context.Context, key, agentID string) error
	// Comment posts body (plain text; implementations render it) to the ticket.
	Comment(ctx context.Context, key, body string) error
	// Transition moves the ticket to the tracker status mapped from to.
	Transition(ctx context.Context, key string, to State) error
}
