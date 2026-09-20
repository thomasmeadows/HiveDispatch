// Package router fans a single state.RunStore out to one store per Jira
// project, so the dispatcher sees one store while each repo keeps its own
// state branch.
package router

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/state"
)

// Store routes by the ticket key's project prefix.
type Store struct {
	Stores map[string]state.RunStore
}

var _ state.RunStore = (*Store)(nil)

func (s *Store) pick(key string) (state.RunStore, error) {
	project, _, _ := strings.Cut(key, "-")
	if st, ok := s.Stores[strings.ToUpper(project)]; ok {
		return st, nil
	}
	return nil, fmt.Errorf("router: no store for project %q", project)
}

// Load implements state.RunStore.
func (s *Store) Load(ctx context.Context, key string) (*state.Run, error) {
	st, err := s.pick(key)
	if err != nil {
		return nil, err
	}
	return st.Load(ctx, key)
}

// Save implements state.RunStore.
func (s *Store) Save(ctx context.Context, run *state.Run) error {
	st, err := s.pick(run.Ticket)
	if err != nil {
		return err
	}
	return st.Save(ctx, run)
}

// AppendLog implements state.RunStore.
func (s *Store) AppendLog(ctx context.Context, key string, e state.LogEntry) error {
	st, err := s.pick(key)
	if err != nil {
		return err
	}
	return st.AppendLog(ctx, key, e)
}

// WriteLog implements state.RunStore.
func (s *Store) WriteLog(ctx context.Context, key, name, content string) (string, error) {
	st, err := s.pick(key)
	if err != nil {
		return "", err
	}
	return st.WriteLog(ctx, key, name, content)
}

// List implements state.RunStore by concatenating every store.
func (s *Store) List(ctx context.Context) ([]state.Run, error) {
	var out []state.Run
	for _, st := range s.Stores {
		runs, err := st.List(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, runs...)
	}
	return out, nil
}

// Prune implements state.RunStore across every store.
func (s *Store) Prune(ctx context.Context, before time.Time) (int, error) {
	total := 0
	for _, st := range s.Stores {
		n, err := st.Prune(ctx, before)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
