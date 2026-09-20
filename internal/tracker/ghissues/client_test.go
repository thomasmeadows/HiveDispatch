package ghissues

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

func testCfg(url string) config.GitHubConfig {
	return config.GitHubConfig{APIURL: url, Token: "tok", Labels: config.GitHubLabels{
		Ready: "hive:ready", InProgress: "hive:in-progress", NeedsInfo: "hive:needs-info",
		InReview: "hive:in-review", NeedsHuman: "hive:needs-human",
	}}
}

var testRepos = []config.RepoConfig{
	{Name: "o/r", Project: "HD"},
	{Name: "o/other", Project: "OT"},
}

func newTestClient(t *testing.T, mux *http.ServeMux, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := New(testCfg(srv.URL), testRepos, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestKeyMapping(t *testing.T) {
	c := newTestClient(t, http.NewServeMux())
	repo, n, err := c.keyToRef("HD-12")
	if err != nil || repo != "o/r" || n != 12 {
		t.Errorf("keyToRef = %q %d %v", repo, n, err)
	}
	if _, _, err := c.keyToRef("NOPE-1"); err == nil {
		t.Error("unknown project should error")
	}
	if _, _, err := c.keyToRef("HD-x"); err == nil {
		t.Error("non-numeric should error")
	}
	if key, ok := c.refToKey("o/other", 3); !ok || key != "OT-3" {
		t.Errorf("refToKey = %q %v", key, ok)
	}
}

func TestDoMapsNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/issues/99", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})
	c := newTestClient(t, mux)
	_, err := c.Get(context.Background(), "HD-99")
	if !errors.Is(err, tracker.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}
