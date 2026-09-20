package ghissues

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

func TestSplitAndWithMarker(t *testing.T) {
	at := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	body := withMarker("Print the version.", "worker-a", at)
	if !strings.HasSuffix(body, "<!-- hivedispatch-claim: worker-a 2026-09-20T10:00:00Z -->") {
		t.Errorf("body = %q", body)
	}
	clean, claim := splitMarker(body)
	if clean != "Print the version." || claim == nil || claim.AgentID != "worker-a" || !claim.At.Equal(at) {
		t.Errorf("clean=%q claim=%+v", clean, claim)
	}
	if clean, claim := splitMarker("no marker\n\n"); clean != "no marker" || claim != nil {
		t.Errorf("no marker: %q %+v", clean, claim)
	}
	// Two markers: the last wins and nothing after it survives.
	twice := withMarker(withMarker("x", "a", at), "b", at.Add(time.Hour))
	if clean, claim := splitMarker(twice); claim == nil || claim.AgentID != "b" || strings.Contains(clean, "hivedispatch") {
		t.Errorf("twice: %q %+v", clean, claim)
	}
	if _, claim := splitMarker("<!-- hivedispatch-claim: -->"); claim != nil {
		t.Errorf("empty marker should be unclaimed: %+v", claim)
	}
}

// issueServer holds one issue body and records PATCHes.
type issueServer struct {
	mu      sync.Mutex
	body    string
	patches []string
}

func (s *issueServer) mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/issues/12", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"number": 12, "title": "t", "body": s.body, "labels": []any{}, "updated_at": "2026-09-20T10:00:00Z"})
	})
	mux.HandleFunc("GET /repos/o/r/issues/12/comments", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[]`)) })
	mux.HandleFunc("PATCH /repos/o/r/issues/12", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(b) != 1 {
			w.WriteHeader(400) // only the body may be patched here
			return
		}
		s.body, _ = b["body"].(string)
		s.patches = append(s.patches, s.body)
		_ = json.NewEncoder(w).Encode(map[string]any{"number": 12, "body": s.body})
	})
	return mux
}

var claimAt = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

func TestClaimWonAndRelease(t *testing.T) {
	s := &issueServer{body: "Print the version."}
	c := newTestClient(t, s.mux())
	ctx := context.Background()
	won, err := c.Claim(ctx, "HD-12", "worker-a", claimAt)
	if err != nil || !won {
		t.Fatalf("won=%v err=%v", won, err)
	}
	if !strings.Contains(s.body, "hivedispatch-claim: worker-a 2026-09-20T12:00:00Z") || !strings.HasPrefix(s.body, "Print the version.") {
		t.Errorf("body after claim = %q", s.body)
	}
	if err := c.Release(ctx, "HD-12", "worker-b"); !errors.Is(err, tracker.ErrNotClaimHolder) {
		t.Errorf("non-holder release: %v", err)
	}
	if err := c.Release(ctx, "HD-12", "worker-a"); err != nil {
		t.Fatal(err)
	}
	if s.body != "Print the version." {
		t.Errorf("body after release = %q", s.body)
	}
}

func TestClaimLostOnReadBack(t *testing.T) {
	s := &issueServer{body: "x"}
	c := newTestClient(t, s.mux(), withBeforeReadBack(func(string) {
		s.mu.Lock()
		s.body = withMarker("x", "worker-b", claimAt.Add(time.Second))
		s.mu.Unlock()
	}))
	won, err := c.Claim(context.Background(), "HD-12", "worker-a", claimAt)
	if err != nil || won {
		t.Fatalf("won=%v err=%v", won, err)
	}
}

func TestHeartbeatRequiresHolderAndRefreshes(t *testing.T) {
	s := &issueServer{body: withMarker("x", "worker-a", claimAt.Add(-time.Hour))}
	c := newTestClient(t, s.mux(), withNow(func() time.Time { return claimAt }))
	ctx := context.Background()
	if err := c.Heartbeat(ctx, "HD-12", "worker-b"); !errors.Is(err, tracker.ErrNotClaimHolder) {
		t.Errorf("non-holder heartbeat: %v", err)
	}
	if len(s.patches) != 0 {
		t.Error("non-holder must not write")
	}
	if err := c.Heartbeat(ctx, "HD-12", "worker-a"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.body, "worker-a 2026-09-20T12:00:00Z") {
		t.Errorf("heartbeat did not refresh: %q", s.body)
	}
}
