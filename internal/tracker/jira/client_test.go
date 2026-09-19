package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

func testCfg(baseURL string) config.JiraConfig {
	return config.JiraConfig{
		BaseURL: baseURL,
		Email:   "me@example.com",
		Token:   "tok",
		JQL:     `project = HIVE AND status = "Ready"`,
		Fields:  config.JiraFields{AgentID: "customfield_10042", ClaimedAt: "customfield_10043"},
		Statuses: config.JiraStatuses{
			Ready: "Ready", InProgress: "In Progress", NeedsInfo: "Needs Info",
			InReview: "In Review", NeedsHuman: "Needs Human",
		},
	}
}

// newTestClient returns a client pointed at a mux-backed test server.
func newTestClient(t *testing.T, mux *http.ServeMux) *Client {
	t.Helper()
	return newTestClientWith(t, mux)
}

// newTestClientWith is newTestClient with extra options.
func newTestClientWith(t *testing.T, mux *http.ServeMux, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := New(testCfg(srv.URL), opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDoSendsBasicAuthAndJSON(t *testing.T) {
	mux := http.NewServeMux()
	var gotAuth, gotAccept, gotCT string
	var gotBody map[string]any
	mux.HandleFunc("POST /rest/api/3/echo", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotCT = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	c := newTestClient(t, mux)
	var out struct{ OK bool }
	if err := c.do(context.Background(), http.MethodPost, "/rest/api/3/echo", map[string]string{"a": "b"}, &out); err != nil {
		t.Fatal(err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("me@example.com:tok"))
	if gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}
	if gotAccept != "application/json" || gotCT != "application/json" {
		t.Errorf("headers accept=%q ct=%q", gotAccept, gotCT)
	}
	if gotBody["a"] != "b" {
		t.Errorf("body = %v", gotBody)
	}
	if !out.OK {
		t.Error("response not decoded")
	}
}

func TestDoMapsErrorsToAPIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rest/api/3/issue/NOPE-1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errorMessages":["Issue does not exist or you do not have permission to see it."],"errors":{}}`))
	})
	mux.HandleFunc("PUT /rest/api/3/issue/HIVE-1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errorMessages":[],"errors":{"customfield_10042":"Field 'customfield_10042' cannot be set. It is not on the appropriate screen, or unknown."}}`))
	})
	c := newTestClient(t, mux)

	err := c.do(context.Background(), http.MethodGet, "/rest/api/3/issue/NOPE-1", nil, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Fatalf("err = %v", err)
	}
	if !errors.Is(err, tracker.ErrNotFound) {
		t.Error("404 should satisfy errors.Is(err, tracker.ErrNotFound)")
	}

	err = c.do(context.Background(), http.MethodPut, "/rest/api/3/issue/HIVE-1", map[string]any{}, nil)
	if !errors.As(err, &apiErr) || apiErr.Status != 400 {
		t.Fatalf("err = %v", err)
	}
	if len(apiErr.Messages) != 1 || apiErr.Messages[0] != "customfield_10042: Field 'customfield_10042' cannot be set. It is not on the appropriate screen, or unknown." {
		t.Errorf("messages = %v", apiErr.Messages)
	}
}

func TestDoHandlesNoContent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /rest/api/3/issue/HIVE-1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	c := newTestClient(t, mux)
	if err := c.do(context.Background(), http.MethodPut, "/rest/api/3/issue/HIVE-1", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestNewRejectsBadURL(t *testing.T) {
	cfg := testCfg("://bad")
	if _, err := New(cfg); err == nil {
		t.Fatal("expected error")
	}
}
