package dispatch

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// agentPool tracks which of every repository's agents are working a ticket.
// Each agent works one ticket at a time.
type agentPool struct {
	mu      sync.Mutex
	busy    map[string]bool
	changed chan struct{} // closed and replaced whenever an agent frees up
}

func agentKey(repo config.RepoConfig, a config.Agent) string {
	return strings.ToUpper(repo.Project) + "/" + strings.ToLower(a.Name)
}

// agentsOf is repo's pool; a repository configured without agents (tests,
// or a RepoConfig built by hand) gets the default one.
func agentsOf(repo config.RepoConfig) []config.Agent {
	if len(repo.Agents) == 0 {
		return config.DefaultAgents()
	}
	return repo.Agents
}

// roleOf is an agent's role; an unset one (a hand-built RepoConfig) codes.
func roleOf(a config.Agent) string {
	if a.Role == "" {
		return config.RoleCoding
	}
	return a.Role
}

// stateFor is the board column agents of role take work from.
func stateFor(role string) tracker.State {
	switch role {
	case config.RolePlanning:
		return tracker.StatePlanning
	case config.RoleReview:
		return tracker.StateInReview
	}
	return tracker.StateReady
}

// hasRole reports whether any repository has an agent of role, so columns
// nobody works are not polled.
func (d *Dispatcher) hasRole(role string) bool {
	for _, r := range d.Cfg.Repos {
		for _, a := range agentsOf(r) {
			if roleOf(a) == role {
				return true
			}
		}
	}
	return false
}

// candidates are the agents of role that may work t: the one a
// hive:agent:<name> label pins it to, when that agent has this role, else
// every agent of role. A label naming no agent of the repository at all is
// an error; one naming an agent of another role pins the other column.
func candidates(repo config.RepoConfig, t tracker.Ticket, role string) ([]config.Agent, error) {
	agents := agentsOf(repo)
	var pool []config.Agent
	for _, a := range agents {
		if roleOf(a) == role {
			pool = append(pool, a)
		}
	}
	for _, l := range t.Labels {
		if len(l) <= len(config.PinLabelPrefix) || !strings.EqualFold(l[:len(config.PinLabelPrefix)], config.PinLabelPrefix) {
			continue
		}
		name := l[len(config.PinLabelPrefix):]
		known := false
		for _, a := range agents {
			if !strings.EqualFold(a.Name, name) {
				continue
			}
			known = true
			if roleOf(a) == role {
				return []config.Agent{a}, nil
			}
		}
		if !known {
			return nil, fmt.Errorf("labelled %s, but %s has no agent %q in %s/%s", l, repo.Name, name, config.RepoDir, config.AgentsFileName)
		}
	}
	return pool, nil
}

// takeAgent reserves the first free agent among cands, waiting for one when
// wait is set (until ctx ends).
func (d *Dispatcher) takeAgent(ctx context.Context, repo config.RepoConfig, cands []config.Agent, wait bool) (config.Agent, bool) {
	p := &d.agents
	for {
		p.mu.Lock()
		if p.busy == nil {
			p.busy = map[string]bool{}
			p.changed = make(chan struct{})
		}
		for _, a := range cands {
			if k := agentKey(repo, a); !p.busy[k] {
				p.busy[k] = true
				p.mu.Unlock()
				return a, true
			}
		}
		changed := p.changed
		p.mu.Unlock()
		if !wait {
			return config.Agent{}, false
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return config.Agent{}, false
		}
	}
}

func (d *Dispatcher) releaseAgent(repo config.RepoConfig, a config.Agent) {
	p := &d.agents
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.busy, agentKey(repo, a))
	close(p.changed)
	p.changed = make(chan struct{})
}

// executorFor is the executor for an agent's kind; Executor is the fallback
// (and, in tests, the only one).
func (d *Dispatcher) executorFor(a config.Agent) executor.Executor {
	e, ok := d.Executors[a.Executor]
	if !ok {
		e = d.Executor
	}
	if d.Tracer.Enabled() {
		return tracedExecutor{inner: e, tracer: d.Tracer, agent: a}
	}
	return e
}
