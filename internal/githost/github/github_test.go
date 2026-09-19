package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/githost"
)

func newClient(t *testing.T, mux *http.ServeMux) *Client {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := New(config.GitHubConfig{APIURL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestFindPRQueriesByHead(t *testing.T) {
	mux := http.NewServeMux()
	var gotQuery, gotAuth string
	mux.HandleFunc("GET /repos/o/r/pulls", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`[{"number":7,"html_url":"https://github.com/o/r/pull/7","draft":false}]`))
	})
	c := newClient(t, mux)
	pr, err := c.FindPR(context.Background(), "o/r", "hive/HIVE-1")
	if err != nil || pr == nil || pr.Number != 7 || pr.URL != "https://github.com/o/r/pull/7" {
		t.Fatalf("pr=%+v err=%v", pr, err)
	}
	if gotQuery != "head=o%3Ahive%2FHIVE-1&per_page=1&state=open" {
		t.Errorf("query = %q", gotQuery)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth = %q", gotAuth)
	}
}

func TestFindPRNone(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/pulls", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[]`)) })
	pr, err := newClient(t, mux).FindPR(context.Background(), "o/r", "hive/HIVE-1")
	if err != nil || pr != nil {
		t.Fatalf("pr=%v err=%v", pr, err)
	}
}

func TestOpenPRPostsAndMapsErrors(t *testing.T) {
	mux := http.NewServeMux()
	var body map[string]any
	mux.HandleFunc("POST /repos/o/r/pulls", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["head"] == "bad" {
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"message":"No commits between main and bad"}]}`))
			return
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"number":8,"html_url":"https://github.com/o/r/pull/8","draft":true}`))
	})
	c := newClient(t, mux)
	pr, err := c.OpenPR(context.Background(), "o/r", githost.Request{Title: "T", Body: "B", Head: "hive/HIVE-1", Base: "main", Draft: true})
	if err != nil || pr.Number != 8 || !pr.Draft {
		t.Fatalf("pr=%+v err=%v", pr, err)
	}
	if body["title"] != "T" || body["head"] != "hive/HIVE-1" || body["base"] != "main" || body["draft"] != true {
		t.Errorf("body = %v", body)
	}
	_, err = c.OpenPR(context.Background(), "o/r", githost.Request{Head: "bad", Base: "main"})
	if err == nil || !strings.Contains(err.Error(), "422") || !strings.Contains(err.Error(), "No commits between") {
		t.Errorf("err = %v", err)
	}
}
