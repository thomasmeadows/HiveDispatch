// Package grok adapts Grok Build's headless CLI to a coding executor.
// Its streaming-messages-json format uses the Messages wire protocol, so
// the existing claudecli runner supplies parsing, tracing and supervision.
package grok

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/thomasmeadows/hivedispatch/internal/claudecli"
	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/prompt"
	"github.com/thomasmeadows/hivedispatch/internal/repoconfig"
)

// Config selects the worker's Grok Build executable.
type Config struct{ Binary string }

// Executor runs Grok Build in the prepared worktree.
type Executor struct{ cfg Config }

var _ executor.Executor = (*Executor)(nil)
var errCodingOnly = errors.New("grok: coding agents only; use claude or codex for planning and review")

// New returns an executor, defaulting to grok on PATH.
func New(cfg Config) *Executor {
	if cfg.Binary == "" {
		cfg.Binary = "grok"
	}
	return &Executor{cfg: cfg}
}

// Name implements executor.Executor.
func (e *Executor) Name() string { return "grok" }

// Plan refuses a read-only run until Grok's isolation is supported here.
func (e *Executor) Plan(context.Context, executor.Task) (executor.Footprint, error) {
	return executor.Footprint{}, errCodingOnly
}

// Advise refuses planning and review for the same reason as Plan.
func (e *Executor) Advise(context.Context, executor.Advice) (json.RawMessage, error) {
	return nil, errCodingOnly
}

// Run executes one prompt, preserving the CLI's authentication and permission
// settings. A private temporary file avoids exposing ticket text in argv.
func (e *Executor) Run(ctx context.Context, t executor.Task) (executor.Result, error) {
	rc, err := repoconfig.Load(t.Workspace)
	if err != nil {
		return executor.Result{}, err
	}
	text := t.Prompt
	if g := strings.TrimSpace(rc.Guidance); g != "" {
		text += "\n\n## Repository guidance\n\n" + g + "\n"
	}
	file, err := os.CreateTemp("", "hivedispatch-grok-prompt-*")
	if err != nil {
		return executor.Result{}, err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err = file.WriteString(text); err != nil {
		_ = file.Close()
		return executor.Result{}, err
	}
	if err = file.Close(); err != nil {
		return executor.Result{}, err
	}
	args := []string{"--prompt-file", file.Name(), "--output-format", "streaming-messages-json", "--no-auto-update"}
	if t.Model != "" {
		args = append(args, "--model", t.Model)
	}
	if t.ResumeToken != "" {
		args = append(args, "--resume", t.ResumeToken)
	}
	if t.StepBudget > 0 {
		args = append(args, "--max-turns", strconv.Itoa(t.StepBudget))
	}
	var env []string
	if dirs := rc.Executor.ExtraPath(); len(dirs) > 0 {
		env = []string{"PATH=" + strings.Join(dirs, string(os.PathListSeparator)) + string(os.PathListSeparator) + os.Getenv("PATH")}
	}
	tr, exit, log, err := claudecli.Run(ctx, claudecli.Cmd{Binary: e.cfg.Binary, Dir: t.Workspace, Args: args, Env: env, StepBudget: t.StepBudget})
	if err != nil {
		return executor.Result{}, fmt.Errorf("grok: %w (install Grok Build or set grok.binary)", err)
	}
	res := mapOutcome(tr, exit)
	res.Log = log
	if res.Usage.Model == "" {
		res.Usage.Model = t.Model
	}
	return res, nil
}

func mapOutcome(tr claudecli.Transcript, exit claudecli.Exit) executor.Result {
	res := executor.Result{ResumeToken: tr.SessionID, ChangedFiles: tr.EditedFiles, Steps: tr.Steps, Usage: executor.Usage{Model: tr.Model}}
	text := ""
	if r := tr.Result; r != nil {
		text = strings.TrimSpace(r.Result)
		if len(r.Errors) > 0 {
			text = strings.TrimSpace(text + "\n" + strings.Join(r.Errors, "\n"))
		}
		res.Usage.CostUSD = r.TotalCostUSD
		if u := r.Usage; u != nil {
			res.Usage.InputTokens = u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
			res.Usage.OutputTokens = u.OutputTokens
		}
	}
	fail := func(c executor.Cause, s string) executor.Result {
		res.Status, res.StopCause = executor.StatusFailed, c
		if s == "" {
			s = c.Describe()
		}
		res.Summary = capSummary(s)
		return res
	}
	switch {
	case errors.Is(exit.CtxErr, context.DeadlineExceeded):
		return fail(executor.CauseTimeout, text)
	case exit.StepTripped:
		return fail(executor.CauseStepBudget, text)
	case errors.Is(exit.CtxErr, context.Canceled):
		return fail(executor.CauseKilled, text)
	}
	if tr.Result != nil && (tr.Result.Subtype == "error_max_turns" || tr.Result.StopReason == "max_turn_requests") {
		return fail(executor.CauseStepBudget, text)
	}
	if tr.Result == nil || tr.Result.IsError || strings.HasPrefix(tr.Result.Subtype, "error") || exit.ExitErr != nil {
		if tr.Result == nil {
			text = "grok exited without a result"
		}
		if exit.ExitErr != nil {
			text += "\n" + exit.ExitErr.Error()
		}
		if exit.Stderr != "" {
			text += "\n" + exit.Stderr
		}
		low := strings.ToLower(text)
		for _, s := range []string{"rate limit", "usage limit", "insufficient_quota", "too many requests", "429"} {
			if strings.Contains(low, s) {
				return fail(executor.CauseBudget, text)
			}
		}
		return fail(executor.CauseError, text)
	}
	if reason := tr.Result.StopReason; reason != "end_turn" && reason != "stop_sequence" {
		return fail(executor.CauseError, "grok stopped without completing: "+reason+"\n"+text)
	}
	if i := strings.LastIndex(text, prompt.NeedsInputMarker); i >= 0 && (i == 0 || text[i-1] == '\n') {
		if q := strings.TrimSpace(text[i+len(prompt.NeedsInputMarker):]); q != "" {
			res.Status = executor.StatusNeedsInput
			res.Question = q
			res.Summary = capSummary(text)
			return res
		}
	}
	res.Status = executor.StatusCompleted
	if text == "" {
		text = "Run completed."
	}
	res.Summary = capSummary(text)
	return res
}

func capSummary(s string) string {
	if len(s) > 4000 {
		return s[:4000] + "…"
	}
	return s
}
