// Package openclaw adapts OpenClaw agent exec to a coding executor.
package openclaw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/command-line-interfaces/openclawcli"
	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/prompt"
	"github.com/thomasmeadows/hivedispatch/internal/repoconfig"
)

// Config selects the executable; credentials remain with OpenClaw.
type Config struct{ Binary string }

// Executor runs a fresh embedded OpenClaw turn in the ticket's worktree.
type Executor struct{ cfg Config }

var _ executor.Executor = (*Executor)(nil)
var errCodingOnly = errors.New("openclaw: coding agents only; use claude or codex for planning and review")

// New defaults to openclaw on PATH.
func New(cfg Config) *Executor {
	if cfg.Binary == "" {
		cfg.Binary = "openclaw"
	}
	return &Executor{cfg: cfg}
}

// Name implements executor.Executor.
func (e *Executor) Name() string { return "openclaw" }

// Plan rejects runs whose read-only access cannot be enforced.
func (e *Executor) Plan(context.Context, executor.Task) (executor.Footprint, error) {
	return executor.Footprint{}, errCodingOnly
}

// Advise rejects planning and review for the same reason as Plan.
func (e *Executor) Advise(context.Context, executor.Advice) (json.RawMessage, error) {
	return nil, errCodingOnly
}

// Run starts fresh state for every turn. agent exec exposes neither resume
// nor live tool events / a step-limit flag, so StepBudget cannot be enforced.
// The caller's deadline bounds the entire process group. Without one (agent-run),
// use the worker's normal 45-minute default instead of running indefinitely.
func (e *Executor) Run(ctx context.Context, t executor.Task) (executor.Result, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 45*time.Minute)
		defer cancel()
	}
	workspace, err := filepath.Abs(t.Workspace)
	if err != nil {
		return executor.Result{}, err
	}
	rc, err := repoconfig.Load(workspace)
	if err != nil {
		return executor.Result{}, err
	}
	text := t.Prompt
	if g := strings.TrimSpace(rc.Guidance); g != "" {
		text += "\n\n## Repository guidance\n\n" + g + "\n"
	}
	args := []string{"agent", "exec", "--message-file", "-", "--cwd", workspace, "--json", "--timeout", "0"}
	if t.Model != "" {
		args = append(args, "--model", t.Model)
	}
	var env []string
	if dirs := rc.Executor.ExtraPath(); len(dirs) > 0 {
		env = []string{"PATH=" + strings.Join(dirs, string(os.PathListSeparator)) + string(os.PathListSeparator) + os.Getenv("PATH")}
	}
	tr, exit, log, err := openclawcli.Run(ctx, openclawcli.Cmd{Binary: e.cfg.Binary, Dir: workspace, Args: args, Stdin: text, Env: env})
	if err != nil {
		return executor.Result{}, fmt.Errorf("openclaw: %w (install OpenClaw with agent exec support or set openclaw.binary)", err)
	}
	res := mapOutcome(tr, exit)
	res.Log = log
	if t.StepBudget > 0 {
		res.Log += "\nOpenClaw agent exec does not expose live tool events or a step limit; only the wall-clock timeout is enforced.\n"
	}
	if res.Usage.Model == "" {
		res.Usage.Model = t.Model
	}
	return res, nil
}

func mapOutcome(tr openclawcli.Result, exit openclawcli.Exit) executor.Result {
	res := executor.Result{Usage: executor.Usage{Model: tr.Model, InputTokens: tr.Usage.Input, OutputTokens: tr.Usage.Output, CostUSD: tr.CostUSD}}
	text := strings.TrimSpace(tr.Final)
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
	case errors.Is(exit.CtxErr, context.Canceled):
		return fail(executor.CauseKilled, text)
	case exit.ParseErr != nil:
		return fail(executor.CauseError, "invalid OpenClaw output: "+exit.ParseErr.Error()+"\n"+exit.Stderr)
	}
	if tr.Status == "timeout" {
		return fail(executor.CauseTimeout, text)
	}
	if !tr.OK || tr.Status != "ok" || tr.Error != nil || exit.ExitErr != nil {
		if tr.Error != nil {
			text = strings.TrimSpace(text + "\n" + tr.Error.Message)
		}
		if exit.ExitErr != nil {
			text = strings.TrimSpace(text + "\n" + exit.ExitErr.Error())
		}
		text = strings.TrimSpace(text + "\n" + exit.Stderr)
		low := strings.ToLower(text)
		for _, s := range []string{"rate limit", "quota", "resource_exhausted", "too many requests", "429"} {
			if strings.Contains(low, s) {
				return fail(executor.CauseBudget, text)
			}
		}
		if text == "" {
			text = "OpenClaw returned an unsuccessful or missing result status"
		}
		return fail(executor.CauseError, text)
	}
	if i := strings.LastIndex(text, prompt.NeedsInputMarker); i >= 0 && (i == 0 || text[i-1] == '\n') {
		if q := strings.TrimSpace(text[i+len(prompt.NeedsInputMarker):]); q != "" {
			res.Status, res.Question, res.Summary = executor.StatusNeedsInput, q, capSummary(text)
			return res
		}
	}
	if text == "" {
		text = "Run completed."
	}
	res.Status, res.Summary = executor.StatusCompleted, capSummary(text)
	return res
}

func capSummary(s string) string {
	if len(s) > 4000 {
		return s[:4000] + "…"
	}
	return s
}
