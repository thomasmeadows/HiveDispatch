package fake

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

var now = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

func seeded() *Tracker {
	f := New()
	f.Add(tracker.Ticket{Key: "HIVE-1", Summary: "one"})
	f.Add(tracker.Ticket{Key: "HIVE-2", Summary: "two", Status: "in_review"})
	return f
}

func TestPollReturnsOnlyReady(t *testing.T) {
	f := seeded()
	got, err := f.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Key != "HIVE-1" {
		t.Fatalf("got %+v", got)
	}
}

func TestGetUnknownIsNotFound(t *testing.T) {
	_, err := seeded().Get(context.Background(), "HIVE-99")
	if !errors.Is(err, tracker.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestClaimWon(t *testing.T) {
	f := seeded()
	won, err := f.Claim(context.Background(), "HIVE-1", "worker-a", now)
	if err != nil || !won {
		t.Fatalf("won=%v err=%v", won, err)
	}
	tk, _ := f.Get(context.Background(), "HIVE-1")
	if tk.Claim == nil || tk.Claim.AgentID != "worker-a" || !tk.Claim.At.Equal(now) {
		t.Fatalf("claim = %+v", tk.Claim)
	}
}

func TestClaimLostToRace(t *testing.T) {
	f := seeded()
	f.BeforeReadBack = func(key string) {
		// Simulate worker-b writing between our write and our read-back.
		f.overwriteClaim(key, "worker-b", now.Add(time.Second))
	}
	won, err := f.Claim(context.Background(), "HIVE-1", "worker-a", now)
	if err != nil {
		t.Fatal(err)
	}
	if won {
		t.Fatal("worker-a should have lost the race")
	}
}

func TestHeartbeatAndReleaseRequireHolder(t *testing.T) {
	f := seeded()
	f.Now = func() time.Time { return now.Add(time.Minute) }
	ctx := context.Background()
	if _, err := f.Claim(ctx, "HIVE-1", "worker-a", now); err != nil {
		t.Fatal(err)
	}
	if err := f.Heartbeat(ctx, "HIVE-1", "worker-b"); !errors.Is(err, tracker.ErrNotClaimHolder) {
		t.Errorf("heartbeat by non-holder: %v", err)
	}
	if err := f.Release(ctx, "HIVE-1", "worker-b"); !errors.Is(err, tracker.ErrNotClaimHolder) {
		t.Errorf("release by non-holder: %v", err)
	}
	if err := f.Heartbeat(ctx, "HIVE-1", "worker-a"); err != nil {
		t.Errorf("heartbeat by holder: %v", err)
	}
	tk, _ := f.Get(ctx, "HIVE-1")
	if !tk.Claim.At.After(now) {
		t.Errorf("heartbeat did not advance At: %v", tk.Claim.At)
	}
	if err := f.Release(ctx, "HIVE-1", "worker-a"); err != nil {
		t.Errorf("release by holder: %v", err)
	}
	tk, _ = f.Get(ctx, "HIVE-1")
	if tk.Claim != nil {
		t.Errorf("claim not cleared: %+v", tk.Claim)
	}
}

func TestCommentAndTransitionRecorded(t *testing.T) {
	f := seeded()
	ctx := context.Background()
	if err := f.Comment(ctx, "HIVE-1", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := f.Transition(ctx, "HIVE-1", tracker.StateInReview); err != nil {
		t.Fatal(err)
	}
	if got := f.Comments("HIVE-1"); len(got) != 1 || got[0] != "hello" {
		t.Errorf("comments = %v", got)
	}
	if got := f.Transitions("HIVE-1"); len(got) != 1 || got[0] != tracker.StateInReview {
		t.Errorf("transitions = %v", got)
	}
	tk, _ := f.Get(ctx, "HIVE-1")
	if tk.Status != "in_review" {
		t.Errorf("status = %q", tk.Status)
	}
	if len(tk.Comments) != 1 || tk.Comments[0].Body != "hello" {
		t.Errorf("ticket comments = %+v", tk.Comments)
	}
}

func TestGetReturnsCopy(t *testing.T) {
	f := seeded()
	tk, _ := f.Get(context.Background(), "HIVE-1")
	tk.Summary = "mutated"
	again, _ := f.Get(context.Background(), "HIVE-1")
	if again.Summary != "one" {
		t.Error("Get must return a copy")
	}
}
