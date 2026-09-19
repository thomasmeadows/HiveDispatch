# Phase 4: Claude Code Executor Adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A real `executor.Executor` that runs Claude Code headless in the ticket worktree, enforces the step budget and wall-clock timeout by killing the process group, maps every exit to a `Status`/`Cause`, captures the session id as the resume token, and detects `HIVE_NEEDS_INPUT:` — so `hivedispatch run` takes a real ticket to a real PR with real code.

**Architecture:** `internal/executor/claudecode` has three parts: a `stream.go` parser for `--output-format stream-json` lines (session id, tool-use count, edited files, rate-limit info, final result), a `command.go` builder that turns config + task into argv, and `claudecode.go` which runs the subprocess with a process group, feeds the prompt on stdin, cancels on step budget, and maps the outcome. Repo-level executor policy lives in `.hivedispatch.yaml` inside the governed repo, read by `internal/repoconfig`. Tests drive the adapter with a fake `claude` shell script that replays recorded transcripts.

**Tech Stack:** Go 1.27 stdlib (`os/exec`, `bufio`, `syscall`), Claude Code CLI 2.1.x (`-p`, `--output-format stream-json --verbose`, `--tools`, `--allowedTools`, `--permission-mode`, `--resume`, `--json-schema`, `--max-budget-usd`, `--no-session-persistence`).

**Spec:** `docs/design-spec.md` — Executor contract, Run phases (plan mode), Failure handling, Budget and scheduling; `docs/decisions.md` (step budget by tool-use count, stable worktree path, `dontAsk` default).

## Global Constraints

- Never pass `--bare`: it skips loading stored credentials (verified on 2.1.278 — headless runs report "Not logged in").
- `resumeToken` is the `session_id` from the stream and is never interpreted.
- Stop cause precedence: context deadline → `Timeout`; context cancelled → `Killed`; step counter tripped → `StepBudget`; result `is_error` with rate-limit signal → `Budget`; any other `is_error` or missing result → `Error`.
- The adapter always returns a `Result` with `Status` set and a non-empty `Summary`, even on failure.
- Tests never invoke the real `claude`; they use `testdata/fakeclaude.sh`. Integration against the real CLI is manual (Task 6).
- `go vet ./... && go test -race ./... && golangci-lint run` green before every commit; commit messages end with `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.

---

## File Structure

```
internal/executor/claudecode/stream.go           parse stream-json lines into a transcript summary
internal/executor/claudecode/stream_test.go
internal/executor/claudecode/testdata/stream_success.jsonl   real transcript (captured 2026-09-19, paths sanitised)
internal/executor/claudecode/testdata/stream_error.jsonl     hand-written: is_error result with rate-limit text
internal/executor/claudecode/testdata/stream_needsinput.jsonl hand-written: HIVE_NEEDS_INPUT in result
internal/executor/claudecode/testdata/fakeclaude.sh          replays a transcript chosen by FAKE_CLAUDE_MODE
internal/executor/claudecode/outcome.go          needs-input parsing, summary truncation, cause mapping
internal/executor/claudecode/outcome_test.go
internal/executor/claudecode/command.go          Config, argv builder
internal/executor/claudecode/command_test.go
internal/executor/claudecode/claudecode.go       Executor: Run, Plan, process supervision
internal/executor/claudecode/claudecode_test.go
internal/repoconfig/repoconfig.go                .hivedispatch.yaml in the governed repo
internal/repoconfig/repoconfig_test.go
internal/config/config.go                        + Executor string ("claude"|"fake"), ClaudeConfig{Binary, Model}
cmd/hivedispatch/main.go                         executor selection
docs/setup.md, README.md, docs/decisions.md, .hivedispatch.yaml (this repo's own, for dogfooding)
```

---

### Task 1: Stream parser

**Files:**
- Create: `internal/executor/claudecode/stream.go`, `stream_test.go`, `testdata/stream_error.jsonl`, `testdata/stream_needsinput.jsonl` (the success fixture already exists)

**Interfaces:**
- Produces:

```go
type rateLimit struct { Status string; ResetsAt time.Time }
type transcript struct {
    SessionID    string
    ToolUses     int
    EditedFiles  []string         // from Edit/Write/MultiEdit/NotebookEdit tool_use inputs, deduplicated, relative to cwd when possible
    RateLimit    *rateLimit       // last rate_limit_event seen
    Result       *resultMsg       // nil if the process died before emitting one
    Lines        int
}
type resultMsg struct { Subtype string; IsError bool; Result string; SessionID string; NumTurns int; StopReason, TerminalReason string; APIErrorStatus *int; StructuredOutput json.RawMessage; TotalCostUSD float64 }
type streamParser struct { cwd string; onToolUse func(count int); t transcript }
func newStreamParser(cwd string, onToolUse func(int)) *streamParser
func (p *streamParser) Line(raw []byte)        // tolerant: non-JSON lines are ignored
func (p *streamParser) Transcript() transcript
```

- [ ] **Step 1: Write the hand-written fixtures**

`testdata/stream_error.jsonl`:

```json
{"type": "system", "subtype": "init", "cwd": "/work/HIVE-2", "session_id": "err-session", "tools": [], "model": "claude-opus-5", "permissionMode": "dontAsk"}
{"type": "assistant", "session_id": "err-session", "message": {"role": "assistant", "content": [{"type": "tool_use", "id": "toolu_1", "name": "Write", "input": {"file_path": "/work/HIVE-2/cmd/app/main.go", "content": "package main"}}]}}
{"type": "user", "session_id": "err-session", "message": {"role": "user", "content": [{"tool_use_id": "toolu_1", "type": "tool_result", "content": "ok"}]}}
{"type": "assistant", "session_id": "err-session", "message": {"role": "assistant", "content": [{"type": "tool_use", "id": "toolu_2", "name": "Edit", "input": {"file_path": "/work/HIVE-2/cmd/app/main.go", "old_string": "a", "new_string": "b"}}]}}
{"type": "rate_limit_event", "rate_limit_info": {"status": "rejected", "resetsAt": 1789810200, "rateLimitType": "five_hour", "unifiedWindows": {"five_hour": {"utilization": 1.0, "resetsAt": 1789810200}}}}
{"type": "result", "subtype": "error_during_execution", "is_error": true, "num_turns": 3, "stop_reason": null, "terminal_reason": "error", "session_id": "err-session", "result": "You've hit your usage limit. Resets at 4:30am UTC.", "total_cost_usd": 0.12, "permission_denials": [], "api_error_status": 429}
```

`testdata/stream_needsinput.jsonl`:

```json
{"type": "system", "subtype": "init", "cwd": "/work/HIVE-3", "session_id": "q-session", "tools": ["Read"], "model": "claude-opus-5", "permissionMode": "dontAsk"}
{"type": "assistant", "session_id": "q-session", "message": {"role": "assistant", "content": [{"type": "text", "text": "I looked at the code.\n\nHIVE_NEEDS_INPUT: Should the flag print semver or the git SHA?"}]}}
{"type": "result", "subtype": "success", "is_error": false, "num_turns": 1, "stop_reason": "end_turn", "terminal_reason": "completed", "session_id": "q-session", "result": "I looked at the code.\n\nHIVE_NEEDS_INPUT: Should the flag print semver or the git SHA?", "total_cost_usd": 0.05, "permission_denials": [], "api_error_status": null}
```

- [ ] **Step 2: Write the failing tests**

`stream_test.go`:

```go
package claudecode

import (
	"bufio"
	"os"
	"testing"
	"time"
)

func parseFixture(t *testing.T, name, cwd string, onToolUse func(int)) transcript {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p := newStreamParser(cwd, onToolUse)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		p.Line(sc.Bytes())
	}
	return p.Transcript()
}

