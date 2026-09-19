package claudecode

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
)

func TestParseNeedsInput(t *testing.T) {
	q, ok := parseNeedsInput("I looked.\n\nHIVE_NEEDS_INPUT: Which DB?\nMore context here.")
	if !ok || q != "Which DB?\nMore context here." {
		t.Errorf("ok=%v q=%q", ok, q)
	}
	if _, ok := parseNeedsInput("all done"); ok {
		t.Error("no marker should not match")
	}
	if _, ok := parseNeedsInput("HIVE_NEEDS_INPUT:"); ok {
		t.Error("empty question should not match")
	}
}

func TestMapOutcomePrecedence(t *testing.T) {
	ok := transcript{SessionID: "s", Result: &resultMsg{Result: "done"}}
	cases := []struct {
		name   string
		tr     transcript
		exit   exitInfo
		status executor.Status
		cause  executor.Cause
	}{
		{"timeout", ok, exitInfo{CtxErr: context.DeadlineExceeded}, executor.StatusFailed, executor.CauseTimeout},
		{"step", ok, exitInfo{StepTripped: true, CtxErr: context.Canceled}, executor.StatusFailed, executor.CauseStepBudget},
		{"killed", ok, exitInfo{CtxErr: context.Canceled}, executor.StatusFailed, executor.CauseKilled},
		{"noresult", transcript{SessionID: "s"}, exitInfo{ExitErr: errors.New("exit 1"), Stderr: "boom"}, executor.StatusFailed, executor.CauseError},
		{"budget429", transcript{Result: &resultMsg{IsError: true, Result: "x", APIErrorStatus: intp(429)}}, exitInfo{}, executor.StatusFailed, executor.CauseBudget},
		{"budgettext", transcript{Result: &resultMsg{IsError: true, Result: "You've hit your usage limit"}}, exitInfo{}, executor.StatusFailed, executor.CauseBudget},
		{"error", transcript{Result: &resultMsg{IsError: true, Result: "Not logged in"}}, exitInfo{}, executor.StatusFailed, executor.CauseError},
		{"needsinput", transcript{Result: &resultMsg{Result: "hm\nHIVE_NEEDS_INPUT: which?"}}, exitInfo{}, executor.StatusNeedsInput, executor.CauseNone},
		{"completed", ok, exitInfo{}, executor.StatusCompleted, executor.CauseNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := mapOutcome(c.tr, c.exit)
			if res.Status != c.status || res.StopCause != c.cause {
				t.Errorf("got %s/%s want %s/%s", res.Status, res.StopCause, c.status, c.cause)
			}
			if res.Summary == "" {
				t.Error("summary must never be empty")
			}
		})
	}
}

func TestMapOutcomeDetails(t *testing.T) {
	reset := time.Date(2026, 9, 20, 4, 30, 0, 0, time.UTC)
	tr := transcript{
		SessionID: "sess", EditedFiles: []string{"a.go"},
		RateLimit: &rateLimit{Status: "rejected", ResetsAt: reset},
		Result:    &resultMsg{IsError: true, Result: "limit"},
	}
	res := mapOutcome(tr, exitInfo{})
	if res.StopCause != executor.CauseBudget || res.ResumeToken != "sess" || len(res.ChangedFiles) != 1 {
		t.Errorf("res = %+v", res)
	}
	if !strings.Contains(res.Summary, "04:30 UTC") {
		t.Errorf("summary should include reset time: %q", res.Summary)
	}
	long := transcript{Result: &resultMsg{Result: strings.Repeat("x", maxSummary+100)}}
	if got := mapOutcome(long, exitInfo{}).Summary; len(got) > maxSummary+3 {
		t.Errorf("summary not truncated: %d", len(got))
	}
	q := mapOutcome(transcript{Result: &resultMsg{Result: "HIVE_NEEDS_INPUT: A or B?"}}, exitInfo{})
	if q.Question != "A or B?" {
		t.Errorf("question = %q", q.Question)
	}
}

func intp(i int) *int { return &i }
