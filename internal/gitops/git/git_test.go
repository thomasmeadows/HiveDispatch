package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/gitops/git/gittest"
)

func repoCfg(remote string) config.RepoConfig {
	return config.RepoConfig{Name: "o/r", URL: remote, DefaultBranch: "main", JiraProject: "HIVE"}
}

func TestPrepareCreatesWorktreeOnTicketBranch(t *testing.T) {
	remote := gittest.NewRemote(t)
	w := New(t.TempDir())
	ws, err := w.Prepare(context.Background(), repoCfg(remote), "HIVE-1")
	if err != nil {
		t.Fatal(err)
	}
	if ws.Branch != "hive/HIVE-1" || ws.Base != "origin/main" {
		t.Errorf("ws = %+v", ws)
	}
	if got := gittest.Git(t, ws.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "hive/HIVE-1\n" {
		t.Errorf("branch = %q", got)
	}
	if _, err := os.Stat(filepath.Join(ws.Path, "README.md")); err != nil {
		t.Error("worktree should contain the default branch's files")
	}
	again, err := w.Prepare(context.Background(), repoCfg(remote), "HIVE-1")
	if err != nil || again.Path != ws.Path {
		t.Errorf("second Prepare = %+v, %v", again, err)
	}
}

func TestPrepareResumesRemoteBranchFromFreshRoot(t *testing.T) {
	remote := gittest.NewRemote(t)
	other := gittest.Clone(t, remote)
	gittest.Git(t, other, "checkout", "-q", "-b", "hive/HIVE-2")
	if err := os.WriteFile(filepath.Join(other, "wip.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, other, "add", "wip.txt")
	gittest.Git(t, other, "commit", "-q", "-m", "wip")
	gittest.Git(t, other, "push", "-q", "origin", "hive/HIVE-2")

	w := New(t.TempDir())
	ws, err := w.Prepare(context.Background(), repoCfg(remote), "HIVE-2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws.Path, "wip.txt")); err != nil {
		t.Error("worktree should resume from the pushed branch")
	}
}

func TestFinalizeCommitsDirtyTreeAndPushes(t *testing.T) {
	remote := gittest.NewRemote(t)
	w := New(t.TempDir())
	ctx := context.Background()
	ws, err := w.Prepare(ctx, repoCfg(remote), "HIVE-3")
	if err != nil {
		t.Fatal(err)
	}
	pushed, err := w.Finalize(ctx, ws, "hive: checkpoint")
	if err != nil || pushed {
		t.Fatalf("clean tree: pushed=%v err=%v", pushed, err)
	}
	if err := os.WriteFile(filepath.Join(ws.Path, "new.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	pushed, err = w.Finalize(ctx, ws, "hive: WIP (timeout)")
	if err != nil || !pushed {
		t.Fatalf("dirty tree: pushed=%v err=%v", pushed, err)
	}
	if got := gittest.Git(t, ws.Path, "log", "-1", "--format=%s %an"); got != "hive: WIP (timeout) HiveDispatch\n" {
		t.Errorf("last commit = %q", got)
	}
	if got := gittest.Git(t, ws.Path, "status", "--porcelain"); got != "" {
		t.Errorf("tree still dirty: %q", got)
	}
	remoteLog := gittest.Git(t, remote, "log", "-1", "--format=%s", "hive/HIVE-3")
	if remoteLog != "hive: WIP (timeout)\n" {
		t.Errorf("remote branch log = %q", remoteLog)
	}
	// Idempotent: nothing new → still reports the branch as pushed.
	pushed, err = w.Finalize(ctx, ws, "hive: checkpoint")
	if err != nil || !pushed {
		t.Fatalf("no-op finalize: pushed=%v err=%v", pushed, err)
	}
}
