// Package fake is a directory-backed gitops.Workspaces for tests.
package fake

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/gitops"
)

// Workspaces creates one directory per ticket under Root.
type Workspaces struct {
	Root    string
	Changed map[string]bool // ticket key → Finalize reports pushed

	mu        sync.Mutex
	finalized []string
}

var _ gitops.Workspaces = (*Workspaces)(nil)

// New returns a fake rooted at root.
func New(root string) *Workspaces {
	return &Workspaces{Root: root, Changed: map[string]bool{}}
}

// Prepare creates <root>/<key> and returns it on branch.
func (w *Workspaces) Prepare(_ context.Context, repo config.RepoConfig, key, branch string) (gitops.Workspace, error) {
	dir := filepath.Join(w.Root, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return gitops.Workspace{}, err
	}
	return gitops.Workspace{Path: dir, Branch: branch, Base: "origin/" + repo.DefaultBranch}, nil
}

// Finalize records the call and reports Changed for the ticket.
func (w *Workspaces) Finalize(_ context.Context, ws gitops.Workspace, message string) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	key := filepath.Base(ws.Path)
	w.finalized = append(w.finalized, key+": "+message)
	return w.Changed[key], nil
}

// Finalized returns "<key>: <message>" for every Finalize call.
func (w *Workspaces) Finalized() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.finalized...)
}

// Diff implements gitops.Workspaces with a placeholder diff.
func (w *Workspaces) Diff(_ context.Context, ws gitops.Workspace) (string, error) {
	return "diff --git a/" + filepath.Base(ws.Path) + " (fake)\n", nil
}
