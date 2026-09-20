package codex

import (
	"context"
	"errors"
	"strings"

	"github.com/thomasmeadows/hivedispatch/internal/codexcli"
	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/prompt"
)

const maxSummary = 4000

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

// mapOutcome turns what was parsed plus how the process ended into a Result.
func mapOutcome(tr codexcli.Transcript, exit codexcli.Exit) executor.Result {
	res := executor.Result{ResumeToken: tr.ThreadID, ChangedFiles: tr.EditedFiles}
	text := strings.TrimSpace(tr.LastMessage)
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
	case tr.Error != "":
		summary := tr.Error
		if text != "" {
			summary = text + "\n\n" + tr.Error
		}
		if codexcli.LooksLikeBudget(tr) {
			return fail(executor.CauseBudget, summary)
		}
		return fail(executor.CauseError, summary)
	case !tr.TurnCompleted || exit.ExitErr != nil:
		summary := "executor exited without completing a turn"
		if exit.ExitErr != nil {
			summary += ": " + exit.ExitErr.Error()
		}
		if s := strings.TrimSpace(exit.Stderr); s != "" {
			summary += "\n" + truncate(s, 1000)
		}
		return fail(executor.CauseError, summary)
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
