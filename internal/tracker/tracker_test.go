package tracker

import (
	"strings"
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

func TestName(t *testing.T) {
	for _, tc := range []struct{ key, summary, want string }{
		{"JIRA-SCRUM-4", "Create website", "jira-scrum-4-create-website"},
		{"GITHUB-HIVEDISPATCH-12", "Add `--version` flag (CLI)!", "github-hivedispatch-12-add-version-flag-cli"},
		{"GITHUB-R-1", "", "github-r-1"},
		{"GITHUB-R-1", "   ¿¡ ", "github-r-1"},
		{"GITHUB-R-2", "Make the worker poll every repository in parallel and report back quickly", "github-r-2-make-the-worker-poll-every-repository-in"},
	} {
		if got := Name(tc.key, tc.summary); got != tc.want {
			t.Errorf("Name(%q, %q) = %q, want %q", tc.key, tc.summary, got, tc.want)
		}
	}
}

func TestKeyCandidates(t *testing.T) {
	for in, want := range map[string][]string{
		"jira-scrum-4-create-website": {"JIRA-SCRUM-4"},
		"GITHUB-APP-2-12":             {"GITHUB-APP-2", "GITHUB-APP-2-12"},
		"github-app-2-12-fix-bug-3":   {"GITHUB-APP-2", "GITHUB-APP-2-12", "GITHUB-APP-2-12-FIX-BUG-3"},
		"HIVE-1":                      {"HIVE-1"},
		"nonsense":                    {"NONSENSE"},
	} {
		got := KeyCandidates(in)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("KeyCandidates(%q) = %v, want %v", in, got, want)
		}
	}
}
