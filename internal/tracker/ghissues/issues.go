package ghissues

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// maxPages bounds pagination per request so a misbehaving API cannot spin us.
const maxPages = 5

type issueJSON struct {
	Number      int    `json:"number"`
	NodeID      string `json:"node_id"` // GraphQL id, needed for the project board
	Title       string `json:"title"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	UpdatedAt   string `json:"updated_at"`
	PullRequest *struct {
		URL string `json:"url"`
	} `json:"pull_request"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

type commentJSON struct {
	ID   int64 `json:"id"`
	User struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
	} `json:"user"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

// stateLabels returns the configured state labels in State order.
func (c *Client) stateLabels() map[tracker.State]string {
	l := c.cfg.Labels
	return map[tracker.State]string{
		tracker.StateReady:      l.Ready,
		tracker.StateInProgress: l.InProgress,
		tracker.StateNeedsInfo:  l.NeedsInfo,
		tracker.StateInReview:   l.InReview,
		tracker.StateNeedsHuman: l.NeedsHuman,
	}
}

func (c *Client) isStateLabel(name string) bool {
	for _, l := range c.stateLabels() {
		if strings.EqualFold(l, name) {
			return true
		}
	}
	return false
}

// getAll follows Link: rel="next" pagination, appending into out (a pointer
// to a slice), for at most maxPages pages.
func (c *Client) getAll(ctx context.Context, path string, collect func([]byte) error) error {
	next := path
	for page := 0; next != "" && page < maxPages; page++ {
		var raw []byte
		hdr, err := c.doRaw(ctx, next, &raw)
		if err != nil {
			return err
		}
		if err := collect(raw); err != nil {
			return err
		}
		next = nextLink(hdr)
	}
	return nil
}

// doRaw is do() for GET returning the raw body.
func (c *Client) doRaw(ctx context.Context, path string, raw *[]byte) (http.Header, error) {
	var buf jsonRaw
	hdr, err := c.do(ctx, http.MethodGet, path, nil, &buf)
	*raw = buf
	return hdr, err
}

// jsonRaw captures a response body verbatim so pages can be decoded by the caller.
type jsonRaw []byte

// UnmarshalJSON implements json.Unmarshaler by copying the bytes.
func (j *jsonRaw) UnmarshalJSON(b []byte) error {
	*j = append((*j)[:0], b...)
	return nil
}

// Poll returns open issues carrying the ready label across every repo.
func (c *Client) Poll(ctx context.Context) ([]tracker.Ticket, error) {
	var out []tracker.Ticket
	for _, repo := range c.repos {
		q := url.Values{"state": {"open"}, "labels": {c.cfg.Labels.Ready}, "per_page": {"100"}}
		var issues []issueJSON
		err := c.getAll(ctx, "/repos/"+repo.Name+"/issues?"+q.Encode(), func(raw []byte) error {
			var page []issueJSON
			if err := unmarshal(raw, &page); err != nil {
				return err
			}
			issues = append(issues, page...)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("poll %s: %w", repo.Name, err)
		}
		for _, is := range issues {
			if is.PullRequest != nil {
				continue // the issues API also lists pull requests
			}
			tk, err := c.toTicket(ctx, repo.Name, is)
			if err != nil {
				return nil, err
			}
			out = append(out, tk)
		}
	}
	return out, nil
}

// Get fetches one issue with its comments.
func (c *Client) Get(ctx context.Context, key string) (tracker.Ticket, error) {
	repo, n, err := c.keyToRef(key)
	if err != nil {
		return tracker.Ticket{}, err
	}
	var is issueJSON
	if _, err := c.do(ctx, http.MethodGet, issuePath(repo, n, ""), nil, &is); err != nil {
		return tracker.Ticket{}, err
	}
	return c.toTicket(ctx, repo, is)
}

func (c *Client) toTicket(ctx context.Context, repo string, is issueJSON) (tracker.Ticket, error) {
	key, _ := c.refToKey(repo, is.Number)
	body, claim := splitMarker(is.Body)
	t := tracker.Ticket{
		Key: key, Summary: is.Title, Description: body, URL: is.HTMLURL,
		Status: "open", Claim: claim, Updated: parseTime(is.UpdatedAt),
	}
	for _, l := range is.Labels {
		t.Labels = append(t.Labels, l.Name)
		if c.isStateLabel(l.Name) {
			t.Status = l.Name
		}
	}
	err := c.getAll(ctx, issuePath(repo, is.Number, "/comments?per_page=100"), func(raw []byte) error {
		var page []commentJSON
		if err := unmarshal(raw, &page); err != nil {
			return err
		}
		for _, cm := range page {
			t.Comments = append(t.Comments, tracker.Comment{
				ID: strconv.FormatInt(cm.ID, 10), AuthorID: strconv.FormatInt(cm.User.ID, 10),
				Author: cm.User.Login, Body: cm.Body, Created: parseTime(cm.CreatedAt),
			})
		}
		return nil
	})
	if err != nil {
		return tracker.Ticket{}, fmt.Errorf("comments %s: %w", key, err)
	}
	return t, nil
}

// Comment posts a Markdown comment.
func (c *Client) Comment(ctx context.Context, key, body string) error {
	repo, n, err := c.keyToRef(key)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPost, issuePath(repo, n, "/comments"), map[string]string{"body": body}, nil)
	return err
}

// Transition swaps the issue's state label: every other state label is
// removed, then the target is added. Non-state labels are untouched. With a
// project board configured, the issue's card is then moved to the column
// for the new state; the label is already set when that fails, so the
// error only reports the board.
func (c *Client) Transition(ctx context.Context, key string, to tracker.State) error {
	target, ok := c.stateLabels()[to]
	if !ok || target == "" {
		return fmt.Errorf("ghissues: unknown state %q", to)
	}
	repo, n, err := c.keyToRef(key)
	if err != nil {
		return err
	}
	var is issueJSON
	if _, err := c.do(ctx, http.MethodGet, issuePath(repo, n, ""), nil, &is); err != nil {
		return err
	}
	for _, l := range is.Labels {
		if c.isStateLabel(l.Name) && !strings.EqualFold(l.Name, target) {
			if _, err := c.do(ctx, http.MethodDelete, issuePath(repo, n, "/labels/"+url.PathEscape(l.Name)), nil, nil); err != nil {
				return fmt.Errorf("remove label %s: %w", l.Name, err)
			}
		}
	}
	if _, err := c.do(ctx, http.MethodPost, issuePath(repo, n, "/labels"), map[string][]string{"labels": {target}}, nil); err != nil {
		return err
	}
	if !c.cfg.Project.Enabled() {
		return nil
	}
	if err := c.moveProjectItem(ctx, is.NodeID, to); err != nil {
		return fmt.Errorf("%s labelled %s, but the project board was not updated: %w", key, target, err)
	}
	return nil
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
