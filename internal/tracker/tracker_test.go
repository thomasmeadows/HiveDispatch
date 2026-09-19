package tracker

import (
	"testing"
	"time"
)

func TestClaimFresh(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	var nilClaim *Claim
	if nilClaim.Fresh(now, time.Hour) {
		t.Error("nil claim must not be fresh")
	}
	c := &Claim{AgentID: "a", At: now.Add(-30 * time.Minute)}
	if !c.Fresh(now, time.Hour) {
		t.Error("30m-old claim with 1h timeout should be fresh")
	}
	if c.Fresh(now, 10*time.Minute) {
		t.Error("30m-old claim with 10m timeout should be stale")
	}
}

func TestStateValid(t *testing.T) {
	for _, s := range []State{StateReady, StateInProgress, StateNeedsInfo, StateInReview, StateNeedsHuman} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	if State("bogus").Valid() {
		t.Error("bogus should be invalid")
	}
}
