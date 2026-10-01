// Package deepcode adapts the DeepCode CLI (`deepcode -x`) to
// executor.Executor. DeepCode reads its API key and model from
// ~/.deepcode/settings.json, which the operator owns; HiveDispatch never
// handles the key. The session id is stored as the resume token and handed
// back as `deepcode -x -r <id>`.
package deepcode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/thomasmeadows/hivedispatch/internal/command-line-interfaces/deepcodecli"
	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/prompt"
	"github.com/thomasmeadows/hivedispatch/internal/repoconfig"
)

const maxSummary = 4000

// errCodingOnly is returned for read-only runs: DeepCode cannot be held to
// read-only from its command line, so it only ever codes.
var errCodingOnly = errors.New("deepcode: runs coding agents only; planning and review run through claude or codex")

// Config is the worker-level DeepCode configuration.
type Config struct {
	Binary string // default "deepcode"
	Home   string // DeepCode's data directory; "" = ~/.deepcode
}

// Executor runs DeepCode headless.
type Executor struct {
	cfg Config
}

var _ executor.Executor = (*Executor)(nil)

// New returns an Executor; an empty Binary means "deepcode" on PATH.
func New(cfg Config) *Executor {
	if cfg.Binary == "" {
		cfg.Binary = "deepcode"
	}
	return &Executor{cfg: cfg}
}

// Name implements executor.Executor.
func (e *Executor) Name() string { return "deepcode" }

// Run implements executor.Executor. The agent's model is not passed:
// DeepCode has no model flag and uses the one in its settings.json.
func (e *Executor) Run(ctx context.Context, t executor.Task) (executor.Result, error) {
	rc, err := repoconfig.Load(t.Workspace)
	if err != nil {
		return executor.Result{}, err
	}
	promptText := t.Prompt
	if g := strings.TrimSpace(rc.Guidance); g != "" {
		promptText += "\n\n## Repository guidance\n\n" + g + "\n"
	}
	tr, exit, log, err := deepcodecli.Run(ctx, deepcodecli.Cmd{
		Binary: e.cfg.Binary, Dir: t.Workspace, Prompt: promptText, Resume: t.ResumeToken,
		StepBudget: t.StepBudget, Env: pathEnv(rc.Executor.ExtraPath()), Home: e.cfg.Home,
	})
	if err != nil {
		return executor.Result{}, err
	}
	res := mapOutcome(tr, exit)
	res.Log = log
	return res, nil
}

// Plan implements executor.Executor; DeepCode does not plan.
func (e *Executor) Plan(context.Context, executor.Task) (executor.Footprint, error) {
	return executor.Footprint{}, errCodingOnly
}

// Advise implements executor.Executor; DeepCode does not advise.
func (e *Executor) Advise(context.Context, executor.Advice) (json.RawMessage, error) {
	return nil, errCodingOnly
}

// truncate caps a summary at maxSummary.
func truncate(s string) string {
	if len(s) <= maxSummary {
		return s
	}
	return s[:maxSummary] + "…"
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

// mapOutcome turns the session and how the process ended into a Result.
func mapOutcome(tr deepcodecli.Transcript, exit deepcodecli.Exit) executor.Result {
	res := executor.Result{ResumeToken: tr.SessionID, ChangedFiles: tr.EditedFiles, Steps: tr.Steps, Usage: tr.Usage}
	text := strings.TrimSpace(exit.Stdout)
	if text == "" {
		text = strings.TrimSpace(tr.Reply)
	}
	stderr := strings.TrimSpace(exit.Stderr)
	fail := func(c executor.Cause, summary string) executor.Result {
		res.Status, res.StopCause = executor.StatusFailed, c
		if summary == "" {
			summary = c.Describe()
		}
		res.Summary = truncate(summary)
		return res
	}
	switch {
	case errors.Is(exit.CtxErr, context.DeadlineExceeded):
		return fail(executor.CauseTimeout, text)
	case exit.StepTripped:
		return fail(executor.CauseStepBudget, text)
	case errors.Is(exit.CtxErr, context.Canceled):
		return fail(executor.CauseKilled, text)
	case tr.Status == "waiting_for_user":
		res.Status = executor.StatusNeedsInput
		res.Question = strings.TrimSpace(tr.Reply)
		if res.Question == "" {
			res.Question = stderr
		}
		res.Summary = truncate(res.Question)
		return res
	case tr.Status == "ask_permission":
		return fail(executor.CauseError, stderr+"\n\nDeepCode wanted permission it cannot ask for when run by HiveDispatch: allow that tool in the permissions of ~/.deepcode/settings.json, or deny it.")
	case exit.ExitErr != nil || (tr.Status != "" && tr.Status != "completed"):
		reason := tr.FailReason
		if reason == "" {
			reason = stderr
		}
		if reason == "" && exit.ExitErr != nil {
			reason = "deepcode exited: " + exit.ExitErr.Error()
		}
		if deepcodecli.LooksLikeBudget(reason) {
			return fail(executor.CauseBudget, reason)
		}
		return fail(executor.CauseError, reason)
	}
	if q, ok := parseNeedsInput(text); ok {
		res.Status = executor.StatusNeedsInput
		res.Question = q
		res.Summary = truncate(text)
		return res
	}
	res.Status = executor.StatusCompleted
	if text == "" {
		text = "Run completed."
	}
	res.Summary = truncate(text)
	return res
}

// pathEnv returns a PATH entry with dirs prepended, or nil when there is
// nothing to add.
func pathEnv(dirs []string) []string {
	if len(dirs) == 0 {
		return nil
	}
	return []string{"PATH=" + strings.Join(dirs, string(os.PathListSeparator)) + string(os.PathListSeparator) + os.Getenv("PATH")}
}
