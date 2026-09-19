package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

func TestCommentPostsADF(t *testing.T) {
	mux := http.NewServeMux()
	var got map[string]any
	mux.HandleFunc("POST /rest/api/3/issue/HIVE-1/comment", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"1"}`))
	})
	c := newTestClient(t, mux)
	if err := c.Comment(context.Background(), "HIVE-1", "first line\nsecond"); err != nil {
		t.Fatal(err)
	}
	body, _ := got["body"].(map[string]any)
	if body["type"] != "doc" || body["version"] != float64(1) {
		t.Fatalf("body = %v", got)
	}
	content, _ := body["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("paragraphs = %v", content)
	}
}

func transitionsMux(posted *string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rest/api/3/issue/HIVE-1/transitions", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"transitions":[
		  {"id":"11","name":"Start work","to":{"name":"In Progress"}},
		  {"id":"21","name":"Ask","to":{"name":"needs info"}},
		  {"id":"31","name":"Review","to":{"name":"In Review"}}
		]}`))
	})
	mux.HandleFunc("POST /rest/api/3/issue/HIVE-1/transitions", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Transition struct{ ID string } `json:"transition"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		*posted = body.Transition.ID
		w.WriteHeader(204)
	})
	return mux
}

func TestTransitionMatchesStatusNameCaseInsensitively(t *testing.T) {
	var posted string
	c := newTestClient(t, transitionsMux(&posted))
	if err := c.Transition(context.Background(), "HIVE-1", tracker.StateNeedsInfo); err != nil {
		t.Fatal(err)
	}
	if posted != "21" {
		t.Errorf("posted transition %q, want 21", posted)
	}
}

func TestTransitionUnavailableListsOptions(t *testing.T) {
	var posted string
	c := newTestClient(t, transitionsMux(&posted))
	err := c.Transition(context.Background(), "HIVE-1", tracker.StateNeedsHuman)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), `"Needs Human"`) || !strings.Contains(err.Error(), "In Progress") {
		t.Errorf("err = %v, want target and available names", err)
	}
	if posted != "" {
		t.Error("must not post when no transition matches")
	}
}

func TestTransitionRejectsInvalidState(t *testing.T) {
	c := newTestClient(t, http.NewServeMux())
	if err := c.Transition(context.Background(), "HIVE-1", tracker.State("bogus")); err == nil {
		t.Fatal("expected error")
	}
}
