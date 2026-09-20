package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/config"
)

func adminMux(fields []map[string]any, created *[]map[string]any, addedToScreen *[]string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rest/api/3/myself", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"accountId":"acc-1","displayName":"Thomas","emailAddress":"me@example.com"}`))
	})
	mux.HandleFunc("GET /rest/api/3/field/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("type") != "custom" {
			w.WriteHeader(400)
			return
		}
		// One item per page to exercise pagination.
		start := 0
		_, _ = fmt.Sscanf(r.URL.Query().Get("startAt"), "%d", &start)
		page := map[string]any{"startAt": start, "total": len(fields), "isLast": start+1 >= len(fields), "values": []any{}}
		if start < len(fields) {
			page["values"] = []any{fields[start]}
		}
		_ = json.NewEncoder(w).Encode(page)
	})
	mux.HandleFunc("GET /rest/api/3/project", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"key":"HIVE","name":"Hive","style":"classic"},{"key":"TM","name":"Team","style":"next-gen"}]`))
	})
	mux.HandleFunc("GET /rest/api/3/issue/{key}/editmeta", func(w http.ResponseWriter, r *http.Request) {
		editable := map[string]any{"summary": map[string]any{}}
		if r.PathValue("key") == "HIVE-1" {
			editable["customfield_10042"] = map[string]any{"name": "HiveDispatch Agent"}
			editable["customfield_10043"] = map[string]any{"name": "HiveDispatch Claimed At"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"fields": editable})
	})
	mux.HandleFunc("GET /rest/api/3/status", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"To Do"},{"name":"Ready"},{"name":"In Progress"},{"name":"Needs Info"},{"name":"In Review"},{"name":"Done"}]`))
	})
	mux.HandleFunc("POST /rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		key := "HIVE-1"
		if jql, _ := body["jql"].(string); strings.Contains(jql, "TM") {
			key = "TM-1"
		}
		_, _ = fmt.Fprintf(w, `{"issues":[{"key":%q,"fields":{"summary":"x","status":{"name":"Ready"},"comment":{"comments":[]}}}],"isLast":true}`, key)
	})
	mux.HandleFunc("POST /rest/api/3/field", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		*created = append(*created, body)
		id := fmt.Sprintf("customfield_2000%d", len(*created))
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "name": body["name"]})
	})
	mux.HandleFunc("POST /rest/api/3/screens/addToDefault/{id}", func(w http.ResponseWriter, r *http.Request) {
		*addedToScreen = append(*addedToScreen, r.PathValue("id"))
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`[]`))
	})
	return mux
}