func TestParseSuccessTranscript(t *testing.T) {
	var counts []int
	tr := parseFixture(t, "stream_success.jsonl", "/work/HIVE-1", func(n int) { counts = append(counts, n) })
	if tr.SessionID != "5b89d684-8de0-4b4e-99b8-a7f283ffd611" {
		t.Errorf("session = %q", tr.SessionID)
	}
	if tr.ToolUses != 1 || len(counts) != 1 || counts[0] != 1 {
		t.Errorf("tool uses = %d, callbacks = %v", tr.ToolUses, counts)
	}
	if len(tr.EditedFiles) != 0 {
		t.Errorf("Read is not an edit: %v", tr.EditedFiles)
	}
	if tr.RateLimit == nil || tr.RateLimit.Status != "allowed" || !tr.RateLimit.ResetsAt.Equal(time.Unix(1789810200, 0)) {
		t.Errorf("rate limit = %+v", tr.RateLimit)
	}
	if tr.Result == nil || tr.Result.IsError || tr.Result.Result != "hello" || tr.Result.NumTurns != 2 || tr.Result.SessionID != tr.SessionID {
		t.Errorf("result = %+v", tr.Result)
	}
}

func TestParseErrorTranscriptCollectsEditsAndRateLimit(t *testing.T) {
	tr := parseFixture(t, "stream_error.jsonl", "/work/HIVE-2", nil)
	if tr.ToolUses != 2 {
		t.Errorf("tool uses = %d", tr.ToolUses)
	}
	if len(tr.EditedFiles) != 1 || tr.EditedFiles[0] != "cmd/app/main.go" {
		t.Errorf("edited = %v", tr.EditedFiles)
	}
	if tr.RateLimit == nil || tr.RateLimit.Status != "rejected" {
		t.Errorf("rate limit = %+v", tr.RateLimit)
	}
	if tr.Result == nil || !tr.Result.IsError || tr.Result.APIErrorStatus == nil || *tr.Result.APIErrorStatus != 429 {
		t.Errorf("result = %+v", tr.Result)
	}
}

