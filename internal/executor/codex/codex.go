// Package codex adapts the Codex CLI (`codex exec`) to executor.Executor.
//
// The thread id Codex prints in its first event is stored as the resume
// token; the orchestrator never interprets it and hands it back as
// `codex exec resume <id>`.
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/thomasmeadows/hivedispatch/internal/codexcli"
	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/repoconfig"
)

// Executor runs Codex headless.
type Executor struct {
	cfg Config
}

var _ executor.Executor = (*Executor)(nil)

// New returns an Executor; an empty Binary means "codex" on PATH.
func New(cfg Config) *Executor {
	if cfg.Binary == "" {
		cfg.Binary = "codex"
	}
	return &Executor{cfg: cfg}
}

// Name implements executor.Executor.
func (e *Executor) Name() string { return "codex" }

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
	tr, exit, log, err := codexcli.Run(ctx, codexcli.Cmd{
		Binary: e.cfg.Binary, Dir: t.Workspace,
		Args:  buildArgs(e.cfg, rc.Executor, t.ResumeToken, false, ""),
		Stdin: promptText, StepBudget: t.StepBudget,
		Env: pathEnv(rc.Executor.ExtraPath()),
	})
	if err != nil {
		return executor.Result{}, err
	}
	res := mapOutcome(tr, exit)
	res.Log = log
	return res, nil
}

// Plan implements executor.Executor by running read-only with an output
// schema and reading the declared file list from the final message.
func (e *Executor) Plan(ctx context.Context, t executor.Task) (executor.Footprint, error) {
	rc, err := repoconfig.Load(t.Workspace)
	if err != nil {
		return executor.Footprint{}, err
	}
	schema, err := os.CreateTemp("", "hivedispatch-codex-schema-*.json")
	if err != nil {
		return executor.Footprint{}, fmt.Errorf("plan: %w", err)
	}
	defer func() { _ = os.Remove(schema.Name()) }()
	if _, err := schema.WriteString(planSchema); err != nil {
		_ = schema.Close()
		return executor.Footprint{}, fmt.Errorf("plan: %w", err)
	}
	if err := schema.Close(); err != nil {
		return executor.Footprint{}, fmt.Errorf("plan: %w", err)
	}
	planPrompt := t.Prompt + "\n\nDo not make changes. List every file you would create or modify to complete this ticket, as repository-relative paths, in the requested JSON shape.\n"
	tr, exit, _, err := codexcli.Run(ctx, codexcli.Cmd{
		Binary: e.cfg.Binary, Dir: t.Workspace,
		Args:  buildArgs(e.cfg, rc.Executor, "", true, schema.Name()),
		Stdin: planPrompt,
		Env:   pathEnv(rc.Executor.ExtraPath()),
	})
	if err != nil {
		return executor.Footprint{}, fmt.Errorf("plan: %w", err)
	}
	if tr.Error != "" {
		return executor.Footprint{}, fmt.Errorf("plan: %s", truncate(tr.Error, 500))
	}
	if exit.ExitErr != nil || !tr.TurnCompleted {
		msg := "plan: codex exited without completing a turn"
		if s := strings.TrimSpace(exit.Stderr); s != "" {
			msg += ": " + truncate(s, 500)
		}
		if exit.ExitErr != nil {
			return executor.Footprint{}, fmt.Errorf("%s: %w", msg, exit.ExitErr)
		}
		return executor.Footprint{}, errors.New(msg)
	}
	var fp struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal([]byte(tr.LastMessage), &fp); err != nil {
		return executor.Footprint{}, fmt.Errorf("plan: parse footprint: %w", err)
	}
	return executor.Footprint{Files: fp.Files}, nil
}

// pathEnv returns a PATH entry with dirs prepended, or nil when there is
// nothing to add.
func pathEnv(dirs []string) []string {
	if len(dirs) == 0 {
		return nil
	}
	return []string{"PATH=" + strings.Join(dirs, string(os.PathListSeparator)) + string(os.PathListSeparator) + os.Getenv("PATH")}
}
