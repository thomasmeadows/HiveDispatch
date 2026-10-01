// Package antigravity adapts Google's agy CLI to a coding executor.
package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/antigravitycli"
	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/prompt"
	"github.com/thomasmeadows/hivedispatch/internal/repoconfig"
)

// Config selects the Antigravity CLI executable.
type Config struct{ Binary string }

// Executor runs Antigravity in the ticket's worktree.
type Executor struct{ cfg Config }

var _ executor.Executor = (*Executor)(nil)
var errCodingOnly = errors.New("antigravity: coding agents only; use claude or codex for planning and review")

// New defaults to agy on PATH.
func New(cfg Config) *Executor {
	if cfg.Binary == "" {
		cfg.Binary = "agy"
	}
	return &Executor{cfg: cfg}
}

// Name implements executor.Executor.
func (e *Executor) Name() string { return "antigravity" }

// Plan rejects read-only runs, which this adapter cannot enforce.
func (e *Executor) Plan(context.Context, executor.Task) (executor.Footprint, error) {
	return executor.Footprint{}, errCodingOnly
}

// Advise rejects planning and review for the same reason as Plan.
func (e *Executor) Advise(context.Context, executor.Advice) (json.RawMessage, error) {
	return nil, errCodingOnly
}

// Run sends one JSON user message on stdin and closes it; agy finishes the
// turn before exiting. The operator's authentication and permissions remain intact.
func (e *Executor) Run(ctx context.Context, t executor.Task) (executor.Result, error) {
	rc, err := repoconfig.Load(t.Workspace)
	if err != nil {
		return executor.Result{}, err
	}
	text := t.Prompt
	if g := strings.TrimSpace(rc.Guidance); g != "" {
		text += "\n\n## Repository guidance\n\n" + g + "\n"
	}
	input, err := json.Marshal(map[string]any{"event": "user", "message": map[string]string{"content": text}})
	if err != nil {
		return executor.Result{}, err
	}
	// Avoid the CLI's five-minute default truncating ordinary coding runs.
	timeout := 45 * time.Minute
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
		if timeout <= 0 {
			timeout = time.Millisecond
		}
	}
	args := []string{"--input-format", "stream-json", "--output-format", "stream-json", "--print-timeout", timeout.String()}
	if t.Model != "" {
		args = append(args, "--model", t.Model)
	}
	if t.ResumeToken != "" {
		args = append(args, "--conversation", t.ResumeToken)
	}
	var env []string
	if dirs := rc.Executor.ExtraPath(); len(dirs) > 0 {
		env = []string{"PATH=" + strings.Join(dirs, string(os.PathListSeparator)) + string(os.PathListSeparator) + os.Getenv("PATH")}
	}
	tr, exit, log, err := antigravitycli.Run(ctx, antigravitycli.Cmd{Binary: e.cfg.Binary, Dir: t.Workspace, Args: args, Stdin: string(input) + "\n", StepBudget: t.StepBudget, Env: env})
	if err != nil {
		return executor.Result{}, fmt.Errorf("antigravity: %w (install Antigravity CLI or set antigravity.binary)", err)
	}
	res := mapOutcome(tr, exit, t.ResumeToken != "")
	if res.Usage.Model == "" {
		res.Usage.Model = t.Model
	}
	res.Log = log
	return res, nil
}

func mapOutcome(tr antigravitycli.Transcript, exit antigravitycli.Exit, resumed bool) executor.Result {
	res := executor.Result{ResumeToken: tr.SessionID, Steps: tr.Steps, Usage: tr.Usage}
	res.Usage.Model = tr.Model
	text := ""
	if r := tr.Result; r != nil {
		text = strings.TrimSpace(r.Response)
		if r.Error != "" {
			text = strings.TrimSpace(text + "\n" + r.Error)
		}
		// Never attribute an old conversation's cumulative totals to a resumed run.
		if !tr.HasStepUsage && !resumed {
			res.Usage.InputTokens = r.Usage.InputTokens + r.Usage.CacheReadTokens
			res.Usage.OutputTokens = r.Usage.OutputTokens
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
	case exit.ParseErr != nil:
		return fail(executor.CauseError, "invalid Antigravity output: "+exit.ParseErr.Error())
	}
	if tr.Result == nil {
		text = "antigravity exited without a result"
	} else {
		switch tr.Result.Status {
		case "CANCELED", "INTERRUPTED":
			return fail(executor.CauseKilled, text)
		case "WAITING":
			if text == "" {
				text = "Antigravity is waiting for input; inspect the run log and reply to this ticket."
			}
			res.Status = executor.StatusNeedsInput
			res.Question = text
			res.Summary = capSummary(text)
			return res
		case "SUCCESS":
			if exit.ExitErr == nil && tr.Result.Error == "" {
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
		default:
			if text == "" {
				text = "antigravity stopped with status " + tr.Result.Status
			}
		}
	}
	if exit.ExitErr != nil {
		text += "\n" + exit.ExitErr.Error()
	}
	if exit.Stderr != "" {
		text += "\n" + exit.Stderr
	}
	low := strings.ToLower(text)
	if strings.Contains(low, "deadline exceeded") || strings.Contains(low, "timed out") {
		return fail(executor.CauseTimeout, text)
	}
	for _, s := range []string{"rate limit", "quota", "resource_exhausted", "resource exhausted", "too many requests", "429"} {
		if strings.Contains(low, s) {
			return fail(executor.CauseBudget, text)
		}
	}
	return fail(executor.CauseError, text)
}

func capSummary(s string) string {
	if len(s) > 4000 {
		return s[:4000] + "…"
	}
	return s
}
