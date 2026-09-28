// Package router fans a single tracker.Tracker out to one tracker per
// project, so the dispatcher sees one queue while each repository keeps its
// own tracker — Jira for one, GitHub Issues for another.
package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// Router routes by the ticket key's project prefix.
type Router struct {
	ByProject map[string]tracker.Tracker // upper-case project key → tracker
	Log       *slog.Logger               // nil = slog.Default()
}

var _ tracker.Tracker = (*Router)(nil)

func project(key string) string {
	p, _, _ := strings.Cut(key, "-")
	return strings.ToUpper(p)
}

func (r *Router) pick(key string) (tracker.Tracker, error) {
	if t, ok := r.ByProject[project(key)]; ok {
		return t, nil
	}
	return nil, fmt.Errorf("router: no tracker for project %q", project(key))
}

// Poll implements tracker.Tracker. It polls every tracker in project order
// and keeps only tickets of that tracker's own project, so a query shared
// by several repositories yields each ticket once. A failing tracker is
// logged and skipped — one unreachable site must not stall the others —
// and Poll errors only when every tracker fails.
func (r *Router) Poll(ctx context.Context, state tracker.State) ([]tracker.Ticket, error) {
	projects := make([]string, 0, len(r.ByProject))
	for p := range r.ByProject {
		projects = append(projects, p)
	}
	sort.Strings(projects)
	var out []tracker.Ticket
	var errs []error
	for _, p := range projects {
		tickets, err := r.ByProject[p].Poll(ctx, state)
		if err != nil {
			r.log().Warn("poll failed", "project", p, "err", err)
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
			continue
		}
		for _, t := range tickets {
			if project(t.Key) == p {
				out = append(out, t)
			}
		}
	}
	if len(errs) > 0 && len(errs) == len(projects) {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

func (r *Router) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// Get implements tracker.Tracker.
func (r *Router) Get(ctx context.Context, key string) (tracker.Ticket, error) {
	t, err := r.pick(key)
	if err != nil {
		return tracker.Ticket{}, err
	}
	return t.Get(ctx, key)
}

// Claim implements tracker.Tracker.
func (r *Router) Claim(ctx context.Context, key, agentID string, at time.Time) (bool, error) {
	t, err := r.pick(key)
	if err != nil {
		return false, err
	}
	return t.Claim(ctx, key, agentID, at)
}

// Heartbeat implements tracker.Tracker.
func (r *Router) Heartbeat(ctx context.Context, key, agentID string) error {
	t, err := r.pick(key)
	if err != nil {
		return err
	}
	return t.Heartbeat(ctx, key, agentID)
}

// Release implements tracker.Tracker.
func (r *Router) Release(ctx context.Context, key, agentID string) error {
	t, err := r.pick(key)
	if err != nil {
		return err
	}
	return t.Release(ctx, key, agentID)
}

// Comment implements tracker.Tracker.
func (r *Router) Comment(ctx context.Context, key, body string) error {
	t, err := r.pick(key)
	if err != nil {
		return err
	}
	return t.Comment(ctx, key, body)
}

// Transition implements tracker.Tracker.
func (r *Router) Transition(ctx context.Context, key string, to tracker.State) error {
	t, err := r.pick(key)
	if err != nil {
		return err
	}
	return t.Transition(ctx, key, to)
}
