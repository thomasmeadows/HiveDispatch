package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Names of the custom fields HiveDispatch creates for the claim protocol
// and, when no ids are configured, looks up by name.
const (
	FieldNameAgent     = "HiveDispatch Agent"      // which worker holds the ticket
	FieldNameClaimedAt = "HiveDispatch Claimed At" // that worker's last heartbeat
)

// CheckReport is the result of a live configuration check.
type CheckReport struct {
	User            string
	MissingFields   []string
	MissingStatuses []string
	SampleTickets   int
}

// OK reports whether nothing is missing.
func (r CheckReport) OK() bool {
	return len(r.MissingFields) == 0 && len(r.MissingStatuses) == 0
}

type fieldJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Check verifies credentials, custom field IDs, status names, and that the
// trigger JQL runs. It performs only reads.
func (c *Client) Check(ctx context.Context) (CheckReport, error) {
	var rep CheckReport
	var me struct {
		DisplayName string `json:"displayName"`
	}
	if err := c.do(ctx, http.MethodGet, "/rest/api/3/myself", nil, &me); err != nil {
		return rep, fmt.Errorf("authentication failed: %w", err)
	}
	rep.User = me.DisplayName

	fields, err := c.listFields(ctx)
	if err != nil {
		return rep, err
	}
	have := map[string]bool{}
	byName := map[string]string{}
	for _, f := range fields {
		have[f.ID] = true
		byName[f.Name] = f.ID
	}
	// Unset ids are resolved by name; set ids must exist.
	for _, want := range []struct{ id, name string }{{c.cfg.Fields.AgentID, FieldNameAgent}, {c.cfg.Fields.ClaimedAt, FieldNameClaimedAt}} {
		switch {
		case want.id == "":
			if _, ok := byName[want.name]; !ok {
				rep.MissingFields = append(rep.MissingFields, want.name)
			}
		case !have[want.id]:
			rep.MissingFields = append(rep.MissingFields, want.id)
		}
	}

	var statuses []struct {
		Name string `json:"name"`
	}
	if err := c.do(ctx, http.MethodGet, "/rest/api/3/status", nil, &statuses); err != nil {
		return rep, err
	}
	st := c.cfg.Statuses
	for _, want := range []string{st.Ready, st.InProgress, st.NeedsInfo, st.InReview, st.NeedsHuman} {
		found := false
		for _, s := range statuses {
			if strings.EqualFold(s.Name, want) {
				found = true
				break
			}
		}
		if !found {
			rep.MissingStatuses = append(rep.MissingStatuses, want)
		}
	}

	tickets, err := c.Poll(ctx)
	if err != nil {
		return rep, fmt.Errorf("trigger JQL failed: %w", err)
	}
	rep.SampleTickets = len(tickets)
	return rep, nil
}

// ResolveFields fills in any claim field ID that the config left empty by
// looking the field up by the name EnsureFields gives it. Explicit IDs in
// the config are kept, which allows renamed fields or several sites.
func (c *Client) ResolveFields(ctx context.Context) error {
	if c.cfg.Fields.AgentID != "" && c.cfg.Fields.ClaimedAt != "" {
		return nil
	}
	fields, err := c.listFields(ctx)
	if err != nil {
		return err
	}
	byName := map[string]string{}
	for _, f := range fields {
		byName[f.Name] = f.ID
	}
	var missing []string
	if c.cfg.Fields.AgentID == "" {
		if id, ok := byName[FieldNameAgent]; ok {
			c.cfg.Fields.AgentID = id
		} else {
			missing = append(missing, FieldNameAgent)
		}
	}
	if c.cfg.Fields.ClaimedAt == "" {
		if id, ok := byName[FieldNameClaimedAt]; ok {
			c.cfg.Fields.ClaimedAt = id
		} else {
			missing = append(missing, FieldNameClaimedAt)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("jira: custom field(s) %q not found — run `hivedispatch init -jira` to create them, or set jira.fields to their customfield_NNNNN ids", strings.Join(missing, ", "))
	}
	return nil
}

// EnsuredFields are the custom field IDs after EnsureFields.
type EnsuredFields struct {
	AgentID   string
	ClaimedAt string
}

// EnsureFields creates the two claim custom fields if they do not exist and
// adds newly created ones to the default screen so they are writable.
func (c *Client) EnsureFields(ctx context.Context) (EnsuredFields, error) {
	fields, err := c.listFields(ctx)
	if err != nil {
		return EnsuredFields{}, err
	}
	byName := map[string]string{}
	for _, f := range fields {
		byName[f.Name] = f.ID
	}
	var out EnsuredFields
	out.AgentID, err = c.ensureField(ctx, byName, FieldNameAgent,
		"com.atlassian.jira.plugin.system.customfieldtypes:textfield",
		"com.atlassian.jira.plugin.system.customfieldtypes:textsearcher")
	if err != nil {
		return out, err
	}
	out.ClaimedAt, err = c.ensureField(ctx, byName, FieldNameClaimedAt,
		"com.atlassian.jira.plugin.system.customfieldtypes:datetime",
		"com.atlassian.jira.plugin.system.customfieldtypes:datetimerange")
	return out, err
}

func (c *Client) listFields(ctx context.Context) ([]fieldJSON, error) {
	var fields []fieldJSON
	if err := c.do(ctx, http.MethodGet, "/rest/api/3/field", nil, &fields); err != nil {
		return nil, fmt.Errorf("list fields: %w", err)
	}
	return fields, nil
}

func (c *Client) ensureField(ctx context.Context, byName map[string]string, name, typ, searcher string) (string, error) {
	if id, ok := byName[name]; ok {
		return id, nil
	}
	var created fieldJSON
	err := c.do(ctx, http.MethodPost, "/rest/api/3/field", map[string]any{
		"name":        name,
		"description": "Managed by HiveDispatch. Do not edit by hand.",
		"type":        typ,
		"searcherKey": searcher,
	}, &created)
	if err != nil {
		return "", fmt.Errorf("create field %q: %w", name, err)
	}
	if err := c.do(ctx, http.MethodPost, "/rest/api/3/screens/addToDefault/"+url.PathEscape(created.ID), nil, nil); err != nil {
		return created.ID, fmt.Errorf("field %s created but not added to default screen: %w", created.ID, err)
	}
	return created.ID, nil
}