func TestParseIgnoresGarbage(t *testing.T) {
	p := newStreamParser("/w", nil)
	p.Line([]byte("Warning: no stdin data received"))
	p.Line([]byte(""))
	p.Line([]byte(`{"type":"unknown_future_event"}`))
	if tr := p.Transcript(); tr.Result != nil || tr.Lines != 3 {
		t.Errorf("transcript = %+v", tr)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/executor/claudecode/`
Expected: FAIL — `undefined: newStreamParser`.

- [ ] **Step 4: Write `stream.go`**

```go
// Package claudecode adapts the Claude Code CLI to executor.Executor.
//
// The CLI is run headless with --output-format stream-json. The orchestrator
// never interprets the session id it stores as the resume token; it only
// hands it back with --resume.
package claudecode

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"
)

type rateLimit struct {
	Status   string
	ResetsAt time.Time
}

type resultMsg struct {
	Subtype          string          `json:"subtype"`
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	SessionID        string          `json:"session_id"`
	NumTurns         int             `json:"num_turns"`
	StopReason       string          `json:"stop_reason"`
	TerminalReason   string          `json:"terminal_reason"`
	APIErrorStatus   *int            `json:"api_error_status"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
}

// transcript is what the parser learned from a run.
type transcript struct {
	SessionID   string
	ToolUses    int
	EditedFiles []string
	RateLimit   *rateLimit
	Result      *resultMsg
	Lines       int
}

type streamParser struct {
	cwd       string
	onToolUse func(count int)
	t         transcript
	edited    map[string]bool
}

func newStreamParser(cwd string, onToolUse func(int)) *streamParser {
	return &streamParser{cwd: cwd, onToolUse: onToolUse, edited: map[string]bool{}}
}

// editingTools are the built-in tools whose input names a file they change.
var editingTools = map[string]string{
	"Edit": "file_path", "Write": "file_path", "MultiEdit": "file_path", "NotebookEdit": "notebook_path",
}

type envelope struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Message   *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	RateLimitInfo *struct {
		Status   string `json:"status"`
		ResetsAt int64  `json:"resetsAt"`
	} `json:"rate_limit_info"`
}

type contentBlock struct {
	Type  string                     `json:"type"`
	Name  string                     `json:"name"`
	Input map[string]json.RawMessage `json:"input"`
}

// Line consumes one line of output. Non-JSON lines are counted and ignored.
func (p *streamParser) Line(raw []byte) {
	p.t.Lines++
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || raw[0] != '{' {
		return
	}
	var env envelope
	if json.Unmarshal(raw, &env) != nil {
		return
	}
	switch env.Type {
	case "system":
		if env.Subtype == "init" && env.SessionID != "" {
			p.t.SessionID = env.SessionID
		}
	case "assistant":
		if env.Message == nil {
			return
		}
		var blocks []contentBlock
		if json.Unmarshal(env.Message.Content, &blocks) != nil {
			return
		}
		for _, b := range blocks {
			if b.Type != "tool_use" {
				continue
			}
			p.t.ToolUses++
			if field, ok := editingTools[b.Name]; ok {
				var path string
				if json.Unmarshal(b.Input[field], &path) == nil && path != "" {
					p.addEdited(path)
				}
			}
			if p.onToolUse != nil {
				p.onToolUse(p.t.ToolUses)
			}
		}
	case "rate_limit_event":
		if env.RateLimitInfo != nil {
			p.t.RateLimit = &rateLimit{Status: env.RateLimitInfo.Status, ResetsAt: time.Unix(env.RateLimitInfo.ResetsAt, 0)}
		}
	case "result":
		var r resultMsg
		if json.Unmarshal(raw, &r) == nil {
			p.t.Result = &r
			if r.SessionID != "" {
				p.t.SessionID = r.SessionID
			}
		}
	}
}

func (p *streamParser) addEdited(path string) {
	if rel, err := filepath.Rel(p.cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
		path = rel
	}
	if !p.edited[path] {
		p.edited[path] = true
		p.t.EditedFiles = append(p.t.EditedFiles, path)
	}
}

// Transcript returns what has been parsed so far.
func (p *streamParser) Transcript() transcript {
	return p.t
}
```

- [ ] **Step 5: Run tests, lint, commit**

Run: `go vet ./... && go test -race ./internal/executor/claudecode/ && ~/go/bin/golangci-lint run ./...`
Expected: PASS, 0 issues.

```bash
git add internal/executor/claudecode
git commit -m "feat(claudecode): stream-json parser with real transcript fixture

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 2: Outcome mapping

**Files:**
- Create: `internal/executor/claudecode/outcome.go`, `outcome_test.go`

**Interfaces:**
- Produces:

```go
const maxSummary = 4000
func parseNeedsInput(text string) (question string, ok bool)   // last line starting with prompt.NeedsInputMarker; question = remainder of text from the marker
func truncate(s string, n int) string
// mapOutcome turns a transcript plus how the process ended into a Result.
type exitInfo struct { CtxErr error; StepTripped bool; ExitErr error; Stderr string }
func mapOutcome(tr transcript, exit exitInfo) executor.Result
```

Mapping rules (in order):
1. `exit.CtxErr == context.DeadlineExceeded` → Failed/Timeout.
2. `exit.StepTripped` → Failed/StepBudget.
3. `exit.CtxErr == context.Canceled` → Failed/Killed.
4. `tr.Result == nil` → Failed/Error, summary from stderr tail or "executor exited without a result".
5. `tr.Result.IsError`: Budget if `APIErrorStatus == 429`, or `RateLimit.Status` is not `allowed`, or the text mentions "usage limit"/"rate limit"; else Error. Summary = result text; for Budget append "Quota resets at <UTC time>" when known.
6. Not error: if `parseNeedsInput` matches → NeedsInput with Question; else Completed with Summary = result text (truncated).
Always: `ResumeToken = tr.SessionID`, `ChangedFiles = tr.EditedFiles`, `Summary` defaults to a cause description when empty.

- [ ] **Step 1: Write the failing tests**

```go
package claudecode

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
)

func TestParseNeedsInput(t *testing.T) {
	q, ok := parseNeedsInput("I looked.\n\nHIVE_NEEDS_INPUT: Which DB?\nMore context here.")
	if !ok || q != "Which DB?\nMore context here." {
		t.Errorf("ok=%v q=%q", ok, q)
	}
	if _, ok := parseNeedsInput("all done"); ok {
		t.Error("no marker should not match")
	}
	if _, ok := parseNeedsInput("HIVE_NEEDS_INPUT:"); ok {
		t.Error("empty question should not match")
	}
}

func TestMapOutcomePrecedence(t *testing.T) {
	ok := transcript{SessionID: "s", Result: &resultMsg{Result: "done"}}
	cases := []struct {
		name   string
		tr     transcript
		exit   exitInfo
		status executor.Status
		cause  executor.Cause
	}{
		{"timeout", ok, exitInfo{CtxErr: context.DeadlineExceeded}, executor.StatusFailed, executor.CauseTimeout},
		{"step", ok, exitInfo{StepTripped: true, CtxErr: context.Canceled}, executor.StatusFailed, executor.CauseStepBudget},
		{"killed", ok, exitInfo{CtxErr: context.Canceled}, executor.StatusFailed, executor.CauseKilled},
		{"noresult", transcript{SessionID: "s"}, exitInfo{ExitErr: errors.New("exit 1"), Stderr: "boom"}, executor.StatusFailed, executor.CauseError},
		{"budget429", transcript{Result: &resultMsg{IsError: true, Result: "x", APIErrorStatus: intp(429)}}, exitInfo{}, executor.StatusFailed, executor.CauseBudget},
		{"budgettext", transcript{Result: &resultMsg{IsError: true, Result: "You've hit your usage limit"}}, exitInfo{}, executor.StatusFailed, executor.CauseBudget},
		{"error", transcript{Result: &resultMsg{IsError: true, Result: "Not logged in"}}, exitInfo{}, executor.StatusFailed, executor.CauseError},
		{"needsinput", transcript{Result: &resultMsg{Result: "hm\nHIVE_NEEDS_INPUT: which?"}}, exitInfo{}, executor.StatusNeedsInput, executor.CauseNone},
		{"completed", ok, exitInfo{}, executor.StatusCompleted, executor.CauseNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := mapOutcome(c.tr, c.exit)
			if res.Status != c.status || res.StopCause != c.cause {
				t.Errorf("got %s/%s want %s/%s", res.Status, res.StopCause, c.status, c.cause)
			}
			if res.Summary == "" {
				t.Error("summary must never be empty")
			}
		})
	}
}

func TestMapOutcomeDetails(t *testing.T) {
	reset := time.Date(2026, 9, 20, 4, 30, 0, 0, time.UTC)
	tr := transcript{
		SessionID: "sess", EditedFiles: []string{"a.go"},
		RateLimit: &rateLimit{Status: "rejected", ResetsAt: reset},
		Result:    &resultMsg{IsError: true, Result: "limit"},
	}
	res := mapOutcome(tr, exitInfo{})
	if res.StopCause != executor.CauseBudget || res.ResumeToken != "sess" || len(res.ChangedFiles) != 1 {
		t.Errorf("res = %+v", res)
	}
	if !strings.Contains(res.Summary, "04:30 UTC") {
		t.Errorf("summary should include reset time: %q", res.Summary)
	}
	long := transcript{Result: &resultMsg{Result: strings.Repeat("x", maxSummary+100)}}
	if got := mapOutcome(long, exitInfo{}).Summary; len(got) > maxSummary+3 {
		t.Errorf("summary not truncated: %d", len(got))
	}
	q := mapOutcome(transcript{Result: &resultMsg{Result: "HIVE_NEEDS_INPUT: A or B?"}}, exitInfo{})
	if q.Question != "A or B?" {
		t.Errorf("question = %q", q.Question)
	}
}

