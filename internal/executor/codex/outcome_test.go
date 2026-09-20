package codex

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/codexcli"
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
	ok := codexcli.Transcript{ThreadID: "s", LastMessage: "done", TurnCompleted: true}
	cases := []struct {
		name   string
		tr     codexcli.Transcript
		exit   codexcli.Exit
		status executor.Status
		cause  executor.Cause
	}{
		{"timeout", ok, codexcli.Exit{CtxErr: context.DeadlineExceeded}, executor.StatusFailed, executor.CauseTimeout},
		{"step", ok, codexcli.Exit{StepTripped: true, CtxErr: context.Canceled}, executor.StatusFailed, executor.CauseStepBudget},
		{"killed", ok, codexcli.Exit{CtxErr: context.Canceled}, executor.StatusFailed, executor.CauseKilled},
		{"noturn", codexcli.Transcript{ThreadID: "s"}, codexcli.Exit{ExitErr: errors.New("exit 1"), Stderr: "boom"}, executor.StatusFailed, executor.CauseError},
		{"budgettext", codexcli.Transcript{Error: "You've hit your usage limit"}, codexcli.Exit{ExitErr: errors.New("exit 1")}, executor.StatusFailed, executor.CauseBudget},
		{"error", codexcli.Transcript{Error: "Not logged in"}, codexcli.Exit{}, executor.StatusFailed, executor.CauseError},
		{"exitnonzero", ok, codexcli.Exit{ExitErr: errors.New("exit 2")}, executor.StatusFailed, executor.CauseError},
		{"needsinput", codexcli.Transcript{LastMessage: "hm\nHIVE_NEEDS_INPUT: which?", TurnCompleted: true}, codexcli.Exit{}, executor.StatusNeedsInput, executor.CauseNone},
		{"completed", ok, codexcli.Exit{}, executor.StatusCompleted, executor.CauseNone},
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
	tr := codexcli.Transcript{ThreadID: "thr", EditedFiles: []string{"a.go"}, Error: "rate limit exceeded"}
	res := mapOutcome(tr, codexcli.Exit{})
	if res.StopCause != executor.CauseBudget || res.ResumeToken != "thr" || len(res.ChangedFiles) != 1 || !res.RetryAfter.IsZero() {
		t.Errorf("res = %+v", res)
	}
	if !strings.Contains(res.Summary, "rate limit exceeded") {
		t.Errorf("summary should carry the error: %q", res.Summary)
	}
	crash := mapOutcome(codexcli.Transcript{}, codexcli.Exit{ExitErr: errors.New("exit status 139"), Stderr: "segfault"})
	if !strings.Contains(crash.Summary, "segfault") || !strings.Contains(crash.Summary, "exit status 139") {
		t.Errorf("crash summary = %q", crash.Summary)
	}
	long := codexcli.Transcript{LastMessage: strings.Repeat("x", maxSummary+100), TurnCompleted: true}
	if got := mapOutcome(long, codexcli.Exit{}).Summary; len(got) > maxSummary+3 {
		t.Errorf("summary not truncated: %d", len(got))
	}
	q := mapOutcome(codexcli.Transcript{LastMessage: "HIVE_NEEDS_INPUT: A or B?", TurnCompleted: true}, codexcli.Exit{})
	if q.Question != "A or B?" {
		t.Errorf("question = %q", q.Question)
	}
	empty := mapOutcome(codexcli.Transcript{TurnCompleted: true}, codexcli.Exit{})
	if empty.Status != executor.StatusCompleted || empty.Summary != "Run completed." {
		t.Errorf("empty = %+v", empty)
	}
}
