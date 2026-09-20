package router

import (
	"context"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/state"
	"github.com/thomasmeadows/hivedispatch/internal/state/localdir"
)

func TestRoutesByProject(t *testing.T) {
	a, b := localdir.New(t.TempDir()), localdir.New(t.TempDir())
	r := &Store{Stores: map[string]state.RunStore{"HIVE": a, "OPS": b}}
	ctx := context.Background()
	if err := r.Save(ctx, &state.Run{Ticket: "OPS-3", Agent: "x"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.Load(ctx, "OPS-3"); got.Agent != "x" {
		t.Errorf("not routed to b: %+v", got)
	}
	if got, _ := a.Load(ctx, "OPS-3"); got.Agent != "" {
		t.Errorf("leaked into a: %+v", got)
	}
	if _, err := r.Load(ctx, "NOPE-1"); err == nil {
		t.Error("unknown project must error")
	}
}

func TestListMergesAndPruneSums(t *testing.T) {
	a, b := localdir.New(t.TempDir()), localdir.New(t.TempDir())
	r := &Store{Stores: map[string]state.RunStore{"HIVE": a, "OPS": b}}
	ctx := context.Background()
	old := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return old }
	b.Now = a.Now
	_ = r.Save(ctx, &state.Run{Ticket: "HIVE-1", Phase: state.PhaseDone})
	_ = r.Save(ctx, &state.Run{Ticket: "OPS-1", Phase: state.PhaseDone})
	if runs, _ := r.List(ctx); len(runs) != 2 {
		t.Errorf("list = %+v", runs)
	}
	if n, err := r.Prune(ctx, old.Add(time.Hour)); err != nil || n != 2 {
		t.Errorf("pruned %d, %v", n, err)
	}
}
