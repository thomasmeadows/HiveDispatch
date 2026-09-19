# Phase 5: Model-Backed Triage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `triage.Triager` that runs Claude Code read-only in the ticket worktree, inspects the repo with Read/Grep/Glob, and returns `dispatch` / `needs_info` / `reject` as structured output — closing the MVP: an underspecified ticket gets a question, a human answers from a phone, the run resumes.

**Architecture:** The Claude Code process runner and stream parser move from `internal/executor/claudecode` into a shared `internal/claudecli` package (`Run`, `Parser`, `Transcript`) so triage and execution share supervision (process group kill, step budget, timeout, bounded logs). `internal/triage/claudecode` builds a triage prompt, runs the CLI with `--restricted --tools Read,Grep,Glob --permission-mode plan --json-schema`, and validates the decision. The dispatcher prepares the worktree before triage so the triager has a checkout, and passes `RepoPath`.

**Tech Stack:** Go 1.27 stdlib; Claude Code CLI (`--restricted`, `--tools`, `--permission-mode plan`, `--json-schema`, `--output-format stream-json`).

**Spec:** `docs/design-spec.md` — Triage agent (inputs, tools, decisions, guardrail, step budget), Failure handling ("Triage rejects" row), the human-in-the-loop path.

## Global Constraints

- Triage is read-only by construction: `--restricted` (no Bash), `--tools Read,Grep,Glob`, `--permission-mode plan`, `--no-session-persistence`. It never claims, comments, or writes.
- Triage has its own step budget and timeout (worker config `triage.step_budget`, `triage.timeout`; defaults 40 and 5m).
- A decision that fails validation (unknown kind, `needs_info` without a question) is an error; the dispatcher leaves the ticket Ready and the next poll retries.
- No `--bare`, ever.
- `go vet ./... && go test -race ./... && golangci-lint run` green before every commit; commit messages end with `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.

---

## File Structure

```
internal/claudecli/stream.go            Parser, Transcript, ResultMsg, RateLimit (moved from executor/claudecode, exported)
internal/claudecli/stream_test.go
internal/claudecli/run.go               Run(ctx, Cmd) (Transcript, Exit, log) with process group, step budget
internal/claudecli/run_test.go
internal/claudecli/clitest/fakeclaude.sh + fixtures (moved from executor/claudecode/testdata)
internal/claudecli/clitest/clitest.go   Setup(t, mode) (binary, argsFile, stdinFile)
internal/executor/claudecode/*          uses claudecli; keeps command.go, outcome.go, claudecode.go (thinner)
internal/triage/claudecode/prompt.go    triage prompt and decision schema
internal/triage/claudecode/triager.go   Triager: New, Decide
internal/triage/claudecode/triager_test.go
internal/triage/claudecode/testdata/stream_decision_*.jsonl
internal/dispatch/handle.go             Prepare before triage; RepoPath in triage.Input
internal/dispatch/dispatch_test.go      assert RepoPath is passed
internal/config/config.go               + Triage {Kind "claude"|"passthrough", StepBudget, Timeout}
cmd/hivedispatch/main.go                triager selection
docs/setup.md, README.md, docs/decisions.md
```

---

### Task 1: Extract `internal/claudecli`

**Files:**
- Create: `internal/claudecli/stream.go`, `stream_test.go`, `run.go`, `run_test.go`, `clitest/clitest.go`, `clitest/fakeclaude.sh`, `clitest/stream_success.jsonl`, `clitest/stream_error.jsonl`, `clitest/stream_needsinput.jsonl`
- Modify: `internal/executor/claudecode/{stream.go→deleted, outcome.go, claudecode.go, *_test.go}`

**Interfaces:**
- Produces:

```go
package claudecli
type RateLimit struct { Status string; ResetsAt time.Time }
type ResultMsg struct { ...same fields as before, exported... }
type Transcript struct { SessionID string; ToolUses int; EditedFiles []string; RateLimit *RateLimit; Result *ResultMsg; Lines int }
type Parser struct{...}; func NewParser(cwd string, onToolUse func(int)) *Parser; (p *Parser) Line([]byte); (p *Parser) Transcript() Transcript
type Cmd struct { Binary, Dir string; Args []string; Stdin string; StepBudget int; MaxLog int }
type Exit struct { CtxErr error; StepTripped bool; ExitErr error; Stderr string }
func Run(ctx context.Context, c Cmd) (Transcript, Exit, string /*log*/, error)   // error only when the process cannot start
// clitest
func Setup(t *testing.T, mode string) (binary, argsFile, stdinFile string)
```

- [ ] **Step 1: Move files**

```bash
mkdir -p internal/claudecli/clitest
git mv internal/executor/claudecode/testdata/fakeclaude.sh internal/claudecli/clitest/fakeclaude.sh
git mv internal/executor/claudecode/testdata/stream_success.jsonl internal/claudecli/clitest/
git mv internal/executor/claudecode/testdata/stream_error.jsonl internal/claudecli/clitest/
git mv internal/executor/claudecode/testdata/stream_needsinput.jsonl internal/claudecli/clitest/
git mv internal/executor/claudecode/stream.go internal/claudecli/stream.go
git mv internal/executor/claudecode/stream_test.go internal/claudecli/stream_test.go
```

Edit `fakeclaude.sh`: fixtures are now beside the script, so `$here/stream_success.jsonl` etc. still resolve — no change needed. Add a `decision` mode later (Task 2 adds fixtures under the triage package and points the script at them via `FAKE_CLAUDE_FIXTURE`): add at the top of the `case`:

```sh
  fixture)    cat "$FAKE_CLAUDE_FIXTURE" ;;
