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

// candidates are the agents that may work t: the one its hive:agent:<name>
// label pins it to, else every agent of repo.
func candidates(repo config.RepoConfig, t tracker.Ticket) ([]config.Agent, error) {
	agents := agentsOf(repo)
	for _, l := range t.Labels {
		if len(l) <= len(config.PinLabelPrefix) || !strings.EqualFold(l[:len(config.PinLabelPrefix)], config.PinLabelPrefix) {
			continue
		}
		name := l[len(config.PinLabelPrefix):]
		for _, a := range agents {
			if strings.EqualFold(a.Name, name) {
				return []config.Agent{a}, nil
			}
		}
		return nil, fmt.Errorf("labelled %s, but %s has no agent %q in %s/%s", l, repo.Name, name, config.RepoDir, config.AgentsFileName)
	}
	return agents, nil
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
	if e, ok := d.Executors[a.Executor]; ok {
		return e
	}
	return d.Executor
}
