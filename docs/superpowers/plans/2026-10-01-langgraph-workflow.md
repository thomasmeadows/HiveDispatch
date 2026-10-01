# LangGraph Workflow Executor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `langgraph` executor. In one coding run it executes a Python LangGraph workflow: plan → code → checks → fix → self-review → finish. The graph's code node calls back into `hivedispatch agent-run`, which runs Claude Code or Codex.

**Architecture:**
- **Go:**
  - `internal/graphcli` runs `hivegraph` as a subprocess. It sends the task as JSON on stdin and reads JSONL events back. Stopping a run sends SIGTERM to the process group, waits through a grace period, then sends SIGKILL.
  - `internal/executor/langgraph` maps that runner onto `executor.Executor`.
  - `hivedispatch agent-run` exposes the existing CLI executors to the graph.
- **Python:** the `graph/` package, `hivegraph`, holds the graph. Its plan and review nodes use a chat model through `langchain-openai`. Checkpoints go to SQLite in the worktree's git directory.

**Tech Stack:** Go 1.27 (stdlib + yaml.v3); Python ≥ 3.11 with `langgraph`, `langgraph-checkpoint-sqlite`, `langchain-openai`, `langsmith`, `pytest` and `ruff`; Vue (the existing `web/`) for the agent form.

**Spec:** `docs/superpowers/specs/2026-10-01-langgraph-workflow-design.md`

## Global Constraints

- Go stays stdlib-only plus `gopkg.in/yaml.v3`. Python dependencies live only in `graph/pyproject.toml`.
- Before every commit, run vet, `go test -race` and golangci-lint on the Go packages touched, and `ruff check`, `ruff format --check` and `pytest` when `graph/` changed. Never commit red, and never pipe a check's exit status away.
- Neither the Go tests nor the Python tests touch the network: fakes only.
- `executor: langgraph`; `code_with: claude | codex | fake`, default `claude`, valid only with `langgraph`.
- `checks` run as `sh -c`, each with a 10-minute timeout. Defaults: `max_fix_rounds` 3, `max_review_rounds` 1.
- The graph's chat model must use an OpenAI-compatible provider: openai, deepseek, huggingface, ollama, or a custom `base_url`. `anthropic` is rejected.
- To stop `hivegraph`: SIGTERM to its process group, a 15 s grace period, then SIGKILL.
- Checkpoints go to `<git dir>/hivegraph/checkpoints.sqlite`.
- LangChain is fully optional. A worker with no `langgraph` agent needs no Python, and prints and warns nothing about the graph.
- The review diff is capped at 100 KiB. The check output fed back to the CLI is the last 8 KiB.
- Event shapes are as in the spec: `step`, `usage`, `node` and `result`.
- Commit messages are Conventional Commits and end with the `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` line.

## Review Focus

1. **`hivegraph` not installed:** the run fails with a clear `CauseError` summary naming `pip install ./graph`. It never hangs, and `check` warns. (Task 4 test.)
2. **`agent-run` prints garbage or nothing:** the code node fails the run with the stderr tail. It doesn't crash, and it doesn't loop. (Task 8 test.)
3. **Checks that hang:** each times out after its limit and counts as failed, with "timed out" in its output. (Task 8 test with a tiny timeout.)
4. **A resumed ticket after a human reply:** the same thread is used, `plan` is skipped, the CLI gets `-resume <session>`, and the prompt is the reply. (Task 9 test.)
5. **The worker is interrupted mid-run:** SIGTERM reaches `hivegraph`'s children before SIGKILL. (Task 4 test with a script that traps TERM.)

---

### Task 1: Agent `code_with`, the `langgraph` executor kind, and policy `checks`/`graph`

**Files:**
- Modify: `internal/config/agents.go` (the Agent struct, ParseAgents validation)
- Modify: `internal/repoconfig/repoconfig.go` (Config gets `Checks`, `Graph`; defaults and validation in Parse)
- Modify: `internal/executor/executor.go` (`Task.CodeWith`, `Advice.CodeWith`, `Result.StepsTraced`)
- Test: `internal/config/agents_test.go`, `internal/repoconfig/repoconfig_test.go`

**Interfaces produced:**
- `config.Agent.CodeWith string` (yaml `code_with`, json `code_with`)
- `repoconfig.Config.Checks []string`
- `repoconfig.Config.Graph GraphPolicy{MaxFixRounds, MaxReviewRounds int}`
- `executor.Task.CodeWith`, `executor.Advice.CodeWith`, `executor.Result.StepsTraced bool`

- [ ] **Step 1: Failing tests.** Append to `internal/config/agents_test.go`:

```go
func TestParseAgentsLangGraph(t *testing.T) {
	got, err := ParseAgents([]byte("agents:\n  - name: g\n    executor: langgraph\n  - name: h\n    executor: langgraph\n    code_with: codex\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].CodeWith != "claude" || got[1].CodeWith != "codex" {
		t.Fatalf("code_with = %q, %q", got[0].CodeWith, got[1].CodeWith)
	}
	for _, bad := range []string{
		"agents:\n  - name: a\n    executor: claude\n    code_with: codex\n",
		"agents:\n  - name: a\n    executor: langgraph\n    code_with: gemini\n",
	} {
		if _, err := ParseAgents([]byte(bad)); err == nil || !strings.Contains(err.Error(), "code_with") {
			t.Errorf("%q: err = %v, want a code_with problem", bad, err)
		}
	}
}
```

Append to `internal/repoconfig/repoconfig_test.go`:

```go
func TestParseChecksAndGraph(t *testing.T) {
	c, err := Parse([]byte("checks:\n  - go vet ./...\n  - go test ./...\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Checks) != 2 || c.Graph.MaxFixRounds != 3 || c.Graph.MaxReviewRounds != 1 {
		t.Fatalf("got %+v", c)
	}
	c, err = Parse([]byte("graph:\n  max_fix_rounds: 0\n  max_review_rounds: 0\n"))
	if err != nil || c.Graph.MaxFixRounds != 0 || c.Graph.MaxReviewRounds != 0 {
		t.Fatalf("explicit zeros: %+v %v", c.Graph, err)
	}
	if _, err := Parse([]byte("graph:\n  max_fix_rounds: -1\n")); err == nil {
		t.Fatal("negative rounds accepted")
	}
	if _, err := Parse([]byte("checks:\n  - \"  \"\n")); err == nil {
		t.Fatal("blank check accepted")
	}
}
```

- [ ] **Step 2: Run them and see them fail.** Run `go test ./internal/config/ ./internal/repoconfig/`. Expected: compile errors for `CodeWith`, `Checks` and `Graph`.

- [ ] **Step 3: Implement.**
  - `Agent` gets `CodeWith string \`yaml:"code_with,omitempty" json:"code_with,omitempty"\`` with the comment `// langgraph only: the CLI its code node runs (claude, codex or fake)`.
  - In `ParseAgents`, replace the executor check with:

```go
		switch a.Executor {
		case "claude", "codex", "fake":
			if a.CodeWith != "" {
				problems = append(problems, fmt.Sprintf("agents[%d].code_with: only an executor: langgraph agent has one", i))
			}
		case "langgraph":
			if a.CodeWith == "" {
				a.CodeWith = "claude"
			}
			if a.CodeWith != "claude" && a.CodeWith != "codex" && a.CodeWith != "fake" {
				problems = append(problems, fmt.Sprintf("agents[%d].code_with: want claude, codex or fake, got %q", i, a.CodeWith))
			}
		default:
			problems = append(problems, fmt.Sprintf("agents[%d].executor: want claude, codex, langgraph or fake, got %q", i, a.Executor))
		}
```

  - Update the `Executor` field comment to list `langgraph`.
  - In repoconfig, the `Config` struct gets:

```go
	Checks []string    `yaml:"checks"` // langgraph runs: commands that must exit 0 after each code step
	Graph  GraphPolicy `yaml:"graph"`
```

```go
// GraphPolicy bounds a langgraph run's loops. Nil means the default.
type GraphPolicy struct {
	MaxFixRounds    int `yaml:"max_fix_rounds"`    // code → checks → fix loops; default 3
	MaxReviewRounds int `yaml:"max_review_rounds"` // self-review → fix loops; default 1
}
```

    Explicit zeros must survive. To allow that, decode `graph` into `*int` fields through a private struct in Parse:

```go
	var g struct {
		Graph struct {
			MaxFixRounds    *int `yaml:"max_fix_rounds"`
			MaxReviewRounds *int `yaml:"max_review_rounds"`
		} `yaml:"graph"`
	}
	if err := yaml.Unmarshal(raw, &g); err != nil {
		return c, fmt.Errorf("%s: %w", FileName, err)
	}
	c.Graph = GraphPolicy{MaxFixRounds: 3, MaxReviewRounds: 1}
	if p := g.Graph.MaxFixRounds; p != nil {
		c.Graph.MaxFixRounds = *p
	}
	if p := g.Graph.MaxReviewRounds; p != nil {
		c.Graph.MaxReviewRounds = *p
	}
	if c.Graph.MaxFixRounds < 0 || c.Graph.MaxReviewRounds < 0 {
		return c, fmt.Errorf("%s: graph.max_fix_rounds and graph.max_review_rounds must not be negative", FileName)
	}
	for i, chk := range c.Checks {
		if strings.TrimSpace(chk) == "" {
			return c, fmt.Errorf("%s: checks[%d] is empty", FileName, i)
		}
	}
```

    The `Graph` field on `Config` takes the tag `yaml:"-"` so the first Unmarshal leaves it alone.
  - In `executor.go`, `Task` gets `CodeWith string // langgraph: the CLI its code node runs`. `Advice` gets the same field. `Result` gets `StepsTraced bool // the executor traced its own steps; tracing skips Steps`.

- [ ] **Step 4: Run the tests and see them pass.** Run `go test -race ./internal/config/ ./internal/repoconfig/ ./internal/executor/...`.
- [ ] **Step 5: Lint and commit.** Run `golangci-lint run ./internal/config/... ./internal/repoconfig/... ./internal/executor/...`, then `git commit -m "feat(config): langgraph executor kind, code_with, and policy checks/graph rounds"`.

### Task 2: The graph's chat-model settings (`graph:` in the worker config)

**Files:**
- Create: `internal/supervisor/graphconfig.go`
- Test: `internal/supervisor/graphconfig_test.go`

**Interfaces produced:** `supervisor.GraphConfig{Binary string; Model Config}` and `supervisor.LoadGraphConfig(workerConfigPath string, getenv func(string) string) (GraphConfig, error)`. `Model` has its preset applied. An empty `graph.provider` falls back to `LoadConfig`, the supervisor's settings.

- [ ] **Step 1: Failing test.**

