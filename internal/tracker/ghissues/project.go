package ghissues

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// GitHub Projects (v2) has no REST API; everything here goes through
// GraphQL. The board is a mirror of the state labels: Transition swaps the
// label first and only then moves the card, so a board failure never leaves
// the queue inconsistent.

// projectRef is the resolved board: the node ids the mutations need.
type projectRef struct {
	ID      string
	Title   string
	FieldID string
	Options map[string]string // lower-cased option name → option id
}

const projectQuery = `query($owner: String!, $number: Int!) {
  repositoryOwner(login: $owner) {
    ... on ProjectV2Owner {
      projectV2(number: $number) {
        id title
        fields(first: 100) { nodes { ... on ProjectV2SingleSelectField { id name options { id name } } } }
      }
    }
  }
}`

const issueItemsQuery = `query($id: ID!) {
  node(id: $id) { ... on Issue { projectItems(first: 100) { nodes { id project { id } } } } }
}`

const addItemMutation = `mutation($project: ID!, $content: ID!) {
  addProjectV2ItemById(input: {projectId: $project, contentId: $content}) { item { id } }
}`

const setFieldMutation = `mutation($project: ID!, $item: ID!, $field: ID!, $option: String!) {
  updateProjectV2ItemFieldValue(input: {projectId: $project, itemId: $item, fieldId: $field, value: {singleSelectOptionId: $option}}) { projectV2Item { id } }
}`

// graphql posts one operation. GraphQL reports failures in the body with a
// 200, so those are turned into errors here; the INSUFFICIENT_SCOPES case
// gets the fix spelled out because the default `gh auth login` token lacks it.
func (c *Client) graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	var resp struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	body := map[string]any{"query": query, "variables": vars}
	if _, err := c.do(ctx, http.MethodPost, "graphql", body, &resp); err != nil {
		return err
	}
	if len(resp.Errors) > 0 {
		msgs := make([]string, 0, len(resp.Errors))
		scope := false
		for _, e := range resp.Errors {
			msgs = append(msgs, e.Message)
			scope = scope || e.Type == "INSUFFICIENT_SCOPES"
		}
		err := fmt.Errorf("github graphql: %s", strings.Join(msgs, "; "))
		if scope {
			err = fmt.Errorf("%w — the token needs the project scope: `gh auth refresh -s project`, or for a fine-grained token grant Projects: read and write", err)
		}
		return err
	}
	if out != nil && len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, out); err != nil {
			return fmt.Errorf("github graphql: decode: %w", err)
		}
	}
	return nil
}

// projectColumns returns the configured column per state.
func (c *Client) projectColumns() map[tracker.State]string {
	col := c.cfg.Project.Columns
	return map[tracker.State]string{
		tracker.StateReady:      col.Ready,
		tracker.StateInProgress: col.InProgress,
		tracker.StateNeedsInfo:  col.NeedsInfo,
		tracker.StateInReview:   col.InReview,
		tracker.StateNeedsHuman: col.NeedsHuman,
	}
}

// project resolves the configured board once and caches it; refresh drops
// the cache first, for when a column was added after the worker started.
func (c *Client) project(ctx context.Context, refresh bool) (*projectRef, error) {
	c.projMu.Lock()
	defer c.projMu.Unlock()
	if c.proj != nil && !refresh {
		return c.proj, nil
	}
	var data struct {
		RepositoryOwner struct {
			ProjectV2 *struct {
				ID     string `json:"id"`
				Title  string `json:"title"`
				Fields struct {
					Nodes []struct {
						ID      string `json:"id"`
						Name    string `json:"name"`
						Options []struct {
							ID   string `json:"id"`
							Name string `json:"name"`
						} `json:"options"`
					} `json:"nodes"`
				} `json:"fields"`
			} `json:"projectV2"`
		} `json:"repositoryOwner"`
	}
	p := c.cfg.Project
	if err := c.graphql(ctx, projectQuery, map[string]any{"owner": p.Owner, "number": p.Number}, &data); err != nil {
		return nil, fmt.Errorf("project %s/%d: %w", p.Owner, p.Number, err)
	}
	pv := data.RepositoryOwner.ProjectV2
	if pv == nil {
		return nil, fmt.Errorf("project %s/%d: not found, or the token cannot see it", p.Owner, p.Number)
	}
	ref := &projectRef{ID: pv.ID, Title: pv.Title, Options: map[string]string{}}
	for _, f := range pv.Fields.Nodes {
		if !strings.EqualFold(f.Name, p.Field) || f.Options == nil {
			continue // other fields, and a same-named field that is not single-select
		}
		ref.FieldID = f.ID
		for _, o := range f.Options {
			ref.Options[strings.ToLower(o.Name)] = o.ID
		}
	}
	if ref.FieldID == "" {
		return nil, fmt.Errorf("project %q has no single-select field %q", pv.Title, p.Field)
	}
	c.proj = ref
	return ref, nil
}

// moveProjectItem puts the issue (by GraphQL node id) in the column for
// state, adding it to the board first when it is not there yet.
func (c *Client) moveProjectItem(ctx context.Context, nodeID string, state tracker.State) error {
	column := c.projectColumns()[state]
	ref, err := c.project(ctx, false)
	if err != nil {
		return err
	}
	option, ok := ref.Options[strings.ToLower(column)]
	if !ok {
		// The column may have been added since startup: look once more.
		if ref, err = c.project(ctx, true); err != nil {
			return err
		}
		if option, ok = ref.Options[strings.ToLower(column)]; !ok {
			return fmt.Errorf("project %q has no %s option %q — add the column to the board or change github.project.columns", ref.Title, c.cfg.Project.Field, column)
		}
	}
	var items struct {
		Node struct {
			ProjectItems struct {
				Nodes []struct {
					ID      string `json:"id"`
					Project struct {
						ID string `json:"id"`
					} `json:"project"`
				} `json:"nodes"`
			} `json:"projectItems"`
		} `json:"node"`
	}
	if err := c.graphql(ctx, issueItemsQuery, map[string]any{"id": nodeID}, &items); err != nil {
		return fmt.Errorf("list project items: %w", err)
	}
	itemID := ""
	for _, it := range items.Node.ProjectItems.Nodes {
		if it.Project.ID == ref.ID {
			itemID = it.ID
			break
		}
	}
	if itemID == "" {
		var added struct {
			AddProjectV2ItemByID struct {
				Item struct {
					ID string `json:"id"`
				} `json:"item"`
			} `json:"addProjectV2ItemById"`
		}
		if err := c.graphql(ctx, addItemMutation, map[string]any{"project": ref.ID, "content": nodeID}, &added); err != nil {
			return fmt.Errorf("add to project %q: %w", ref.Title, err)
		}
		itemID = added.AddProjectV2ItemByID.Item.ID
	}
	vars := map[string]any{"project": ref.ID, "item": itemID, "field": ref.FieldID, "option": option}
	if err := c.graphql(ctx, setFieldMutation, vars, nil); err != nil {
		return fmt.Errorf("move to %q on project %q: %w", column, ref.Title, err)
	}
	return nil
}
