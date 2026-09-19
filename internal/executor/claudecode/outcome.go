package claudecode

import (
	"context"
	"errors"
	"strings"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/prompt"
)

const maxSummary = 4000

// exitInfo describes how the subprocess ended.
type exitInfo struct {
	CtxErr      error
	StepTripped bool
	ExitErr     error
	Stderr      string
}

// parseNeedsInput finds the last HIVE_NEEDS_INPUT: line and returns the
// question, which runs to the end of the text.
func parseNeedsInput(text string) (string, bool) {
	idx := strings.LastIndex(text, prompt.NeedsInputMarker)
	if idx < 0 {
		return "", false
	}
	if idx > 0 && text[idx-1] != '\n' {
		return "", false
	}
	q := strings.TrimSpace(text[idx+len(prompt.NeedsInputMarker):])
	if q == "" {
		return "", false
	}
	return q, true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func looksLikeBudget(tr transcript) bool {
	r := tr.Result
	if r != nil && r.APIErrorStatus != nil && *r.APIErrorStatus == 429 {
		return true
	}
	if tr.RateLimit != nil && tr.RateLimit.Status != "" && tr.RateLimit.Status != "allowed" {
		return true
	}
	if r != nil {
		low := strings.ToLower(r.Result)
		return strings.Contains(low, "usage limit") || strings.Contains(low, "rate limit")
	}
	return false
}

// mapOutcome turns what was parsed plus how the process ended into a Result.
func mapOutcome(tr transcript, exit exitInfo) executor.Result {
	res := executor.Result{ResumeToken: tr.SessionID, ChangedFiles: tr.EditedFiles}
	text := ""
	if tr.Result != nil {
		text = strings.TrimSpace(tr.Result.Result)
	}
	fail := func(c executor.Cause, summary string) executor.Result {
		res.Status = executor.StatusFailed
		res.StopCause = c
		if summary == "" {
			summary = c.Describe()
		}
		res.Summary = truncate(summary, maxSummary)
		return res
	}
	switch {
	case errors.Is(exit.CtxErr, context.DeadlineExceeded):
		return fail(executor.CauseTimeout, text)
	case exit.StepTripped:
		return fail(executor.CauseStepBudget, text)
	case errors.Is(exit.CtxErr, context.Canceled):
		return fail(executor.CauseKilled, text)
	case tr.Result == nil:
		summary := "executor exited without a result"
		if exit.ExitErr != nil {
			summary += ": " + exit.ExitErr.Error()
		}
		if s := strings.TrimSpace(exit.Stderr); s != "" {
			summary += "\n" + truncate(s, 1000)
		}
		return fail(executor.CauseError, summary)
	case tr.Result.IsError:
		if looksLikeBudget(tr) {
			if tr.RateLimit != nil && !tr.RateLimit.ResetsAt.IsZero() {
				text += "\n\nQuota resets at " + tr.RateLimit.ResetsAt.UTC().Format("2006-01-02 15:04 UTC") + "."
			}
			return fail(executor.CauseBudget, text)
		}
		return fail(executor.CauseError, text)
	}
	if q, ok := parseNeedsInput(text); ok {
		res.Status = executor.StatusNeedsInput
		res.Question = q
		res.Summary = truncate(text, maxSummary)
		return res
	}
	res.Status = executor.StatusCompleted
	if text == "" {
		text = "Run completed."
	}
	res.Summary = truncate(text, maxSummary)
	return res
}
