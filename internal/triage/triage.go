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
