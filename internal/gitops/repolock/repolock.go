// Package repolock serialises git operations on one repository's shared
// .git directory. Ticket worktrees and the state-branch worktree all hang
// off the same base clone; concurrent fetches, worktree adds and pushes
// there contend for the same ref and index locks, and git fails rather
// than waits. Every writer takes the lock for its repository directory
// (<workroot>/repos/<owner>__<repo>) first.
package repolock

import (
	"path/filepath"
	"sync"
)

var (
	mu    sync.Mutex
	locks = map[string]*sync.Mutex{}
)

// For returns the lock for the repository directory dir.
func For(dir string) *sync.Mutex {
	dir = filepath.Clean(dir)
	mu.Lock()
	defer mu.Unlock()
	l, ok := locks[dir]
	if !ok {
		l = &sync.Mutex{}
		locks[dir] = l
	}
	return l
}