```go
package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeWorkerConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadGraphConfig(t *testing.T) {
	env := func(k string) string { return map[string]string{"DEEPSEEK_API_KEY": "k"}[k] }

	g, err := LoadGraphConfig(writeWorkerConfig(t, "graph:\n  provider: openai\n  model: gpt-x\n"), env)
	if err != nil || g.Binary != "hivegraph" || g.Model.Provider != "openai" || g.Model.Model != "gpt-x" || g.Model.APIKeyEnv != "OPENAI_API_KEY" || g.Model.BaseURL == "" {
		t.Fatalf("explicit: %+v %v", g, err)
	}

	g, err = LoadGraphConfig(writeWorkerConfig(t, "supervisor:\n  provider: deepseek\ngraph:\n  binary: /opt/hivegraph\n"), env)
	if err != nil || g.Binary != "/opt/hivegraph" || g.Model.Provider != "deepseek" || g.Model.Model != "deepseek-flash" {
		t.Fatalf("inherit supervisor: %+v %v", g, err)
	}

	if _, err := LoadGraphConfig(writeWorkerConfig(t, "graph:\n  provider: anthropic\n"), env); err == nil || !strings.Contains(err.Error(), "OpenAI-compatible") {
		t.Fatalf("anthropic: %v", err)
	}
	if _, err := LoadGraphConfig(writeWorkerConfig(t, "supervisor:\n  provider: anthropic\n"), env); err == nil || !strings.Contains(err.Error(), "graph.provider") {
		t.Fatalf("anthropic inherited: %v", err)
	}
}
```

- [ ] **Step 2: Run it and see it fail.** Run `go test ./internal/supervisor/ -run TestLoadGraphConfig`. Expected: undefined `LoadGraphConfig`.
- [ ] **Step 3: Implement `graphconfig.go`.**

```go
package supervisor

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// GraphConfig is the graph: block of the worker config: the hivegraph
// binary and the chat model its plan and review nodes use.
type GraphConfig struct {
	Binary string
	Model  Config
}

// LoadGraphConfig reads graph: from the worker config. Without a
// graph.provider the supervisor's model is used. The graph talks to its
// model through LangChain's OpenAI client, so Anthropic is refused.
func LoadGraphConfig(workerConfigPath string, getenv func(string) string) (GraphConfig, error) {
	var f struct {
		Graph struct {
			Binary string `yaml:"binary"`
			Config `yaml:",inline"`
		} `yaml:"graph"`
	}
	raw, err := os.ReadFile(workerConfigPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return GraphConfig{}, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return GraphConfig{}, fmt.Errorf("parse config %s: %w", workerConfigPath, err)
	}
	g := GraphConfig{Binary: f.Graph.Binary, Model: f.Graph.Config}
	if g.Binary == "" {
		g.Binary = "hivegraph"
	}
	if g.Model.Provider == "" {
		sup, err := LoadConfig(workerConfigPath, getenv)
		if err != nil {
			return g, err
		}
		if sup.Provider == "anthropic" {
			return g, fmt.Errorf("%s: the supervisor uses anthropic, which the graph cannot: set graph.provider to an OpenAI-compatible provider (openai, deepseek, huggingface or ollama)", workerConfigPath)
		}
		g.Model = sup
		return g, nil
	}
	if g.Model.Provider == "anthropic" {
		return g, fmt.Errorf("%s: graph.provider anthropic: the graph needs an OpenAI-compatible provider (openai, deepseek, huggingface or ollama)", workerConfigPath)
	}
	if err := g.Model.validate(); err != nil {
		return g, fmt.Errorf("%s: graph.%w", workerConfigPath, err)
	}
	g.Model.applyPreset()
	return g, nil
}
```

- [ ] **Step 4: Run the test, then the supervisor package, and see them pass.** Run `go test -race ./internal/supervisor/`.
- [ ] **Step 5: Lint and commit.** `feat(supervisor): graph: block for the LangGraph chat model, inheriting the supervisor's`.

### Task 3: `hivedispatch agent-run`

**Files:**
- Create: `cmd/hivedispatch/agentrun.go`, `cmd/hivedispatch/agentrun_test.go`
- Modify: `cmd/hivedispatch/main.go` (add `case "agent-run"` to the switch; it stays out of `usage`)

**Interfaces produced:**
- Command: `hivedispatch agent-run -executor claude|codex|fake -workspace DIR [-model M] [-resume TOKEN] [-step-budget N] [-config P]`. The prompt is read on stdin.
- Output: one JSON object, `agentRunResult`:

```json
{"status","stop_cause","summary","question","resume_token","changed_files":[],"retry_after":"RFC3339 or empty",
 "steps":[{"kind","name","input","output","is_error","start","end"}],
 "usage":{"model","input_tokens","output_tokens","cost_usd"}}
```

  It exits 0 when it printed a result, 1 when it could not run, and 2 on bad flags.

- [ ] **Step 1: Failing test.**

```go
package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentRunFake(t *testing.T) {
	var out, errb bytes.Buffer
	code := runAgentRun([]string{"-executor", "fake", "-workspace", t.TempDir()}, strings.NewReader("do it"), &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var r agentRunResult
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	if r.Status != "completed" || r.Summary == "" {
		t.Fatalf("result %+v", r)
	}
}

func TestAgentRunBadExecutor(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runAgentRun([]string{"-executor", "gemini", "-workspace", "."}, strings.NewReader(""), &out, &errb); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if out.Len() != 0 {
		t.Fatalf("printed %q on a usage error", out.String())
	}
}
```

- [ ] **Step 2: Run it and see it fail.** Run `go test ./cmd/hivedispatch/ -run TestAgentRun`. Expected: undefined.
- [ ] **Step 3: Implement `agentrun.go`.** It loads the worker config only for the binary paths. A missing config is fine, and the defaults are used.

```go
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

// agentStep and agentRunResult are agent-run's output: an executor.Result
// as JSON, for the LangGraph code node.
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
// JSON. The LangGraph workflow's code node calls it; it is not for people.
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
```

Add to `main.go`'s switch: `case "agent-run": return runAgentRun(args[1:], os.Stdin, stdout, stderr)`.

- [ ] **Step 4: Run the tests and see them pass.** Run `go test -race ./cmd/hivedispatch/`.
- [ ] **Step 5: Lint and commit.** `feat: hivedispatch agent-run, a CLI executor as JSON for the graph's code node`.

### Task 4: `internal/graphcli`, the `hivegraph` runner and event parser

**Files:**
- Create: `internal/graphcli/run.go`, `internal/graphcli/stream.go`, `internal/graphcli/run_test.go`, `internal/graphcli/stream_test.go`, `internal/graphcli/graphtest/fakegraph.sh`

**Interfaces produced:**

```go
type Cmd struct {
	Binary     string
	Dir        string
	Args       []string // ["run"]
	Stdin      string   // the task JSON
	StepBudget int      // code-node steps before the run is stopped; 0 = unlimited
	Env        []string
	Grace      time.Duration // SIGTERM → SIGKILL; 0 = 15s
	MaxLog     int
}
type Exit struct{ CtxErr error; StepTripped bool; ExitErr error; Stderr string }
type Result struct { // the "result" event
	Status, StopCause, Summary, Question, ResumeToken string
	ChangedFiles []string
	StepsTraced  bool
}
type Transcript struct {
	Steps  []executor.Step
	Usage  executor.Usage // summed; Model from the last usage event
	Nodes  []string       // node names, in order
	Result *Result
	Lines  int
}
func Run(ctx context.Context, c Cmd) (Transcript, Exit, string, error)
func NewParser(onStep func(count int)) *Parser
func (p *Parser) Line(raw []byte)
func (p *Parser) Transcript() Transcript
```

- [ ] **Step 1: Failing parser test** (`stream_test.go`).

```go
package graphcli

import (
	"testing"
	"time"
)

func TestParserEvents(t *testing.T) {
	var counts []int
	p := NewParser(func(n int) { counts = append(counts, n) })
	for _, l := range []string{
		`not json`,
		`{"type":"node","name":"plan","start":"2026-10-01T12:00:00Z","end":"2026-10-01T12:00:02Z"}`,
		`{"type":"step","kind":"tool","name":"Bash","input":"{}","output":"ok","is_error":false,"start":"2026-10-01T12:00:03Z","end":"2026-10-01T12:00:05Z"}`,
		`{"type":"step","kind":"message","name":"assistant","output":"hi","start":"2026-10-01T12:00:06Z","end":"2026-10-01T12:00:06Z"}`,
		`{"type":"usage","model":"claude-opus-5-5","input_tokens":10,"output_tokens":2,"cost_usd":0.1}`,
		`{"type":"usage","model":"claude-opus-5-5","input_tokens":5,"output_tokens":1,"cost_usd":0.05}`,
		`{"type":"result","status":"completed","stop_cause":"","summary":"done","resume_token":"th-1","changed_files":["a.go"],"steps_traced":true}`,
	} {
		p.Line([]byte(l))
	}
	tr := p.Transcript()
	if len(tr.Steps) != 2 || tr.Steps[0].Name != "Bash" || tr.Steps[0].End.Sub(tr.Steps[0].Start) != 2*time.Second || tr.Steps[1].Kind != "message" {
		t.Fatalf("steps %+v", tr.Steps)
	}
	if tr.Usage.InputTokens != 15 || tr.Usage.OutputTokens != 3 || tr.Usage.CostUSD < 0.149 || tr.Usage.Model != "claude-opus-5-5" {
		t.Fatalf("usage %+v", tr.Usage)
	}
	if len(tr.Nodes) != 1 || tr.Nodes[0] != "plan" {
		t.Fatalf("nodes %v", tr.Nodes)
	}
	if r := tr.Result; r == nil || r.Status != "completed" || r.ResumeToken != "th-1" || !r.StepsTraced || len(r.ChangedFiles) != 1 {
		t.Fatalf("result %+v", tr.Result)
	}
	if len(counts) != 1 || counts[0] != 1 {
		t.Fatalf("only tool steps count toward the budget: %v", counts)
	}
}
```

- [ ] **Step 2: Failing runner tests** (`run_test.go`). The fake script `graphtest/fakegraph.sh` behaves according to `$FAKEGRAPH_MODE`:

```sh
#!/bin/sh
# A stand-in for hivegraph: replays events by mode.
cat > /dev/null
case "$FAKEGRAPH_MODE" in
ok)
  echo '{"type":"step","kind":"tool","name":"Bash","input":"{}","output":"ok","is_error":false,"start":"2026-10-01T12:00:00Z","end":"2026-10-01T12:00:01Z"}'
  echo '{"type":"result","status":"completed","summary":"done","resume_token":"th-1","changed_files":[]}'
  ;;
noresult)
  echo 'boom' >&2
  exit 3
  ;;
steps)
  i=0
  while [ $i -lt 10 ]; do
    echo '{"type":"step","kind":"tool","name":"Bash","start":"2026-10-01T12:00:00Z","end":"2026-10-01T12:00:01Z"}'
    i=$((i+1))
  done
  sleep 30
  ;;
trap)
  trap 'echo "{\"type\":\"node\",\"name\":\"got-term\"}"; exit 0' TERM
  echo '{"type":"node","name":"started"}'
  while :; do sleep 0.05; done
  ;;
ignore)
  trap '' TERM
  echo '{"type":"node","name":"started"}'
  while :; do sleep 0.05; done
  ;;
esac
```

