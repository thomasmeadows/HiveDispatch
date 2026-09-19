// Package claudecode implements triage.Triager with the Claude Code CLI in
// read-only mode. The model may read, grep and glob the repository; it
// cannot run commands or write, so a bad decision costs one poll cycle.
package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/claudecli"
	"github.com/thomasmeadows/hivedispatch/internal/triage"
)

// Config is the worker-level triage configuration.
type Config struct {
	Binary     string        // default "claude"
	Model      string        // optional
	StepBudget int           // default 40 tool calls
	Timeout    time.Duration // default 5m
}

// Triager runs Claude Code read-only to decide about a ticket.
type Triager struct {
	cfg Config
}

var _ triage.Triager = (*Triager)(nil)

// New returns a Triager with defaults applied.
func New(cfg Config) *Triager {
	if cfg.Binary == "" {
		cfg.Binary = "claude"
	}
	if cfg.StepBudget == 0 {
		cfg.StepBudget = 40
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Minute
	}
	return &Triager{cfg: cfg}
}

func (t *Triager) args() []string {
	a := []string{
		"-p", "--output-format", "stream-json", "--verbose",
		"--permission-mode", "plan", "--restricted",
		"--tools", "Read,Grep,Glob",
		"--no-session-persistence",
		"--json-schema", decisionSchema,
	}
	if t.cfg.Model != "" {
		a = append(a, "--model", t.cfg.Model)
	}
	return a
}

// Decide implements triage.Triager.
func (t *Triager) Decide(ctx context.Context, in triage.Input) (triage.Decision, error) {
	if in.RepoPath == "" {
		return triage.Decision{}, errors.New("triage: RepoPath is required (a checkout to inspect)")
	}
	ctx, cancel := context.WithTimeout(ctx, t.cfg.Timeout)
	defer cancel()
	tr, exit, _, err := claudecli.Run(ctx, claudecli.Cmd{
		Binary: t.cfg.Binary, Dir: in.RepoPath, Args: t.args(),
		Stdin: buildPrompt(in), StepBudget: t.cfg.StepBudget,
	})
	if err != nil {
		return triage.Decision{}, err
	}
	switch {
	case exit.StepTripped:
		return triage.Decision{}, fmt.Errorf("triage %s: step budget (%d) exhausted", in.Ticket.Key, t.cfg.StepBudget)
	case exit.CtxErr != nil:
		return triage.Decision{}, fmt.Errorf("triage %s: %w", in.Ticket.Key, exit.CtxErr)
	case tr.Result == nil:
		return triage.Decision{}, fmt.Errorf("triage %s: no result (%w): %s", in.Ticket.Key, exit.ExitErr, exit.Stderr)
	case tr.Result.IsError:
		return triage.Decision{}, fmt.Errorf("triage %s: %s", in.Ticket.Key, tr.Result.Result)
	}
	raw := []byte(tr.Result.Result)
	if len(tr.Result.StructuredOutput) > 0 {
		raw = tr.Result.StructuredOutput
	}
	var d decisionJSON
	if err := json.Unmarshal(raw, &d); err != nil {
		return triage.Decision{}, fmt.Errorf("triage %s: parse decision: %w", in.Ticket.Key, err)
	}
	return toDecision(d, in)
}
