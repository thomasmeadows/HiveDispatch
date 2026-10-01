// Package langgraph adapts hivegraph, HiveDispatch's LangGraph workflow,
// to executor.Executor. The workflow plans, codes through `hivedispatch
// agent-run`, runs the repository's checks, fixes, and reviews its diff;
// this package hands it the task and maps its result.
package langgraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/graphcli"
	"github.com/thomasmeadows/hivedispatch/internal/repoconfig"
	"github.com/thomasmeadows/hivedispatch/internal/trace"
)

// checkTimeout bounds each policy check.
const checkTimeout = 10 * time.Minute

const maxSummary = 4000

// ModelConfig is the chat model the plan and review nodes use.
type ModelConfig struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	BaseURL   string `json:"base_url"`
	APIKeyEnv string `json:"api_key_env"`
}

// Config wires the executor.
type Config struct {
	Binary       string                       // hivegraph; default on PATH
	Hivedispatch string                       // this binary, for agent-run
	WorkerConfig string                       // passed to agent-run -config
	Model        ModelConfig                  // the plan and review nodes' chat model
	Inner        map[string]executor.Executor // claude, codex, fake: for Plan and Advise
	Grace        time.Duration                // SIGTERM → SIGKILL; 0 = graphcli's default
}

// Executor runs hivegraph.
type Executor struct{ cfg Config }

var _ executor.Executor = (*Executor)(nil)

// New returns an Executor; an empty Binary means "hivegraph" on PATH.
func New(cfg Config) *Executor {
	if cfg.Binary == "" {
		cfg.Binary = "hivegraph"
	}
	return &Executor{cfg: cfg}
}

// Name implements executor.Executor.
func (e *Executor) Name() string { return "langgraph" }

