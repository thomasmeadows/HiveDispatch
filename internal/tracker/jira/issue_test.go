package jira

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGetMapsIssueToTicket(t *testing.T) {
	mux := http.NewServeMux()
	var gotFields string
	mux.HandleFunc("GET /rest/api/3/issue/HIVE-1", func(w http.ResponseWriter, r *http.Request) {
		gotFields = r.URL.Query().Get("fields")
		_, _ = w.Write(fixture(t, "issue.json"))
	})
	c := newTestClient(t, mux)
	tk, err := c.Get(context.Background(), "HIVE-1")
	if err != nil {
		t.Fatal(err)
	}
	if gotFields != "summary,description,status,labels,updated,comment,customfield_10042,customfield_10043" {
		t.Errorf("fields param = %q", gotFields)
	}
	if tk.Key != "HIVE-1" || tk.Summary != "Add a --version flag" || tk.Description != "Print the build version." {
		t.Errorf("basic fields: %+v", tk)
	}
	if tk.Status != "Ready" || len(tk.Labels) != 1 || tk.Labels[0] != "hive" {
		t.Errorf("status/labels: %+v", tk)
	}
	if tk.URL != c.base.String()+"/browse/HIVE-1" {
		t.Errorf("url = %q", tk.URL)
	}
	if tk.Claim == nil || tk.Claim.AgentID != "worker-b" {
		t.Fatalf("claim = %+v", tk.Claim)
	}
	wantAt := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	if !tk.Claim.At.Equal(wantAt) {
		t.Errorf("claim.At = %v, want %v", tk.Claim.At, wantAt)
	}
	if len(tk.Comments) != 1 {
		t.Fatalf("comments = %+v", tk.Comments)
	}
	cm := tk.Comments[0]
	if cm.ID != "20001" || cm.AuthorID != "acc-1" || cm.Author != "Thomas" || cm.Body != "Please also update the README." {
		t.Errorf("comment = %+v", cm)
	}
	if !cm.Created.Equal(time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("comment.Created = %v", cm.Created)
	}
}

func TestGetNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rest/api/3/issue/HIVE-9", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"errorMessages":["Issue does not exist"],"errors":{}}`))
	})
	c := newTestClient(t, mux)
	_, err := c.Get(context.Background(), "HIVE-9")
	if !errors.Is(err, tracker.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestPollFollowsNextPageToken(t *testing.T) {
	mux := http.NewServeMux()
	var bodies []map[string]any
	mux.HandleFunc("POST /rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		if body["nextPageToken"] == "tok2" {
			_, _ = w.Write(fixture(t, "search_page2.json"))
			return
		}
		_, _ = w.Write(fixture(t, "search_page1.json"))
	})
	c := newTestClient(t, mux)
	got, err := c.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Key != "HIVE-1" || got[1].Key != "HIVE-2" {
		t.Fatalf("got %+v", got)
	}
	if got[0].Claim != nil {
		t.Errorf("null custom fields should mean no claim, got %+v", got[0].Claim)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d", len(bodies))
	}
	if bodies[0]["jql"] != `project = HIVE AND status = "Ready"` {
		t.Errorf("jql = %v", bodies[0]["jql"])
	}
	if _, has := bodies[0]["nextPageToken"]; has {
		t.Error("first request must not send nextPageToken")
	}
	if f, ok := bodies[0]["fields"].([]any); !ok || len(f) != 8 {
		t.Errorf("fields = %v", bodies[0]["fields"])
	}
}

func TestPollStopsAtPageLimit(t *testing.T) {
	mux := http.NewServeMux()
	calls := 0
	mux.HandleFunc("POST /rest/api/3/search/jql", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"issues":[],"nextPageToken":"again","isLast":false}`))
	})
	c := newTestClient(t, mux)
	_, err := c.Poll(context.Background())
	if err == nil {
		t.Fatal("expected error when pagination never terminates")
	}
	if calls != maxPollPages {
		t.Errorf("calls = %d, want %d", calls, maxPollPages)
	}
}