```

- [ ] **Step 2: Export the parser**

In `internal/claudecli/stream.go`: `package claudecli`; rename `rateLimit`→`RateLimit`, `resultMsg`→`ResultMsg`, `transcript`→`Transcript`, `streamParser`→`Parser`, `newStreamParser`→`NewParser`. Update the package doc comment: "Package claudecli runs the Claude Code CLI headless and parses its stream-json output. Both the executor and the triager build on it." Update `stream_test.go` accordingly (fixture paths become `clitest/…`).

- [ ] **Step 3: Write `clitest/clitest.go`**

```go
// Package clitest provides a fake `claude` binary for tests.
package clitest

import (
	"path/filepath"
	"runtime"
	"testing"
)

// Setup points the fake at mode and returns the binary path plus the files
// where it records argv and stdin.
func Setup(t *testing.T, mode string) (binary, argsFile, stdinFile string) {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller info")
	}
	binary = filepath.Join(filepath.Dir(self), "fakeclaude.sh")
	argsFile = filepath.Join(t.TempDir(), "args")
	stdinFile = filepath.Join(t.TempDir(), "stdin")
	t.Setenv("FAKE_CLAUDE_MODE", mode)
	t.Setenv("FAKE_CLAUDE_ARGS_FILE", argsFile)
	t.Setenv("FAKE_CLAUDE_STDIN_FILE", stdinFile)
	return binary, argsFile, stdinFile
}

// Fixture makes mode "fixture" replay path.
func Fixture(t *testing.T, path string) {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_CLAUDE_FIXTURE", abs)
}
```

- [ ] **Step 4: Write `run.go`** (the supervision code lifted from `claudecode.Run`)

```go
package claudecli

import (
	"bufio"
	"bytes"
	"context"
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
	waitDelay      = 5 * time.Second
	maxStderrBytes = 64 << 10
)

// Cmd describes one headless invocation.
type Cmd struct {
	Binary     string
	Dir        string
	Args       []string
	Stdin      string
	StepBudget int // 0 = unlimited
	MaxLog     int // 0 = defaultMaxLog
}

