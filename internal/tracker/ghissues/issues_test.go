package ghissues

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

const issueFixture = `{
  "number": 12, "title": "Add --version", "state": "open",
  "html_url": "https://github.com/o/r/issues/12",
  "body": "Print the version.\n\n<!-- hivedispatch-claim: worker-b 2026-09-20T10:00:00Z -->",
  "labels": [{"name": "hive:ready"}, {"name": "bug"}],
  "updated_at": "2026-09-20T10:15:00Z"
}`

func TestGetMapsIssue(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/issues/12", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(issueFixture)) })
	mux.HandleFunc("GET /repos/o/r/issues/12/comments", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id": 5, "user": {"login": "thomas", "id": 7}, "body": "Also the SHA.", "created_at": "2026-09-20T09:00:00Z"}]`))
	})
	c := newTestClient(t, mux)
	tk, err := c.Get(context.Background(), "HD-12")
	if err != nil {
		t.Fatal(err)
	}
	if tk.Key != "HD-12" || tk.Summary != "Add --version" || tk.Description != "Print the version." || tk.URL != "https://github.com/o/r/issues/12" {
		t.Errorf("ticket = %+v", tk)
	}
	if tk.Status != "hive:ready" || len(tk.Labels) != 2 {
		t.Errorf("status/labels = %q %v", tk.Status, tk.Labels)
	}
	if tk.Claim == nil || tk.Claim.AgentID != "worker-b" || !tk.Claim.At.Equal(time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("claim = %+v", tk.Claim)
	}
	if len(tk.Comments) != 1 || tk.Comments[0].Author != "thomas" || tk.Comments[0].AuthorID != "7" || tk.Comments[0].Body != "Also the SHA." || tk.Comments[0].ID != "5" {
		t.Errorf("comments = %+v", tk.Comments)
	}
}

func TestPollPaginatesSkipsPRsAndSpansRepos(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("labels") != "hive:ready" || r.URL.Query().Get("state") != "open" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`[{"number": 2, "title": "two", "labels": [{"name":"hive:ready"}], "updated_at": "2026-09-20T10:00:00Z"}]`))
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<http://%s/repos/o/r/issues?labels=hive%%3Aready&state=open&page=2>; rel="next"`, r.Host))
		_, _ = w.Write([]byte(`[{"number": 1, "title": "one", "labels": [{"name":"hive:ready"}], "updated_at": "2026-09-20T10:00:00Z"},
		                       {"number": 9, "title": "a PR", "pull_request": {"url": "x"}, "labels": [{"name":"hive:ready"}], "updated_at": "2026-09-20T10:00:00Z"}]`))
	})
	mux.HandleFunc("GET /repos/o/other/issues", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"number": 7, "title": "seven", "labels": [{"name":"hive:ready"}], "updated_at": "2026-09-20T10:00:00Z"}]`))
	})
	mux.HandleFunc("GET /repos/{o}/{r}/issues/{n}/comments", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[]`)) })
	c := newTestClient(t, mux)
	got, err := c.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, tk := range got {
		keys = append(keys, tk.Key)
	}
	if strings.Join(keys, ",") != "HD-1,HD-2,OT-7" {
		t.Errorf("keys = %v", keys)
	}
}

func TestCommentAndTransition(t *testing.T) {
	mux := http.NewServeMux()
	var posted string
	var removed []string
	var added []string
	mux.HandleFunc("POST /repos/o/r/issues/12/comments", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		posted = b["body"]
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id": 1}`))
	})
	mux.HandleFunc("GET /repos/o/r/issues/12", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(issueFixture)) })
	mux.HandleFunc("DELETE /repos/o/r/issues/12/labels/{name}", func(w http.ResponseWriter, r *http.Request) {
		removed = append(removed, r.PathValue("name"))
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("POST /repos/o/r/issues/12/labels", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Labels []string }
		_ = json.NewDecoder(r.Body).Decode(&b)
		added = append(added, b.Labels...)
		_, _ = w.Write([]byte(`[]`))
	})
	c := newTestClient(t, mux)
	if err := c.Comment(context.Background(), "HD-12", "hello **world**"); err != nil || posted != "hello **world**" {
		t.Errorf("comment: %v posted=%q", err, posted)
	}
	if err := c.Transition(context.Background(), "HD-12", tracker.StateInProgress); err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "hive:ready" {
		t.Errorf("removed = %v (bug must stay)", removed)
	}
	if len(added) != 1 || added[0] != "hive:in-progress" {
		t.Errorf("added = %v", added)
	}
	if err := c.Transition(context.Background(), "HD-12", tracker.State("bogus")); err == nil {
		t.Error("invalid state must error")
	}
}
