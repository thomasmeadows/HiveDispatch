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

// newTestClient serves o/r alone, as the worker does; newMultiClient serves
// both test repositories (label checks and polling span them).
func newTestClient(t *testing.T, mux *http.ServeMux, opts ...Option) *Client {
	t.Helper()
	return newClientFor(t, mux, testRepos[:1], opts...)
}

func newMultiClient(t *testing.T, mux *http.ServeMux) *Client {
	t.Helper()
	return newClientFor(t, mux, testRepos)
}

func newClientFor(t *testing.T, mux *http.ServeMux, repos []config.RepoConfig, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := New(testCfg(srv.URL), repos, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestKeyMapping(t *testing.T) {
	c, err := New(testCfg("http://unused"), testRepos[:1])
	if err != nil {
		t.Fatal(err)
	}
	repo, n, err := c.keyToRef("issues-12")
	if err != nil || repo != "o/r" || n != 12 {
		t.Errorf("keyToRef = %q %d %v", repo, n, err)
	}
	if _, _, err := c.keyToRef("PROJECT2-1"); err == nil {
		t.Error("another board should error")
	}
	if _, _, err := c.keyToRef("ISSUES-x"); err == nil {
		t.Error("non-numeric should error")
	}
	if key, ok := c.refToKey("o/r", 3); !ok || key != "ISSUES-3" {
		t.Errorf("refToKey = %q %v", key, ok)
	}
	if _, _, err := newMultiClient(t, http.NewServeMux()).keyToRef("ISSUES-1"); err == nil {
		t.Error("a key is ambiguous across two repositories")
	}
}

func TestBoardNamesTheProjectWhenSet(t *testing.T) {
	if got := Board(config.GitHubProject{}); got != "ISSUES" {
		t.Errorf("no board = %q", got)
	}
	if got := Board(config.GitHubProject{Owner: "o", Number: 2}); got != "PROJECT2" {
		t.Errorf("board 2 = %q", got)
	}
}

func TestDoMapsNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/issues/99", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})
	c := newTestClient(t, mux)
	_, err := c.Get(context.Background(), "ISSUES-99")
	if !errors.Is(err, tracker.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}
