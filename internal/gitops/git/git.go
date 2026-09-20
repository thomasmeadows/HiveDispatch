// Package git implements gitops.Workspaces with git worktrees.
//
// Layout under Root:
//
//	<owner>__<name>/repo      --no-checkout base clone, fetched on every Prepare
//	<owner>__<name>/<KEY>     worktree on hive/<KEY>
//	<owner>__<name>/.state    reserved for the state branch worktree
package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/gitops"
)

// Identity used for safety commits, so they are distinguishable from the
// agent's own commits.
const (
	commitName  = "HiveDispatch"
	commitEmail = "hivedispatch@localhost"
)

// Workspaces manages base clones and per-ticket worktrees under Root.
type Workspaces struct {
	Root string
}

var _ gitops.Workspaces = (*Workspaces)(nil)

// New returns a Workspaces rooted at root.
func New(root string) *Workspaces {
	return &Workspaces{Root: root}
}

// RepoDir is the directory holding everything for one repo.
func (w *Workspaces) RepoDir(repo config.RepoConfig) string {
	return filepath.Join(w.Root, strings.ReplaceAll(repo.Name, "/", "__"))
}

// EnsureBase clones the repo if needed and fetches. It returns the base
// clone path.
func (w *Workspaces) EnsureBase(ctx context.Context, repo config.RepoConfig) (string, error) {
	base := filepath.Join(w.RepoDir(repo), "repo")
	if _, err := os.Stat(filepath.Join(base, ".git")); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(w.RepoDir(repo), 0o755); err != nil {
			return "", err
		}
		if _, err := run(ctx, w.RepoDir(repo), "clone", "-q", "--no-checkout", repo.URL, base); err != nil {
			return "", fmt.Errorf("clone %s: %w", repo.Name, err)
		}
	}
	if _, err := run(ctx, base, "fetch", "-q", "--prune", "origin"); err != nil {
		return "", fmt.Errorf("fetch %s: %w", repo.Name, err)
	}
	return base, nil
}

// Prepare returns the ticket's worktree, creating it from the remote ticket
// branch if one exists, else from the default branch.
func (w *Workspaces) Prepare(ctx context.Context, repo config.RepoConfig, key string) (gitops.Workspace, error) {
	base, err := w.EnsureBase(ctx, repo)
	if err != nil {
		return gitops.Workspace{}, err
	}
	ws := gitops.Workspace{
		Path:   filepath.Join(w.RepoDir(repo), key),
		Branch: gitops.BranchName(key),
		Base:   "origin/" + repo.DefaultBranch,
	}
	if _, err := os.Stat(filepath.Join(ws.Path, ".git")); err == nil {
		// An existing worktree is reused as is, except that a branch with
		// nothing of its own (typically one whose PR was merged) is
		// fast-forwarded so the agent sees the current default branch.
		// A diverged branch is left alone: that is the review loop.
		_, _ = run(ctx, ws.Path, "merge", "-q", "--ff-only", ws.Base) // no-op when diverged
		return ws, nil
	}
	if _, err := run(ctx, base, "worktree", "prune"); err != nil {
		return gitops.Workspace{}, err
	}
	var args []string
	switch {
	case refExists(ctx, base, "refs/heads/"+ws.Branch):
		args = []string{"worktree", "add", "-q", ws.Path, ws.Branch}
	case refExists(ctx, base, "refs/remotes/origin/"+ws.Branch):
		args = []string{"worktree", "add", "-q", "--track", "-b", ws.Branch, ws.Path, "origin/" + ws.Branch}
	default:
		args = []string{"worktree", "add", "-q", "-b", ws.Branch, ws.Path, ws.Base}
	}
	if _, err := run(ctx, base, args...); err != nil {
		return gitops.Workspace{}, fmt.Errorf("worktree %s: %w", key, err)
	}
	return ws, nil
}

// Finalize commits a dirty tree under the HiveDispatch identity and pushes
// the branch when it has commits beyond Base.
func (w *Workspaces) Finalize(ctx context.Context, ws gitops.Workspace, message string) (bool, error) {
	status, err := run(ctx, ws.Path, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	if status != "" {
		if _, err := run(ctx, ws.Path, "add", "-A"); err != nil {
			return false, err
		}
		if _, err := runEnv(ctx, ws.Path, identityEnv(commitName, commitEmail), "commit", "-q", "-m", message); err != nil {
			return false, err
		}
	}
	ahead, err := run(ctx, ws.Path, "rev-list", "--count", ws.Base+"..HEAD")
	if err != nil {
		return false, err
	}
	if ahead == "0" {
		return false, nil
	}
	if _, err := run(ctx, ws.Path, "push", "-q", "-u", "origin", ws.Branch); err != nil {
		return false, fmt.Errorf("push %s: %w", ws.Branch, err)
	}
	return true, nil
}