// task is what hivegraph reads on stdin.
type task struct {
	Ticket              string      `json:"ticket"`
	Prompt              string      `json:"prompt"`
	Workspace           string      `json:"workspace"`
	ResumeToken         string      `json:"resume_token"`
	StepBudget          int         `json:"step_budget"`
	CodeWith            string      `json:"code_with"`
	Model               string      `json:"model"`
	BaseRef             string      `json:"base_ref"`
	GitDir              string      `json:"git_dir"`
	Hivedispatch        string      `json:"hivedispatch"`
	WorkerConfig        string      `json:"worker_config"`
	Checks              []string    `json:"checks"`
	AllowedCommands     []string    `json:"allowed_commands"`
	CheckTimeoutSeconds int         `json:"check_timeout_seconds"`
	MaxFixRounds        int         `json:"max_fix_rounds"`
	MaxReviewRounds     int         `json:"max_review_rounds"`
	Guidance            string      `json:"guidance"`
	Path                []string    `json:"path"`
	ChatModel           ModelConfig `json:"chat_model"`
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// Run implements executor.Executor.
func (e *Executor) Run(ctx context.Context, t executor.Task) (executor.Result, error) {
	rc, err := repoconfig.Load(t.Workspace)
	if err != nil {
		return executor.Result{}, err
	}
	base, err := git(ctx, t.Workspace, "rev-parse", "HEAD")
	if err != nil {
		return executor.Result{}, fmt.Errorf("langgraph: resolve HEAD: %w", err)
	}
	gitDir, err := git(ctx, t.Workspace, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return executor.Result{}, fmt.Errorf("langgraph: resolve git dir: %w", err)
	}
	codeWith := t.CodeWith
	if codeWith == "" {
		codeWith = "claude"
	}
	checks := rc.Checks
	if checks == nil {
		checks = []string{}
	}
	allowed := rc.Executor.LangGraph.AllowedCommands
	if allowed == nil {
		allowed = []string{}
	}
	path := rc.Executor.ExtraPath()
	if path == nil {
		path = []string{}
	}
	in, err := json.Marshal(task{
		Ticket: t.TicketKey, Prompt: t.Prompt, Workspace: t.Workspace, ResumeToken: t.ResumeToken,
		StepBudget: t.StepBudget, CodeWith: codeWith, Model: t.Model, BaseRef: base, GitDir: gitDir,
		Hivedispatch: e.cfg.Hivedispatch, WorkerConfig: e.cfg.WorkerConfig,
		Checks: checks, AllowedCommands: allowed, CheckTimeoutSeconds: int(checkTimeout / time.Second),
		MaxFixRounds: rc.Graph.MaxFixRounds, MaxReviewRounds: rc.Graph.MaxReviewRounds,
		Guidance: rc.Guidance, Path: path, ChatModel: e.cfg.Model,
	})
	if err != nil {
		return executor.Result{}, err
	}
	var env []string
	if parent := trace.SpanFrom(ctx).DottedOrder(); parent != "" {
		env = append(env, "LANGSMITH_PARENT="+parent)
	}
	tr, exit, log, err := graphcli.Run(ctx, graphcli.Cmd{
		Binary: e.cfg.Binary, Dir: t.Workspace, Args: []string{"run"}, Stdin: string(in),
		StepBudget: t.StepBudget, Env: env, Grace: e.cfg.Grace,
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// mapOutcome turns the transcript and how hivegraph ended into a Result.
func mapOutcome(tr graphcli.Transcript, exit graphcli.Exit) executor.Result {
	res := executor.Result{Steps: tr.Steps, Usage: tr.Usage}
	fail := func(c executor.Cause, summary string) executor.Result {
		res.Status, res.StopCause = executor.StatusFailed, c
		if summary == "" {
			summary = c.Describe()
		}
		res.Summary = truncate(summary, maxSummary)
		return res
	}
	r := tr.Result
	if r != nil {
		res.ResumeToken, res.ChangedFiles, res.StepsTraced = r.ResumeToken, r.ChangedFiles, r.StepsTraced
	}
	switch {
	case errors.Is(exit.CtxErr, context.DeadlineExceeded):
		return fail(executor.CauseTimeout, summaryOf(r))
	case exit.StepTripped:
		return fail(executor.CauseStepBudget, summaryOf(r))
	case errors.Is(exit.CtxErr, context.Canceled):
		return fail(executor.CauseKilled, summaryOf(r))
	case r == nil:
		s := "hivegraph exited without a result"
		if exit.ExitErr != nil {
			s += ": " + exit.ExitErr.Error()
		}
		if st := strings.TrimSpace(exit.Stderr); st != "" {
			s += "\n" + truncate(st, 1000)
		}
		return fail(executor.CauseError, s)
	}
	switch executor.Status(r.Status) {
	case executor.StatusCompleted, executor.StatusNeedsInput:
		res.Status = executor.Status(r.Status)
		res.Question = r.Question
		res.Summary = truncate(r.Summary, maxSummary)
		return res
	}
	cause := executor.Cause(r.StopCause)
	if cause == executor.CauseNone {
		cause = executor.CauseError
	}
	return fail(cause, r.Summary)
}

func summaryOf(r *graphcli.Result) string {
	if r == nil {
		return ""
	}
	return r.Summary
}

func (e *Executor) inner(codeWith string) (executor.Executor, error) {
	if codeWith == "" {
		codeWith = "claude"
	}
	if codeWith == "langgraph" {
		return nil, errors.New("langgraph: code_with langgraph is for coding agents only; planning and review run through claude or codex")
	}
	ex, ok := e.cfg.Inner[codeWith]
	if !ok {
		return nil, fmt.Errorf("langgraph: no %s executor for code_with", codeWith)
	}
	return ex, nil
}

// Plan implements executor.Executor through the code_with CLI.
func (e *Executor) Plan(ctx context.Context, t executor.Task) (executor.Footprint, error) {
	ex, err := e.inner(t.CodeWith)
	if err != nil {
		return executor.Footprint{}, err
	}
	return ex.Plan(ctx, t)
}

// Advise implements executor.Executor through the code_with CLI: planning
// and review runs are the CLI's, as for any other agent.
func (e *Executor) Advise(ctx context.Context, a executor.Advice) (json.RawMessage, error) {
	ex, err := e.inner(a.CodeWith)
	if err != nil {
		return nil, err
	}
	return ex.Advise(ctx, a)
}
