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
	URL         string    `json:"url,omitempty"` // the ticket this record belongs to; a key can be reused across trackers
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
	// List returns every run record.
	List(ctx context.Context) ([]Run, error)
	// Prune deletes raw logs older than before and finished runs
	// (PhaseDone) not updated since before. events.jsonl is kept. It
	// returns how many files were removed.
	Prune(ctx context.Context, before time.Time) (int, error)
}
