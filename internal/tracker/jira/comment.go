package jira

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// Comment posts body as a plain-text comment (rendered to ADF).
func (c *Client) Comment(ctx context.Context, key, body string) error {
	return c.do(ctx, http.MethodPost, issuePath(key, "/comment"), map[string]any{
		"body": textToADF(body),
	}, nil)
}

type transitionsResponse struct {
	Transitions []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		To   struct {
			Name string `json:"name"`
		} `json:"to"`
	} `json:"transitions"`
}

// Transition moves the issue to the Jira status configured for state.
func (c *Client) Transition(ctx context.Context, key string, to tracker.State) error {
	target, err := c.statusName(to)
	if err != nil {
		return err
	}
	var resp transitionsResponse
	if err := c.do(ctx, http.MethodGet, issuePath(key, "/transitions"), nil, &resp); err != nil {
		return err
	}
	var available []string
	for _, tr := range resp.Transitions {
		if strings.EqualFold(tr.To.Name, target) {
			return c.do(ctx, http.MethodPost, issuePath(key, "/transitions"), map[string]any{
				"transition": map[string]string{"id": tr.ID},
			}, nil)
		}
		available = append(available, tr.To.Name)
	}
	return fmt.Errorf("jira: %s has no transition to %q (available: %s)", key, target, strings.Join(available, ", "))
}

// statusName maps a HiveDispatch state to the configured Jira status name.
func (c *Client) statusName(s tracker.State) (string, error) {
	st := c.cfg.Statuses
	switch s {
	case tracker.StateReady:
		return st.Ready, nil
	case tracker.StateInProgress:
		return st.InProgress, nil
	case tracker.StateNeedsInfo:
		return st.NeedsInfo, nil
	case tracker.StateInReview:
		return st.InReview, nil
	case tracker.StateNeedsHuman:
		return st.NeedsHuman, nil
	}
	return "", fmt.Errorf("jira: unknown state %q", s)
}
