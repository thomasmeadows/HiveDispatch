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
	"sync/atomic"
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
	MaxConcurrent     int // tickets worked at once, each in its own worktree; <1 means 1
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
		MaxConcurrent:     c.MaxConcurrent,
		Repos:             c.Repos,
	}
}

// Dispatcher runs the control loop for one worker.
type Dispatcher struct {
	Cfg     Config
	Tracker tracker.Tracker
	Triager triage.Triager
	// Executors by agent kind (claude, codex, fake); Executor answers any
	// kind missing from the map.
	Executors  map[string]executor.Executor
	Executor   executor.Executor
	Workspaces gitops.Workspaces
	Host       githost.GitHost
	Store      state.RunStore
	Schedule   *schedule.Schedule // nil = always open
	Now        func() time.Time   // nil = time.Now
	Log        *slog.Logger       // nil = slog.Default()

	pauseMu     sync.Mutex
	pausedUntil time.Time // no new work is started before this

	agents agentPool // which repository agents are working a ticket

	slotsOnce sync.Once
	slots     chan struct{} // one token per ticket being worked
	flightMu  sync.Mutex
	inFlight  map[string]bool // ticket keys being worked
	running   sync.WaitGroup  // every ticket goroutine

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

// Once polls and handles every ticket, up to Cfg.MaxConcurrent at a time
// (each in its own worktree), unless the schedule is closed or a budget
// stop is in force. It waits for every ticket it started and returns how
// many were actually worked (not skipped or lost).
func (d *Dispatcher) Once(ctx context.Context) (int, error) {
	var wg sync.WaitGroup
	var handled atomic.Int64
	err := d.poll(ctx, true, &wg, &handled)
	wg.Wait()
	return int(handled.Load()), err
}

// poll polls the tracker and starts a goroutine per ready ticket that is not
// already being worked. With wait, it blocks for a free slot so every ticket
// is started; without, it starts what fits and leaves the rest for the next
// poll. wg and handled, when non-nil, track the tickets started here.
func (d *Dispatcher) poll(ctx context.Context, wait bool, wg *sync.WaitGroup, handled *atomic.Int64) error {
	if !d.Schedule.Open(d.now()) {
		d.log().Debug("outside run window; not polling")
		return nil
	}
	if until := d.PausedUntil(); d.now().Before(until) {
		d.log().Debug("paused after budget stop; not polling", "until", until)
		return nil
	}
	tickets, err := d.Tracker.Poll(ctx)
	if err != nil {
		return err
	}
	if d.Polled != nil {
		d.Polled(d.now())
	}
	for _, t := range tickets {
		if ctx.Err() != nil {
			break
		}
		repo, ok := d.repoFor(t.Key)
		if !ok {
			d.log().Warn("no repo configured for ticket", "ticket", t.Key)
			continue
		}
		cands, err := candidates(repo, t)
		if err != nil {
			d.log().Warn("ticket skipped", "ticket", t.Key, "err", err)
			continue
		}
		if !d.markInFlight(t.Key) {
			continue // already being worked by this worker
		}
		agent, ok := d.takeAgent(ctx, repo, cands, wait)
		if !ok {
			d.unmarkInFlight(t.Key)
			continue // its agents are busy: it waits for the next poll
		}
		if !d.takeSlot(ctx, wait) {
			d.releaseAgent(repo, agent)
			d.unmarkInFlight(t.Key)
			break // every slot is busy: the rest wait for the next poll
		}
		// A run that finished while we waited may have hit the budget.
		if until := d.PausedUntil(); d.now().Before(until) {
			d.releaseSlot()
			d.releaseAgent(repo, agent)
			d.unmarkInFlight(t.Key)
			break
		}
		d.running.Add(1)
		if wg != nil {
			wg.Add(1)
		}
		go func(t tracker.Ticket) {
			defer d.running.Done()
			if wg != nil {
				defer wg.Done()
			}
			defer d.unmarkInFlight(t.Key)
			defer d.releaseAgent(repo, agent)
			defer d.releaseSlot()
			// handle logs the lifecycle of every ticket it works; only
			// the error path needs a line here.
			out, err := d.handle(ctx, t, repo, agent)
			if err != nil {
				d.log().Error("handle failed", "ticket", t.Key, "outcome", out, "err", err)
			}
			if handled != nil && out != OutcomeSkipped && out != OutcomeClaimLost {
				handled.Add(1)
			}
		}(t)
	}
	return nil
}

func (d *Dispatcher) markInFlight(key string) bool {
	d.flightMu.Lock()
	defer d.flightMu.Unlock()
	if d.inFlight == nil {
		d.inFlight = map[string]bool{}
	}
	if d.inFlight[key] {
		return false
	}
	d.inFlight[key] = true
	return true
}

func (d *Dispatcher) unmarkInFlight(key string) {
	d.flightMu.Lock()
	defer d.flightMu.Unlock()
	delete(d.inFlight, key)
}

// takeSlot claims one of Cfg.MaxConcurrent slots, waiting for one when
// wait is set (until ctx ends).
func (d *Dispatcher) takeSlot(ctx context.Context, wait bool) bool {
	d.slotsOnce.Do(func() { d.slots = make(chan struct{}, max(d.Cfg.MaxConcurrent, 1)) })
	if !wait {
		select {
		case d.slots <- struct{}{}:
			return true
		default:
			return false
		}
	}
	select {
	case d.slots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (d *Dispatcher) releaseSlot() { <-d.slots }

// Run polls on the configured interval until loopCtx is cancelled, starting
// ready tickets as slots free up; polling continues while tickets run. Runs
// in flight use runCtx, so a caller can stop polling (drain) before it hard-
// cancels work; Run returns only once every run has finished. Pass the same
// context for both to stop immediately.
func (d *Dispatcher) Run(loopCtx, runCtx context.Context) error {
	defer d.running.Wait()
	for {
		if err := d.poll(runCtx, false, nil, nil); err != nil {
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