func intp(i int) *int { return &i }
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/executor/claudecode/ -run 'NeedsInput|MapOutcome'`
Expected: FAIL — `undefined: parseNeedsInput`.

- [ ] **Step 3: Write `outcome.go`**

```go
package claudecode

import (
	"context"
	"errors"
	"strings"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/prompt"
)

const maxSummary = 4000

// exitInfo describes how the subprocess ended.
type exitInfo struct {
	CtxErr      error
	StepTripped bool
	ExitErr     error
	Stderr      string
}

// parseNeedsInput finds the last HIVE_NEEDS_INPUT: line and returns the
// question, which runs to the end of the text.
func parseNeedsInput(text string) (string, bool) {
	idx := strings.LastIndex(text, prompt.NeedsInputMarker)
	if idx < 0 {
		return "", false
	}
	if idx > 0 && text[idx-1] != '\n' {
		return "", false
	}
	q := strings.TrimSpace(text[idx+len(prompt.NeedsInputMarker):])
	if q == "" {
		return "", false
	}
	return q, true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func looksLikeBudget(tr transcript) bool {
	r := tr.Result
	if r != nil && r.APIErrorStatus != nil && *r.APIErrorStatus == 429 {
		return true
	}
	if tr.RateLimit != nil && tr.RateLimit.Status != "" && tr.RateLimit.Status != "allowed" {
		return true
	}
	if r != nil {
		low := strings.ToLower(r.Result)
		return strings.Contains(low, "usage limit") || strings.Contains(low, "rate limit")
	}
	return false
}

// mapOutcome turns what was parsed plus how the process ended into a Result.
func mapOutcome(tr transcript, exit exitInfo) executor.Result {
	res := executor.Result{ResumeToken: tr.SessionID, ChangedFiles: tr.EditedFiles}
	text := ""
	if tr.Result != nil {
		text = strings.TrimSpace(tr.Result.Result)
	}
	fail := func(c executor.Cause, summary string) executor.Result {
		res.Status = executor.StatusFailed
		res.StopCause = c
		if summary == "" {
			summary = c.Describe()
		}
		res.Summary = truncate(summary, maxSummary)
		return res
	}
	switch {
	case errors.Is(exit.CtxErr, context.DeadlineExceeded):
		return fail(executor.CauseTimeout, text)
	case exit.StepTripped:
		return fail(executor.CauseStepBudget, text)
	case errors.Is(exit.CtxErr, context.Canceled):
		return fail(executor.CauseKilled, text)
	case tr.Result == nil:
		summary := "executor exited without a result"
		if exit.ExitErr != nil {
			summary += ": " + exit.ExitErr.Error()
		}
		if s := strings.TrimSpace(exit.Stderr); s != "" {
			summary += "\n" + truncate(s, 1000)
		}
		return fail(executor.CauseError, summary)
	case tr.Result.IsError:
		if looksLikeBudget(tr) {
			if tr.RateLimit != nil && !tr.RateLimit.ResetsAt.IsZero() {
				text += "\n\nQuota resets at " + tr.RateLimit.ResetsAt.UTC().Format("2006-01-02 15:04 UTC") + "."
			}
			return fail(executor.CauseBudget, text)
		}
		return fail(executor.CauseError, text)
	}
	if q, ok := parseNeedsInput(text); ok {
		res.Status = executor.StatusNeedsInput
		res.Question = q
		res.Summary = truncate(text, maxSummary)
		return res
	}
	res.Status = executor.StatusCompleted
	if text == "" {
		text = "Run completed."
	}
	res.Summary = truncate(text, maxSummary)
	return res
}
```

- [ ] **Step 4: Run tests, lint, commit**

Run: `go vet ./... && go test -race ./internal/executor/claudecode/ && ~/go/bin/golangci-lint run ./...`
Expected: PASS, 0 issues.

```bash
git add internal/executor/claudecode
git commit -m "feat(claudecode): outcome mapping with needs-input and budget detection

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 3: Repo config and command builder

**Files:**
- Create: `internal/repoconfig/repoconfig.go`, `repoconfig_test.go`, `internal/executor/claudecode/command.go`, `command_test.go`

**Interfaces:**
- Produces:

```go
// repoconfig — .hivedispatch.yaml in the governed repo
type Config struct {
    Executor ExecutorConfig `yaml:"executor"`
    Guidance string         `yaml:"guidance"`   // appended to every prompt
}
type ExecutorConfig struct {
    Model          string   `yaml:"model"`
    PermissionMode string   `yaml:"permission_mode"`  // default dontAsk
    Tools          []string `yaml:"tools"`            // default ["default"]
    AllowedTools   []string `yaml:"allowed_tools"`    // e.g. "Bash(go test:*)"
    MaxBudgetUSD   float64  `yaml:"max_budget_usd"`
}
const FileName = ".hivedispatch.yaml"
func Load(dir string) (Config, error)   // missing file → defaults, nil error
// claudecode
type Config struct { Binary string; Model string }   // worker-level; Binary default "claude"
func buildArgs(cfg Config, rc repoconfig.ExecutorConfig, resume string, plan bool) []string
```

- [ ] **Step 1: Write the failing tests**

`internal/repoconfig/repoconfig_test.go`:

```go
package repoconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsWhenMissing(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.Executor.PermissionMode != "dontAsk" || len(c.Executor.Tools) != 1 || c.Executor.Tools[0] != "default" {
		t.Errorf("defaults = %+v", c.Executor)
	}
}

func TestLoadParsesAndValidates(t *testing.T) {
	dir := t.TempDir()
	body := "executor:\n  model: sonnet\n  permission_mode: acceptEdits\n  allowed_tools: [\"Bash(go test:*)\", Edit]\n  max_budget_usd: 2.5\nguidance: Run go test before finishing.\n"
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Executor.Model != "sonnet" || c.Executor.PermissionMode != "acceptEdits" || len(c.Executor.AllowedTools) != 2 || c.Executor.MaxBudgetUSD != 2.5 || c.Guidance == "" {
		t.Errorf("c = %+v", c)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("executor:\n  permission_mode: yolo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("invalid permission mode should error")
	}
}
```

`internal/executor/claudecode/command_test.go`:

```go
package claudecode

import (
	"strings"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/repoconfig"
)

func TestBuildArgsRun(t *testing.T) {
	rc := repoconfig.ExecutorConfig{PermissionMode: "dontAsk", Tools: []string{"default"}, AllowedTools: []string{"Bash(go test:*)", "Edit"}, MaxBudgetUSD: 2.5, Model: "sonnet"}
	got := strings.Join(buildArgs(Config{}, rc, "sess-1", false), " ")
	for _, want := range []string{"-p", "--output-format stream-json", "--verbose", "--permission-mode dontAsk", "--tools default", "--allowedTools Bash(go test:*) Edit", "--max-budget-usd 2.5", "--model sonnet", "--resume sess-1"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "--bare") {
		t.Error("--bare skips credentials and must never be passed")
	}
}

func TestBuildArgsPlan(t *testing.T) {
	got := strings.Join(buildArgs(Config{Model: "opus"}, repoconfig.ExecutorConfig{PermissionMode: "dontAsk", Tools: []string{"default"}}, "", true), " ")
	for _, want := range []string{"--permission-mode plan", "--json-schema", "--no-session-persistence", "--model opus", "--output-format json"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "--resume") || strings.Contains(got, "stream-json") {
		t.Errorf("plan must not resume or stream: %q", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/repoconfig/ ./internal/executor/claudecode/`
Expected: FAIL — `undefined: Load`, `undefined: buildArgs`.

- [ ] **Step 3: Write `internal/repoconfig/repoconfig.go`**

```go
// Package repoconfig reads .hivedispatch.yaml from a governed repository.
// Per-repo policy lives in the repo so changes go through the same review
// as code.
package repoconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// FileName is the config file at the repo root.
const FileName = ".hivedispatch.yaml"

// Config is the per-repo policy.
type Config struct {
	Executor ExecutorConfig `yaml:"executor"`
	Guidance string         `yaml:"guidance"`
}

// ExecutorConfig controls how the coding agent is invoked.
type ExecutorConfig struct {
	Model          string   `yaml:"model"`
	PermissionMode string   `yaml:"permission_mode"`
	Tools          []string `yaml:"tools"`
	AllowedTools   []string `yaml:"allowed_tools"`
	MaxBudgetUSD   float64  `yaml:"max_budget_usd"`
}

var permissionModes = map[string]bool{"acceptEdits": true, "auto": true, "bypassPermissions": true, "manual": true, "dontAsk": true, "plan": true}

// Load reads dir/.hivedispatch.yaml; a missing file yields defaults.
func Load(dir string) (Config, error) {
	var c Config
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return c, err
	}
	if err == nil {
		if err := yaml.Unmarshal(raw, &c); err != nil {
			return c, fmt.Errorf("%s: %w", FileName, err)
		}
	}
	if c.Executor.PermissionMode == "" {
		c.Executor.PermissionMode = "dontAsk"
	}
	if len(c.Executor.Tools) == 0 {
		c.Executor.Tools = []string{"default"}
	}
	if !permissionModes[c.Executor.PermissionMode] {
		return c, fmt.Errorf("%s: executor.permission_mode %q is not a Claude Code permission mode", FileName, c.Executor.PermissionMode)
	}
	return c, nil
}
```

- [ ] **Step 4: Write `internal/executor/claudecode/command.go`**

```go
package claudecode

import (
	"strconv"
	"strings"

	"github.com/thomasmeadows/hivedispatch/internal/repoconfig"
)

// Config is the worker-level executor configuration.
type Config struct {
	Binary string // default "claude"
	Model  string // default model when the repo config sets none
}

// planSchema is the structured output requested from Plan().
const planSchema = `{"type":"object","properties":{"files":{"type":"array","items":{"type":"string"}}},"required":["files"]}`

// buildArgs assembles the argv for a run or a plan. --bare is deliberately
// absent: it skips credential loading.
func buildArgs(cfg Config, rc repoconfig.ExecutorConfig, resume string, plan bool) []string {
	args := []string{"-p"}
	if plan {
		args = append(args, "--output-format", "json", "--permission-mode", "plan", "--json-schema", planSchema, "--no-session-persistence")
	} else {
		args = append(args, "--output-format", "stream-json", "--verbose", "--permission-mode", rc.PermissionMode)
		if resume != "" {
			args = append(args, "--resume", resume)
		}
	}
	if len(rc.Tools) > 0 {
		args = append(args, "--tools", strings.Join(rc.Tools, ","))
	}
	if len(rc.AllowedTools) > 0 && !plan {
		args = append(args, "--allowedTools")
		args = append(args, rc.AllowedTools...)
	}
	if rc.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(rc.MaxBudgetUSD, 'f', -1, 64))
	}
	if model := firstNonEmpty(rc.Model, cfg.Model); model != "" {
		args = append(args, "--model", model)
	}
	return args
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
```

- [ ] **Step 5: Run tests, lint, commit**

Run: `go vet ./... && go test -race ./internal/repoconfig/ ./internal/executor/claudecode/ && ~/go/bin/golangci-lint run ./...`
Expected: PASS, 0 issues.

```bash
git add internal/repoconfig internal/executor/claudecode
git commit -m "feat: repo-level .hivedispatch.yaml and Claude Code argv builder

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 4: The executor — Run and Plan with process supervision

**Files:**
- Create: `internal/executor/claudecode/claudecode.go`, `claudecode_test.go`, `testdata/fakeclaude.sh` (mode `+x`)

**Interfaces:**
- Produces:

```go
func New(cfg Config) *Executor
func (e *Executor) Name() string   // "claude-code"
func (e *Executor) Run(ctx, t executor.Task) (executor.Result, error)
func (e *Executor) Plan(ctx, t executor.Task) (executor.Footprint, error)
```

Behaviour of `Run`:
1. `rc := repoconfig.Load(t.Workspace)`; error → returned as `error` (the dispatcher turns it into Failed/Error).
2. Prompt on stdin = `t.Prompt` + (if `rc.Guidance != ""`) `"\n\n## Repository guidance\n\n" + rc.Guidance`.
3. `exec.CommandContext(ctx, binary, args...)`, `Dir = t.Workspace`, `Setpgid`, `cmd.Cancel` kills the process group with SIGKILL, `WaitDelay = 5s`.
4. Stdout scanned line by line (1 MiB buffer) into the parser; every line also appended to a bounded log buffer (2 MiB). Stderr captured (bounded).
5. `onToolUse`: when `count > t.StepBudget && t.StepBudget > 0`, set `stepTripped` and cancel the run context.
6. After `Wait`, `mapOutcome(transcript, exitInfo{...})`; `Result.Log` = combined stdout/stderr text.

`Plan`: `--output-format json`, `--json-schema`; parse the single JSON object; prefer `structured_output`, fall back to parsing `result` as JSON; return files.

- [ ] **Step 1: Write the fake CLI**

`testdata/fakeclaude.sh`:

```sh
#!/bin/sh
# Fake `claude` for tests. Chosen by FAKE_CLAUDE_MODE; records argv and stdin.
here=$(cd "$(dirname "$0")" && pwd)
[ -n "$FAKE_CLAUDE_ARGS_FILE" ] && printf '%s\n' "$@" > "$FAKE_CLAUDE_ARGS_FILE"
[ -n "$FAKE_CLAUDE_STDIN_FILE" ] && cat > "$FAKE_CLAUDE_STDIN_FILE"
case "$FAKE_CLAUDE_MODE" in
  success)    cat "$here/stream_success.jsonl" ;;
  error)      cat "$here/stream_error.jsonl"; exit 1 ;;
  needsinput) cat "$here/stream_needsinput.jsonl" ;;
  hang)       head -1 "$here/stream_success.jsonl"; sleep 60 ;;
  manysteps)
    head -1 "$here/stream_success.jsonl"
    i=0
    while [ $i -lt 50 ]; do
      printf '{"type":"assistant","session_id":"s","message":{"content":[{"type":"tool_use","id":"t%s","name":"Read","input":{"file_path":"/x"}}]}}\n' "$i"
      i=$((i+1))
      sleep 0.02
    done
    sleep 60 ;;
  plan)       printf '{"type":"result","subtype":"success","is_error":false,"result":"{\\"files\\":[\\"a.go\\"]}","structured_output":{"files":["a.go","b.go"]},"session_id":"p"}\n' ;;
  crash)      echo "segfault" >&2; exit 139 ;;
  *)          echo "unknown FAKE_CLAUDE_MODE=$FAKE_CLAUDE_MODE" >&2; exit 2 ;;