```go
package graphcli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fake(t *testing.T, mode string) Cmd {
	t.Helper()
	bin, err := filepath.Abs("graphtest/fakegraph.sh")
	if err != nil {
		t.Fatal(err)
	}
	return Cmd{Binary: bin, Dir: t.TempDir(), Args: []string{"run"}, Stdin: "{}", Env: []string{"FAKEGRAPH_MODE=" + mode}, Grace: 300 * time.Millisecond}
}

func TestRunOK(t *testing.T) {
	tr, exit, log, err := Run(context.Background(), fake(t, "ok"))
	if err != nil || exit.ExitErr != nil || tr.Result == nil || tr.Result.Summary != "done" || len(tr.Steps) != 1 || !strings.Contains(log, `"result"`) {
		t.Fatalf("tr %+v exit %+v err %v", tr, exit, err)
	}
}

func TestRunNoResult(t *testing.T) {
	tr, exit, _, err := Run(context.Background(), fake(t, "noresult"))
	if err != nil || tr.Result != nil || exit.ExitErr == nil || !strings.Contains(exit.Stderr, "boom") {
		t.Fatalf("tr %+v exit %+v err %v", tr, exit, err)
	}
}

func TestRunMissingBinary(t *testing.T) {
	c := fake(t, "ok")
	c.Binary = filepath.Join(t.TempDir(), "nope")
	if _, _, _, err := Run(context.Background(), c); err == nil || !strings.Contains(err.Error(), "pip install") {
		t.Fatalf("err %v", err)
	}
}

func TestRunStepBudget(t *testing.T) {
	c := fake(t, "steps")
	c.StepBudget = 3
	start := time.Now()
	_, exit, _, _ := Run(context.Background(), c)
	if !exit.StepTripped || exit.CtxErr != nil || time.Since(start) > 10*time.Second {
		t.Fatalf("exit %+v after %v", exit, time.Since(start))
	}
}

func TestCancelSendsTermFirst(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := fake(t, "trap")
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	tr, exit, _, _ := Run(ctx, c)
	if len(tr.Nodes) < 2 || tr.Nodes[len(tr.Nodes)-1] != "got-term" {
		t.Fatalf("SIGTERM never reached the graph: nodes %v exit %+v", tr.Nodes, exit)
	}
}

func TestCancelKillsAfterGrace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	start := time.Now()
	_, exit, _, _ := Run(ctx, fake(t, "ignore"))
	if time.Since(start) > 5*time.Second || exit.CtxErr == nil {
		t.Fatalf("a TERM-ignoring graph was not killed: %v %+v", time.Since(start), exit)
	}
}

func TestMain(m *testing.M) {
	_ = os.Chmod("graphtest/fakegraph.sh", 0o755)
	os.Exit(m.Run())
}
```

- [ ] **Step 3: Run them and see them fail.** Run `go test ./internal/graphcli/`.
- [ ] **Step 4: Implement `stream.go`.**

```go
// Package graphcli runs hivegraph, the LangGraph workflow, as a subprocess
// and parses its JSONL events. hivegraph runs Claude Code or Codex through
// `hivedispatch agent-run`, which kills its CLI on SIGTERM; so stopping a
// run sends SIGTERM to the group and waits before SIGKILL.
package graphcli

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
)

// Result is hivegraph's final "result" event.
type Result struct {
	Status       string   `json:"status"`
	StopCause    string   `json:"stop_cause"`
	Summary      string   `json:"summary"`
	Question     string   `json:"question"`
	ResumeToken  string   `json:"resume_token"`
	ChangedFiles []string `json:"changed_files"`
	StepsTraced  bool     `json:"steps_traced"`
}

// Transcript is what the parser learned from a run.
type Transcript struct {
	Steps  []executor.Step
	Usage  executor.Usage
	Nodes  []string
	Result *Result
	Lines  int
}

type event struct {
	Type         string  `json:"type"`
	Kind         string  `json:"kind"`
	Name         string  `json:"name"`
	Input        string  `json:"input"`
	Output       string  `json:"output"`
	IsError      bool    `json:"is_error"`
	Start        string  `json:"start"`
	End          string  `json:"end"`
	Model        string  `json:"model"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// Parser consumes hivegraph's stdout lines.
type Parser struct {
	onStep func(count int)
	tools  int
	t      Transcript
}

// NewParser returns a parser that calls onStep with the running count of
// tool steps, for the step budget.
func NewParser(onStep func(int)) *Parser { return &Parser{onStep: onStep} }

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// Line consumes one line. Non-JSON lines are counted and ignored.
func (p *Parser) Line(raw []byte) {
	p.t.Lines++
	s := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(s, "{") {
		return
	}
	var e event
	if json.Unmarshal([]byte(s), &e) != nil {
		return
	}
	switch e.Type {
	case "node":
		p.t.Nodes = append(p.t.Nodes, e.Name)
	case "step":
		if len(p.t.Steps) < executor.MaxSteps {
			p.t.Steps = append(p.t.Steps, executor.Step{
				Kind: executor.StepKind(e.Kind), Name: e.Name, Input: executor.CapOutput(e.Input), Output: executor.CapOutput(e.Output),
				IsError: e.IsError, Start: parseTime(e.Start), End: parseTime(e.End),
			})
		}
		if e.Kind == string(executor.StepTool) {
			p.tools++
			if p.onStep != nil {
				p.onStep(p.tools)
			}
		}
	case "usage":
		p.t.Usage.InputTokens += e.InputTokens
		p.t.Usage.OutputTokens += e.OutputTokens
		p.t.Usage.CostUSD += e.CostUSD
		if e.Model != "" {
			p.t.Usage.Model = e.Model
		}
	case "result":
		var r Result
		if json.Unmarshal([]byte(s), &r) == nil {
			p.t.Result = &r
		}
	}
}

// Transcript returns what has been parsed so far.
func (p *Parser) Transcript() Transcript { return p.t }
```

- [ ] **Step 5: Implement `run.go`.** It follows `codexcli/run.go`, with two differences: the cancel function sends SIGTERM and lets `WaitDelay` escalate, and a missing binary gets a helpful message.

```go
package graphcli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	maxLineBytes   = 1 << 20
	defaultMaxLog  = 2 << 20
	defaultGrace   = 15 * time.Second
	maxStderrBytes = 64 << 10
)

// Cmd describes one hivegraph invocation.
type Cmd struct {
	Binary     string
	Dir        string
	Args       []string
	Stdin      string
	StepBudget int           // tool steps before the run is stopped; 0 = unlimited
	Env        []string      // extra KEY=VALUE entries (later wins)
	Grace      time.Duration // between SIGTERM and SIGKILL; 0 = 15s
	MaxLog     int           // 0 = defaultMaxLog
}

// Exit describes how the process ended.
type Exit struct {
	CtxErr      error
	StepTripped bool
	ExitErr     error
	Stderr      string
}

type boundedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	n   int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.n - b.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		b.buf.Write(p)
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Run executes c and parses its events. The error is non-nil only when
// hivegraph could not be started.
func Run(ctx context.Context, c Cmd) (Transcript, Exit, string, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	tripped := false
	parser := NewParser(func(n int) {
		if c.StepBudget > 0 && n > c.StepBudget {
			mu.Lock()
			tripped = true
			mu.Unlock()
			cancel()
		}
	})
	grace := c.Grace
	if grace == 0 {
		grace = defaultGrace
	}
	maxLog := c.MaxLog
	if maxLog == 0 {
		maxLog = defaultMaxLog
	}
	cmd := exec.CommandContext(runCtx, c.Binary, c.Args...)
	cmd.Dir = c.Dir
	cmd.Stdin = strings.NewReader(c.Stdin)
	cmd.Env = append(os.Environ(), c.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// SIGTERM first: agent-run turns it into killing the CLI's own
		// process group. WaitDelay then escalates to SIGKILL.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = grace
	logBuf := &boundedBuffer{n: maxLog}
	stderr := &boundedBuffer{n: maxStderrBytes}
	cmd.Stderr = io.MultiWriter(stderr, logBuf)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Transcript{}, Exit{}, "", err
	}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return Transcript{}, Exit{}, "", fmt.Errorf("start %s: %w (install the graph with `pipx install ./graph` or `pip install ./graph`, or set graph.binary)", c.Binary, err)
		}
		return Transcript{}, Exit{}, "", fmt.Errorf("start %s: %w", c.Binary, err)
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), maxLineBytes)
	for sc.Scan() {
		line := sc.Bytes()
		_, _ = logBuf.Write(append(append([]byte(nil), line...), '\n'))
		parser.Line(line)
	}
	waitErr := cmd.Wait()
	if runCtx.Err() != nil && cmd.Process != nil {
		// WaitDelay killed only the leader; take the group with it.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	mu.Lock()
	t := tripped
	mu.Unlock()
	exit := Exit{CtxErr: ctx.Err(), StepTripped: t, ExitErr: waitErr, Stderr: stderr.String()}
	if t {
		exit.CtxErr = nil
	}
	return parser.Transcript(), exit, logBuf.String(), nil
}
```

- [ ] **Step 6: Run the tests and see them pass.** Run `go test -race -count=3 ./internal/graphcli/`. If `TestCancelKillsAfterGrace` is flaky because the stdout pipe is held open, rely on `WaitDelay`, which closes the pipes, and keep the group kill after `Wait`.
- [ ] **Step 7: Lint and commit.** `feat(graphcli): run hivegraph with SIGTERM-then-SIGKILL and parse its events`.

### Task 5: `internal/executor/langgraph`

**Files:**
- Create: `internal/executor/langgraph/langgraph.go`, `internal/executor/langgraph/langgraph_test.go`

**Interfaces consumed:**
- From Task 4: `graphcli.Run`, `Cmd`, `Transcript`, `Exit`.
- From Task 1: `repoconfig.Load(dir)` with `Checks`, `Graph`, `Executor.ExtraPath()` and `Guidance`.
- The `executor.Executor`s for `code_with`, which `Plan`/`Advise` delegate to.

**Interfaces produced:**

```go
type Config struct {
	Binary      string                       // hivegraph
	Hivedispatch string                      // path of this binary, for agent-run
	WorkerConfig string                      // passed to agent-run -config
	Model       ModelConfig                  // the chat model for plan/review
	Inner       map[string]executor.Executor // claude, codex, fake: for Plan and Advise
	Grace       time.Duration                // 0 = graphcli default
}
type ModelConfig struct{ Provider, Model, BaseURL, APIKeyEnv string }
func New(cfg Config) *Executor // Name() == "langgraph"
```

Task JSON written to `hivegraph run`'s stdin:

```json
{"ticket":"HIVE-1","prompt":"...","workspace":"/w","resume_token":"","step_budget":200,
 "code_with":"claude","model":"","base_ref":"<sha>","hivedispatch":"/usr/bin/hivedispatch","worker_config":"/home/u/.config/hivedispatch/config.yaml",
 "checks":["go test ./..."],"check_timeout_seconds":600,"max_fix_rounds":3,"max_review_rounds":1,
 "guidance":"...","path":["/home/u/go/bin"],"git_dir":"/repo/.git/worktrees/HIVE-1",
 "chat_model":{"provider":"deepseek","model":"deepseek-flash","base_url":"https://api.deepseek.com/v1","api_key_env":"DEEPSEEK_API_KEY"}}
```

- [ ] **Step 1: Failing tests.** They use a fake `hivegraph` written into a temp dir. The script saves its stdin and replays an event file:

```go
package langgraph

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
	exfake "github.com/thomasmeadows/hivedispatch/internal/executor/fake"
	"github.com/thomasmeadows/hivedispatch/internal/trace"
	tracefake "github.com/thomasmeadows/hivedispatch/internal/trace/fake"
)

