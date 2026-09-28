package router

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
	"github.com/thomasmeadows/hivedispatch/internal/tracker/fake"
)

type broken struct{ *fake.Tracker }

func (broken) Poll(context.Context, tracker.State) ([]tracker.Ticket, error) {
	return nil, errors.New("site down")
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRoutesByProjectAndUnionsPolls(t *testing.T) {
	a, b := fake.New(), fake.New()
	a.Add(tracker.Ticket{Key: "AA-1"})
	b.Add(tracker.Ticket{Key: "BB-2"})
	// A query shared across projects must not hand B's ticket to A's tracker.
	a.Add(tracker.Ticket{Key: "BB-2"})
	r := &Router{ByProject: map[string]tracker.Tracker{"AA": a, "BB": b}, Log: quiet()}
	got, err := r.Poll(context.Background(), tracker.StateReady)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Key != "AA-1" || got[1].Key != "BB-2" {
		t.Errorf("Poll = %+v", got)
	}
	ctx := context.Background()
	if won, err := r.Claim(ctx, "BB-2", "w", time.Now()); err != nil || !won {
		t.Fatalf("Claim = %v, %v", won, err)
	}
	if err := r.Comment(ctx, "BB-2", "hi"); err != nil {
		t.Fatal(err)
	}
	if len(b.Comments("BB-2")) != 1 || len(a.Comments("BB-2")) != 0 {
		t.Error("comment went to the wrong tracker")
	}
	if _, err := r.Get(ctx, "ZZ-1"); err == nil {
		t.Error("unknown project should error")
	}
}

func TestPollSurvivesOneBrokenTracker(t *testing.T) {
	ok := fake.New()
	ok.Add(tracker.Ticket{Key: "OK-1"})
	r := &Router{ByProject: map[string]tracker.Tracker{"OK": ok, "BAD": broken{fake.New()}}, Log: quiet()}
	got, err := r.Poll(context.Background(), tracker.StateReady)
	if err != nil || len(got) != 1 {
		t.Fatalf("Poll = %+v, %v; one broken tracker must not stall the rest", got, err)
	}
	r = &Router{ByProject: map[string]tracker.Tracker{"BAD": broken{fake.New()}}, Log: quiet()}
	if _, err := r.Poll(context.Background(), tracker.StateReady); err == nil {
		t.Error("every tracker failing is an error")
	}
}
