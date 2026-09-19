package dispatch

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/githost"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

var now = time.Date(2026, 9, 19, 14, 2, 0, 0, time.UTC)

func mustContain(t *testing.T, got string, wants ...string) {
	t.Helper()
	if !strings.HasPrefix(got, Marker) {
		t.Errorf("missing marker prefix: %q", got)
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
}

func TestIsOurs(t *testing.T) {
	if !isOurs(tracker.Comment{Body: Marker + " hi"}) || isOurs(tracker.Comment{Body: "hi"}) {
		t.Error("isOurs wrong")
	}
}

func TestReports(t *testing.T) {
	mustContain(t, reportTriageNeedsInfo("Which DB?"), "Which DB?", "Ready")
	mustContain(t, reportRejected("too big"), "too big")
	mustContain(t, reportNeedsInput("Which DB?"), "Which DB?", "Ready")
	mustContain(t, reportPRFailed(errors.New("403"), "hive/HIVE-1"), "403", "hive/HIVE-1")

	pr := &githost.PR{URL: "https://x/pull/1"}
	mustContain(t, reportCompleted(executor.Result{Summary: "did it"}, pr, "hive/HIVE-1", true), "https://x/pull/1", "did it")
	got := reportCompleted(executor.Result{Summary: "nothing to do"}, nil, "hive/HIVE-1", false)
	mustContain(t, got, "no code changes", "nothing to do")

	res := executor.Result{Status: executor.StatusFailed, StopCause: executor.CauseTimeout, Summary: "got halfway"}
	got = reportFailed(res, 1, 3, "hive/HIVE-1", true, false, now)
	mustContain(t, got, "14:02 UTC", executor.CauseTimeout.Describe(), "1 of 3", "hive/HIVE-1", "got halfway", "retry")
	got = reportFailed(res, 3, 3, "hive/HIVE-1", false, true, now)
	mustContain(t, got, "3 of 3", "No changes were made", "human")
	if strings.Contains(got, "retry") {
		t.Error("exhausted report must not promise a retry")
	}
}
