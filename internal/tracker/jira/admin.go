package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
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
	MissingFields   []string // names (when resolving by name) or ids (when configured) not found
	DuplicateFields []string // "name: id, id" where more than one field carries a claim-field name
	MissingStatuses []string
	SampleTickets   int      // tickets matched by the trigger JQL
	SampleIssue     string   // issue used to verify the claim fields are editable
	NotEditableOn   string   // SampleIssue when the claim fields cannot be set on it
	Projects        []string // "KEY (team-managed)" for every project on the site
	UnknownProjects []string // configured project keys that do not exist
}

// OK reports whether nothing blocks a run.
func (r CheckReport) OK() bool {
	return len(r.MissingFields) == 0 && len(r.MissingStatuses) == 0 && r.NotEditableOn == "" && len(r.UnknownProjects) == 0
}

type fieldJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// listCustomFields returns every custom field on the site.
//
// It uses /field/search rather than /field: on some sites /field omits
// custom fields that are not on a company-managed screen, which made
// freshly created claim fields invisible.
func (c *Client) listCustomFields(ctx context.Context) ([]fieldJSON, error) {
	var out []fieldJSON
	for start := 0; ; {
		q := url.Values{"type": {"custom"}, "startAt": {fmt.Sprint(start)}, "maxResults": {"100"}}
		var page struct {
			Values []fieldJSON `json:"values"`
			IsLast bool        `json:"isLast"`
			Total  int         `json:"total"`
		}
		if err := c.do(ctx, http.MethodGet, "/rest/api/3/field/search?"+q.Encode(), nil, &page); err != nil {
			return nil, fmt.Errorf("list fields: %w", err)
		}
		out = append(out, page.Values...)
		start += len(page.Values)
		if page.IsLast || len(page.Values) == 0 || start >= page.Total {
			return out, nil
		}
	}
}

// byName groups custom field ids by name, lowest id first.
func byName(fields []fieldJSON) map[string][]string {
	m := map[string][]string{}
	for _, f := range fields {
		m[f.Name] = append(m[f.Name], f.ID)
	}
	for _, ids := range m {
		sort.Strings(ids)
	}
	return m
}

// resolve fills empty claim field ids from names. It returns the names
// that could not be found and the names that matched several fields.
func (c *Client) resolve(fields []fieldJSON) (missing, duplicates []string) {
	names := byName(fields)
	pick := func(id *string, name string) {
		if *id != "" {
			return
		}
		ids := names[name]
		switch {
		case len(ids) == 0:
			missing = append(missing, name)
		default:
			if len(ids) > 1 {
				duplicates = append(duplicates, name+": "+strings.Join(ids, ", "))
			}
			*id = ids[0]
		}
	}
	pick(&c.cfg.Fields.AgentID, FieldNameAgent)
	pick(&c.cfg.Fields.ClaimedAt, FieldNameClaimedAt)
	return missing, duplicates
}

// ResolveFields fills in any claim field id the config left empty by
// looking the field up by name. Explicit ids are kept.
func (c *Client) ResolveFields(ctx context.Context) error {
	if c.cfg.Fields.AgentID != "" && c.cfg.Fields.ClaimedAt != "" {
		return nil
	}
	fields, err := c.listCustomFields(ctx)
	if err != nil {
		return err
	}
	if missing, _ := c.resolve(fields); len(missing) > 0 {
		return fmt.Errorf("jira: custom field(s) %q not found — run `hivedispatch init -jira` to create them, or set jira.fields to their customfield_NNNNN ids", strings.Join(missing, ", "))
	}
	return nil
}