// fakeGraph writes a hivegraph that saves its stdin to task.json and
// prints events.
func fakeGraph(t *testing.T, events string) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(events), 0o600); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(dir, "hivegraph")
	script := "#!/bin/sh\ncat > " + filepath.Join(dir, "task.json") + "\nenv > " + filepath.Join(dir, "env.txt") + "\ncat " + filepath.Join(dir, "events.jsonl") + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

// gitRepo is a workspace with one commit, so base_ref and git_dir resolve.
func gitRepo(t *testing.T) string {
	t.Helper()
	w := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", w}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(w, ".hive-dispatch"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w, ".hive-dispatch", "policy.yaml"), []byte("checks:\n  - go test ./...\nguidance: be nice\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return w
}

func newExec(bin string) *Executor {
	return New(Config{Binary: bin, Hivedispatch: "/bin/hivedispatch", WorkerConfig: "/cfg.yaml",
		Model: ModelConfig{Provider: "deepseek", Model: "deepseek-flash", BaseURL: "https://api.deepseek.com/v1", APIKeyEnv: "DEEPSEEK_API_KEY"},
		Inner: map[string]executor.Executor{"fake": exfake.New()}})
}

const okEvents = `{"type":"step","kind":"tool","name":"Bash","input":"{}","output":"ok","start":"2026-10-01T12:00:00Z","end":"2026-10-01T12:00:01Z"}
{"type":"usage","model":"claude-opus-5-5","input_tokens":3,"output_tokens":1}
{"type":"result","status":"completed","summary":"done","resume_token":"th-1","changed_files":["a.go"],"steps_traced":true}
`

