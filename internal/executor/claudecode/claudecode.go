// Package claudecode adapts the Claude Code CLI to executor.Executor.
//
// The orchestrator never interprets the session id it stores as the resume
// token; it only hands it back with --resume.
package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/thomasmeadows/hivedispatch/internal/claudecli"
	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/repoconfig"
)

// Executor runs Claude Code headless.
type Executor struct {
	cfg Config
}

var _ executor.Executor = (*Executor)(nil)

// New returns an Executor; an empty Binary means "claude" on PATH.
func New(cfg Config) *Executor {
	if cfg.Binary == "" {
		cfg.Binary = "claude"
	}
	return &Executor{cfg: cfg}
}

// Name implements executor.Executor.
func (e *Executor) Name() string { return "claude-code" }

// Run implements executor.Executor.
func (e *Executor) Run(ctx context.Context, t executor.Task) (executor.Result, error) {
	rc, err := repoconfig.Load(t.Workspace)
	if err != nil {
		return executor.Result{}, err
	}
	promptText := t.Prompt
	if g := strings.TrimSpace(rc.Guidance); g != "" {
		promptText += "\n\n## Repository guidance\n\n" + g + "\n"
	}
	tr, exit, log, err := claudecli.Run(ctx, claudecli.Cmd{
		Binary: e.cfg.Binary, Dir: t.Workspace,
		Args:  buildArgs(e.cfg, rc.Executor, t.ResumeToken, false),
		Stdin: promptText, StepBudget: t.StepBudget,
	})
	if err != nil {
		return executor.Result{}, err
	}
	res := mapOutcome(tr, exit)
	res.Log = log
	return res, nil
}

// Plan implements executor.Executor by running in plan mode with a JSON
// schema and reading the declared file list.
func (e *Executor) Plan(ctx context.Context, t executor.Task) (executor.Footprint, error) {
	rc, err := repoconfig.Load(t.Workspace)
	if err != nil {
		return executor.Footprint{}, err
	}
	planPrompt := t.Prompt + "\n\nDo not make changes. List every file you would create or modify to complete this ticket, as repository-relative paths, in the requested JSON shape.\n"
	cmd := claudecli.Command(ctx, claudecli.Cmd{
		Binary: e.cfg.Binary, Dir: t.Workspace,
		Args:  buildArgs(e.cfg, rc.Executor, "", true),
		Stdin: planPrompt,
	})
	out, err := cmd.Output()
	if err != nil {
		return executor.Footprint{}, fmt.Errorf("plan: %w", err)
	}
	var r claudecli.ResultMsg
	if err := json.Unmarshal(out, &r); err != nil {
		return executor.Footprint{}, fmt.Errorf("plan: parse result: %w", err)
	}
	if r.IsError {
		return executor.Footprint{}, fmt.Errorf("plan: %s", truncate(r.Result, 500))
	}
	var fp struct {
		Files []string `json:"files"`
	}
	raw := []byte(r.Result)
	if len(r.StructuredOutput) > 0 {
		raw = r.StructuredOutput
	}
	if err := json.Unmarshal(raw, &fp); err != nil {
		return executor.Footprint{}, fmt.Errorf("plan: parse footprint: %w", err)
	}
	return executor.Footprint{Files: fp.Files}, nil
}