esac
```

`chmod +x internal/executor/claudecode/testdata/fakeclaude.sh`

- [ ] **Step 2: Write the failing tests**

`claudecode_test.go`:

```go
package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
)

func fakeExec(t *testing.T, mode string) (*Executor, string) {
	t.Helper()
	bin, err := filepath.Abs("testdata/fakeclaude.sh")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_CLAUDE_MODE", mode)
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_CLAUDE_ARGS_FILE", args)
	t.Setenv("FAKE_CLAUDE_STDIN_FILE", filepath.Join(t.TempDir(), "stdin"))
	return New(Config{Binary: bin}), args
}

func task(t *testing.T) executor.Task {
	t.Helper()
	return executor.Task{TicketKey: "HIVE-1", Prompt: "do it", Workspace: t.TempDir(), StepBudget: 10}
}

func TestRunSuccess(t *testing.T) {
	e, argsFile := fakeExec(t, "success")
	res, err := e.Run(context.Background(), task(t))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != executor.StatusCompleted || res.Summary != "hello" || res.ResumeToken != "5b89d684-8de0-4b4e-99b8-a7f283ffd611" {
		t.Errorf("res = %+v", res)
	}
	if !strings.Contains(res.Log, `"type": "result"`) {
		t.Error("log should contain the raw stream")
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "stream-json") || strings.Contains(string(args), "--resume") {
		t.Errorf("args = %s", args)
	}
	stdin, _ := os.ReadFile(os.Getenv("FAKE_CLAUDE_STDIN_FILE"))
	if string(stdin) != "do it" {
		t.Errorf("stdin = %q", stdin)
	}
}

