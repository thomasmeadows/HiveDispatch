package prefixed

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
	"github.com/thomasmeadows/hivedispatch/internal/tracker/fake"
)

func TestKeysGainAndLoseThePrefix(t *testing.T) {
	inner := fake.New()
	inner.Add(tracker.Ticket{Key: "SCRUM-4", Summary: "Create website"})
	p := New(inner, "jira")
	ctx := context.Background()

	got, err := p.Poll(ctx, tracker.StateReady)
	if err != nil || len(got) != 1 || got[0].Key != "JIRA-SCRUM-4" {
		t.Fatalf("Poll = %+v, %v", got, err)
	}
	tk, err := p.Get(ctx, "jira-scrum-4")
	if err != nil || tk.Key != "JIRA-SCRUM-4" || tk.Summary != "Create website" {
		t.Fatalf("Get = %+v, %v", tk, err)
	}
	won, err := p.Claim(ctx, "JIRA-SCRUM-4", "m1", time.Now())
	if err != nil || !won {
		t.Fatalf("Claim = %v, %v", won, err)
	}
	if err := p.Heartbeat(ctx, "JIRA-SCRUM-4", "m1"); err != nil {
		t.Fatal(err)
	}
	if err := p.Comment(ctx, "JIRA-SCRUM-4", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(ctx, "JIRA-SCRUM-4", tracker.StateInProgress); err != nil {
		t.Fatal(err)
	}
	if err := p.Release(ctx, "JIRA-SCRUM-4", "m1"); err != nil {
		t.Fatal(err)
	}
	if raw, _ := inner.Get(ctx, "SCRUM-4"); raw.Claim != nil {
		t.Errorf("inner still claimed: %+v", raw.Claim)
	}
	if _, err := p.Get(ctx, "GITHUB-R-1"); !errors.Is(err, tracker.ErrNotFound) {
		t.Errorf("a key with another prefix = %v, want ErrNotFound", err)
	}
}
