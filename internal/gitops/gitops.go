// Package gitops defines how the dispatcher obtains and finalizes a
// per-ticket workspace. The real implementation (Phase 3) uses git
// worktrees; the fake uses plain directories.
package gitops

import (
	"context"

	"github.com/thomasmeadows/hivedispatch/internal/config"
)

// Workspace is a prepared checkout for one ticket.
type Workspace struct {
	Path   string
	Branch string
}

// Workspaces prepares and finalizes ticket workspaces.
type Workspaces interface {
	// Prepare returns a workspace on the ticket's branch, creating it from
	// the repo's default branch if needed. Calling it again for the same
	// ticket returns the same workspace (resume).
	Prepare(ctx context.Context, repo config.RepoConfig, ticketKey string) (Workspace, error)
	// Finalize commits any uncommitted changes with message and pushes the
	// branch if it has commits beyond the default branch. It reports
	// whether anything was pushed.
	Finalize(ctx context.Context, ws Workspace, message string) (pushed bool, err error)
}

// BranchName derives the work branch from the ticket key.
func BranchName(ticketKey string) string {
	return "hive/" + ticketKey
}
