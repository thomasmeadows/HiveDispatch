// Package prefixed puts a repository's ticket_prefix in front of its
// tracker's own keys, so tickets read prefix-board-number (JIRA-SCRUM-4,
// GITHUB-HIVEDISPATCH-12) everywhere HiveDispatch shows or routes them,
// while the tracker keeps working with the keys it knows (SCRUM-4).
package prefixed

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// Tracker wraps Inner, adding Prefix to outgoing keys and removing it from
// incoming ones.
type Tracker struct {
	Inner  tracker.Tracker
	Prefix string // upper-case, no dash
}

var _ tracker.Tracker = (*Tracker)(nil)

// New wraps inner with prefix.
func New(inner tracker.Tracker, prefix string) *Tracker {
	return &Tracker{Inner: inner, Prefix: strings.ToUpper(prefix)}
}

func (p *Tracker) out(t tracker.Ticket) tracker.Ticket {
	t.Key = p.Prefix + "-" + t.Key
	return t
}

// in strips the prefix; a key without it is not this tracker's.
func (p *Tracker) in(key string) (string, error) {
	native, ok := strings.CutPrefix(strings.ToUpper(key), p.Prefix+"-")
	if !ok || native == "" {
		return "", fmt.Errorf("%w: %s does not start with %s-", tracker.ErrNotFound, key, p.Prefix)
	}
	return native, nil
}

// Poll implements tracker.Tracker.
func (p *Tracker) Poll(ctx context.Context) ([]tracker.Ticket, error) {
	ts, err := p.Inner.Poll(ctx)
	for i := range ts {
		ts[i] = p.out(ts[i])
	}
	return ts, err
}

// Get implements tracker.Tracker.
func (p *Tracker) Get(ctx context.Context, key string) (tracker.Ticket, error) {
	native, err := p.in(key)
	if err != nil {
		return tracker.Ticket{}, err
	}
	t, err := p.Inner.Get(ctx, native)
	if err != nil {
		return t, err
	}
	return p.out(t), nil
}

// Claim implements tracker.Tracker.
func (p *Tracker) Claim(ctx context.Context, key, agentID string, at time.Time) (bool, error) {
	native, err := p.in(key)
	if err != nil {
		return false, err
	}
	return p.Inner.Claim(ctx, native, agentID, at)
}

// Heartbeat implements tracker.Tracker.
func (p *Tracker) Heartbeat(ctx context.Context, key, agentID string) error {
	native, err := p.in(key)
	if err != nil {
		return err
	}
	return p.Inner.Heartbeat(ctx, native, agentID)
}

// Release implements tracker.Tracker.
func (p *Tracker) Release(ctx context.Context, key, agentID string) error {
	native, err := p.in(key)
	if err != nil {
		return err
	}
	return p.Inner.Release(ctx, native, agentID)
}

// Comment implements tracker.Tracker.
func (p *Tracker) Comment(ctx context.Context, key, body string) error {
	native, err := p.in(key)
	if err != nil {
		return err
	}
	return p.Inner.Comment(ctx, native, body)
}

// Transition implements tracker.Tracker.
func (p *Tracker) Transition(ctx context.Context, key string, to tracker.State) error {
	native, err := p.in(key)
	if err != nil {
		return err
	}
	return p.Inner.Transition(ctx, native, to)
}
