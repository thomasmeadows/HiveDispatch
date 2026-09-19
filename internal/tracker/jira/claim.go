package jira

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// Claim writes agentID and at to the claim fields, then re-reads the issue.
// It reports won=true only when the read-back still shows agentID.
func (c *Client) Claim(ctx context.Context, key, agentID string, at time.Time) (bool, error) {
	if err := c.setClaimFields(ctx, key, agentID, at.UTC().Format(jiraTime)); err != nil {
		return false, err
	}
	if c.beforeReadBack != nil {
		c.beforeReadBack(key)
	}
	t, err := c.Get(ctx, key)
	if err != nil {
		return false, fmt.Errorf("jira: claim read-back %s: %w", key, err)
	}
	return t.Claim != nil && t.Claim.AgentID == agentID, nil
}

// Heartbeat refreshes claimed_at if agentID holds the claim.
func (c *Client) Heartbeat(ctx context.Context, key, agentID string) error {
	if err := c.requireHolder(ctx, key, agentID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPut, issuePath(key, ""), map[string]any{
		"fields": map[string]any{c.cfg.Fields.ClaimedAt: c.now().UTC().Format(jiraTime)},
	}, nil)
}

// Release clears both claim fields if agentID holds the claim.
func (c *Client) Release(ctx context.Context, key, agentID string) error {
	if err := c.requireHolder(ctx, key, agentID); err != nil {
		return err
	}
	return c.setClaimFields(ctx, key, nil, nil)
}

func (c *Client) requireHolder(ctx context.Context, key, agentID string) error {
	t, err := c.Get(ctx, key)
	if err != nil {
		return err
	}
	if t.Claim == nil || t.Claim.AgentID != agentID {
		return tracker.ErrNotClaimHolder
	}
	return nil
}

// setClaimFields writes both claim fields; nil values clear them.
func (c *Client) setClaimFields(ctx context.Context, key string, agentID, at any) error {
	err := c.do(ctx, http.MethodPut, issuePath(key, ""), map[string]any{
		"fields": map[string]any{
			c.cfg.Fields.AgentID:   agentID,
			c.cfg.Fields.ClaimedAt: at,
		},
	}, nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusBadRequest &&
		strings.Contains(err.Error(), "not on the appropriate screen") {
		return fmt.Errorf("%w\n  hint: the claim custom fields must be on the issue's edit screen — see docs/setup.md", err)
	}
	return err
}
