// Package gitbranch stores run state on an orphan branch, hive/state, checked
// out as a worktree of the repo's base clone.
//
// One file per ticket, written only by the worker holding the claim. Every
// write is committed and pushed before the next action starts. A push
// rejection means someone else pushed: rebase if they touched other files,
// fail loudly if they touched ours — that is a broken claim, not a race to
// smooth over.
package gitbranch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/state"
)

// Branch is the orphan branch name.
const Branch = "hive/state"

// ErrClaimInvariant is returned when another worker modified a file this
// worker owns.
var ErrClaimInvariant = errors.New("gitbranch: another worker modified our run file (claim invariant broken)")

const (
	commitName  = "HiveDispatch"
	commitEmail = "hivedispatch@localhost"
)

// Store is a state.RunStore backed by the state branch worktree at Dir.
type Store struct {
	Dir string
	Now func() time.Time

	mu sync.Mutex
}

var _ state.RunStore = (*Store)(nil)

// Open ensures the state branch exists (creating and pushing an orphan if
// not) and is checked out at dir as a worktree of base.
func Open(ctx context.Context, base, dir string) (*Store, error) {
	s := &Store{Dir: dir, Now: time.Now}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		_, _ = runEnv(ctx, dir, identityEnv(commitName, commitEmail), "pull", "-q", "--rebase", "origin", Branch) // best effort
		return s, nil
	}
	if _, err := run(ctx, base, "fetch", "-q", "--prune", "origin"); err != nil {
		return nil, err
	}
	if _, err := run(ctx, base, "worktree", "prune"); err != nil {
		return nil, err
	}
	switch {
	case refExists(ctx, base, "refs/heads/"+Branch):
		if _, err := run(ctx, base, "worktree", "add", "-q", dir, Branch); err != nil {
			return nil, err
		}
	case refExists(ctx, base, "refs/remotes/origin/"+Branch):
		if _, err := run(ctx, base, "worktree", "add", "-q", "--track", "-b", Branch, dir, "origin/"+Branch); err != nil {
			return nil, err
		}
	default:
		if _, err := run(ctx, base, "worktree", "add", "-q", "--orphan", "-b", Branch, dir); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("HiveDispatch run state. Managed automatically; do not edit by hand.\n"), 0o644); err != nil {
			return nil, err
		}
		if err := s.commitAndPush(ctx, "hive: init state", nil); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) runPath(key string) string {
	return filepath.Join("runs", key+".json")
}

// Load reads the run from the local worktree.
func (s *Store) Load(_ context.Context, key string) (*state.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(filepath.Join(s.Dir, s.runPath(key)))
	if errors.Is(err, os.ErrNotExist) {
		return &state.Run{Ticket: key}, nil
	}
	if err != nil {
		return nil, err
	}
	var run state.Run
	if err := json.Unmarshal(raw, &run); err != nil {
		return nil, fmt.Errorf("gitbranch: parse %s: %w", key, err)
	}
	return &run, nil
}

// Save writes, commits, and pushes the run file.
func (s *Store) Save(ctx context.Context, run *state.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run.UpdatedAt = s.Now().UTC()
	raw, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	rel := s.runPath(run.Ticket)
	if err := s.write(rel, raw); err != nil {
		return err
	}
	return s.commitAndPush(ctx, fmt.Sprintf("hive: %s %s", run.Ticket, run.Phase), []string{rel})
}

// AppendLog appends an event and pushes.
func (s *Store) AppendLog(ctx context.Context, key string, e state.LogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Time.IsZero() {
		e.Time = s.Now().UTC()
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	rel := filepath.Join("logs", key, "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(s.Dir, rel)), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.Dir, rel), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(raw, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	return s.commitAndPush(ctx, fmt.Sprintf("hive: %s %s", key, e.Event), []string{rel})
}

// WriteLog stores a raw log and pushes.
func (s *Store) WriteLog(ctx context.Context, key, name, content string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rel := filepath.Join("logs", key, name+".log")
	if err := s.write(rel, []byte(content)); err != nil {
		return "", err
	}
	return filepath.Join(s.Dir, rel), s.commitAndPush(ctx, fmt.Sprintf("hive: %s log %s", key, name), []string{rel})
}

func (s *Store) write(rel string, raw []byte) error {
	p := filepath.Join(s.Dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, raw, 0o644)
}

// commitAndPush commits everything staged from the worktree and pushes.
// On rejection it fetches; if the incoming commits touch any of ours it
// returns ErrClaimInvariant, otherwise it rebases and pushes again.
func (s *Store) commitAndPush(ctx context.Context, msg string, ours []string) error {
	env := identityEnv(commitName, commitEmail)
	if _, err := run(ctx, s.Dir, "add", "-A"); err != nil {
		return err
	}
	if _, err := run(ctx, s.Dir, "diff", "--cached", "--quiet"); err != nil {
		// Non-zero exit means there is something to commit.
		if _, err := runEnv(ctx, s.Dir, env, "commit", "-q", "-m", msg); err != nil {
			return err
		}
	}
	if _, err := run(ctx, s.Dir, "push", "-q", "-u", "origin", Branch); err == nil {
		return nil
	}
	if _, err := run(ctx, s.Dir, "fetch", "-q", "origin", Branch); err != nil {
		return err
	}
	incoming, err := run(ctx, s.Dir, "diff", "--name-only", "HEAD...origin/"+Branch)
	if err != nil {
		return err
	}
	for _, f := range strings.Split(incoming, "\n") {
		for _, o := range ours {
			if filepath.ToSlash(f) == filepath.ToSlash(o) {
				return fmt.Errorf("%w: %s", ErrClaimInvariant, o)
			}
		}
	}
	if _, err := runEnv(ctx, s.Dir, env, "rebase", "-q", "origin/"+Branch); err != nil {
		_, _ = run(ctx, s.Dir, "rebase", "--abort")
		return fmt.Errorf("gitbranch: rebase onto origin/%s: %w", Branch, err)
	}
	if _, err := run(ctx, s.Dir, "push", "-q", "-u", "origin", Branch); err != nil {
		return fmt.Errorf("gitbranch: push after rebase: %w", err)
	}
	return nil
}