// Exit describes how the process ended.
type Exit struct {
	CtxErr      error // the caller's context error, if any
	StepTripped bool  // the step budget cancelled the run
	ExitErr     error // from Wait
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

// Command builds an *exec.Cmd with process-group kill on cancel. Exported so
// callers that want the plain JSON output (Plan, triage) can use cmd.Output.
func Command(ctx context.Context, c Cmd) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.Binary, c.Args...)
	cmd.Dir = c.Dir
	cmd.Stdin = strings.NewReader(c.Stdin)
	cmd.Env = append(os.Environ(), "CLAUDECODE=")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = waitDelay
	return cmd
}

// Run executes c, parsing stream-json from stdout. It returns the parsed
// transcript, how the process ended, and the captured log. The error is
// non-nil only when the process could not be started.
func Run(ctx context.Context, c Cmd) (Transcript, Exit, string, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var tripped bool
	var mu sync.Mutex
	parser := NewParser(c.Dir, func(count int) {
		if c.StepBudget > 0 && count > c.StepBudget {
			mu.Lock()
			tripped = true
			mu.Unlock()
			cancel()
		}
	})
	maxLog := c.MaxLog
	if maxLog == 0 {
		maxLog = defaultMaxLog
	}
	cmd := Command(runCtx, c)
	logBuf := &boundedBuffer{n: maxLog}
	stderr := &boundedBuffer{n: maxStderrBytes}
	cmd.Stderr = io.MultiWriter(stderr, logBuf)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Transcript{}, Exit{}, "", err
	}
	if err := cmd.Start(); err != nil {
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

- [ ] **Step 5: Write `run_test.go`** (lift the timeout, step budget, crash cases from the executor tests)

```go
package claudecli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/claudecli/clitest"
)

func TestRunSuccessCapturesTranscriptAndLog(t *testing.T) {
	bin, _, stdinFile := clitest.Setup(t, "success")
	tr, exit, log, err := Run(context.Background(), Cmd{Binary: bin, Dir: t.TempDir(), Stdin: "hi", StepBudget: 10})
	if err != nil || exit.ExitErr != nil || tr.Result == nil || tr.Result.Result != "hello" {
		t.Fatalf("tr=%+v exit=%+v err=%v", tr, exit, err)
	}
	if !strings.Contains(log, `"type": "result"`) {
		t.Error("log missing stream")
	}
	if b, _ := readFile(stdinFile); b != "hi" {
		t.Errorf("stdin = %q", b)
	}
}

func TestRunTimeoutKillsGroup(t *testing.T) {
	bin, _, _ := clitest.Setup(t, "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	tr, exit, _, err := Run(ctx, Cmd{Binary: bin, Dir: t.TempDir()})
	if err != nil || !errors.Is(exit.CtxErr, context.DeadlineExceeded) || tr.SessionID == "" {
		t.Errorf("tr=%+v exit=%+v err=%v", tr, exit, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("not killed promptly")
	}
}

func TestRunStepBudgetTrips(t *testing.T) {
	bin, _, _ := clitest.Setup(t, "manysteps")
	_, exit, _, err := Run(context.Background(), Cmd{Binary: bin, Dir: t.TempDir(), StepBudget: 5})
	if err != nil || !exit.StepTripped || exit.CtxErr != nil {
		t.Errorf("exit=%+v err=%v", exit, err)
	}
}

func TestRunCrash(t *testing.T) {
	bin, _, _ := clitest.Setup(t, "crash")
	tr, exit, _, err := Run(context.Background(), Cmd{Binary: bin, Dir: t.TempDir()})
	if err != nil || exit.ExitErr == nil || tr.Result != nil || !strings.Contains(exit.Stderr, "segfault") {
		t.Errorf("tr=%+v exit=%+v err=%v", tr, exit, err)
	}
}

func readFile(p string) (string, error) {
	b, err := os.ReadFile(p)
	return string(b), err
}
```

(add `"os"` to imports.)

- [ ] **Step 6: Slim the executor**

In `internal/executor/claudecode`:
- `outcome.go`: `exitInfo` → use `claudecli.Exit`; `transcript` → `claudecli.Transcript`; `looksLikeBudget(tr claudecli.Transcript)`; `mapOutcome(tr claudecli.Transcript, exit claudecli.Exit)`.
- `claudecode.go`: delete `boundedBuffer`, `command`, the scanning loop; `Run` becomes: load repo config, build prompt, `tr, exit, log, err := claudecli.Run(ctx, claudecli.Cmd{Binary: e.cfg.Binary, Dir: t.Workspace, Args: buildArgs(...), Stdin: promptText, StepBudget: t.StepBudget})`, return `mapOutcome(tr, exit)` with `Log = log`. `Plan` uses `claudecli.Command(ctx, claudecli.Cmd{...}).Output()` and `claudecli.ResultMsg`.
- Tests: `outcome_test.go` uses the exported types; `claudecode_test.go` uses `clitest.Setup` instead of its own `fakeExec`; drop the timeout/step/crash tests that now live in `claudecli` but keep `TestRunTimeoutMapsToCause` (mode `hang`, assert `CauseTimeout`) and `TestRunStepBudgetMapsToCause` (mode `manysteps`, assert `CauseStepBudget`) as thin mapping checks.

- [ ] **Step 7: Run, lint, commit**

Run: `go vet ./... && go test -race -count=2 ./internal/claudecli/... ./internal/executor/... && ~/go/bin/golangci-lint run ./...`
Expected: PASS, 0 issues.

```bash
git add -A
git commit -m "refactor: extract claudecli (stream parser, supervised run) shared by executor and triage

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 2: Claude Code triager

**Files:**
- Create: `internal/triage/claudecode/prompt.go`, `triager.go`, `triager_test.go`, `testdata/stream_decision_dispatch.jsonl`, `testdata/stream_decision_needsinfo.jsonl`, `testdata/stream_decision_invalid.jsonl`

**Interfaces:**
- Produces:

```go
type Config struct { Binary, Model string; StepBudget int; Timeout time.Duration }   // defaults: claude, "", 40, 5m
func New(cfg Config) *Triager
func (t *Triager) Decide(ctx, in triage.Input) (triage.Decision, error)
// prompt.go
const decisionSchema = `{"type":"object","properties":{"decision":{"type":"string","enum":["dispatch","needs_info","reject"]},"reason":{"type":"string"},"question":{"type":"string"},"complexity":{"type":"integer","minimum":1,"maximum":5},"notes":{"type":"string"}},"required":["decision","reason"]}`
func buildPrompt(in triage.Input) string
type decisionJSON struct { Decision, Reason, Question, Notes string; Complexity int }
func toDecision(d decisionJSON, in triage.Input) (triage.Decision, error)
```

`Decide` runs `claudecli.Run` with args `-p --output-format stream-json --verbose --permission-mode plan --restricted --tools Read,Grep,Glob --no-session-persistence --json-schema <decisionSchema> [--model M]` in `in.RepoPath`, with `StepBudget` and a `Timeout` on the context. It parses `Result.StructuredOutput` (fallback: `Result.Result` as JSON). On `dispatch`, `Decision.Prompt = prompt.Render(in.Ticket, in.Repo, in.Branch)` plus a `## Triage notes` section when `notes` is non-empty.

- [ ] **Step 1: Fixtures**

`testdata/stream_decision_dispatch.jsonl`:

```json
{"type": "system", "subtype": "init", "cwd": "/work/HIVE-1", "session_id": "tri-1", "tools": ["Read", "Grep", "Glob"], "model": "claude-opus-5", "permissionMode": "plan"}
{"type": "assistant", "session_id": "tri-1", "message": {"role": "assistant", "content": [{"type": "tool_use", "id": "t1", "name": "Grep", "input": {"pattern": "version"}}]}}
{"type": "result", "subtype": "success", "is_error": false, "num_turns": 2, "stop_reason": "end_turn", "terminal_reason": "completed", "session_id": "tri-1", "result": "{\"decision\":\"dispatch\",\"reason\":\"small, well-specified\",\"complexity\":1,\"notes\":\"main.go already parses flags; add a case.\"}", "structured_output": {"decision": "dispatch", "reason": "small, well-specified", "complexity": 1, "notes": "main.go already parses flags; add a case."}, "total_cost_usd": 0.02, "permission_denials": [], "api_error_status": null}
```

`testdata/stream_decision_needsinfo.jsonl`:

```json
{"type": "system", "subtype": "init", "cwd": "/work/HIVE-1", "session_id": "tri-2", "tools": ["Read"], "model": "claude-opus-5", "permissionMode": "plan"}
{"type": "result", "subtype": "success", "is_error": false, "num_turns": 1, "stop_reason": "end_turn", "terminal_reason": "completed", "session_id": "tri-2", "result": "{\"decision\":\"needs_info\",\"reason\":\"ambiguous\",\"question\":\"Should --version print semver or the git SHA?\"}", "structured_output": {"decision": "needs_info", "reason": "ambiguous", "question": "Should --version print semver or the git SHA?"}, "total_cost_usd": 0.02, "permission_denials": [], "api_error_status": null}
```

`testdata/stream_decision_invalid.jsonl` (needs_info without a question):

```json
{"type": "system", "subtype": "init", "cwd": "/work/HIVE-1", "session_id": "tri-3", "tools": ["Read"], "model": "claude-opus-5", "permissionMode": "plan"}
{"type": "result", "subtype": "success", "is_error": false, "num_turns": 1, "stop_reason": "end_turn", "terminal_reason": "completed", "session_id": "tri-3", "result": "{\"decision\":\"needs_info\",\"reason\":\"unclear\"}", "structured_output": {"decision": "needs_info", "reason": "unclear"}, "total_cost_usd": 0.02, "permission_denials": [], "api_error_status": null}
```

- [ ] **Step 2: Write the failing tests**

```go
package claudecode

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/claudecli/clitest"
	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
	"github.com/thomasmeadows/hivedispatch/internal/triage"
)

func input(t *testing.T) triage.Input {
	t.Helper()
	return triage.Input{
		Ticket:   tracker.Ticket{Key: "HIVE-1", Summary: "Add --version", Description: "Print the version."},
		Repo:     config.RepoConfig{Name: "o/r", DefaultBranch: "main"},
		Branch:   "hive/HIVE-1",
		RepoPath: t.TempDir(),
		Attempts: 1, LastStopCause: "timeout",
	}
}

func setup(t *testing.T, fixture string) (*Triager, string, string) {
	t.Helper()
	bin, args, stdin := clitest.Setup(t, "fixture")
	clitest.Fixture(t, "testdata/"+fixture)
	return New(Config{Binary: bin, Timeout: 5 * time.Second}), args, stdin
}

func TestDecideDispatch(t *testing.T) {
	tr, argsFile, stdinFile := setup(t, "stream_decision_dispatch.jsonl")
	d, err := tr.Decide(context.Background(), input(t))
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != triage.KindDispatch || d.Complexity != 1 || d.Reason != "small, well-specified" {
		t.Errorf("d = %+v", d)
	}
	for _, want := range []string{"HIVE-1", "Add --version", "## Triage notes", "main.go already parses flags", "HIVE_NEEDS_INPUT:"} {
		if !strings.Contains(d.Prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	args, _ := os.ReadFile(argsFile)
	for _, want := range []string{"--restricted", "--permission-mode\nplan", "--tools\nRead,Grep,Glob", "--json-schema", "--no-session-persistence", "stream-json"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("args missing %q:\n%s", want, args)
		}
	}
	if strings.Contains(string(args), "--bare") || strings.Contains(string(args), "--resume") {
		t.Errorf("args must not include --bare or --resume: %s", args)
	}
	stdin, _ := os.ReadFile(stdinFile)
	for _, want := range []string{"Add --version", "Print the version.", "attempt", "timeout", "dispatch", "needs_info", "reject"} {
		if !strings.Contains(string(stdin), want) {
			t.Errorf("triage prompt missing %q", want)
		}
	}
}

func TestDecideNeedsInfo(t *testing.T) {
	tr, _, _ := setup(t, "stream_decision_needsinfo.jsonl")
	d, err := tr.Decide(context.Background(), input(t))
	if err != nil || d.Kind != triage.KindNeedsInfo || !strings.HasPrefix(d.Question, "Should --version") || d.Prompt != "" {
		t.Errorf("d=%+v err=%v", d, err)
	}
}

func TestDecideInvalidIsError(t *testing.T) {
	tr, _, _ := setup(t, "stream_decision_invalid.jsonl")
	if _, err := tr.Decide(context.Background(), input(t)); err == nil {
		t.Error("needs_info without a question must be an error")
	}
}

func TestDecideRequiresRepoPath(t *testing.T) {
	tr, _, _ := setup(t, "stream_decision_dispatch.jsonl")
	in := input(t)
	in.RepoPath = ""
	if _, err := tr.Decide(context.Background(), in); err == nil {
		t.Error("expected error without a checkout")
	}
}

func TestDecideCLIErrorIsError(t *testing.T) {
	bin, _, _ := clitest.Setup(t, "error")
	tr := New(Config{Binary: bin, Timeout: 5 * time.Second})
	if _, err := tr.Decide(context.Background(), input(t)); err == nil {
		t.Error("is_error result must be an error")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/triage/claudecode/`
Expected: FAIL — `undefined: New`.

- [ ] **Step 4: Write `prompt.go`**

```go
package claudecode

import (
	"fmt"
	"strings"

	"github.com/thomasmeadows/hivedispatch/internal/prompt"
	"github.com/thomasmeadows/hivedispatch/internal/triage"
)

// decisionSchema is the structured output the triager must return.
const decisionSchema = `{"type":"object","properties":{"decision":{"type":"string","enum":["dispatch","needs_info","reject"]},"reason":{"type":"string"},"question":{"type":"string"},"complexity":{"type":"integer","minimum":1,"maximum":5},"notes":{"type":"string"}},"required":["decision","reason"]}`

type decisionJSON struct {
	Decision   string `json:"decision"`
	Reason     string `json:"reason"`
	Question   string `json:"question"`
	Complexity int    `json:"complexity"`
	Notes      string `json:"notes"`
}

func buildPrompt(in triage.Input) string {
	var sb strings.Builder
	sb.WriteString(`You are the triage step of an automated pipeline that turns tickets into pull requests.
A coding agent will attempt this ticket in this repository if you decide "dispatch".
Your job is to decide whether it is worth attempting, using the repository to check.

Use Read, Grep and Glob to answer:
- Does the area the ticket describes exist in this repository?
- Is the request specific enough that an agent could finish it without guessing?
- Is it small enough for one pull request (not architectural or cross-cutting)?

Decide:
- "dispatch": actionable. Put concrete pointers for the agent in "notes" (files, functions, conventions you saw).
- "needs_info": underspecified. Put ONE clear question for the ticket author in "question".
- "reject": out of scope for automation (too large, architectural, or not in this repo). Explain in "reason".

Set "complexity" from 1 (trivial) to 5 (large). Do not modify anything.

`)
	fmt.Fprintf(&sb, "# Ticket %s: %s\n\n", in.Ticket.Key, in.Ticket.Summary)
	if in.Ticket.URL != "" {
		fmt.Fprintf(&sb, "%s\n\n", in.Ticket.URL)
	}
	sb.WriteString("## Description\n\n")
	if strings.TrimSpace(in.Ticket.Description) == "" {
		sb.WriteString("(no description)\n")
	} else {
		sb.WriteString(in.Ticket.Description + "\n")
	}
	if len(in.Ticket.Comments) > 0 {
		sb.WriteString("\n## Discussion\n\n")
		for _, c := range in.Ticket.Comments {
			fmt.Fprintf(&sb, "- [%s] %s: %s\n", c.Created.UTC().Format("2006-01-02 15:04"), c.Author, strings.ReplaceAll(c.Body, "\n", "\n  "))
		}
	}
	fmt.Fprintf(&sb, "\n## Repository\n\n%s, default branch %s. Work would happen on branch %s.\n", in.Repo.Name, in.Repo.DefaultBranch, in.Branch)
	if in.Attempts > 0 {
		fmt.Fprintf(&sb, "\n## History\n\nThis ticket has been attempted %d time(s). Last stop cause: %s. If earlier attempts failed for a reason a better prompt cannot fix, prefer needs_info or reject.\n", in.Attempts, orNone(in.LastStopCause))
	}
	return sb.String()
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// toDecision validates the model's output and renders the executor prompt.
func toDecision(d decisionJSON, in triage.Input) (triage.Decision, error) {
	out := triage.Decision{Reason: strings.TrimSpace(d.Reason), Complexity: d.Complexity}
	switch triage.Kind(d.Decision) {
	case triage.KindDispatch:
		out.Kind = triage.KindDispatch
		out.Prompt = prompt.Render(in.Ticket, in.Repo, in.Branch)
		if n := strings.TrimSpace(d.Notes); n != "" {
			out.Prompt += "\n## Triage notes\n\n" + n + "\n"
		}
	case triage.KindNeedsInfo:
		q := strings.TrimSpace(d.Question)
		if q == "" {
			return out, fmt.Errorf("triage: needs_info without a question")
		}
		out.Kind = triage.KindNeedsInfo
		out.Question = q
	case triage.KindReject:
		out.Kind = triage.KindReject
		if out.Reason == "" {
			out.Reason = "rejected by triage"
		}
	default:
		return out, fmt.Errorf("triage: unknown decision %q", d.Decision)
	}
	return out, nil
}
```

- [ ] **Step 5: Write `triager.go`**

```go
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
		return triage.Decision{}, fmt.Errorf("triage %s: no result (%v): %s", in.Ticket.Key, exit.ExitErr, exit.Stderr)
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
```

- [ ] **Step 6: Run, lint, commit**

Run: `go vet ./... && go test -race ./internal/triage/... && ~/go/bin/golangci-lint run ./...`
Expected: PASS, 0 issues.

```bash
git add internal/triage internal/claudecli/clitest
git commit -m "feat(triage): Claude Code read-only triager with structured decisions

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 3: Dispatcher, config, CLI, docs, end-to-end

**Files:**
- Modify: `internal/dispatch/handle.go`, `internal/dispatch/dispatch_test.go`, `internal/config/config.go`, `config_test.go`, `cmd/hivedispatch/main.go`, `README.md`, `docs/setup.md`, `docs/decisions.md`

- [ ] **Step 1: Dispatcher — prepare before triage**

In `Handle`, after the heartbeat goroutine starts and before the resume/triage branch, prepare the workspace once:

```go
	ws, err := d.Workspaces.Prepare(ctx, repo, t.Key)
	if err != nil {
		run.Attempts++
		res := executor.Result{Status: executor.StatusFailed, StopCause: executor.CauseError, Summary: "workspace: " + err.Error()}
		return d.finishFailed(ctx, t, run, res, false), nil
	}
```

Pass `RepoPath: ws.Path` in `triage.Input`, and change `execute` to take `ws gitops.Workspace` instead of preparing it. Add to `TestDispatchCompletedWithPR`:

```go
	if calls := h.tri.Calls(); len(calls) != 1 || calls[0].RepoPath == "" {
		t.Errorf("triage must receive the workspace path: %+v", calls)
	}
```

- [ ] **Step 2: Config**

Add:

```go
	Triage            TriageConfig  `yaml:"triage"`
// TriageConfig selects and bounds the triage step.
type TriageConfig struct {
	Kind       string        `yaml:"kind"`        // "claude" (default) or "passthrough"
	StepBudget int           `yaml:"step_budget"` // default 40
	Timeout    time.Duration `yaml:"timeout"`     // default 5m
	Model      string        `yaml:"model"`
}
```

Defaults and validation (`triage.kind: want claude or passthrough`). Test `TestLoadTriageDefaults` mirroring `TestLoadExecutorDefaults`.

- [ ] **Step 3: CLI**

Add `-triage claude|passthrough` override alongside `-executor`; construct `triclaude.New(triclaude.Config{Binary: cfg.Claude.Binary, Model: cfg.Triage.Model, StepBudget: cfg.Triage.StepBudget, Timeout: cfg.Triage.Timeout})` or `passthrough.Triager{}`. Update `usage`.

- [ ] **Step 4: Docs and decisions**

README status → "**Status: alpha.** The MVP loop is complete: a Jira ticket is triaged by Claude Code (dispatch / ask / reject), claimed, implemented by Claude Code in an isolated worktree, pushed, and opened as a GitHub PR, with questions posted back to the ticket and resumed when a human answers." Add a short "How a ticket flows" list (poll → triage → claim → branch → run → commit → PR → comment).

`docs/setup.md` add under Claude Code: "Triage runs the same CLI read-only (`--restricted`, Read/Grep/Glob, plan mode) in the ticket worktree with its own `triage.step_budget` (40) and `triage.timeout` (5m). Set `triage.kind: passthrough` to dispatch every ticket without a model call."

`docs/decisions.md` append:

```markdown
## 2026-09-19 — Worktree is prepared before triage

Decided: the dispatcher prepares the ticket worktree first and hands its path to the triager. Rejected: triaging against the `--no-checkout` base clone (nothing to read) or a separate read-only checkout (a second copy per repo). Why: a worktree is cheap, the triager needs real files to inspect, and on dispatch the same worktree is used for the run.

## 2026-09-19 — Triage notes, not a triage-written prompt

Decided: the triager returns `notes` that are appended to the standard executor prompt, rather than authoring the whole prompt. Why: the standard prompt carries the invariants (commit as you go, no merging, HIVE_NEEDS_INPUT protocol); letting the triage model rewrite it would let a bad triage decision remove a guardrail.

## 2026-09-19 — One shared CLI runner

Decided: `internal/claudecli` owns process supervision and stream parsing for both executor and triager. Why: the step budget, timeout, process-group kill and log capture are the same problem in both places; the second user is what proved the extraction.
```

- [ ] **Step 5: Run everything, commit, push**

```bash
go vet ./... && go test -race ./... && ~/go/bin/golangci-lint run ./... && go build -o bin/hivedispatch ./cmd/hivedispatch
git add -A
git commit -m "feat: model-backed triage wired into the dispatcher; MVP loop complete

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
git push
```

- [ ] **Step 6: Manual end-to-end (user)**

1. Underspecified ticket ("Make the CLI better") in Ready → `hivedispatch run -once` → expect a `[HiveDispatch] Before starting, I need more information:` comment, ticket in Needs Info, no branch.
2. Reply in the thread from a phone, move to Ready → run again → expect the run to proceed (this is a fresh triage since triage's needs_info stores no resume token; verify the reply is visible in the prompt via the discussion section) and a PR to open.
3. Well-specified small ticket → dispatch with triage notes visible in `logs/<KEY>/<ts>.log` prompt echo.
4. Ticket asking for a cross-cutting rewrite → `reject`, ticket in Needs Human.

Record surprises in `docs/decisions.md`.

---

## Self-review

**Spec coverage (Phase 5):** triage inputs (ticket, thread, repo metadata, history) ✔ `buildPrompt`; tools `read_repo_tree`/`search_code` via Glob/Grep/Read ✔; `read_ticket_history` via Attempts/LastStopCause ✔; decisions `dispatch`/`needs_info`/`reject` ✔; guardrail read-only ✔; step budget ✔ (`claudecli` StepBudget); needs-info loop end-to-end ✔ (dispatcher from Phase 2 + Task 3 E2E).

**Type consistency:** `claudecli.Transcript/Exit/ResultMsg` used by both adapters; `triage.Input.RepoPath` set in `handle.go` and asserted in tests; `config.TriageConfig` consumed by the CLI.

**Placeholder scan:** none.
