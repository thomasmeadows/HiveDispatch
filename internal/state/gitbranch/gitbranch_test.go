package gitbranch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/gitops/git/gittest"
	"github.com/thomasmeadows/hivedispatch/internal/state"
)

// worker returns a base clone and state dir for one simulated worker.
func worker(t *testing.T, remote string) (base, dir string) {
	t.Helper()
	root := t.TempDir()
	base = filepath.Join(root, "repo")
	gittest.Git(t, root, "clone", "-q", "--no-checkout", remote, base)
	return base, filepath.Join(root, ".state")
}

func TestOpenCreatesOrphanBranchAndPushes(t *testing.T) {
	remote := gittest.NewRemote(t)
	base, dir := worker(t, remote)
	s, err := Open(context.Background(), base, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := gittest.Git(t, s.Dir, "rev-parse", "--abbrev-ref", "HEAD"); got != Branch+"\n" {
		t.Errorf("branch = %q", got)
	}
	if got := gittest.Git(t, s.Dir, "rev-list", "--count", "HEAD"); got != "1\n" {
		t.Errorf("orphan should have exactly one commit, got %q", got)
	}
	if raw, _ := os.ReadFile(filepath.Join(s.Dir, "README.md")); strings.Contains(string(raw), "seed") {
		t.Error("orphan branch must not carry the default branch's files")
	}
	if !strings.Contains(gittest.Git(t, remote, "branch", "--list", Branch), Branch) {
		t.Error("state branch not pushed")
	}
}

func TestSaveLoadAndSecondWorkerSeesIt(t *testing.T) {
	remote := gittest.NewRemote(t)
	ctx := context.Background()
	baseA, dirA := worker(t, remote)
	a, err := Open(ctx, baseA, dirA)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Save(ctx, &state.Run{Ticket: "HIVE-1", Agent: "a", Attempts: 1, Phase: state.PhaseWorking}); err != nil {
		t.Fatal(err)
	}
	if err := a.AppendLog(ctx, "HIVE-1", state.LogEntry{Agent: "a", Event: "claimed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.WriteLog(ctx, "HIVE-1", "run-1", "output"); err != nil {
		t.Fatal(err)
	}
	baseB, dirB := worker(t, remote)
	b, err := Open(ctx, baseB, dirB)
	if err != nil {
		t.Fatal(err)
	}
	run, err := b.Load(ctx, "HIVE-1")
	if err != nil || run.Agent != "a" || run.Phase != state.PhaseWorking {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	if _, err := os.Stat(filepath.Join(b.Dir, "logs", "HIVE-1", "events.jsonl")); err != nil {
		t.Error("events log not replicated")
	}
	if _, err := os.Stat(filepath.Join(b.Dir, "logs", "HIVE-1", "run-1.log")); err != nil {
		t.Error("raw log not replicated")
	}
	if missing, err := b.Load(ctx, "HIVE-9"); err != nil || missing.Ticket != "HIVE-9" || missing.Attempts != 0 {
		t.Errorf("missing run = %+v, %v", missing, err)
	}
}

func TestSaveRebasesOverOtherWorkersFiles(t *testing.T) {
	remote := gittest.NewRemote(t)
	ctx := context.Background()
	baseA, dirA := worker(t, remote)
	a, _ := Open(ctx, baseA, dirA)
	baseB, dirB := worker(t, remote)
	b, _ := Open(ctx, baseB, dirB)

	if err := a.Save(ctx, &state.Run{Ticket: "HIVE-1", Agent: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(ctx, &state.Run{Ticket: "HIVE-2", Agent: "b"}); err != nil {
		t.Fatal(err) // b is behind a; must rebase and push
	}
	if err := a.Save(ctx, &state.Run{Ticket: "HIVE-1", Agent: "a", Attempts: 1}); err != nil {
		t.Fatal(err) // a is behind b; must rebase and push
	}
	if _, err := os.Stat(filepath.Join(a.Dir, "runs", "HIVE-2.json")); err != nil {
		t.Error("a should have b's file after rebasing")
	}
}

func TestSaveFailsLoudlyWhenOurFileWasModified(t *testing.T) {
	remote := gittest.NewRemote(t)
	ctx := context.Background()
	baseA, dirA := worker(t, remote)
	a, _ := Open(ctx, baseA, dirA)
	baseB, dirB := worker(t, remote)
	b, _ := Open(ctx, baseB, dirB)

	if err := a.Save(ctx, &state.Run{Ticket: "HIVE-1", Agent: "a"}); err != nil {
		t.Fatal(err)
	}
	// b writes a file a already owns upstream: a broken claim, caught on push.
	err := b.Save(ctx, &state.Run{Ticket: "HIVE-1", Agent: "b"})
	if !errors.Is(err, ErrClaimInvariant) {
		t.Fatalf("b err = %v, want ErrClaimInvariant", err)
	}
	// a is unaffected and keeps writing.
	if err := a.Save(ctx, &state.Run{Ticket: "HIVE-1", Agent: "a", Attempts: 2}); err != nil {
		t.Fatalf("a err = %v", err)
	}
}
