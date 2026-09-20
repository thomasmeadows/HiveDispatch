// Package localdir stores run state in a plain directory. It is the test
// store and the fallback when no state branch is configured.
package localdir

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/state"
)

// Store writes runs/<KEY>.json and logs/<KEY>/... under Dir.
type Store struct {
	Dir string
	Now func() time.Time

	mu sync.Mutex
}

var _ state.RunStore = (*Store)(nil)

// New returns a store rooted at dir.
func New(dir string) *Store {
	return &Store{Dir: dir, Now: time.Now}
}

func (s *Store) runPath(key string) string {
	return filepath.Join(s.Dir, "runs", key+".json")
}

// Load implements state.RunStore.
func (s *Store) Load(_ context.Context, key string) (*state.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.runPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return &state.Run{Ticket: key}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("state: read %s: %w", key, err)
	}
	var run state.Run
	if err := json.Unmarshal(raw, &run); err != nil {
		return nil, fmt.Errorf("state: parse %s: %w", key, err)
	}
	return &run, nil
}

// Save implements state.RunStore with an atomic write.
func (s *Store) Save(_ context.Context, run *state.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run.UpdatedAt = s.Now().UTC()
	p := s.runPath(run.Ticket)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// AppendLog implements state.RunStore.
func (s *Store) AppendLog(_ context.Context, key string, e state.LogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Time.IsZero() {
		e.Time = s.Now().UTC()
	}
	dir := filepath.Join(s.Dir, "logs", key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(raw, '\n'))
	return err
}

// WriteLog implements state.RunStore.
func (s *Store) WriteLog(_ context.Context, key, name, content string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.Dir, "logs", key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, name+".log")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return "", err
	}
	// Raw logs are aged by mtime; pin it to the store clock.
	now := s.Now()
	return p, os.Chtimes(p, now, now)
}

// List implements state.RunStore.
func (s *Store) List(_ context.Context) ([]state.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ListRuns(s.Dir)
}

// Prune implements state.RunStore.
func (s *Store) Prune(_ context.Context, before time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, _, err := PruneTree(s.Dir, before)
	return n, err
}
