package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// jiraTime is the timestamp layout Jira Cloud uses for datetime fields.
const jiraTime = "2006-01-02T15:04:05.000-0700"

// maxPollPages bounds pagination so a misbehaving server cannot spin us.
const maxPollPages = 20

const pollPageSize = 50

// issueJSON is the subset of a Jira issue HiveDispatch reads. Fields are
// decoded from a raw map because the custom field IDs are configuration.
type issueJSON struct {
	ID     string         `json:"id"`
	Key    string         `json:"key"`
	Fields map[string]any `json:"fields"`
}

type searchResponse struct {
	Issues        []issueJSON `json:"issues"`
	NextPageToken string      `json:"nextPageToken"`
	IsLast        bool        `json:"isLast"`
}

// fields is the explicit field list. Jira's /search/jql defaults to id only.
func (c *Client) fields() []string {
	return []string{
		"summary", "description", "status", "labels", "updated", "comment",
		c.cfg.Fields.AgentID, c.cfg.Fields.ClaimedAt,
	}
}

// Poll runs the configured JQL and returns every matching ticket.
func (c *Client) Poll(ctx context.Context) ([]tracker.Ticket, error) {
	var out []tracker.Ticket
	token := ""
	for page := 0; page < maxPollPages; page++ {
		body := map[string]any{
			"jql":        c.cfg.JQL,
			"fields":     c.fields(),
			"maxResults": pollPageSize,
		}
		if token != "" {
			body["nextPageToken"] = token
		}
		var resp searchResponse
		if err := c.do(ctx, http.MethodPost, "/rest/api/3/search/jql", body, &resp); err != nil {
			return nil, err
		}
		for i := range resp.Issues {
			out = append(out, c.toTicket(resp.Issues[i]))
		}
		if resp.IsLast || resp.NextPageToken == "" {
			return out, nil
		}
		token = resp.NextPageToken
	}
	return nil, fmt.Errorf("jira: poll exceeded %d pages without isLast", maxPollPages)
}

// Get fetches one issue.
func (c *Client) Get(ctx context.Context, key string) (tracker.Ticket, error) {
	q := url.Values{"fields": {strings.Join(c.fields(), ",")}}
	var raw issueJSON
	if err := c.do(ctx, http.MethodGet, issuePath(key, "")+"?"+q.Encode(), nil, &raw); err != nil {
		return tracker.Ticket{}, err
	}
	return c.toTicket(raw), nil
}

// toTicket maps a Jira issue onto the tracker-agnostic Ticket.
func (c *Client) toTicket(raw issueJSON) tracker.Ticket {
	f := raw.Fields
	t := tracker.Ticket{
		Key:     raw.Key,
		Summary: str(f["summary"]),
		Status:  nested(f["status"], "name"),
		URL:     c.base.String() + "/browse/" + raw.Key,
		Updated: parseTime(str(f["updated"])),
	}
	if d, ok := f["description"].(map[string]any); ok {
		t.Description = adfToText(toADF(d))
	}
	if ls, ok := f["labels"].([]any); ok {
		for _, l := range ls {
			t.Labels = append(t.Labels, str(l))
		}
	}
	if agent := str(f[c.cfg.Fields.AgentID]); agent != "" {
		t.Claim = &tracker.Claim{AgentID: agent, At: parseTime(str(f[c.cfg.Fields.ClaimedAt]))}
	}
	if cm, ok := f["comment"].(map[string]any); ok {
		if list, ok := cm["comments"].([]any); ok {
			for _, item := range list {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				comment := tracker.Comment{
					ID:       str(m["id"]),
					AuthorID: nested(m["author"], "accountId"),
					Author:   nested(m["author"], "displayName"),
					Created:  parseTime(str(m["created"])),
				}
				if b, ok := m["body"].(map[string]any); ok {
					comment.Body = adfToText(toADF(b))
				}
				t.Comments = append(t.Comments, comment)
			}
		}
	}
	return t
}

// toADF converts a generic JSON map into an adfNode tree.
func toADF(m map[string]any) *adfNode {
	n := &adfNode{Type: str(m["type"]), Text: str(m["text"])}
	if attrs, ok := m["attrs"].(map[string]any); ok {
		n.Attrs = attrs
	}
	if kids, ok := m["content"].([]any); ok {
		for _, k := range kids {
			if km, ok := k.(map[string]any); ok {
				n.Content = append(n.Content, *toADF(km))
			}
		}
	}
	return n
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func nested(v any, key string) string {
	m, _ := v.(map[string]any)
	return str(m[key])
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(jiraTime, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