// Check verifies credentials, claim fields (existence and editability),
// status names, and that the trigger JQL runs. It performs only reads.
// projects are the Jira project keys HiveDispatch dispatches into; they
// supply a sample issue when the trigger JQL matches nothing.
func (c *Client) Check(ctx context.Context, projects []string) (CheckReport, error) {
	var rep CheckReport
	var me struct {
		DisplayName string `json:"displayName"`
	}
	if err := c.do(ctx, http.MethodGet, "/rest/api/3/myself", nil, &me); err != nil {
		return rep, fmt.Errorf("authentication failed: %w", err)
	}
	rep.User = me.DisplayName

	fields, err := c.listCustomFields(ctx)
	if err != nil {
		return rep, err
	}
	have := map[string]bool{}
	for _, f := range fields {
		have[f.ID] = true
	}
	for _, id := range []string{c.cfg.Fields.AgentID, c.cfg.Fields.ClaimedAt} {
		if id != "" && !have[id] {
			rep.MissingFields = append(rep.MissingFields, id)
		}
	}
	missing, dups := c.resolve(fields)
	rep.MissingFields = append(rep.MissingFields, missing...)
	rep.DuplicateFields = dups

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

	var projs []struct {
		Key   string `json:"key"`
		Style string `json:"style"`
	}
	if err := c.do(ctx, http.MethodGet, "/rest/api/3/project", nil, &projs); err != nil {
		return rep, fmt.Errorf("list projects: %w", err)
	}
	known := map[string]bool{}
	for _, p := range projs {
		known[strings.ToUpper(p.Key)] = true
		label := p.Key
		if p.Style == "next-gen" {
			label += " (team-managed)"
		}
		rep.Projects = append(rep.Projects, label)
	}
	var valid []string
	for _, p := range projects {
		if known[strings.ToUpper(p)] {
			valid = append(valid, p)
		} else {
			rep.UnknownProjects = append(rep.UnknownProjects, p)
		}
	}
	projects = valid

	tickets, err := c.Poll(ctx)
	if err != nil {
		return rep, fmt.Errorf("trigger JQL failed: %w", err)
	}
	rep.SampleTickets = len(tickets)
	sample := ""
	if len(tickets) > 0 {
		sample = tickets[0].Key
	} else if len(projects) > 0 {
		sample, _ = c.anyIssue(ctx, projects)
	}
	if sample != "" && len(rep.MissingFields) == 0 {
		rep.SampleIssue = sample
		editable, err := c.editable(ctx, sample)
		if err != nil {
			return rep, err
		}
		if !editable[c.cfg.Fields.AgentID] || !editable[c.cfg.Fields.ClaimedAt] {
			rep.NotEditableOn = sample
		}
	}
	return rep, nil
}

// anyIssue returns the most recently created issue in projects, or "".
func (c *Client) anyIssue(ctx context.Context, projects []string) (string, error) {
	quoted := make([]string, len(projects))
	for i, p := range projects {
		quoted[i] = `"` + p + `"`
	}
	body := map[string]any{
		"jql":        "project in (" + strings.Join(quoted, ", ") + ") ORDER BY created DESC",
		"fields":     []string{"summary"},
		"maxResults": 1,
	}
	var resp searchResponse
	if err := c.do(ctx, http.MethodPost, "/rest/api/3/search/jql", body, &resp); err != nil {
		return "", err
	}
	if len(resp.Issues) == 0 {
		return "", nil
	}
	return resp.Issues[0].Key, nil
}

// editable returns the set of field ids that can be set on the issue.
func (c *Client) editable(ctx context.Context, key string) (map[string]bool, error) {
	var meta struct {
		Fields map[string]any `json:"fields"`
	}
	if err := c.do(ctx, http.MethodGet, issuePath(key, "/editmeta"), nil, &meta); err != nil {
		return nil, fmt.Errorf("editmeta %s: %w", key, err)
	}
	out := map[string]bool{}
	for id := range meta.Fields {
		out[id] = true
	}
	return out, nil
}

// EnsuredFields are the custom field IDs after EnsureFields.
type EnsuredFields struct {
	AgentID   string
	ClaimedAt string
}

// EnsureFields creates the two claim custom fields if they do not exist and
// adds newly created ones to the default screen so they are writable on
// company-managed projects. Team-managed projects must add the fields to
// their issue types in the project settings.
func (c *Client) EnsureFields(ctx context.Context) (EnsuredFields, error) {
	fields, err := c.listCustomFields(ctx)
	if err != nil {
		return EnsuredFields{}, err
	}
	names := byName(fields)
	var out EnsuredFields
	out.AgentID, err = c.ensureField(ctx, names, FieldNameAgent,
		"com.atlassian.jira.plugin.system.customfieldtypes:textfield",
		"com.atlassian.jira.plugin.system.customfieldtypes:textsearcher")
	if err != nil {
		return out, err
	}
	out.ClaimedAt, err = c.ensureField(ctx, names, FieldNameClaimedAt,
		"com.atlassian.jira.plugin.system.customfieldtypes:datetime",
		"com.atlassian.jira.plugin.system.customfieldtypes:datetimerange")
	return out, err
}

func (c *Client) ensureField(ctx context.Context, names map[string][]string, name, typ, searcher string) (string, error) {
	if ids := names[name]; len(ids) > 0 {
		return ids[0], nil
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
