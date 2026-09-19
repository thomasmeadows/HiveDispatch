package claudecode

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/claudecli"
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
	ok := claudecli.Transcript{SessionID: "s", Result: &claudecli.ResultMsg{Result: "done"}}
	cases := []struct {
		name   string
		tr     claudecli.Transcript
		exit   claudecli.Exit
		status executor.Status
		cause  executor.Cause
	}{
		{"timeout", ok, claudecli.Exit{CtxErr: context.DeadlineExceeded}, executor.StatusFailed, executor.CauseTimeout},
		{"step", ok, claudecli.Exit{StepTripped: true, CtxErr: context.Canceled}, executor.StatusFailed, executor.CauseStepBudget},
		{"killed", ok, claudecli.Exit{CtxErr: context.Canceled}, executor.StatusFailed, executor.CauseKilled},
		{"noresult", claudecli.Transcript{SessionID: "s"}, claudecli.Exit{ExitErr: errors.New("exit 1"), Stderr: "boom"}, executor.StatusFailed, executor.CauseError},
		{"budget429", claudecli.Transcript{Result: &claudecli.ResultMsg{IsError: true, Result: "x", APIErrorStatus: intp(429)}}, claudecli.Exit{}, executor.StatusFailed, executor.CauseBudget},
		{"budgettext", claudecli.Transcript{Result: &claudecli.ResultMsg{IsError: true, Result: "You've hit your usage limit"}}, claudecli.Exit{}, executor.StatusFailed, executor.CauseBudget},
		{"error", claudecli.Transcript{Result: &claudecli.ResultMsg{IsError: true, Result: "Not logged in"}}, claudecli.Exit{}, executor.StatusFailed, executor.CauseError},
		{"needsinput", claudecli.Transcript{Result: &claudecli.ResultMsg{Result: "hm\nHIVE_NEEDS_INPUT: which?"}}, claudecli.Exit{}, executor.StatusNeedsInput, executor.CauseNone},
		{"completed", ok, claudecli.Exit{}, executor.StatusCompleted, executor.CauseNone},
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
	tr := claudecli.Transcript{
		SessionID: "sess", EditedFiles: []string{"a.go"},
		RateLimit: &claudecli.RateLimit{Status: "rejected", ResetsAt: reset},
		Result:    &claudecli.ResultMsg{IsError: true, Result: "limit"},
	}
	res := mapOutcome(tr, claudecli.Exit{})
	if res.StopCause != executor.CauseBudget || res.ResumeToken != "sess" || len(res.ChangedFiles) != 1 {
		t.Errorf("res = %+v", res)
	}
	if !strings.Contains(res.Summary, "04:30 UTC") {
		t.Errorf("summary should include reset time: %q", res.Summary)
	}
	long := claudecli.Transcript{Result: &claudecli.ResultMsg{Result: strings.Repeat("x", maxSummary+100)}}
	if got := mapOutcome(long, claudecli.Exit{}).Summary; len(got) > maxSummary+3 {
		t.Errorf("summary not truncated: %d", len(got))
	}
	q := mapOutcome(claudecli.Transcript{Result: &claudecli.ResultMsg{Result: "HIVE_NEEDS_INPUT: A or B?"}}, claudecli.Exit{})
	if q.Question != "A or B?" {
		t.Errorf("question = %q", q.Question)
	}
}

func intp(i int) *int { return &i }