func TestCheckReportsMissingFieldAndStatus(t *testing.T) {
	fields := []map[string]any{
		{"id": "customfield_10042", "name": "HiveDispatch Agent"},
		{"id": "summary", "name": "Summary"},
	}
	var created []map[string]any
	var added []string
	c := newTestClient(t, adminMux(fields, &created, &added))
	rep, err := c.Check(context.Background(), []string{"HIVE"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.User != "Thomas" {
		t.Errorf("user = %q", rep.User)
	}
	if len(rep.MissingFields) != 1 || rep.MissingFields[0] != "customfield_10043" {
		t.Errorf("missing fields = %v", rep.MissingFields)
	}
	if len(rep.MissingStatuses) != 1 || rep.MissingStatuses[0] != "Needs Human" {
		t.Errorf("missing statuses = %v", rep.MissingStatuses)
	}
	if rep.SampleTickets != 1 {
		t.Errorf("sample tickets = %d", rep.SampleTickets)
	}
	if rep.OK() {
		t.Error("report with missing items must not be OK")
	}
}

func TestEnsureFieldsCreatesOnlyMissing(t *testing.T) {
	fields := []map[string]any{
		{"id": "customfield_10042", "name": "HiveDispatch Agent"},
	}
	var created []map[string]any
	var added []string
	c := newTestClient(t, adminMux(fields, &created, &added))
	got, err := c.EnsureFields(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentID != "customfield_10042" {
		t.Errorf("agent id = %q, want existing field reused", got.AgentID)
	}
	if got.ClaimedAt != "customfield_20001" {
		t.Errorf("claimed at = %q", got.ClaimedAt)
	}
	if len(created) != 1 || created[0]["name"] != "HiveDispatch Claimed At" ||
		created[0]["type"] != "com.atlassian.jira.plugin.system.customfieldtypes:datetime" {
		t.Errorf("created = %v", created)
	}
	if len(added) != 1 || added[0] != "customfield_20001" {
		t.Errorf("added to screen = %v", added)
	}
}

func TestResolveFieldsByNameWhenUnset(t *testing.T) {
	fields := []map[string]any{
		{"id": "customfield_10042", "name": "HiveDispatch Agent"},
		{"id": "customfield_10043", "name": "HiveDispatch Claimed At"},
	}
	var created []map[string]any
	var added []string
	srv := httptest.NewServer(adminMux(fields, &created, &added))
	t.Cleanup(srv.Close)
	cfg := testCfg(srv.URL)
	cfg.Fields = config.JiraFields{} // nothing configured
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ResolveFields(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.cfg.Fields.AgentID != "customfield_10042" || c.cfg.Fields.ClaimedAt != "customfield_10043" {
		t.Errorf("resolved = %+v", c.cfg.Fields)
	}
	if got := c.fields(); got[6] != "customfield_10042" {
		t.Errorf("poll field list not updated: %v", got)
	}
}

func TestResolveFieldsKeepsExplicitIDsAndErrorsWhenMissing(t *testing.T) {
	var created []map[string]any
	var added []string
	srv := httptest.NewServer(adminMux(nil, &created, &added))
	t.Cleanup(srv.Close)
	cfg := testCfg(srv.URL) // explicit ids
	c, _ := New(cfg)
	if err := c.ResolveFields(context.Background()); err != nil {
		t.Fatalf("explicit ids must not need a lookup: %v", err)
	}
	cfg.Fields = config.JiraFields{}
	c, _ = New(cfg)
	err := c.ResolveFields(context.Background())
	if err == nil || !strings.Contains(err.Error(), "init -jira") {
		t.Fatalf("err = %v, want a hint to run init -jira", err)
	}
}

func TestCheckReportsDuplicatesAndNotEditable(t *testing.T) {
	fields := []map[string]any{
		{"id": "customfield_10042", "name": "HiveDispatch Agent"},
		{"id": "customfield_10047", "name": "HiveDispatch Agent"},
		{"id": "customfield_10043", "name": "HiveDispatch Claimed At"},
	}
	var created []map[string]any
	var added []string
	srv := httptest.NewServer(adminMux(fields, &created, &added))
	t.Cleanup(srv.Close)
	cfg := testCfg(srv.URL)
	cfg.Fields = config.JiraFields{}
	cfg.JQL = "project = TM" // sample issue TM-1 is not editable
	c, _ := New(cfg)
	rep, err := c.Check(context.Background(), []string{"TM"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.MissingFields) != 0 || len(rep.MissingStatuses) != 1 {
		t.Errorf("rep = %+v", rep)
	}
	if len(rep.DuplicateFields) != 1 || !strings.Contains(rep.DuplicateFields[0], "customfield_10047") {
		t.Errorf("duplicates = %v", rep.DuplicateFields)
	}
	if rep.NotEditableOn != "TM-1" {
		t.Errorf("NotEditableOn = %q", rep.NotEditableOn)
	}
	if rep.OK() {
		t.Error("not-editable fields must fail the check")
	}
	if c.cfg.Fields.AgentID != "customfield_10042" {
		t.Errorf("should resolve to the lowest id, got %s", c.cfg.Fields.AgentID)
	}
}

func TestCheckUsesProjectSampleWhenJQLIsEmpty(t *testing.T) {
	fields := []map[string]any{
		{"id": "customfield_10042", "name": "HiveDispatch Agent"},
		{"id": "customfield_10043", "name": "HiveDispatch Claimed At"},
	}
	var created []map[string]any
	var added []string
	mux := adminMux(fields, &created, &added)
	mux.HandleFunc("POST /rest/api/3/search/jql/{empty}", func(http.ResponseWriter, *http.Request) {})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := testCfg(srv.URL)
	cfg.JQL = "labels = nothing-matches"
	c, _ := New(cfg)
	rep, err := c.Check(context.Background(), []string{"HIVE"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.NotEditableOn != "" || rep.SampleIssue != "HIVE-1" {
		t.Errorf("rep = %+v", rep)
	}
}

func TestCheckReportsUnknownProjects(t *testing.T) {
	fields := []map[string]any{
		{"id": "customfield_10042", "name": "HiveDispatch Agent"},
		{"id": "customfield_10043", "name": "HiveDispatch Claimed At"},
	}
	var created []map[string]any
	var added []string
	c := newTestClient(t, adminMux(fields, &created, &added))
	rep, err := c.Check(context.Background(), []string{"HIVE", "1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.UnknownProjects) != 1 || rep.UnknownProjects[0] != "1" {
		t.Errorf("unknown = %v", rep.UnknownProjects)
	}
	if len(rep.Projects) != 2 || rep.Projects[1] != "TM (team-managed)" {
		t.Errorf("projects = %v", rep.Projects)
	}
	if rep.OK() {
		t.Error("unknown project must fail the check")
	}
}
