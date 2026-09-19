package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func adminMux(fields []map[string]any, created *[]map[string]any, addedToScreen *[]string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rest/api/3/myself", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"accountId":"acc-1","displayName":"Thomas","emailAddress":"me@example.com"}`))
	})
	mux.HandleFunc("GET /rest/api/3/field", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(fields)
	})
	mux.HandleFunc("GET /rest/api/3/status", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"To Do"},{"name":"Ready"},{"name":"In Progress"},{"name":"Needs Info"},{"name":"In Review"},{"name":"Done"}]`))
	})
	mux.HandleFunc("POST /rest/api/3/search/jql", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"issues":[{"key":"HIVE-1","fields":{"summary":"x","status":{"name":"Ready"},"comment":{"comments":[]}}}],"isLast":true}`))
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
	rep, err := c.Check(context.Background())
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
