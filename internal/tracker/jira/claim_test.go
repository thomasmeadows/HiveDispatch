package jira

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

// claimServer simulates the two claim fields on one issue.
type claimServer struct {
	mu     sync.Mutex
	agent  any
	at     any
	puts   []map[string]any
	screen bool // when true, PUT fails with the "not on screen" error
}

func (s *claimServer) mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /rest/api/3/issue/HIVE-1", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Fields map[string]any `json:"fields"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.screen {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"errorMessages":[],"errors":{"customfield_10042":"Field 'customfield_10042' cannot be set. It is not on the appropriate screen, or unknown."}}`))
			return
		}
		s.puts = append(s.puts, body.Fields)
		if v, ok := body.Fields["customfield_10042"]; ok {
			s.agent = v
		}
		if v, ok := body.Fields["customfield_10043"]; ok {
			s.at = v
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /rest/api/3/issue/HIVE-1", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		resp := map[string]any{"id": "1", "key": "HIVE-1", "fields": map[string]any{
			"summary": "x", "status": map[string]any{"name": "Ready"}, "labels": []any{},
			"updated":           "2026-09-19T10:15:00.000+0000",
			"customfield_10042": s.agent, "customfield_10043": s.at,
			"comment": map[string]any{"comments": []any{}},
		}}
		_ = json.NewEncoder(w).Encode(resp)
	})
	return mux
}

var claimAt = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

func TestClaimWon(t *testing.T) {
	s := &claimServer{}
	c := newTestClient(t, s.mux())
	won, err := c.Claim(context.Background(), "HIVE-1", "worker-a", claimAt)
	if err != nil || !won {
		t.Fatalf("won=%v err=%v", won, err)
	}
	if len(s.puts) != 1 {
		t.Fatalf("puts = %v", s.puts)
	}
	if s.puts[0]["customfield_10042"] != "worker-a" || s.puts[0]["customfield_10043"] != "2026-09-19T12:00:00.000+0000" {
		t.Errorf("put fields = %v", s.puts[0])
	}
}

func TestClaimLostOnReadBack(t *testing.T) {
	s := &claimServer{}
	c := newTestClientWith(t, s.mux(), withBeforeReadBack(func(string) {
		s.mu.Lock()
		s.agent, s.at = "worker-b", "2026-09-19T12:00:01.000+0000"
		s.mu.Unlock()
	}))
	won, err := c.Claim(context.Background(), "HIVE-1", "worker-a", claimAt)
	if err != nil {
		t.Fatal(err)
	}
	if won {
		t.Fatal("should have lost")
	}
}

func TestClaimScreenErrorHasHint(t *testing.T) {
	s := &claimServer{screen: true}
	c := newTestClient(t, s.mux())
	_, err := c.Claim(context.Background(), "HIVE-1", "worker-a", claimAt)
	if err == nil || !strings.Contains(err.Error(), "setup.md") {
		t.Fatalf("err = %v, want setup hint", err)
	}
}

func TestHeartbeatRequiresHolder(t *testing.T) {
	s := &claimServer{agent: "worker-b", at: "2026-09-19T11:00:00.000+0000"}
	c := newTestClientWith(t, s.mux(), withNow(func() time.Time { return claimAt }))
	err := c.Heartbeat(context.Background(), "HIVE-1", "worker-a")
	if !errors.Is(err, tracker.ErrNotClaimHolder) {
		t.Fatalf("err = %v", err)
	}
	if len(s.puts) != 0 {
		t.Error("non-holder must not write")
	}
	if err := c.Heartbeat(context.Background(), "HIVE-1", "worker-b"); err != nil {
		t.Fatal(err)
	}
	if len(s.puts) != 1 || s.puts[0]["customfield_10043"] != "2026-09-19T12:00:00.000+0000" {
		t.Errorf("puts = %v", s.puts)
	}
	if _, has := s.puts[0]["customfield_10042"]; has {
		t.Error("heartbeat must not rewrite agent id")
	}
}

func TestReleaseClearsBothFields(t *testing.T) {
	s := &claimServer{agent: "worker-a", at: "2026-09-19T11:00:00.000+0000"}
	c := newTestClient(t, s.mux())
	if err := c.Release(context.Background(), "HIVE-1", "worker-b"); !errors.Is(err, tracker.ErrNotClaimHolder) {
		t.Fatalf("non-holder release err = %v", err)
	}
	if err := c.Release(context.Background(), "HIVE-1", "worker-a"); err != nil {
		t.Fatal(err)
	}
	if len(s.puts) != 1 || s.puts[0]["customfield_10042"] != nil || s.puts[0]["customfield_10043"] != nil {
		t.Errorf("puts = %v", s.puts)
	}
}
