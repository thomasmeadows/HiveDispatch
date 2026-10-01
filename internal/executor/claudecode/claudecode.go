// Package claudecode adapts the Claude Code CLI to executor.Executor.
//
// The orchestrator never interprets the session id it stores as the resume
// token; it only hands it back with --resume.
package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/thomasmeadows/hivedispatch/internal/command-line-interfaces/claudecli"
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
		Args:  buildArgs(rc.Executor, t.Model, t.ResumeToken, ""),
		Stdin: promptText, StepBudget: t.StepBudget,
		Env: pathEnv(rc.Executor.ExtraPath()),
	})
	if err != nil {
		return executor.Result{}, err
	}
	res := mapOutcome(tr, exit)
	res.Log = log
	if res.Usage.Model == "" {
		res.Usage.Model = t.Model
	}
	return res, nil
}

// Plan implements executor.Executor by running in plan mode with a JSON
// schema and reading the declared file list.
func (e *Executor) Plan(ctx context.Context, t executor.Task) (executor.Footprint, error) {
	raw, err := e.Advise(ctx, executor.Advice{
		TicketKey: t.TicketKey, Workspace: t.Workspace, Model: t.Model, Schema: planSchema,
		Prompt: t.Prompt + "\n\nDo not make changes. List every file you would create or modify to complete this ticket, as repository-relative paths, in the requested JSON shape.\n",
	})
	if err != nil {
		return executor.Footprint{}, fmt.Errorf("plan: %w", err)
	}
	var fp struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(raw, &fp); err != nil {
		return executor.Footprint{}, fmt.Errorf("plan: parse footprint: %w", err)
	}
	return executor.Footprint{Files: fp.Files}, nil
}

// Advise implements executor.Executor: Claude Code in plan mode (it may
// read the workspace but not change it) with a JSON schema for the answer.
func (e *Executor) Advise(ctx context.Context, a executor.Advice) (json.RawMessage, error) {
	rc, err := repoconfig.Load(a.Workspace)
	if err != nil {
		return nil, err
	}
	cmd := claudecli.Command(ctx, claudecli.Cmd{
		Binary: e.cfg.Binary, Dir: a.Workspace,
		Args:  buildArgs(rc.Executor, a.Model, "", a.Schema),
		Stdin: withGuidance(a.Prompt, rc.Guidance),
		Env:   pathEnv(rc.Executor.ExtraPath()),
	})
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var r claudecli.ResultMsg
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, fmt.Errorf("parse result: %w", err)
	}
	if r.IsError {
		return nil, errors.New(truncate(r.Result, 500))
	}
	if len(r.StructuredOutput) > 0 {
		return r.StructuredOutput, nil
	}
	return json.RawMessage(r.Result), nil
}

// pathEnv returns a PATH entry with dirs prepended, or nil when there is
// nothing to add.
func pathEnv(dirs []string) []string {
	if len(dirs) == 0 {
		return nil
	}
	return []string{"PATH=" + strings.Join(dirs, string(os.PathListSeparator)) + string(os.PathListSeparator) + os.Getenv("PATH")}
}

// withGuidance appends the repository policy's guidance to a prompt.
func withGuidance(prompt, guidance string) string {
	if g := strings.TrimSpace(guidance); g != "" {
		return prompt + "\n\n## Repository guidance\n\n" + g + "\n"
	}
	return prompt
}
