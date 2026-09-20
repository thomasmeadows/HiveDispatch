// Package dispatch is the deterministic control loop: poll, claim, triage,
// execute, report. It decides nothing about code and nothing about which
// tickets are worth attempting; those decisions come from the Triager and
// the Executor and this package carries them out.
package dispatch

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/githost"
	"github.com/thomasmeadows/hivedispatch/internal/gitops"
	"github.com/thomasmeadows/hivedispatch/internal/schedule"
	"github.com/thomasmeadows/hivedispatch/internal/state"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
	"github.com/thomasmeadows/hivedispatch/internal/triage"
)

// Config is the subset of worker config the dispatcher needs.
type Config struct {
	AgentID           string
	ClaimTimeout      time.Duration
	HeartbeatInterval time.Duration
	RunTimeout        time.Duration
	PollInterval      time.Duration
	PollJitter        time.Duration
	StepBudget        int
	MaxAttempts       int
	Repos             []config.RepoConfig
}

// ConfigFrom extracts the dispatcher config from the worker config.
func ConfigFrom(c *config.Config) Config {
	return Config{
		AgentID:           c.AgentID,
		ClaimTimeout:      c.ClaimTimeout,
		HeartbeatInterval: c.HeartbeatInterval,
		RunTimeout:        c.RunTimeout,
		PollInterval:      c.PollInterval,
		PollJitter:        c.PollJitter,
		StepBudget:        c.StepBudget,
		MaxAttempts:       c.MaxAttempts,
		Repos:             c.Repos,
	}
}

// Dispatcher runs the control loop for one worker.
type Dispatcher struct {
	Cfg        Config
	Tracker    tracker.Tracker
	Triager    triage.Triager
	Executor   executor.Executor
	Workspaces gitops.Workspaces
	Host       githost.GitHost
	Store      state.RunStore
	Schedule   *schedule.Schedule // nil = always open
	Now        func() time.Time   // nil = time.Now
	Log        *slog.Logger       // nil = slog.Default()

	pauseMu     sync.Mutex
	pausedUntil time.Time // no new work is started before this

	// Polled is called after each successful poll with the time it happened.
	// The CLI uses it for its status line; nil = no-op. Polling itself is
	// deliberately not logged: it happens every few seconds and says nothing.
	Polled func(at time.Time)
}

// pauseMargin is added to a provider's reset time so the first poll after
// a quota reset does not land a few seconds early.
const pauseMargin = time.Minute

// defaultBudgetBackoff is the pause when the provider gave no reset time.
const defaultBudgetBackoff = 15 * time.Minute

// pauseFor stops new work until until, keeping the later of any existing
// pause. A zero until means "unknown reset": use the default backoff.
func (d *Dispatcher) pauseFor(until time.Time) {
	if until.IsZero() {
		until = d.now().Add(defaultBudgetBackoff)
	} else {
		until = until.Add(pauseMargin)
	}
	d.pauseMu.Lock()
	defer d.pauseMu.Unlock()
	if until.After(d.pausedUntil) {
		d.pausedUntil = until
		d.log().Warn("budget exhausted; not starting new work", "until", until.UTC().Format(time.RFC3339))
	}
}

// PausedUntil reports when polling resumes after a budget stop; zero when
// not paused.
func (d *Dispatcher) PausedUntil() time.Time {
	d.pauseMu.Lock()
	defer d.pauseMu.Unlock()
	return d.pausedUntil
}

// Outcome summarises what Handle did with a ticket.
type Outcome string

// Outcomes.
const (
	OutcomeSkipped    Outcome = "skipped"
	OutcomeClaimLost  Outcome = "claim_lost"
	OutcomeNeedsInfo  Outcome = "needs_info"
	OutcomeRejected   Outcome = "rejected"
	OutcomeCompleted  Outcome = "completed"
	OutcomeNeedsInput Outcome = "needs_input"
	OutcomeFailed     Outcome = "failed"
)

func (d *Dispatcher) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Dispatcher) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

// repoFor maps a ticket key to the repo whose project prefix matches.
func (d *Dispatcher) repoFor(key string) (config.RepoConfig, bool) {
	project, _, ok := strings.Cut(key, "-")
	if !ok {
		return config.RepoConfig{}, false
	}
	for _, r := range d.Cfg.Repos {
		if strings.EqualFold(r.Project, project) {
			return r, true
		}
	}
	return config.RepoConfig{}, false
}

// Once polls and handles every ticket, unless the schedule is closed.
// It returns how many tickets were actually worked (not skipped or lost).
func (d *Dispatcher) Once(ctx context.Context) (int, error) {
	if !d.Schedule.Open(d.now()) {
		d.log().Debug("outside run window; not polling")
		return 0, nil
	}
	if until := d.PausedUntil(); d.now().Before(until) {
		d.log().Debug("paused after budget stop; not polling", "until", until)
		return 0, nil
	}
	tickets, err := d.Tracker.Poll(ctx)
	if err != nil {
		return 0, err
	}
	if d.Polled != nil {
		d.Polled(d.now())
	}
	handled := 0
	for _, t := range tickets {
		if ctx.Err() != nil {
			break
		}
		// Handle logs the lifecycle of every ticket it works; only the
		// error path needs a line here.
		out, err := d.Handle(ctx, t)
		if err != nil {
			d.log().Error("handle failed", "ticket", t.Key, "outcome", out, "err", err)
		}
		if out != OutcomeSkipped && out != OutcomeClaimLost {
			handled++
		}
	}
	return handled, nil
}

// Run polls on the configured interval until loopCtx is cancelled. Runs in
// flight use runCtx, so a caller can stop polling (drain) before it hard-
// cancels work. Pass the same context for both to stop immediately.
func (d *Dispatcher) Run(loopCtx, runCtx context.Context) error {
	for {
		if _, err := d.Once(runCtx); err != nil {
			d.log().Error("poll failed", "err", err)
		}
		wait := d.Cfg.PollInterval
		if d.Cfg.PollJitter > 0 {
			wait += time.Duration(rand.Int64N(int64(d.Cfg.PollJitter)))
		}
		select {
		case <-loopCtx.Done():
			return loopCtx.Err()
		case <-time.After(wait):
		}
	}
}
