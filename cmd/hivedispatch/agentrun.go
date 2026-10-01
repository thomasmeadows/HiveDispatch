package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/executor/claudecode"
	"github.com/thomasmeadows/hivedispatch/internal/executor/codex"
	exfake "github.com/thomasmeadows/hivedispatch/internal/executor/fake"
)

// agentStep, agentUsage and agentRunResult are agent-run's output: an
// executor.Result as JSON, for the LangGraph workflow's code node.
type agentStep struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Input   string `json:"input"`
	Output  string `json:"output"`
	IsError bool   `json:"is_error"`
	Start   string `json:"start"`
	End     string `json:"end"`
}

type agentUsage struct {
	Model        string  `json:"model"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

type agentRunResult struct {
	Status       string      `json:"status"`
	StopCause    string      `json:"stop_cause"`
	Summary      string      `json:"summary"`
	Question     string      `json:"question"`
	ResumeToken  string      `json:"resume_token"`
	ChangedFiles []string    `json:"changed_files"`
	RetryAfter   string      `json:"retry_after"`
	Steps        []agentStep `json:"steps"`
	Usage        agentUsage  `json:"usage"`
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func toAgentRunResult(r executor.Result) agentRunResult {
	out := agentRunResult{
		Status: string(r.Status), StopCause: string(r.StopCause), Summary: r.Summary, Question: r.Question,
		ResumeToken: r.ResumeToken, ChangedFiles: r.ChangedFiles, RetryAfter: stamp(r.RetryAfter),
		Steps: make([]agentStep, 0, len(r.Steps)),
		Usage: agentUsage{Model: r.Usage.Model, InputTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens, CostUSD: r.Usage.CostUSD},
	}
	if out.ChangedFiles == nil {
		out.ChangedFiles = []string{}
	}
	for _, s := range r.Steps {
		out.Steps = append(out.Steps, agentStep{Kind: string(s.Kind), Name: s.Name, Input: s.Input, Output: s.Output, IsError: s.IsError, Start: stamp(s.Start), End: stamp(s.End)})
	}
	return out
}

// runAgentRun runs one executor in a workspace and prints its result as
// JSON. The LangGraph workflow's code node calls it; it is not for people,
// so it is left out of the usage text.
func runAgentRun(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent-run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "path to worker config (for the CLI binaries)")
	kind := fs.String("executor", "claude", "claude, codex or fake")
	workspace := fs.String("workspace", "", "the worktree to run in")
	model := fs.String("model", "", "the agent's model")
	resume := fs.String("resume", "", "the CLI session to resume")
	budget := fs.Int("step-budget", 0, "tool calls before the run is stopped; 0 = unlimited")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *workspace == "" {
		fmt.Fprintln(stderr, "agent-run: -workspace is required")
		return 2
	}
	var claudeBin, codexBin string
	if cfg, err := config.Load(*cfgPath); err == nil {
		claudeBin, codexBin = cfg.Claude.Binary, cfg.Codex.Binary
	}
	var ex executor.Executor
	switch *kind {
	case "claude":
		ex = claudecode.New(claudecode.Config{Binary: claudeBin})
	case "codex":
		ex = codex.New(codex.Config{Binary: codexBin})
	case "fake":
		ex = exfake.New()
	default:
		fmt.Fprintf(stderr, "agent-run: -executor: want claude, codex or fake, got %q\n", *kind)
		return 2
	}
	prompt, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "agent-run: read prompt:", err)
		return 1
	}
	// SIGTERM from the graph runner cancels the run, which kills the CLI's
	// own process group: a plain SIGKILL would orphan it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := ex.Run(ctx, executor.Task{Prompt: string(prompt), Workspace: *workspace, ResumeToken: *resume, StepBudget: *budget, Model: *model})
	if err != nil {
		fmt.Fprintln(stderr, "agent-run:", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(toAgentRunResult(res)); err != nil {
		fmt.Fprintln(stderr, "agent-run: write result:", err)
		return 1
	}
	return 0
}
