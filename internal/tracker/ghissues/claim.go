package ghissues

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// markerPrefix opens the hidden claim comment at the end of an issue body.
const markerPrefix = "<!-- hivedispatch-claim:"

// splitMarker separates the human-readable body from the claim marker.
// Every marker is removed from the body; the last one defines the claim.
func splitMarker(body string) (clean string, claim *tracker.Claim) {
	var sb strings.Builder
	rest := body
	for {
		idx := strings.Index(rest, markerPrefix)
		if idx < 0 {
			sb.WriteString(rest)
			break
		}
		sb.WriteString(rest[:idx])
		rest = rest[idx+len(markerPrefix):]
		inner := rest
		if end := strings.Index(rest, "-->"); end >= 0 {
			inner = rest[:end]
			rest = rest[end+len("-->"):]
		} else {
			rest = ""
		}
		claim = parseMarker(inner)
	}
	return strings.TrimRight(sb.String(), " \t\r\n"), claim
}

func parseMarker(inner string) *tracker.Claim {
	fields := strings.Fields(inner)
	if len(fields) == 0 {
		return nil
	}
	c := &tracker.Claim{AgentID: fields[0]}
	if len(fields) > 1 {
		if t, err := time.Parse(time.RFC3339, fields[1]); err == nil {
			c.At = t.UTC()
		}
	}
	return c
}

// withMarker appends a claim marker to a clean body.
func withMarker(clean, agent string, at time.Time) string {
	return clean + "\n\n" + markerPrefix + " " + agent + " " + at.UTC().Format(time.RFC3339) + " -->"
}

func unmarshal(raw []byte, v any) error {
	return jsonUnmarshal(raw, v)
}

// Claim appends the marker to the body, then re-reads the issue. It
// reports won=true only when the read-back still shows agentID.
func (c *Client) Claim(ctx context.Context, key, agentID string, at time.Time) (bool, error) {
	repo, n, err := c.keyToRef(key)
	if err != nil {
		return false, err
	}
	var is issueJSON
	if _, err := c.do(ctx, http.MethodGet, issuePath(repo, n, ""), nil, &is); err != nil {
		return false, err
	}
	clean, _ := splitMarker(is.Body)
	if err := c.patchBody(ctx, repo, n, withMarker(clean, agentID, at)); err != nil {
		return false, err
	}
	if c.beforeReadBack != nil {
		c.beforeReadBack(key)
	}
	if _, err := c.do(ctx, http.MethodGet, issuePath(repo, n, ""), nil, &is); err != nil {
		return false, fmt.Errorf("ghissues: claim read-back %s: %w", key, err)
	}
	_, claim := splitMarker(is.Body)
	return claim != nil && claim.AgentID == agentID, nil
}

// Heartbeat refreshes the marker timestamp if agentID holds the claim.
func (c *Client) Heartbeat(ctx context.Context, key, agentID string) error {
	repo, n, clean, err := c.requireHolder(ctx, key, agentID)
	if err != nil {
		return err
	}
	return c.patchBody(ctx, repo, n, withMarker(clean, agentID, c.now()))
}

// Release removes the marker if agentID holds the claim.
func (c *Client) Release(ctx context.Context, key, agentID string) error {
	repo, n, clean, err := c.requireHolder(ctx, key, agentID)
	if err != nil {
		return err
	}
	return c.patchBody(ctx, repo, n, clean)
}

func (c *Client) requireHolder(ctx context.Context, key, agentID string) (repo string, n int, clean string, err error) {
	repo, n, err = c.keyToRef(key)
	if err != nil {
		return "", 0, "", err
	}
	var is issueJSON
	if _, err := c.do(ctx, http.MethodGet, issuePath(repo, n, ""), nil, &is); err != nil {
		return "", 0, "", err
	}
	clean, claim := splitMarker(is.Body)
	if claim == nil || claim.AgentID != agentID {
		return "", 0, "", tracker.ErrNotClaimHolder
	}
	return repo, n, clean, nil
}

// patchBody rewrites only the issue body.
func (c *Client) patchBody(ctx context.Context, repo string, n int, body string) error {
	_, err := c.do(ctx, http.MethodPatch, issuePath(repo, n, ""), map[string]string{"body": body}, nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden {
		return fmt.Errorf("%w\n  hint: the GitHub token cannot edit issues — grant it Issues: Read and write (fine-grained) or the repo scope (classic)", err)
	}
	return err
}