func TestRunPassesResumeAndGuidance(t *testing.T) {
	e, argsFile := fakeExec(t, "success")
	tk := task(t)
	tk.ResumeToken = "old-session"
	if err := os.WriteFile(filepath.Join(tk.Workspace, ".hivedispatch.yaml"), []byte("guidance: Always run make test.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), tk); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--resume\nold-session") {
		t.Errorf("args = %s", args)
	}
	stdin, _ := os.ReadFile(os.Getenv("FAKE_CLAUDE_STDIN_FILE"))
	if !strings.Contains(string(stdin), "Always run make test.") {
		t.Errorf("guidance missing from prompt: %q", stdin)
	}
}

func TestRunNeedsInput(t *testing.T) {
	e, _ := fakeExec(t, "needsinput")
	res, err := e.Run(context.Background(), task(t))
	if err != nil || res.Status != executor.StatusNeedsInput || !strings.HasPrefix(res.Question, "Should the flag") {
		t.Errorf("res=%+v err=%v", res, err)
	}
}

func TestRunErrorTranscript(t *testing.T) {
	e, _ := fakeExec(t, "error")
	res, err := e.Run(context.Background(), task(t))
	if err != nil || res.Status != executor.StatusFailed || res.StopCause != executor.CauseBudget {
		t.Errorf("res=%+v err=%v", res, err)
	}
	if len(res.ChangedFiles) != 1 || res.ChangedFiles[0] != "cmd/app/main.go" {
		t.Errorf("changed = %v", res.ChangedFiles)
	}
}

func TestRunCrashWithoutResult(t *testing.T) {
	e, _ := fakeExec(t, "crash")
	res, err := e.Run(context.Background(), task(t))
	if err != nil || res.Status != executor.StatusFailed || res.StopCause != executor.CauseError || !strings.Contains(res.Summary, "segfault") {
		t.Errorf("res=%+v err=%v", res, err)
	}
}

func TestRunTimeoutKillsProcessGroup(t *testing.T) {
	e, _ := fakeExec(t, "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	res, err := e.Run(ctx, task(t))
	if err != nil || res.StopCause != executor.CauseTimeout {
		t.Errorf("res=%+v err=%v", res, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("hung child was not killed promptly")
	}
	if res.ResumeToken == "" {
		t.Error("session id from init should survive a timeout")
	}
}

func TestRunStepBudget(t *testing.T) {
	e, _ := fakeExec(t, "manysteps")
	tk := task(t)
	tk.StepBudget = 5
	start := time.Now()
	res, err := e.Run(context.Background(), tk)
	if err != nil || res.StopCause != executor.CauseStepBudget {
		t.Errorf("res=%+v err=%v", res, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("step budget did not stop the run promptly")
	}
}

func TestPlanUsesStructuredOutput(t *testing.T) {
	e, argsFile := fakeExec(t, "plan")
	fp, err := e.Plan(context.Background(), task(t))
	if err != nil || len(fp.Files) != 2 || fp.Files[1] != "b.go" {
		t.Errorf("fp=%+v err=%v", fp, err)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--json-schema") || !strings.Contains(string(args), "plan") {
		t.Errorf("args = %s", args)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/executor/claudecode/ -run 'Run|Plan'`
Expected: FAIL — `undefined: New`.

- [ ] **Step 4: Write `claudecode.go`**

```go
package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
	"github.com/thomasmeadows/hivedispatch/internal/repoconfig"
)

const (
	maxLineBytes = 1 << 20 // one stream-json line
	maxLogBytes  = 2 << 20 // kept from stdout+stderr for Result.Log
	waitDelay    = 5 * time.Second
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

// boundedBuffer keeps the first n bytes written to it.
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

func (e *Executor) command(ctx context.Context, dir string, args []string, stdin string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, e.cfg.Binary, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(os.Environ(), "CLAUDECODE=") // never inherit a parent session marker
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Kill the whole group so tool subprocesses die with the agent.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = waitDelay
	return cmd
}

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

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var stepTripped bool
	var stepMu sync.Mutex
	parser := newStreamParser(t.Workspace, func(count int) {
		if t.StepBudget > 0 && count > t.StepBudget {
			stepMu.Lock()
			stepTripped = true
			stepMu.Unlock()
			cancel()
		}
	})

	cmd := e.command(runCtx, t.Workspace, buildArgs(e.cfg, rc.Executor, t.ResumeToken, false), promptText)
	logBuf := &boundedBuffer{n: maxLogBytes}
	stderr := &boundedBuffer{n: 64 << 10}
	cmd.Stderr = io.MultiWriter(stderr, logBuf)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return executor.Result{}, err
	}
	if err := cmd.Start(); err != nil {
		return executor.Result{}, fmt.Errorf("start %s: %w", e.cfg.Binary, err)
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), maxLineBytes)
	for sc.Scan() {
		line := sc.Bytes()
		_, _ = logBuf.Write(append(append([]byte(nil), line...), '\n'))
		parser.Line(line)
	}
	waitErr := cmd.Wait()

	stepMu.Lock()
	tripped := stepTripped
	stepMu.Unlock()
	exit := exitInfo{CtxErr: ctx.Err(), StepTripped: tripped, ExitErr: waitErr, Stderr: stderr.String()}
	if tripped {
		exit.CtxErr = nil // the cancellation was ours, not the caller's
	}
	res := mapOutcome(parser.Transcript(), exit)
	res.Log = logBuf.String()
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
	cmd := e.command(ctx, t.Workspace, buildArgs(e.cfg, rc.Executor, "", true), planPrompt)
	out, err := cmd.Output()
	if err != nil {
		return executor.Footprint{}, fmt.Errorf("plan: %w", err)
	}
	var r resultMsg
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
```

Note on `exit.CtxErr`: `runCtx` is cancelled both by the caller's ctx and by the step-budget trip; `mapOutcome` needs the *caller's* `ctx.Err()`, which is why `exit.CtxErr` reads `ctx.Err()` (the parent) and step-trip wins when the parent is still live.

- [ ] **Step 5: Run tests, lint, commit**

Run: `go vet ./... && go test -race -count=2 ./internal/executor/claudecode/ && ~/go/bin/golangci-lint run ./...`
Expected: PASS twice, 0 issues. If `TestRunTimeoutKillsProcessGroup` is slow, `Setpgid`/`Cancel` are not taking effect — check that `cmd.Cancel` is set before `Start`.

```bash
git add internal/executor/claudecode
git commit -m "feat(claudecode): headless Claude Code executor with process-group supervision

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 5: Worker config and CLI wiring

**Files:**
- Modify: `internal/config/config.go`, `internal/config/config_test.go`, `cmd/hivedispatch/main.go`, `cmd/hivedispatch/main_test.go`

**Interfaces:**
- Produces:

```go
Config.Executor string        `yaml:"executor"`   // "claude" (default) | "fake"
Config.Claude   ClaudeConfig  `yaml:"claude"`
type ClaudeConfig struct { Binary string `yaml:"binary"`; Model string `yaml:"model"` }
```

- [ ] **Step 1: Tests**

Append to `internal/config/config_test.go`:

```go
func TestLoadExecutorDefaults(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	t.Setenv("HIVE_GITHUB_TOKEN", "gh")
	cfg, err := Load(writeTemp(t, validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Executor != "claude" || cfg.Claude.Binary != "claude" {
		t.Errorf("cfg = %+v", cfg)
	}
	if _, err := Load(writeTemp(t, validYAML+"executor: gpt\n")); err == nil || !strings.Contains(err.Error(), "executor") {
		t.Errorf("err = %v", err)
	}
}
```

- [ ] **Step 2: Config changes**

Add fields, defaults (`def(&c.Executor, "claude")`, `def(&c.Claude.Binary, "claude")`), and validation (`executor: want claude or fake, got %q`).

- [ ] **Step 3: CLI**

In `runRun`: replace the `-placeholder` flag with `executorFlag := fs.String("executor", "", "override config executor: claude or fake")` and `placeholder` kept; choose:

```go
	name := cfg.Executor
	if *executorFlag != "" {
		name = *executorFlag
	}
	var ex executor.Executor
	switch name {
	case "fake":
		f := exfake.New()
		f.Placeholder = *placeholder
		ex = f
	case "claude":
		ex = claudecode.New(claudecode.Config{Binary: cfg.Claude.Binary, Model: cfg.Claude.Model})
	default:
		fmt.Fprintf(stderr, "unknown executor %q\n", name)
		return 2
	}
```

Update `usage`: `run   [-config P] [-once] [-executor claude|fake] [-placeholder]`. Remove the "fake executor until Phase 4" wording.

- [ ] **Step 4: Run, lint, commit**

Run: `go vet ./... && go test -race ./... && ~/go/bin/golangci-lint run ./... && go build -o bin/hivedispatch ./cmd/hivedispatch`

```bash
git add -A
git commit -m "feat(cli): select executor (claude default, fake for trials)

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 6: Docs, dogfood config, decisions, manual end-to-end

**Files:**
- Create: `.hivedispatch.yaml` (this repo's own policy)
- Modify: `README.md`, `docs/setup.md`, `docs/decisions.md`

- [ ] **Step 1: This repo's `.hivedispatch.yaml`**

```yaml
# HiveDispatch policy for this repository. Read by the worker from the ticket worktree.
executor:
  permission_mode: dontAsk
  tools: [default]
  allowed_tools:
    - Read
    - Edit
    - Write
    - Glob
    - Grep
    - "Bash(go build:*)"
    - "Bash(go test:*)"
    - "Bash(go vet:*)"
    - "Bash(gofmt:*)"
    - "Bash(git add:*)"
    - "Bash(git commit:*)"
    - "Bash(git status:*)"
    - "Bash(git diff:*)"
    - "Bash(git log:*)"
guidance: |
  Go 1.27, stdlib only plus gopkg.in/yaml.v3. Every external system sits behind an
  interface in internal/ with a fake; unit tests never touch the network. Run
  `go vet ./... && go test -race ./...` before finishing. Record design decisions in
  docs/decisions.md with the alternative rejected. Commit messages use conventional
  prefixes (feat:, fix:, test:, docs:, chore:).
```

- [ ] **Step 2: `docs/setup.md` additions**

Append a section:

```markdown
## 8. Claude Code

The worker shells out to the `claude` CLI. Log in once as the user that runs the worker (`claude` then `/login`) — headless runs reuse the stored credentials. Do not set `--bare` anywhere; it skips credential loading.

Per-repo policy lives in `.hivedispatch.yaml` at the repository root:

```yaml
executor:
  model: sonnet                 # optional; default is the CLI's default
  permission_mode: dontAsk      # acceptEdits | auto | bypassPermissions | manual | dontAsk | plan
  tools: [default]              # built-in tool set; use a list to restrict
  allowed_tools:                # pre-approved patterns for dontAsk mode
    - Edit
    - "Bash(go test:*)"
  max_budget_usd: 5             # optional; API-billing accounts only
guidance: |
  Free text appended to every prompt for this repo.
```

`dontAsk` fails closed: anything not in `allowed_tools` is denied and the agent must work around it or ask. `bypassPermissions` is for sandboxed runs only.

The run log for each attempt is saved to the state branch under `logs/<KEY>/<timestamp>.log`.
```

- [ ] **Step 3: README status and decisions**

README status → "Phases 0–4 of the MVP are done. `hivedispatch run` takes a Jira ticket to a GitHub PR with real code from Claude Code. Phase 5 adds model-backed triage and the needs-info loop."

`docs/decisions.md` append:

```markdown
## 2026-09-19 — Never pass --bare to Claude Code

Decided: the adapter never uses `--bare`. Discovered: on 2.1.278, `--bare` skips loading stored credentials, so headless runs fail with "Not logged in". Hooks and plugins are disabled instead through the repo's tool allowlist.

## 2026-09-19 — Rate-limit events are the budget signal

Decided: `rate_limit_event` messages on the stream (status, resetsAt, per-window utilization) are the primary budget signal; `api_error_status == 429` and "usage limit" text are fallbacks. The spec assumed subscription plans expose no quota data; the stream does. The reset time goes into the recovery comment, and the utilization is the input for a later pre-flight check.

## 2026-09-19 — Per-repo policy is read by the executor from the worktree

Decided: the Claude Code adapter reads `.hivedispatch.yaml` from the ticket worktree at run time. Rejected: threading repo config through `executor.Task`. Why: keeps the executor contract unchanged and the policy versioned with the code it governs — the branch the agent works on carries the rules it works under.

## 2026-09-19 — Plan() prefers structured_output

Decided: with `--json-schema`, Claude Code returns both `structured_output` (object) and `result` (JSON string); the adapter uses the former and falls back to parsing the latter. Verified against 2.1.278.
```

- [ ] **Step 4: Run everything, commit, push**

```bash
go vet ./... && go test -race ./... && ~/go/bin/golangci-lint run ./... && go build -o bin/hivedispatch ./cmd/hivedispatch
git add -A
git commit -m "docs: Claude Code setup, repo policy file, phase 4 decisions

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
git push
```

- [ ] **Step 5: Manual end-to-end (user's Jira + a scratch GitHub repo + logged-in Claude Code)**

Create a trivial Ready ticket on the scratch repo, e.g. "Add a `--version` flag to cmd/app that prints `app 0.1.0`", then:

```sh
./bin/hivedispatch run -once
```

Expected: branch `hive/<KEY>` with the agent's commits (plus a `hive: checkpoint` commit only if the agent left uncommitted changes), a PR titled `<KEY>: <summary>`, ticket in In Review with the PR link and the agent's summary, `hive/state` holding `runs/<KEY>.json` (phase `done`, `resumeToken` set) and `logs/<KEY>/<ts>.log`.

Then force a step-budget stop: set `step_budget: 3` in the worker config, move the ticket back to Ready, run again. Expected: Failed/StepBudget comment ("step budget exhausted… Attempt 2 of 3"), WIP committed and pushed if anything changed, no new PR, claim released, ticket back in Ready.

Record surprises (permission denials, stream shapes, timing) in `docs/decisions.md`.

---

## Self-review

**Spec coverage (Phase 4 of the MVP strategy):** stream parser with real fixtures ✔ (Task 1); session id as opaque resume token ✔; step budget by tool-use count with process-group kill ✔ (Task 4); timeout via context ✔; stop-cause mapping incl. budget with reset time ✔ (Task 2); `HIVE_NEEDS_INPUT` detection ✔; `Plan()` with plan mode + JSON schema ✔; repo-level policy in `.hivedispatch.yaml` with `dontAsk` + allowlist default ✔ (Task 3); CLI selection and docs ✔ (Tasks 5–6); E2E with forced step-budget stop ✔ (Task 6).

**Type consistency:** `transcript`/`resultMsg`/`rateLimit` (Task 1) used by `mapOutcome` (Task 2) and `Run`/`Plan` (Task 4). `exitInfo` fields match between Tasks 2 and 4. `repoconfig.ExecutorConfig` (Task 3) consumed by `buildArgs` and `Run`. `Config{Binary, Model}` (Task 3) constructed in the CLI (Task 5). `prompt.NeedsInputMarker` reused from Phase 2.

**Placeholder scan:** none.
