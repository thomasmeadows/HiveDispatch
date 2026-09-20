package ghissues

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// CheckReport is the result of a live configuration check.
type CheckReport struct {
	User          string
	Repos         []string            // repos that exist and allow issues
	MissingRepos  []string            // configured repos that could not be read
	MissingLabels map[string][]string // repo → state labels not yet created
	SampleTickets int
	SampleIssue   string // issue used to probe write access
	NotWritable   string // SampleIssue when the token cannot edit issues

	Project        string   // title of the configured project board, when it was found
	ProjectField   string   // the single-select field the columns belong to
	ProjectError   string   // why the board could not be used; empty when none is configured
	MissingColumns []string // configured column names the board's field does not have
}

// OK reports whether nothing blocks a run.
func (r CheckReport) OK() bool {
	return len(r.MissingRepos) == 0 && len(r.MissingLabels) == 0 && r.NotWritable == "" &&
		r.ProjectError == "" && len(r.MissingColumns) == 0
}

// labelColours give each state a distinct, muted colour on the issue list.
var labelColours = map[tracker.State]string{
	tracker.StateReady: "0e8a16", tracker.StateInProgress: "1d76db", tracker.StateNeedsInfo: "fbca04",
	tracker.StateInReview: "5319e7", tracker.StateNeedsHuman: "d93f0b",
}

var labelDescriptions = map[tracker.State]string{
	tracker.StateReady:      "HiveDispatch may pick this up",
	tracker.StateInProgress: "A HiveDispatch worker is on it",
	tracker.StateNeedsInfo:  "HiveDispatch asked a question; answer, then relabel hive:ready",
	tracker.StateInReview:   "HiveDispatch opened a pull request",
	tracker.StateNeedsHuman: "HiveDispatch could not handle this automatically",
}

func (c *Client) existingLabels(ctx context.Context, repo string) (map[string]bool, error) {
	have := map[string]bool{}
	err := c.getAll(ctx, "/repos/"+repo+"/labels?per_page=100", func(raw []byte) error {
		var page []struct {
			Name string `json:"name"`
		}
		if err := unmarshal(raw, &page); err != nil {
			return err
		}
		for _, l := range page {
			have[strings.ToLower(l.Name)] = true
		}
		return nil
	})
	return have, err
}

// Check verifies the token, each repo, the state labels and, when one is
// configured, the project board and its columns; it performs only reads.
func (c *Client) Check(ctx context.Context) (CheckReport, error) {
	rep := CheckReport{MissingLabels: map[string][]string{}}
	var me struct {
		Login string `json:"login"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/user", nil, &me); err != nil {
		return rep, fmt.Errorf("authentication failed: %w", err)
	}
	rep.User = me.Login
	if c.cfg.Project.Enabled() {
		c.checkProject(ctx, &rep)
	}
	for _, repo := range c.repos {
		var info struct {
			HasIssues bool `json:"has_issues"`
		}
		if _, err := c.do(ctx, http.MethodGet, "/repos/"+repo.Name, nil, &info); err != nil {
			rep.MissingRepos = append(rep.MissingRepos, repo.Name+": "+err.Error())
			continue
		}
		if !info.HasIssues {
			rep.MissingRepos = append(rep.MissingRepos, repo.Name+": issues are disabled on this repository")
			continue
		}
		rep.Repos = append(rep.Repos, repo.Name)
		have, err := c.existingLabels(ctx, repo.Name)
		if err != nil {
			return rep, err
		}
		for _, l := range c.stateLabels() {
			if !have[strings.ToLower(l)] {
				rep.MissingLabels[repo.Name] = append(rep.MissingLabels[repo.Name], l)
			}
		}
		if len(rep.MissingLabels[repo.Name]) == 0 {
			delete(rep.MissingLabels, repo.Name)
		} else {
			sort.Strings(rep.MissingLabels[repo.Name])
		}
	}
	tickets, err := c.Poll(ctx)
	if err != nil {
		return rep, fmt.Errorf("poll failed: %w", err)
	}
	rep.SampleTickets = len(tickets)
	if len(tickets) > 0 {
		// GitHub cannot report a fine-grained token's permissions, so probe
		// with a write that changes nothing: re-send the body as it is.
		rep.SampleIssue = tickets[0].Key
		repo, n, err := c.keyToRef(tickets[0].Key)
		if err != nil {
			return rep, err
		}
		var is issueJSON
		if _, err := c.do(ctx, http.MethodGet, issuePath(repo, n, ""), nil, &is); err != nil {
			return rep, err
		}
		if err := c.patchBody(ctx, repo, n, is.Body); err != nil {
			var apiErr *APIError
			if errors.As(err, &apiErr) && (apiErr.Status == http.StatusForbidden || apiErr.Status == http.StatusNotFound) {
				rep.NotWritable = tickets[0].Key
			} else {
				return rep, err
			}
		}
	}
	return rep, nil
}

// checkProject resolves the board and lists the configured columns it lacks.
func (c *Client) checkProject(ctx context.Context, rep *CheckReport) {
	rep.ProjectField = c.cfg.Project.Field
	ref, err := c.project(ctx, true)
	if err != nil {
		rep.ProjectError = err.Error()
		return
	}
	rep.Project = ref.Title
	for _, col := range c.projectColumns() {
		if _, ok := ref.Options[strings.ToLower(col)]; !ok {
			rep.MissingColumns = append(rep.MissingColumns, col)
		}
	}
	sort.Strings(rep.MissingColumns)
}

// EnsureLabels creates any missing state label in every repo and returns
// how many were created.
func (c *Client) EnsureLabels(ctx context.Context) (int, error) {
	created := 0
	for _, repo := range c.repos {
		have, err := c.existingLabels(ctx, repo.Name)
		if err != nil {
			return created, fmt.Errorf("%s: %w", repo.Name, err)
		}
		for state, name := range c.stateLabels() {
			if have[strings.ToLower(name)] {
				continue
			}
			body := map[string]string{"name": name, "color": labelColours[state], "description": labelDescriptions[state]}
			if _, err := c.do(ctx, http.MethodPost, "/repos/"+repo.Name+"/labels", body, nil); err != nil {
				return created, fmt.Errorf("%s: create label %s: %w", repo.Name, name, err)
			}
			created++
		}
	}
	return created, nil
}