func TestRunPassesTaskAndMapsResult(t *testing.T) {
	bin, dir := fakeGraph(t, okEvents)
	w := gitRepo(t)
	res, err := newExec(bin).Run(context.Background(), executor.Task{TicketKey: "HIVE-1", Prompt: "do it", Workspace: w, StepBudget: 50, CodeWith: "codex", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != executor.StatusCompleted || res.Summary != "done" || res.ResumeToken != "th-1" || len(res.Steps) != 1 || !res.StepsTraced || res.Usage.InputTokens != 3 || res.Log == "" {
		t.Fatalf("res %+v", res)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "task.json"))
	if err != nil {
		t.Fatal(err)
	}
	var task map[string]any
	if err := json.Unmarshal(raw, &task); err != nil {
		t.Fatal(err)
	}
	if task["ticket"] != "HIVE-1" || task["code_with"] != "codex" || task["model"] != "m" || task["guidance"] != "be nice" || task["hivedispatch"] != "/bin/hivedispatch" || task["max_fix_rounds"] != float64(3) {
		t.Fatalf("task %v", task)
	}
	if checks, _ := task["checks"].([]any); len(checks) != 1 {
		t.Fatalf("checks %v", task["checks"])
	}
	if task["base_ref"] == "" || !strings.Contains(task["git_dir"].(string), ".git") {
		t.Fatalf("git refs %v %v", task["base_ref"], task["git_dir"])
	}
	if cm, _ := task["chat_model"].(map[string]any); cm["api_key_env"] != "DEEPSEEK_API_KEY" {
		t.Fatalf("chat model %v", task["chat_model"])
	}
}

func TestRunWithoutResultFails(t *testing.T) {
	bin, _ := fakeGraph(t, "")
	res, err := newExec(bin).Run(context.Background(), executor.Task{Prompt: "x", Workspace: gitRepo(t)})
	if err != nil || res.Status != executor.StatusFailed || res.StopCause != executor.CauseError {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestRunNeedsInput(t *testing.T) {
	bin, _ := fakeGraph(t, `{"type":"result","status":"needs_input","question":"Which DB?","summary":"asked","resume_token":"th-2"}`+"\n")
	res, err := newExec(bin).Run(context.Background(), executor.Task{Prompt: "x", Workspace: gitRepo(t)})
	if err != nil || res.Status != executor.StatusNeedsInput || res.Question != "Which DB?" || res.ResumeToken != "th-2" {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestRunSetsTraceParent(t *testing.T) {
	bin, dir := fakeGraph(t, okEvents)
	tr := trace.New(&tracefake.Exporter{}, trace.Options{})
	ctx, span := tr.Start(context.Background(), "run langgraph", trace.KindChain, nil)
	defer span.End(nil, nil)
	if _, err := newExec(bin).Run(ctx, executor.Task{Prompt: "x", Workspace: gitRepo(t)}); err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(filepath.Join(dir, "env.txt"))
	if !strings.Contains(string(env), "LANGSMITH_PARENT="+span.DottedOrder()) {
		t.Fatal("LANGSMITH_PARENT not passed")
	}
}

func TestAdviseDelegates(t *testing.T) {
	bin, _ := fakeGraph(t, "")
	raw, err := newExec(bin).Advise(context.Background(), executor.Advice{Kind: executor.AdvicePlan, CodeWith: "fake"})
	if err != nil || !strings.Contains(string(raw), "plan") {
		t.Fatalf("raw %s err %v", raw, err)
	}
}
```

  - Also add `Span.DottedOrder() string` to `internal/trace/trace.go`. It is nil-safe and returns `""` for a nil span. Add a test in `trace_test.go` that `DottedOrder()` equals the exported run's `DottedOrder`.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement `langgraph.go`.**

```go
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
	"path/filepath"
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
	Binary       string
	Hivedispatch string
	WorkerConfig string
	Model        ModelConfig
	Inner        map[string]executor.Executor
	Grace        time.Duration
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
	in, err := json.Marshal(task{
		Ticket: t.TicketKey, Prompt: t.Prompt, Workspace: t.Workspace, ResumeToken: t.ResumeToken,
		StepBudget: t.StepBudget, CodeWith: codeWith, Model: t.Model, BaseRef: base, GitDir: gitDir,
		Hivedispatch: e.cfg.Hivedispatch, WorkerConfig: e.cfg.WorkerConfig,
		Checks: checks, CheckTimeoutSeconds: int(checkTimeout / time.Second),
		MaxFixRounds: rc.Graph.MaxFixRounds, MaxReviewRounds: rc.Graph.MaxReviewRounds,
		Guidance: rc.Guidance, Path: rc.Executor.ExtraPath(), ChatModel: e.cfg.Model,
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

var _ = filepath.Join // keep imports honest if unused helpers are removed
```

  (Drop the `filepath` line if `filepath` ends up unused; it is shown only so the import block compiles while iterating.)

- [ ] **Step 4: Run the tests and see them pass.** Run `go test -race ./internal/executor/langgraph/ ./internal/trace/`.
- [ ] **Step 5: Lint and commit.** `feat(executor): langgraph executor running hivegraph with the repository's checks`.

### Task 6: Dispatch and wiring: CodeWith, StepsTraced, the executor map, `check`

**Files:**
- Modify: `internal/dispatch/handle.go` (Task.CodeWith), `internal/dispatch/stages.go` (`advise` sets `a.CodeWith`), `internal/dispatch/trace.go` (skip steps when `res.StepsTraced`)
- Modify: `cmd/hivedispatch/wire.go` (add a `"langgraph"` executor), `cmd/hivedispatch/main.go` (`check` prints the graph line; the `-executor` help lists langgraph)
- Test: `internal/dispatch/trace_test.go`, `internal/dispatch/agents_test.go` or `roles_test.go`

- [ ] **Step 1: Failing tests** in `internal/dispatch/trace_test.go`:

```go
func TestStepsTracedByTheExecutorAreNotDuplicated(t *testing.T) {
	h := newHarness(t)
	rec := h.traced()
	h.ex.Default = executor.Result{Status: executor.StatusCompleted, Summary: "ok", StepsTraced: true,
		Steps: []executor.Step{{Kind: executor.StepTool, Name: "Bash", Start: time.Now(), End: time.Now()}}}
	h.handle(t)
	run := rec.Named("run fake")
	if len(run) != 1 || len(rec.Children(run[0].ID)) != 0 {
		t.Fatalf("steps were traced twice: %+v", rec.Ended())
	}
}

func TestCodeWithReachesTheExecutor(t *testing.T) {
	h := newHarness(t)
	h.setAgents(config.Agent{Name: "g", Role: config.RoleCoding, Executor: "langgraph", CodeWith: "codex"})
	h.d.Executors = map[string]executor.Executor{"langgraph": h.ex}
	h.handle(t)
	if calls := h.ex.Calls(); len(calls) != 1 || calls[0].CodeWith != "codex" {
		t.Fatalf("calls %+v", calls)
	}
}
```

  Add the `config` import if it is missing.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.**
  - In `execute`, the `executor.Task{...}` literal gets `CodeWith: agent.CodeWith`.
  - In `advise`, add `a.CodeWith = agent.CodeWith` before the call.
  - In `tracedExecutor.Run`, wrap the steps loop in `if !res.StepsTraced { ... }`.
  - In `wire.go`, after the map literal:

```go
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	gc, gerr := supervisor.LoadGraphConfig(opts.configPath, os.Getenv)
	if gerr != nil && usesLangGraph(cfg) {
		logger.Warn("langgraph agents will fail until graph: is fixed", "err", gerr)
	}
	executors["langgraph"] = langgraph.New(langgraph.Config{
		Binary: gc.Binary, Hivedispatch: exe, WorkerConfig: opts.configPath,
		Model: langgraph.ModelConfig{Provider: gc.Model.Provider, Model: gc.Model.Model, BaseURL: gc.Model.BaseURL, APIKeyEnv: gc.Model.APIKeyEnv},
		Inner: map[string]executor.Executor{"claude": executors["claude"], "codex": executors["codex"], "fake": fake},
	})
```

    `wireOptions` gains `configPath string`, and both `newWorker` callers in `main.go` pass `configPath: *cfgPath`. Building the map comes before the `-executor` override, which already replaces every kind, so `langgraph` follows `-executor fake` like the others. The error message becomes `want claude, codex, langgraph or fake`.
  - Add `usesLangGraph(cfg *config.Config) bool` in `wire.go`, which is true when any repository has an agent with `Executor == "langgraph"`. LangChain stays fully optional: without such an agent, nothing graph-related is warned about or printed.
  - In `runCheck`, after the tracing line, only when `usesLangGraph(cfg)`:

```go
	if !usesLangGraph(cfg) {
		// nothing: graph workflows are optional
	} else if gc, err := supervisor.LoadGraphConfig(*cfgPath, os.Getenv); err != nil {
		fmt.Fprintln(stdout, "graph:", err)
	} else if p, err := exec.LookPath(gc.Binary); err != nil {
		fmt.Fprintf(stdout, "graph: %s not found — langgraph agents need it (pipx install ./graph)\n", gc.Binary)
	} else {
		fmt.Fprintf(stdout, "graph: %s, chat model %s/%s\n", p, gc.Model.Provider, gc.Model.Model)
	}
```

- [ ] **Step 4: Run the tests and see them pass.** Run `go test -race ./internal/dispatch/ ./cmd/...`.
- [ ] **Step 5: Lint and commit.** `feat: wire the langgraph executor; pass code_with; skip steps the graph traced itself`.

### Task 7: Python package scaffolding: task, events and the CLI entry point

**Files:**
- Create: `graph/pyproject.toml`, `graph/README.md`, `graph/hivegraph/__init__.py`, `graph/hivegraph/task.py`, `graph/hivegraph/events.py`, `graph/hivegraph/cli.py`, `graph/tests/test_events.py`, `graph/tests/conftest.py`
- Modify: `.gitignore` (add `graph/**/__pycache__/`, `graph/.venv/`, `graph/*.egg-info/`, `graph/.pytest_cache/`, `graph/.ruff_cache/`)

**Interfaces produced (Python):**
- `task.Task.from_json(raw: str) -> Task`: a dataclass with exactly the Go `task` JSON fields. `chat_model` is a `ChatModel` dataclass.
- `events.Emitter(out=sys.stdout)` with methods:
  - `.step(kind, name, input, output, is_error, start, end)`
  - `.usage(model, input_tokens, output_tokens, cost_usd=0.0)`
  - `.node(name, start, end)`
  - `.result(status, stop_cause, summary, question, resume_token, changed_files, steps_traced)`

  Each writes one JSON line and flushes. Times are datetimes, rendered with `isoformat()` in UTC with a `Z`.
- `cli.main(argv=None) -> int`: `hivegraph run` reads the task from stdin and calls `graph.run_task(task, emitter)`. Task 8 supplies `run_task`.

- [ ] **Step 1: `pyproject.toml`**

```toml
[project]
name = "hivegraph"
version = "0.1.0"
description = "HiveDispatch's LangGraph workflow: plan, code, check, fix, review."
requires-python = ">=3.11"
dependencies = [
  "langgraph>=1.2",
  "langgraph-checkpoint-sqlite>=3.1",
  "langchain-openai>=1.6",
  "langsmith>=0.14",
]

[project.optional-dependencies]
dev = ["pytest>=8", "ruff>=0.6"]

[project.scripts]
hivegraph = "hivegraph.cli:main"

[build-system]
requires = ["setuptools>=69"]
build-backend = "setuptools.build_meta"

[tool.setuptools]
packages = ["hivegraph"]

[tool.ruff]
line-length = 110
target-version = "py311"

[tool.ruff.lint]
select = ["E", "F", "I", "B", "UP"]

[tool.pytest.ini_options]
testpaths = ["tests"]
```

- [ ] **Step 2: Failing test** in `tests/test_events.py`:

```python
import io
import json
from datetime import UTC, datetime

from hivegraph.events import Emitter
from hivegraph.task import Task


def test_emitter_writes_one_flushed_line_per_event():
    out = io.StringIO()
    e = Emitter(out)
    t0 = datetime(2026, 10, 1, 12, 0, 0, tzinfo=UTC)
    e.node("plan", t0, t0)
    e.step("tool", "Bash", "{}", "ok", False, t0, t0)
    e.usage("m", 3, 1)
    e.result("completed", "", "done", "", "th", ["a.go"], True)
    lines = [json.loads(line) for line in out.getvalue().splitlines()]
    assert [line["type"] for line in lines] == ["node", "step", "usage", "result"]
    assert lines[1]["start"] == "2026-10-01T12:00:00Z"
    assert lines[3] == {
        "type": "result", "status": "completed", "stop_cause": "", "summary": "done", "question": "",
        "resume_token": "th", "changed_files": ["a.go"], "steps_traced": True,
    }


def test_task_from_json_fills_defaults():
    t = Task.from_json('{"prompt": "x", "workspace": "/w", "chat_model": {"provider": "deepseek", "model": "m"}}')
    assert t.prompt == "x" and t.checks == [] and t.max_fix_rounds == 3 and t.max_review_rounds == 1
    assert t.code_with == "claude" and t.chat_model.model == "m" and t.chat_model.api_key_env == ""
```

- [ ] **Step 3: Implement `events.py`, `task.py` and `cli.py`.**

```python
# events.py
"""JSONL events on stdout: the protocol between hivegraph and the Go worker."""

import json
import sys
from datetime import UTC, datetime
from typing import TextIO


def stamp(t: datetime) -> str:
    return t.astimezone(UTC).isoformat().replace("+00:00", "Z")


class Emitter:
    def __init__(self, out: TextIO | None = None):
        self.out = out or sys.stdout

    def _write(self, event: dict) -> None:
        self.out.write(json.dumps(event) + "\n")
        self.out.flush()

    def node(self, name: str, start: datetime, end: datetime) -> None:
        self._write({"type": "node", "name": name, "start": stamp(start), "end": stamp(end)})

    def step(self, kind, name, input, output, is_error, start: datetime, end: datetime | None) -> None:
        self._write({
            "type": "step", "kind": kind, "name": name, "input": input, "output": output,
            "is_error": is_error, "start": stamp(start), "end": stamp(end) if end else "",
        })

    def usage(self, model: str, input_tokens: int, output_tokens: int, cost_usd: float = 0.0) -> None:
        self._write({"type": "usage", "model": model, "input_tokens": input_tokens,
                     "output_tokens": output_tokens, "cost_usd": cost_usd})

    def result(self, status, stop_cause, summary, question, resume_token, changed_files, steps_traced) -> None:
        self._write({
            "type": "result", "status": status, "stop_cause": stop_cause, "summary": summary,
            "question": question, "resume_token": resume_token, "changed_files": list(changed_files),
            "steps_traced": steps_traced,
        })
```

```python
# task.py
"""The task the Go worker sends on stdin."""

import json
from dataclasses import dataclass, field, fields


@dataclass
class ChatModel:
    provider: str = ""
    model: str = ""
    base_url: str = ""
    api_key_env: str = ""


@dataclass
class Task:
    ticket: str = ""
    prompt: str = ""
    workspace: str = "."
    resume_token: str = ""
    step_budget: int = 0
    code_with: str = "claude"
    model: str = ""
    base_ref: str = ""
    git_dir: str = ""
    hivedispatch: str = "hivedispatch"
    worker_config: str = ""
    checks: list[str] = field(default_factory=list)
    check_timeout_seconds: int = 600
    max_fix_rounds: int = 3
    max_review_rounds: int = 1
    guidance: str = ""
    path: list[str] = field(default_factory=list)
    chat_model: ChatModel = field(default_factory=ChatModel)

    @classmethod
    def from_json(cls, raw: str) -> "Task":
        data = json.loads(raw)
        known = {f.name for f in fields(cls)}
        kwargs = {k: v for k, v in data.items() if k in known and v is not None}
        cm = kwargs.pop("chat_model", {}) or {}
        cm_known = {f.name for f in fields(ChatModel)}
        t = cls(**kwargs)
        t.chat_model = ChatModel(**{k: v for k, v in cm.items() if k in cm_known})
        return t
```

```python
# cli.py
"""hivegraph run: read a task on stdin, run the workflow, write events."""

import argparse
import signal
import sys

from .events import Emitter
from .task import Task


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="hivegraph")
    sub = parser.add_subparsers(dest="cmd", required=True)
    sub.add_parser("run", help="run the workflow for the task on stdin")
    parser.parse_args(argv)
    # SIGTERM from the worker becomes KeyboardInterrupt-like shutdown so the
    # running agent-run child gets the signal too and cleans up its CLI.
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(143))
    emitter = Emitter()
    try:
        task = Task.from_json(sys.stdin.read())
    except Exception as exc:  # noqa: BLE001 - any bad input is a failed run
        emitter.result("failed", "error", f"hivegraph: bad task: {exc}", "", "", [], False)
        return 0
    from .graph import run_task  # imported late: langgraph is slow to import

    return run_task(task, emitter)


if __name__ == "__main__":
    sys.exit(main())
```

    `__init__.py` holds only a docstring.
- [ ] **Step 4: Set up the venv and run the tests.**

```sh
python3 -m venv graph/.venv && graph/.venv/bin/pip install -q -e './graph[dev]'
graph/.venv/bin/pytest graph -q -k "events or task"
graph/.venv/bin/ruff check graph && graph/.venv/bin/ruff format --check graph
```

  Expected: both tests pass. The `cli` import of `.graph` happens only at run time.
- [ ] **Step 5: Commit.** `feat(graph): hivegraph package skeleton, task and event protocol`.

### Task 8: The graph: nodes, routing, checks, the agent-run client, the chat model

**Files:**
- Create: `graph/hivegraph/agentrun.py`, `graph/hivegraph/checks.py`, `graph/hivegraph/model.py`, `graph/hivegraph/prompts.py`, `graph/hivegraph/graph.py`
- Create tests: `graph/tests/test_checks.py`, `graph/tests/test_agentrun.py`, `graph/tests/test_graph.py`, plus `graph/tests/fake_agent_run.py` (a script that stands in for `hivedispatch agent-run`)

**Interfaces produced:**
- `agentrun.run_agent(task, prompt, resume) -> AgentResult` (a dataclass of the Go JSON fields). It runs `[task.hivedispatch, "agent-run", "-executor", task.code_with, "-workspace", task.workspace, "-config", task.worker_config, "-model", task.model, "-resume", resume, "-step-budget", str(task.step_budget)]`, with the prompt on stdin. A non-zero exit or unparsable output gives `AgentResult(status="failed", stop_cause="error", summary="agent-run: <stderr tail>")`.
- `checks.run_checks(task) -> CheckReport(passed: bool, failures: list[CheckFailure(command, output)])`. Each check runs through `subprocess.run(["sh","-c",cmd], cwd=workspace, timeout=task.check_timeout_seconds, env=PATH-extended)`. A timeout is a failure whose output says `timed out after Ns`. Output is the last 8 KiB of the combined stdout and stderr.
- `model.chat_model(task) -> BaseChatModel`: `ChatOpenAI(model=..., base_url=..., api_key=os.environ.get(api_key_env) or "unused", temperature=0)`.
- `graph.build(task, emitter, model, agent=run_agent, checks=run_checks) -> CompiledGraph` and `graph.run_task(task, emitter, model=None, agent=run_agent, checks=run_checks, checkpointer=None) -> int`, which always emits exactly one `result` and returns 0.

**State and routing** (in `graph.py`):

```python
class State(TypedDict, total=False):
    prompt: str            # what the human asked: the ticket, or the reply on resume
    plan: str
    cli_session: str
    fix_round: int
    review_round: int
    feedback: str          # what the next code step must address
    checks_passed: bool
    checks_report: str
    review_findings: str
    review_skipped: str
    summary: str
    status: str            # completed | needs_input | failed
    stop_cause: str
    question: str
    changed_files: list[str]
```

Routing:
- `START → plan`, or `→ code` when `state["plan"]` is already set (a resumed thread).
- `code → checks` when its status is completed, otherwise `→ finish`.
- `checks → review` when passed. `→ code` when failed and `fix_round < max_fix_rounds`. Otherwise `→ finish`.
- `review → code` on `changes` when `review_round < max_review_rounds`, otherwise `→ finish`.
- `finish → END`.

- [ ] **Step 1: The fake agent-run** (`tests/fake_agent_run.py`). It reads scripted results from `$FAKE_AGENT_SCRIPT`, a JSON list consumed in order through a counter file, appends each call's argv and prompt to `$FAKE_AGENT_LOG`, and prints the next result.

```python
#!/usr/bin/env python3
import json
import os
import sys

script = json.load(open(os.environ["FAKE_AGENT_SCRIPT"]))
counter = os.environ["FAKE_AGENT_SCRIPT"] + ".n"
n = int(open(counter).read()) if os.path.exists(counter) else 0
open(counter, "w").write(str(n + 1))
with open(os.environ["FAKE_AGENT_LOG"], "a") as log:
    log.write(json.dumps({"argv": sys.argv[1:], "prompt": sys.stdin.read()}) + "\n")
item = script[min(n, len(script) - 1)]
if item == "garbage":
    print("not json")
    sys.exit(0)
if item == "crash":
    print("agent-run: cannot start claude", file=sys.stderr)
    sys.exit(1)
print(json.dumps(item))
```

- [ ] **Step 2: Failing tests.**

`tests/conftest.py` holds the shared fixtures:

```python
import json
import os
import sys
from pathlib import Path

import pytest

from hivegraph.task import ChatModel, Task

HERE = Path(__file__).parent


def ok(summary="did it", session="s1", **kw):
    r = {"status": "completed", "stop_cause": "", "summary": summary, "question": "", "resume_token": session,
         "changed_files": ["a.go"], "retry_after": "",
         "steps": [{"kind": "tool", "name": "Bash", "input": "{}", "output": "ok", "is_error": False,
                    "start": "2026-10-01T12:00:00Z", "end": "2026-10-01T12:00:01Z"}],
         "usage": {"model": "claude-opus-5-5", "input_tokens": 10, "output_tokens": 2, "cost_usd": 0.01}}
    r.update(kw)
    return r


@pytest.fixture
def agent_script(tmp_path, monkeypatch):
    """Returns set(results) -> Task wired to the fake agent-run."""
    log = tmp_path / "calls.jsonl"
    script = tmp_path / "script.json"
    fake = tmp_path / "hivedispatch"
    fake.write_text(f"#!/bin/sh\nexec {sys.executable} {HERE / 'fake_agent_run.py'} \"$@\"\n")
    fake.chmod(0o755)
    monkeypatch.setenv("FAKE_AGENT_SCRIPT", str(script))
    monkeypatch.setenv("FAKE_AGENT_LOG", str(log))

    def set_(results, **task_kw):
        script.write_text(json.dumps(results))
        kw = dict(prompt="Add a flag", workspace=str(tmp_path), hivedispatch=str(fake), base_ref="HEAD",
                  git_dir=str(tmp_path / ".git"), chat_model=ChatModel(provider="fake", model="fake"))
        kw.update(task_kw)
        return Task(**kw)

    set_.calls = lambda: [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []
    return set_


def events(out):
    return [json.loads(line) for line in out.getvalue().splitlines()]


@pytest.fixture(autouse=True)
def no_tracing(monkeypatch):
    for k in list(os.environ):
        if k.startswith(("LANGSMITH_", "LANGCHAIN_")):
            monkeypatch.delenv(k)
```

`tests/test_checks.py`:

```python
from hivegraph.checks import run_checks
from hivegraph.task import Task


def test_checks_pass_fail_and_timeout(tmp_path):
    t = Task(workspace=str(tmp_path), checks=["true"])
    assert run_checks(t).passed
    t = Task(workspace=str(tmp_path), checks=["echo out; echo err >&2; exit 2", "true"])
    r = run_checks(t)
    assert not r.passed and len(r.failures) == 1 and "out" in r.failures[0].output and "err" in r.failures[0].output
    t = Task(workspace=str(tmp_path), checks=["sleep 5"], check_timeout_seconds=1)
    r = run_checks(t)
    assert not r.passed and "timed out after 1s" in r.failures[0].output


def test_checks_output_is_the_tail(tmp_path):
    t = Task(workspace=str(tmp_path), checks=["head -c 20000 /dev/zero | tr '\\0' a; echo END; exit 1"])
    out = run_checks(t).failures[0].output
    assert len(out) <= 8 * 1024 + 20 and out.rstrip().endswith("END")


def test_path_is_extended(tmp_path):
    bindir = tmp_path / "bin"
    bindir.mkdir()
    tool = bindir / "mytool"
    tool.write_text("#!/bin/sh\nexit 0\n")
    tool.chmod(0o755)
    assert run_checks(Task(workspace=str(tmp_path), checks=["mytool"], path=[str(bindir)])).passed
```

`tests/test_agentrun.py`:

```python
from conftest import ok

from hivegraph.agentrun import run_agent


def test_parses_result_and_passes_flags(agent_script):
    task = agent_script([ok(session="s9")], code_with="codex", model="m", step_budget=7)
    r = run_agent(task, "prompt text", resume="s1")
    assert r.status == "completed" and r.resume_token == "s9" and r.steps[0]["name"] == "Bash"
    call = agent_script.calls()[0]
    assert call["prompt"] == "prompt text"
    argv = call["argv"]
    assert argv[0] == "agent-run" and argv[argv.index("-executor") + 1] == "codex"
    assert argv[argv.index("-resume") + 1] == "s1" and argv[argv.index("-step-budget") + 1] == "7"


def test_garbage_and_crash_are_failures(agent_script):
    task = agent_script(["garbage"])
    assert run_agent(task, "p", resume="").status == "failed"
    task = agent_script(["crash"])
    r = run_agent(task, "p", resume="")
    assert r.status == "failed" and "cannot start claude" in r.summary
```

`tests/test_graph.py` uses `GenericFakeChatModel` from `langchain_core.language_models.fake_chat_models` with `messages=iter([AIMessage(...), ...])`:

```python
import io
import json

from conftest import events, ok
from langchain_core.language_models.fake_chat_models import GenericFakeChatModel
from langchain_core.messages import AIMessage

from hivegraph.checks import CheckFailure, CheckReport
from hivegraph.events import Emitter
from hivegraph.graph import run_task


def model(*texts):
    return GenericFakeChatModel(messages=iter([AIMessage(content=t) for t in texts]))


APPROVE = json.dumps({"verdict": "approve", "findings": ""})


def checks_seq(*passes):
    it = iter(passes)

    def run(task):
        p = next(it)
        return CheckReport(passed=p, failures=[] if p else [CheckFailure("go test ./...", "FAIL x")])

    return run


def result_of(out):
    rs = [e for e in events(out) if e["type"] == "result"]
    assert len(rs) == 1
    return rs[0]


def test_happy_path(agent_script):
    task = agent_script([ok()], checks=["go test ./..."])
    out = io.StringIO()
    run_task(task, Emitter(out), model=model("1. do it", APPROVE), checks=checks_seq(True))
    r = result_of(out)
    assert r["status"] == "completed" and "did it" in r["summary"] and r["changed_files"] == ["a.go"]
    assert r["resume_token"]  # the thread id
    nodes = [e["name"] for e in events(out) if e["type"] == "node"]
    assert nodes == ["plan", "code", "checks", "review", "finish"]
    assert any(e["type"] == "step" and e["name"] == "Bash" for e in events(out))
    assert any(e["type"] == "usage" and e["input_tokens"] == 10 for e in events(out))
    assert "1. do it" in agent_script.calls()[0]["prompt"]


def test_fix_loop_then_green(agent_script):
    task = agent_script([ok(session="s1"), ok(session="s2")], checks=["go test ./..."])
    out = io.StringIO()
    run_task(task, Emitter(out), model=model("plan", APPROVE), checks=checks_seq(False, True))
    calls = agent_script.calls()
    assert len(calls) == 2 and "FAIL x" in calls[1]["prompt"]
    assert calls[1]["argv"][calls[1]["argv"].index("-resume") + 1] == "s1"
    assert result_of(out)["status"] == "completed"


def test_gives_up_after_max_fix_rounds_but_completes(agent_script):
    task = agent_script([ok()], checks=["go test ./..."], max_fix_rounds=1)
    out = io.StringIO()
    run_task(task, Emitter(out), model=model("plan", APPROVE), checks=checks_seq(False, False))
    r = result_of(out)
    assert r["status"] == "completed" and "still failing" in r["summary"] and "go test ./..." in r["summary"]
    assert len(agent_script.calls()) == 2


def test_review_requests_changes_once(agent_script):
    task = agent_script([ok(), ok(summary="fixed")])
    changes = json.dumps({"verdict": "changes", "findings": "rename foo"})
    out = io.StringIO()
    run_task(task, Emitter(out), model=model("plan", changes, APPROVE), checks=checks_seq(True, True))
    calls = agent_script.calls()
    assert len(calls) == 2 and "rename foo" in calls[1]["prompt"]
    assert "fixed" in result_of(out)["summary"]


def test_needs_input_stops(agent_script):
    task = agent_script([ok(status="needs_input", question="Which DB?", summary="asked")])
    out = io.StringIO()
    run_task(task, Emitter(out), model=model("plan"), checks=checks_seq())
    r = result_of(out)
    assert r["status"] == "needs_input" and r["question"] == "Which DB?"


def test_cli_failure_stops_with_its_cause(agent_script):
    task = agent_script([ok(status="failed", stop_cause="budget", summary="quota")])
    out = io.StringIO()
    run_task(task, Emitter(out), model=model("plan"), checks=checks_seq())
    r = result_of(out)
    assert r["status"] == "failed" and r["stop_cause"] == "budget"


class Broken(GenericFakeChatModel):
    def _generate(self, *a, **kw):
        raise RuntimeError("model down")


def test_model_failures_never_stop_the_run(agent_script):
    task = agent_script([ok()])
    out = io.StringIO()
    run_task(task, Emitter(out), model=Broken(messages=iter([])), checks=checks_seq(True))
    r = result_of(out)
    assert r["status"] == "completed" and "review skipped" in r["summary"]
    assert "Add a flag" in agent_script.calls()[0]["prompt"]


def test_unparsable_review_counts_as_approve(agent_script):
    task = agent_script([ok()])
    out = io.StringIO()
    run_task(task, Emitter(out), model=model("plan", "looks great!"), checks=checks_seq(True))
    assert len(agent_script.calls()) == 1 and result_of(out)["status"] == "completed"


def test_crash_inside_a_node_is_a_failed_result(agent_script):
    task = agent_script([ok()])

    def boom(task):
        raise ValueError("checks exploded")

    out = io.StringIO()
    run_task(task, Emitter(out), model=model("plan"), checks=boom)
    r = result_of(out)
    assert r["status"] == "failed" and r["stop_cause"] == "error" and "checks exploded" in r["summary"]
```

- [ ] **Step 3: Run them and see them fail.** Run `graph/.venv/bin/pytest graph -q`. Expected: ImportError for `hivegraph.checks` and the other new modules.
- [ ] **Step 4: Implement `checks.py`.**

```python
"""The repository's checks, run after each code step."""

import os
import subprocess
from dataclasses import dataclass, field

TAIL = 8 * 1024


@dataclass
class CheckFailure:
    command: str
    output: str


@dataclass
class CheckReport:
    passed: bool
    failures: list[CheckFailure] = field(default_factory=list)

    def describe(self) -> str:
        return "\n\n".join(f"$ {f.command}\n{f.output}" for f in self.failures)


def _env(task) -> dict[str, str]:
    env = dict(os.environ)
    if task.path:
        env["PATH"] = os.pathsep.join([*task.path, env.get("PATH", "")])
    return env


def run_checks(task) -> CheckReport:
    failures = []
    for cmd in task.checks:
        try:
            p = subprocess.run(["sh", "-c", cmd], cwd=task.workspace, env=_env(task), capture_output=True,
                               text=True, timeout=task.check_timeout_seconds, errors="replace")
        except subprocess.TimeoutExpired as exc:
            out = (exc.stdout or "") + (exc.stderr or "")
            if isinstance(out, bytes):
                out = out.decode(errors="replace")
            failures.append(CheckFailure(cmd, (out[-TAIL:] + f"\n(timed out after {task.check_timeout_seconds}s)").strip()))
            continue
        if p.returncode != 0:
            out = (p.stdout + p.stderr)[-TAIL:]
            failures.append(CheckFailure(cmd, (out + f"\n(exit {p.returncode})").strip()))
    return CheckReport(passed=not failures, failures=failures)
```

- [ ] **Step 5: Implement `agentrun.py`.**

```python
"""The code node's CLI: `hivedispatch agent-run`, which runs Claude Code or Codex."""

import json
import subprocess
from dataclasses import dataclass, field


@dataclass
class AgentResult:
    status: str = "failed"
    stop_cause: str = "error"
    summary: str = ""
    question: str = ""
    resume_token: str = ""
    changed_files: list[str] = field(default_factory=list)
    retry_after: str = ""
    steps: list[dict] = field(default_factory=list)
    usage: dict = field(default_factory=dict)


def run_agent(task, prompt: str, resume: str) -> AgentResult:
    argv = [task.hivedispatch, "agent-run", "-executor", task.code_with, "-workspace", task.workspace,
            "-step-budget", str(task.step_budget)]
    if task.worker_config:
        argv += ["-config", task.worker_config]
    if task.model:
        argv += ["-model", task.model]
    if resume:
        argv += ["-resume", resume]
    try:
        p = subprocess.run(argv, input=prompt, capture_output=True, text=True, errors="replace")
    except OSError as exc:
        return AgentResult(summary=f"agent-run: {exc}")
    if p.returncode != 0:
        return AgentResult(summary=f"agent-run exited {p.returncode}: {p.stderr.strip()[-1000:]}")
    try:
        data = json.loads(p.stdout)
    except json.JSONDecodeError:
        return AgentResult(summary=f"agent-run printed no result: {(p.stdout + p.stderr).strip()[-1000:]}")
    known = AgentResult.__dataclass_fields__
    return AgentResult(**{k: v for k, v in data.items() if k in known and v is not None})
```

- [ ] **Step 6: Implement `model.py` and `prompts.py`.**

```python
# model.py
"""The chat model for the plan and review nodes: any OpenAI-compatible API."""

import os

from langchain_openai import ChatOpenAI


def chat_model(task):
    cm = task.chat_model
    key = os.environ.get(cm.api_key_env, "") if cm.api_key_env else ""
    return ChatOpenAI(model=cm.model, base_url=cm.base_url or None, api_key=key or "unused", temperature=0)
```

```python
# prompts.py
"""What each node says."""

PLAN = """You are planning a code change another agent will make. Read the ticket and reply with a short,
numbered plan (at most 8 steps) naming the files to touch and how to verify the change. No preamble.

Ticket:
{prompt}

Repository guidance:
{guidance}
"""

CODE_FIRST = """{prompt}

## Plan

{plan}
"""

CODE_FIX = """The repository's checks fail after your change. Fix the code so they pass; do not weaken the checks.

{report}
"""

CODE_REVIEW = """A review of your change asks for the following. Address each point.

{findings}
"""

REVIEW = """Review this diff against the plan. Reply with JSON only:
{{"verdict": "approve" | "changes", "findings": "what must change, or empty"}}
Ask for changes only for real defects or missed plan steps, not style.

Plan:
{plan}

Diff:
{diff}
"""
```

- [ ] **Step 7: Implement `graph.py`.**

```python
"""The workflow: plan → code → checks ⇄ fix → review ⇄ fix → finish."""

import json
import subprocess
import uuid
from datetime import UTC, datetime
from typing import TypedDict

from langgraph.graph import END, START, StateGraph

from . import prompts
from .agentrun import run_agent
from .checks import run_checks

MAX_DIFF = 100 * 1024


class State(TypedDict, total=False):
    prompt: str
    plan: str
    cli_session: str
    fix_round: int
    review_round: int
    feedback: str
    checks_passed: bool
    checks_report: str
    review_findings: str
    review_skipped: str
    summary: str
    status: str
    stop_cause: str
    question: str
    changed_files: list[str]


def now() -> datetime:
    return datetime.now(UTC)


def parse_time(s: str) -> datetime:
    try:
        return datetime.fromisoformat(s.replace("Z", "+00:00"))
    except (ValueError, AttributeError):
        return now()


def _text(msg) -> str:
    c = msg.content
    return c if isinstance(c, str) else "".join(p.get("text", "") for p in c if isinstance(p, dict))


def parse_review(text: str) -> tuple[str, str]:
    """verdict, findings. Anything unparsable is an approval: the cheap model
    never blocks a run."""
    start, end = text.find("{"), text.rfind("}")
    if start < 0 or end < start:
        return "approve", ""
    try:
        data = json.loads(text[start : end + 1])
    except json.JSONDecodeError:
        return "approve", ""
    verdict = data.get("verdict", "approve")
    findings = str(data.get("findings", "") or "")
    if verdict != "changes" or not findings.strip():
        return "approve", ""
    return "changes", findings


def git_diff(task) -> str:
    p = subprocess.run(["git", "-C", task.workspace, "diff", task.base_ref], capture_output=True, text=True,
                       errors="replace")
    diff = p.stdout
    if len(diff) > MAX_DIFF:
        diff = diff[:MAX_DIFF] + "\n…[diff truncated]"
    return diff


def build(task, emitter, model, agent=run_agent, checks=run_checks):
    def timed(name, fn):
        def node(state: State) -> State:
            start = now()
            out = fn(state)
            emitter.node(name, start, now())
            return out

        return node

    def plan(state: State) -> State:
        try:
            msg = model.invoke(prompts.PLAN.format(prompt=state["prompt"], guidance=task.guidance or "(none)"))
            return {"plan": _text(msg).strip() or "(no plan)"}
        except Exception as exc:  # noqa: BLE001 - the plan is optional
            return {"plan": f"(no plan: the planning model failed: {exc})"}

    def code(state: State) -> State:
        feedback = state.get("feedback", "")
        prompt = feedback or prompts.CODE_FIRST.format(prompt=state["prompt"], plan=state.get("plan", ""))
        r = agent(task, prompt, state.get("cli_session", ""))
        for s in r.steps:
            emitter.step(s.get("kind", "tool"), s.get("name", ""), s.get("input", ""), s.get("output", ""),
                         bool(s.get("is_error")), parse_time(s.get("start", "")),
                         parse_time(s["end"]) if s.get("end") else None)
        u = r.usage or {}
        if u.get("input_tokens") or u.get("output_tokens") or u.get("cost_usd"):
            emitter.usage(u.get("model", ""), int(u.get("input_tokens", 0)), int(u.get("output_tokens", 0)),
                          float(u.get("cost_usd", 0.0)))
        changed = sorted(set(state.get("changed_files", [])) | set(r.changed_files or []))
        return {"cli_session": r.resume_token or state.get("cli_session", ""), "status": r.status,
                "stop_cause": r.stop_cause, "question": r.question, "summary": r.summary,
                "changed_files": changed, "feedback": ""}

    def run_checks_node(state: State) -> State:
        report = checks(task)
        out: State = {"checks_passed": report.passed, "checks_report": report.describe()}
        if not report.passed and state.get("fix_round", 0) < task.max_fix_rounds:
            out["fix_round"] = state.get("fix_round", 0) + 1
            out["feedback"] = prompts.CODE_FIX.format(report=report.describe())
        return out

    def review(state: State) -> State:
        try:
            msg = model.invoke(prompts.REVIEW.format(plan=state.get("plan", ""), diff=git_diff(task)))
        except Exception as exc:  # noqa: BLE001 - the review is optional
            return {"review_skipped": f"review skipped: the review model failed: {exc}", "review_findings": ""}
        verdict, findings = parse_review(_text(msg))
        out: State = {"review_findings": findings if verdict == "changes" else ""}
        if verdict == "changes" and state.get("review_round", 0) < task.max_review_rounds:
            out["review_round"] = state.get("review_round", 0) + 1
            out["feedback"] = prompts.CODE_REVIEW.format(findings=findings)
        return out

    def finish(state: State) -> State:
        if state.get("status") != "completed":
            return {}
        parts = [state.get("summary", "").strip() or "Run completed."]
        if task.checks and not state.get("checks_passed", True):
            parts.append("Checks still failing after the fix rounds:\n\n" + state.get("checks_report", ""))
        if state.get("review_findings"):
            parts.append("Unresolved review findings:\n\n" + state["review_findings"])
        if state.get("review_skipped"):
            parts.append(state["review_skipped"] + ".")
        return {"summary": "\n\n".join(parts)}

    def after_start(state: State) -> str:
        return "code" if state.get("plan") else "plan"

    def after_code(state: State) -> str:
        return "checks" if state.get("status") == "completed" else "finish"

    def after_checks(state: State) -> str:
        if state.get("checks_passed"):
            return "review"
        return "code" if state.get("feedback") else "finish"

    def after_review(state: State) -> str:
        return "code" if state.get("feedback") else "finish"

    g = StateGraph(State)
    g.add_node("plan", timed("plan", plan))
    g.add_node("code", timed("code", code))
    g.add_node("checks", timed("checks", run_checks_node))
    g.add_node("review", timed("review", review))
    g.add_node("finish", timed("finish", finish))
    g.add_conditional_edges(START, after_start, ["plan", "code"])
    g.add_edge("plan", "code")
    g.add_conditional_edges("code", after_code, ["checks", "finish"])
    g.add_conditional_edges("checks", after_checks, ["review", "code", "finish"])
    g.add_conditional_edges("review", after_review, ["code", "finish"])
    g.add_edge("finish", END)
    return g


def run_task(task, emitter, model=None, agent=run_agent, checks=run_checks, checkpointer=None) -> int:
    thread = task.resume_token or str(uuid.uuid4())
    try:
        if model is None:
            from .model import chat_model

            model = chat_model(task)
        graph = build(task, emitter, model, agent, checks).compile(checkpointer=checkpointer)
        config = {"configurable": {"thread_id": thread}, "recursion_limit": 100}
        # A new turn on a thread: the prompt is the new message; counters reset.
        inputs: State = {"prompt": task.prompt, "fix_round": 0, "review_round": 0, "feedback": "",
                         "status": "", "review_findings": "", "review_skipped": ""}
        final = graph.invoke(inputs, config)
    except Exception as exc:  # noqa: BLE001 - every failure is reported as a result
        emitter.result("failed", "error", f"hivegraph: {type(exc).__name__}: {exc}", "", thread, [], False)
        return 0
    status = final.get("status") or "failed"
    emitter.result(status, final.get("stop_cause", "") if status == "failed" else "", final.get("summary", ""),
                   final.get("question", ""), thread, final.get("changed_files", []), False)
    return 0
```

  When the plan node is skipped on a resumed thread, the first code prompt must be the reply, not the ticket again plus the plan. So `after_start` routes to `code` with `feedback` unset, and `code` builds `CODE_FIRST` from `state["prompt"]`, which is the reply, and the stored plan. The CLI session is resumed, so it has the earlier context. Task 9 tests this.
- [ ] **Step 8: Run the tests and see them pass.** Run `graph/.venv/bin/pytest graph -q && graph/.venv/bin/ruff check graph && graph/.venv/bin/ruff format --check graph`. Run `ruff format graph` first if needed.
- [ ] **Step 9: Commit.** `feat(graph): plan, code, checks, fix and review nodes with routing`.

### Task 9: Checkpoints, resume, LangSmith parent, and the real entry point

**Files:**
- Modify: `graph/hivegraph/graph.py` (`run_task` opens a `SqliteSaver` at `<git_dir>/hivegraph/checkpoints.sqlite` when no checkpointer is passed, wraps the invoke in `tracing_context(parent=os.environ.get("LANGSMITH_PARENT"))` when that is set, sets `steps_traced` when tracing is on, and calls `wait_for_all_tracers()` before returning)
- Test: `graph/tests/test_resume.py`, `graph/tests/test_cli.py`

- [ ] **Step 1: Failing tests.**

```python
# tests/test_resume.py
import io
import json

from conftest import events, ok
from langchain_core.language_models.fake_chat_models import GenericFakeChatModel
from langchain_core.messages import AIMessage
from langgraph.checkpoint.memory import InMemorySaver

from hivegraph.checks import CheckReport
from hivegraph.events import Emitter
from hivegraph.graph import run_task

APPROVE = json.dumps({"verdict": "approve", "findings": ""})


def test_resume_skips_plan_and_resumes_the_cli(agent_script):
    saver = InMemorySaver()
    task = agent_script([ok(status="needs_input", question="Which DB?", session="s1"), ok(session="s2")])
    out = io.StringIO()
    run_task(task, Emitter(out), model=GenericFakeChatModel(messages=iter([AIMessage("the plan")])),
             checks=lambda t: CheckReport(True), checkpointer=saver)
    first = [e for e in events(out) if e["type"] == "result"][0]
    assert first["status"] == "needs_input"

    task.resume_token, task.prompt = first["resume_token"], "Use Postgres."
    out2 = io.StringIO()
    run_task(task, Emitter(out2), model=GenericFakeChatModel(messages=iter([AIMessage(APPROVE)])),
             checks=lambda t: CheckReport(True), checkpointer=saver)
    nodes = [e["name"] for e in events(out2) if e["type"] == "node"]
    assert nodes[0] == "code" and "plan" not in nodes
    call = agent_script.calls()[1]
    assert call["argv"][call["argv"].index("-resume") + 1] == "s1"
    assert "Use Postgres." in call["prompt"] and "the plan" in call["prompt"]
    assert [e for e in events(out2) if e["type"] == "result"][0]["status"] == "completed"


def test_sqlite_checkpoints_land_in_the_git_dir(agent_script, tmp_path):
    task = agent_script([ok()], git_dir=str(tmp_path / "gitdir"))
    run_task(task, Emitter(io.StringIO()), model=GenericFakeChatModel(messages=iter([AIMessage("p"), AIMessage(APPROVE)])),
             checks=lambda t: CheckReport(True))
    assert (tmp_path / "gitdir" / "hivegraph" / "checkpoints.sqlite").exists()
```

```python
# tests/test_cli.py
import json
import subprocess
import sys


def test_bad_task_is_a_failed_result():
    p = subprocess.run([sys.executable, "-m", "hivegraph.cli", "run"], input="not json", capture_output=True, text=True)
    assert p.returncode == 0
    r = json.loads(p.stdout.strip().splitlines()[-1])
    assert r["type"] == "result" and r["status"] == "failed" and "bad task" in r["summary"]
```

- [ ] **Step 2: Run them and see them fail.** Expected: there is no sqlite file, because `run_task` has no default checkpointer yet.
- [ ] **Step 3: Implement.** In `graph.py`, `run_task` becomes:

```python
def _tracing_enabled() -> bool:
    try:
        from langsmith.utils import tracing_is_enabled

        return bool(tracing_is_enabled())
    except Exception:  # noqa: BLE001
        return False


def run_task(task, emitter, model=None, agent=run_agent, checks=run_checks, checkpointer=None) -> int:
    thread = task.resume_token or str(uuid.uuid4())
    traced = _tracing_enabled()
    try:
        with contextlib.ExitStack() as stack:
            if checkpointer is None and task.git_dir:
                from langgraph.checkpoint.sqlite import SqliteSaver

                path = os.path.join(task.git_dir, "hivegraph", "checkpoints.sqlite")
                os.makedirs(os.path.dirname(path), exist_ok=True)
                checkpointer = stack.enter_context(SqliteSaver.from_conn_string(path))
            parent = os.environ.get("LANGSMITH_PARENT")
            if traced and parent:
                from langsmith.run_helpers import tracing_context

                stack.enter_context(tracing_context(parent=parent))
            if model is None:
                from .model import chat_model

                model = chat_model(task)
            graph = build(task, emitter, model, agent, checks).compile(checkpointer=checkpointer)
            config = {"configurable": {"thread_id": thread}, "recursion_limit": 100,
                      "run_name": f"hivegraph {task.ticket}".strip(), "metadata": {"ticket": task.ticket}}
            inputs: State = {"prompt": task.prompt, "fix_round": 0, "review_round": 0, "feedback": "",
                             "status": "", "review_findings": "", "review_skipped": ""}
            final = graph.invoke(inputs, config)
    except Exception as exc:  # noqa: BLE001 - every failure is reported as a result
        emitter.result("failed", "error", f"hivegraph: {type(exc).__name__}: {exc}", "", thread, [], False)
        return 0
    finally:
        if traced:
            from langchain_core.tracers.langchain import wait_for_all_tracers

            wait_for_all_tracers()
    status = final.get("status") or "failed"
    emitter.result(status, final.get("stop_cause", "") if status == "failed" else "", final.get("summary", ""),
                   final.get("question", ""), thread, final.get("changed_files", []), traced)
    return 0
```

  Add `import contextlib` and `import os`.
- [ ] **Step 4: Run all the Python tests and see them pass.** Run `graph/.venv/bin/pytest graph -q`, then ruff check and ruff format check.
- [ ] **Step 5: Commit.** `feat(graph): SQLite checkpoints for resume, LangSmith parent from the worker`.

### Task 10: The web UI agent form and config fields

**Files:**
- Modify: `web/src/components/AgentsPanel.vue` (`executors` gets `langgraph`; a `code_with` select shown when the executor is langgraph; `openNew` defaults `code_with: ''`; the table shows `langgraph (claude)`; the help text)
- Modify: `web/src/schemas.js`:
  - the worker schema gets a "Graph workflows" section with `graph.binary`, `graph.provider` (select: '', openai, deepseek, huggingface, ollama), `graph.model`, `graph.base_url` and `graph.api_key_env`;
  - the policy schema gets a "Graph workflows (langgraph agents)" section with `checks` (list) and `graph.max_fix_rounds` / `graph.max_review_rounds` (number, placeholders 3 and 1).
- Rebuild: `cd web && npm ci && npm run build`, then commit `internal/web/dist`.
- Test: `internal/web/server_test.go`, if an agents round-trip test exists; extend it to save and load `code_with`. Otherwise `config.ParseAgents` from Task 1 covers it.

- [ ] **Step 1: Make the UI edits.** In the agent form:

```vue
        <label v-if="modal.form.executor === 'langgraph'" class="field">
          <span class="label">Code with</span>
          <select v-model="modal.form.code_with">
            <option value="">claude (default)</option>
            <option value="claude">claude</option>
            <option value="codex">codex</option>
            <option value="fake">fake</option>
          </select>
          <span class="help muted">The CLI the workflow's code steps run. Planning and self-review use the chat model under Configuration → Graph workflows.</span>
        </label>
```

  Before saving, drop `code_with` when the executor is not langgraph: `if (form.executor !== 'langgraph') delete form.code_with`.
  Executor help: "claude runs Claude Code, codex runs Codex; langgraph runs the plan → code → checks → review workflow; fake does nothing (for trying the pipeline)."
- [ ] **Step 2: Build.** Run `cd web && npm ci && npm run build`. Expected: a successful build, with `internal/web/dist` changed.
- [ ] **Step 3: Run the Go web tests.** Run `go test -race ./internal/web/`.
- [ ] **Step 4: Commit.** `feat(web): langgraph agents and graph settings in the website`.

### Task 11: CI, docs, decisions

**Files:**
- Modify: `.github/workflows/ci.yml` (add a `graph` job)
- Modify: `AGENTS.md`, `CONTRIBUTING.md` (a Python section), `docs/config.md` (`graph:`, `checks`, `graph.*`, `code_with`, `agent-run` in the commands table as internal), `README.md` (a "Graph workflows" section), `docs/decisions.md` (a new entry), and the spec's status line
- Modify: `internal/config/starter.go` (a commented `graph:` example under the supervisor example), if the starter config lists optional blocks

- [ ] **Step 1: CI job.**

```yaml
  graph:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-python@v5
        with:
          python-version: "3.12"
      - run: pip install './graph[dev]'
      - run: ruff check graph
      - run: ruff format --check graph
      - run: pytest graph
```

- [ ] **Step 2: Docs.**
  - `AGENTS.md` gets this section after "Dev setup":

    "## Python (`graph/`)
    The LangGraph workflow (`hivegraph`) is a Python package under `graph/`, with its own dependencies in `graph/pyproject.toml`. The Go rule, stdlib plus yaml.v3, is unchanged. For work under `graph/`: `python3 -m venv graph/.venv && graph/.venv/bin/pip install -e './graph[dev]'`, then before each commit run `graph/.venv/bin/ruff check graph && graph/.venv/bin/ruff format --check graph && graph/.venv/bin/pytest graph`. CI runs the same three checks. Tests use LangChain's fake chat models and a fake `agent-run`; never the network."
  - `CONTRIBUTING.md` gets the same commands in its own style.
  - The decision entry: "Graph workflows run as a Python subprocess", with the rejected alternatives exactly as listed in the spec.
- [ ] **Step 3: Commit.** `docs: graph workflows — setup, config, CI, decision`.

### Task 12: Full gate and smoke run

- [ ] **Step 1:** Run `go vet ./... && go test -race ./... && golangci-lint run ./...`. Each must exit 0. Check the exit codes, not just the tail of the output.
- [ ] **Step 2:** Run `graph/.venv/bin/ruff check graph && graph/.venv/bin/ruff format --check graph && graph/.venv/bin/pytest graph`.
- [ ] **Step 3: Smoke run with the real binaries and no Claude quota.**
  - Make a scratch git repo with `.hive-dispatch/policy.yaml` holding `checks: ["test -f done.txt"]`.
  - Build `hivedispatch`, and install `hivegraph` into a venv.
  - Run `internal/executor/langgraph` through a tiny Go test harness, or run `hivegraph run` directly with a hand-written task JSON, `code_with: fake` and the DeepSeek chat model (`DEEPSEEK_API_KEY` from fish).
  - Confirm the event stream: plan (real DeepSeek), then code (fake agent-run), then checks fail → fix → … → finish, with `completed` and the red-checks summary.
- [ ] **Step 4:** If any check fails, fix it before reporting. Then report and stop: merging is the user's call.
