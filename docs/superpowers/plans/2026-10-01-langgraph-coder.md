# LangGraph Coding Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `code_with: langgraph` gives the graph workflow's code step HiveDispatch's own coding agent, inside `hivegraph`. The agent is a LangGraph tool loop on an OpenAI-compatible model, with file tools confined to the worktree and an allowlisted shell.

**Architecture:**
- **Python** gets a new package, `graph/hivegraph/coder/`:
  - `tools.py`: a `Workspace` class holding the tool implementations, with path confinement and the command allowlist.
  - `agent.py`: a `StateGraph` with a model node and LangGraph's `ToolNode`, plus the step budget, the stop flag, error mapping and output trimming.
- **Workflow:** the `code` node in `graph.py` branches to the coder when `code_with == "langgraph"`, and keeps the agent's messages in checkpointed state.
- **Go:** gains only validation and one new task field.

**Tech Stack:** Python ≥3.11 with `langgraph` (`StateGraph`, `ToolNode`), `langchain-core` tools and messages, and `langchain-openai`. Go 1.27 stdlib. Vue for the two form fields.

**Spec:** `docs/superpowers/specs/2026-10-01-langgraph-coder-design.md`

## Global Constraints

- No new dependencies: Go stays stdlib plus yaml.v3, and Python uses only what `graph/pyproject.toml` already lists.
- No deprecated APIs: no `create_react_agent`, and no `langchain` package.
- Before each commit, run Go vet, `go test -race` and golangci-lint on the touched packages, and ruff check, ruff format and pytest when `graph/` changed. Never pipe a check's exit status away.
- `code_with`: `claude | codex | fake | langgraph`. `langgraph` is valid only with `role: coding`.
- The policy key is `executor.langgraph.allowed_commands` (`[]string`). Blank entries are rejected.
- Limits:
  - **Files:** `read_file` 16 KiB, default limit 2000 lines; `list_files` at most 500 paths; `search` at most 200 hits.
  - **Commands:** `run_command` times out at 10 minutes, and its output is the last 8 KiB.
  - **Context:** tool outputs older than the last 20 messages become `[output trimmed]`.
  - **Nudges:** at most 2 when the model replies without a tool call.
- Coder model: `task.model or task.chat_model.model` on the graph provider.
- Step budget: tool calls count across all code rounds of one run.
- Cause mapping: rate limit → `budget`; other model error → `error`; stop flag → `killed`; budget exhausted → `step_budget`.
- Commit messages are Conventional Commits and end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

1. **Path escapes:** `../x`, absolute paths, a symlink inside the worktree pointing out, and `.git/config` are all refused. (Task 2 tests.)
2. **Shell smuggling:** `go test ./...; rm -rf x`, `go test $(rm x)` and `go test | sh` must never run anything but `go` with literal args, and unbalanced quotes are refused. (Task 2 tests.)
3. **A model that never calls `finish`:** the run ends after 2 nudges, with the last text as the summary. It doesn't loop forever. (Task 3 test.)
4. **A long fix loop:** context stays bounded by trimming, and the step budget spans rounds. (Task 3 and Task 4 tests.)
5. **Resume after `ask_human`:** the reply continues the same conversation, and the coder sees its earlier messages. (Task 4 test.)

---

### Task 1: Go — `code_with: langgraph`, `allowed_commands`, task field

**Files:**
- Modify: `internal/config/agents.go`, `internal/config/agents_test.go`
- Modify: `internal/repoconfig/repoconfig.go`, `internal/repoconfig/repoconfig_test.go`
- Modify: `internal/executor/langgraph/langgraph.go`, `internal/executor/langgraph/langgraph_test.go`

**Produces:**
- `repoconfig.ExecutorConfig.LangGraph LangGraphConfig{AllowedCommands []string \`yaml:"allowed_commands"\`}` with `yaml:"langgraph"`.
- The task JSON field `allowed_commands` (`[]string`, never null).

- [ ] **Step 1: Failing tests.**

```go
// internal/config/agents_test.go
func TestParseAgentsCodeWithLangGraph(t *testing.T) {
	got, err := ParseAgents([]byte("agents:\n  - name: d\n    executor: langgraph\n    code_with: langgraph\n"))
	if err != nil || got[0].CodeWith != "langgraph" {
		t.Fatalf("got %+v err %v", got, err)
	}
	_, err = ParseAgents([]byte("agents:\n  - name: p\n    role: planning\n    executor: langgraph\n    code_with: langgraph\n"))
	if err == nil || !strings.Contains(err.Error(), "coding") {
		t.Fatalf("planning with code_with langgraph: err %v", err)
	}
}
```

```go
// internal/repoconfig/repoconfig_test.go
func TestParseLangGraphAllowedCommands(t *testing.T) {
	c, err := Parse([]byte("executor:\n  langgraph:\n    allowed_commands:\n      - go test\n      - git diff\n"))
	if err != nil || len(c.Executor.LangGraph.AllowedCommands) != 2 {
		t.Fatalf("got %+v err %v", c.Executor.LangGraph, err)
	}
	if _, err := Parse([]byte("executor:\n  langgraph:\n    allowed_commands:\n      - \"\"\n")); err == nil {
		t.Fatal("blank allowed command accepted")
	}
}
```

In `internal/executor/langgraph/langgraph_test.go`, extend `gitRepo`'s policy to `"checks:\n  - go test ./...\nguidance: be nice\nexecutor:\n  langgraph:\n    allowed_commands:\n      - go vet\n"`. In `TestRunPassesTaskAndMapsResult` add:

```go
	if ac, _ := task["allowed_commands"].([]any); len(ac) != 1 || ac[0] != "go vet" {
		t.Fatalf("allowed_commands %v", task["allowed_commands"])
	}
```

and a new test:

```go
func TestAdviseWithLangGraphCoderIsAnError(t *testing.T) {
	bin, _ := fakeGraph(t, "")
	if _, err := newExec(bin).Advise(context.Background(), executor.Advice{CodeWith: "langgraph"}); err == nil || !strings.Contains(err.Error(), "coding") {
		t.Fatalf("err %v", err)
	}
}
```

- [ ] **Step 2: Run them and see them fail.** Run `go test ./internal/config/ ./internal/repoconfig/ ./internal/executor/langgraph/`.
- [ ] **Step 3: Implement.**
  - In `ParseAgents`'s langgraph case, accept `langgraph`:

```go
			if a.CodeWith != "claude" && a.CodeWith != "codex" && a.CodeWith != "fake" && a.CodeWith != "langgraph" {
				problems = append(problems, fmt.Sprintf("agents[%d].code_with: want claude, codex, langgraph or fake, got %q", i, a.CodeWith))
			}
			if a.CodeWith == "langgraph" && a.Role != RoleCoding {
				problems = append(problems, fmt.Sprintf("agents[%d].code_with: langgraph codes only: a %s agent runs read-only through claude or codex", i, a.Role))
			}
```

    `a.Role` defaults to coding before this switch; check that it does, and move the defaulting above if not. Update the `CodeWith` field comment.
  - In repoconfig, `ExecutorConfig` gets `LangGraph LangGraphConfig \`yaml:"langgraph"\``:

```go
// LangGraphConfig is the code_with: langgraph agent's policy.
type LangGraphConfig struct {
	AllowedCommands []string `yaml:"allowed_commands"` // word prefixes run_command may use; checks are always allowed
}
```

    In `Parse`, after the checks loop:

```go
	for i, a := range c.Executor.LangGraph.AllowedCommands {
		if strings.TrimSpace(a) == "" {
			return c, fmt.Errorf("%s: executor.langgraph.allowed_commands[%d] is empty", FileName, i)
		}
	}
```

  - In `executor/langgraph`, `task` gets `AllowedCommands []string \`json:"allowed_commands"\``, filled from `rc.Executor.LangGraph.AllowedCommands`, or `[]string{}` when nil.
  - In `inner()`, before the lookup:

```go
	if codeWith == "langgraph" {
		return nil, errors.New("langgraph: code_with langgraph is for coding agents only; planning and review run through claude or codex")
	}
```

- [ ] **Step 4: Run them and see them pass.** Run `go test -race ./internal/config/ ./internal/repoconfig/ ./internal/executor/...`, then lint those packages.
- [ ] **Step 5: Commit.** `feat(config): code_with langgraph for coding agents, executor.langgraph.allowed_commands`.

### Task 2: Python — `coder/tools.py`, the workspace tools and the allowlist

**Files:**
- Create: `graph/hivegraph/coder/__init__.py`, `graph/hivegraph/coder/tools.py`, `graph/tests/test_coder_tools.py`
- Modify: `graph/hivegraph/task.py` (`allowed_commands: list[str]`)
- Modify: `graph/pyproject.toml` (`packages = ["hivegraph", "hivegraph.coder"]`)

**Produces:**
- `Workspace(root: str, allowed: list[str], checks: list[str], path: list[str], command_timeout: int = 600)` with these methods, each returning a `str`:
  - `read_file(path, offset=0, limit=2000)`
  - `write_file(path, content)`
  - `edit_file(path, old, new)`
  - `list_files(glob="**/*")`
  - `search(pattern, glob="**/*")`
  - `run_command(command)`
- `Workspace.changed: list[str]`, the relative paths written or edited, in order and without duplicates.
- Errors are returned as strings starting `error:`. They are never raised, so the agent sees them.

- [ ] **Step 1: Failing tests** (`tests/test_coder_tools.py`).

```python
import os
import subprocess

import pytest

from hivegraph.coder.tools import Workspace


@pytest.fixture
def ws(tmp_path):
    subprocess.run(["git", "init", "-q", str(tmp_path)], check=True)
    (tmp_path / "a.txt").write_text("one\ntwo\nthree\n")
    return Workspace(str(tmp_path), allowed=["echo", "git status"], checks=["echo checked && true"], path=[])


def test_read_numbers_lines_and_pages(ws):
    assert ws.read_file("a.txt") == "1\tone\n2\ttwo\n3\tthree"
    assert ws.read_file("a.txt", offset=1, limit=1) == "2\ttwo"


def test_write_and_edit_track_changes(ws, tmp_path):
    assert ws.write_file("sub/new.go", "package x\n").startswith("wrote")
    assert (tmp_path / "sub" / "new.go").read_text() == "package x\n"
    assert ws.edit_file("a.txt", "two", "TWO").startswith("edited")
    assert "TWO" in (tmp_path / "a.txt").read_text()
    assert ws.changed == ["sub/new.go", "a.txt"]


def test_edit_must_match_exactly_once(ws):
    assert ws.edit_file("a.txt", "nope", "x") == "error: old text not found in a.txt"
    ws.write_file("b.txt", "x x\n")
    assert ws.edit_file("b.txt", "x", "y") == "error: old text matches 2 times in b.txt; include more context"


@pytest.mark.parametrize("bad", ["../escape.txt", "/etc/passwd", ".git/config", "sub/../../x"])
def test_paths_outside_the_worktree_are_refused(ws, bad):
    assert ws.read_file(bad).startswith("error:")
    assert ws.write_file(bad, "x").startswith("error:")


def test_symlink_out_is_refused(ws, tmp_path):
    outside = tmp_path.parent / "outside.txt"
    outside.write_text("secret")
    os.symlink(outside, tmp_path / "link.txt")
    assert ws.read_file("link.txt").startswith("error:")


def test_list_and_search_skip_git(ws):
    ws.write_file("src/m.go", "func Main() {}\n")
    files = ws.list_files().splitlines()
    assert "a.txt" in files and "src/m.go" in files and not any(f.startswith(".git") for f in files)
    assert ws.search("func Main") == "src/m.go:1: func Main() {}"
    assert ws.search("([") .startswith("error:")


def test_allowed_command_runs(ws):
    out = ws.run_command("echo hello world")
    assert "hello world" in out and "exit 0" in out


@pytest.mark.parametrize(
    "cmd",
    ["rm -rf .", "echoo hi", "git push", "sh -c 'echo hi'"],
)
def test_disallowed_commands_are_refused(ws, cmd):
    out = ws.run_command(cmd)
    assert out.startswith("error: not allowed") and "echo" in out


def test_shell_syntax_cannot_smuggle(ws, tmp_path):
    out = ws.run_command(f"echo hi; touch {tmp_path}/pwned")
    assert not (tmp_path / "pwned").exists()
    assert "hi;" in out  # the ; is a literal argument to echo
    out = ws.run_command(f"echo $(touch {tmp_path}/pwned2)")
    assert not (tmp_path / "pwned2").exists()
    assert ws.run_command("echo 'unbalanced").startswith("error:")


def test_checks_run_through_the_shell(ws):
    out = ws.run_command("echo checked && true")
    assert "checked" in out and "exit 0" in out


def test_command_timeout(tmp_path):
    w = Workspace(str(tmp_path), allowed=["sleep"], checks=[], path=[], command_timeout=1)
    assert "timed out after 1s" in w.run_command("sleep 5")
```

- [ ] **Step 2: Run them and see them fail.** Run `graph/.venv/bin/pytest graph/tests/test_coder_tools.py -q`. Expected: ImportError.
- [ ] **Step 3: Implement `tools.py`.**

```python
"""The coding agent's tools: files confined to the worktree, and a shell
limited to the policy's allowed command prefixes plus its checks."""

import os
import re
import shlex
import subprocess
from pathlib import Path

READ_CAP = 16 * 1024
OUT_TAIL = 8 * 1024
MAX_LIST = 500
MAX_HITS = 200


class Workspace:
    def __init__(self, root, allowed, checks, path, command_timeout=600):
        self.root = Path(root).resolve()
        self.allowed = [shlex.split(a) for a in allowed if a.strip()]
        self.allowed_text = list(allowed)
        self.checks = [c.strip() for c in checks]
        self.path = list(path)
        self.command_timeout = command_timeout
        self.changed: list[str] = []

    def _resolve(self, path: str) -> Path | str:
        p = (self.root / path).resolve()
        if p != self.root and self.root not in p.parents:
            return f"error: {path} is outside the worktree"
        rel = p.relative_to(self.root)
        if rel.parts and rel.parts[0] == ".git":
            return f"error: {path} is inside .git"
        return p

    def _mark(self, p: Path) -> None:
        rel = str(p.relative_to(self.root))
        if rel not in self.changed:
            self.changed.append(rel)

    def read_file(self, path: str, offset: int = 0, limit: int = 2000) -> str:
        p = self._resolve(path)
        if isinstance(p, str):
            return p
        if not p.is_file():
            return f"error: {path} is not a file"
        lines = p.read_text(errors="replace").splitlines()
        out = "\n".join(f"{i + 1}\t{line}" for i, line in enumerate(lines[offset : offset + limit], start=offset))
        if len(out) > READ_CAP:
            out = out[:READ_CAP] + "\n…[truncated: read a smaller range]"
        return out

    def write_file(self, path: str, content: str) -> str:
        p = self._resolve(path)
        if isinstance(p, str):
            return p
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(content)
        self._mark(p)
        return f"wrote {path} ({len(content)} bytes)"

    def edit_file(self, path: str, old: str, new: str) -> str:
        p = self._resolve(path)
        if isinstance(p, str):
            return p
        if not p.is_file():
            return f"error: {path} is not a file"
        text = p.read_text(errors="replace")
        n = text.count(old) if old else 0
        if n == 0:
            return f"error: old text not found in {path}"
        if n > 1:
            return f"error: old text matches {n} times in {path}; include more context"
        p.write_text(text.replace(old, new, 1))
        self._mark(p)
        return f"edited {path}"

    def _files(self, glob: str):
        for p in sorted(self.root.glob(glob)):
            rel = p.relative_to(self.root)
            if rel.parts and rel.parts[0] == ".git":
                continue
            if p.is_file() and isinstance(self._resolve(str(rel)), Path):
                yield p, str(rel)

    def list_files(self, glob: str = "**/*") -> str:
        out = []
        for _, rel in self._files(glob):
            out.append(rel)
            if len(out) >= MAX_LIST:
                out.append(f"…[more than {MAX_LIST} files: narrow the glob]")
                break
        return "\n".join(out)

    def search(self, pattern: str, glob: str = "**/*") -> str:
        try:
            rx = re.compile(pattern)
        except re.error as exc:
            return f"error: bad pattern: {exc}"
        hits = []
        for p, rel in self._files(glob):
            try:
                text = p.read_text()
            except (UnicodeDecodeError, OSError):
                continue
            for n, line in enumerate(text.splitlines(), start=1):
                if rx.search(line):
                    hits.append(f"{rel}:{n}: {line}")
                    if len(hits) >= MAX_HITS:
                        return "\n".join(hits) + f"\n…[more than {MAX_HITS} hits]"
        return "\n".join(hits)

    def _env(self) -> dict[str, str]:
        env = dict(os.environ)
        if self.path:
            env["PATH"] = os.pathsep.join([*self.path, env.get("PATH", "")])
        return env

    def run_command(self, command: str) -> str:
        cmd = command.strip()
        if cmd in self.checks:
            argv, shell = ["sh", "-c", cmd], True
        else:
            try:
                argv = shlex.split(cmd)
            except ValueError as exc:
                return f"error: cannot parse command: {exc}"
            if not argv or not any(argv[: len(a)] == a for a in self.allowed):
                allowed = ", ".join(self.allowed_text + self.checks) or "nothing"
                return f"error: not allowed: {cmd!r}. Allowed command prefixes: {allowed}"
            shell = False
        try:
            p = subprocess.run(argv, cwd=self.root, env=self._env(), capture_output=True, text=True,
                               errors="replace", timeout=self.command_timeout)
        except subprocess.TimeoutExpired:
            return f"error: timed out after {self.command_timeout}s"
        except OSError as exc:
            return f"error: {exc}"
        out = (p.stdout + p.stderr)[-OUT_TAIL:]
        via = " (via sh)" if shell else ""
        return f"{out}\n(exit {p.returncode}{via})".strip()
```

  `task.py` gets `allowed_commands: list[str] = field(default_factory=list)`. `coder/__init__.py` holds a docstring.
- [ ] **Step 4: Run the tests, ruff check and ruff format, and see them pass.**
- [ ] **Step 5: Commit.** `feat(graph): coder workspace tools confined to the worktree, with an allowlisted shell`.

### Task 3: Python — `coder/agent.py`, the tool loop

**Files:**
- Create: `graph/hivegraph/coder/agent.py`, `graph/hivegraph/coder/prompts.py`, `graph/tests/test_coder_agent.py`

**Consumes:** `Workspace` from Task 2, `stop.requested`, and `AgentResult` from `hivegraph.agentrun`.

**Produces:**

```python
@dataclass
class CoderRun:
    result: AgentResult           # same shape the code node handles for agent-run
    messages: list[BaseMessage]   # the whole conversation, for the next round
    steps_used: int               # tool calls so far in this run, across rounds

def run(task, prompt: str, messages: list, steps_used: int, model, now=datetime.now) -> CoderRun
def trim(messages: list, keep: int = 20) -> list   # older ToolMessage contents -> "[output trimmed]"
```

- [ ] **Step 1: Failing tests** (`tests/test_coder_agent.py`). The helper is a fake that accepts `bind_tools`:

```python
import subprocess

import pytest
from langchain_core.language_models.fake_chat_models import GenericFakeChatModel
from langchain_core.messages import AIMessage, HumanMessage, ToolMessage

from hivegraph import stop
from hivegraph.coder.agent import run, trim
from hivegraph.task import ChatModel, Task


class ToolFake(GenericFakeChatModel):
    def bind_tools(self, tools, **kw):
        return self


def call(name, args, i="c"):
    return AIMessage(content="", tool_calls=[{"name": name, "args": args, "id": f"{i}-{name}"}],
                     usage_metadata={"input_tokens": 10, "output_tokens": 2, "total_tokens": 12})


def model(*msgs):
    return ToolFake(messages=iter(msgs))


@pytest.fixture
def task(tmp_path):
    subprocess.run(["git", "init", "-q", str(tmp_path)], check=True)
    (tmp_path / "a.txt").write_text("hello\n")
    return Task(workspace=str(tmp_path), allowed_commands=["echo"], checks=[], step_budget=10,
                chat_model=ChatModel(model="m"))


def test_edits_then_finishes(task, tmp_path):
    r = run(task, "change hello", [], 0, model(
        call("edit_file", {"path": "a.txt", "old": "hello", "new": "bye"}, "1"),
        call("run_command", {"command": "echo ok"}, "2"),
        call("finish", {"summary": "Changed hello to bye."}, "3"),
    ))
    assert (tmp_path / "a.txt").read_text() == "bye\n"
    res = r.result
    assert res.status == "completed" and res.summary == "Changed hello to bye." and res.changed_files == ["a.txt"]
    assert [s["name"] for s in res.steps] == ["edit_file", "run_command", "finish"]
    assert res.usage["input_tokens"] == 30 and r.steps_used == 3
    assert isinstance(r.messages[0], HumanMessage) and r.messages[0].content == "change hello"


def test_ask_human_is_needs_input(task):
    r = run(task, "x", [], 0, model(call("ask_human", {"question": "Which DB?"})))
    assert r.result.status == "needs_input" and r.result.question == "Which DB?"


def test_refusals_are_seen_by_the_agent(task):
    r = run(task, "x", [], 0, model(call("run_command", {"command": "rm -rf ."}, "1"),
                                    call("finish", {"summary": "ok"}, "2")))
    tool_msgs = [m for m in r.messages if isinstance(m, ToolMessage)]
    assert tool_msgs[0].content.startswith("error: not allowed")
    assert r.result.steps[0]["is_error"] is True


def test_step_budget_spans_rounds(task):
    task.step_budget = 3
    r = run(task, "x", [], 2, model(call("list_files", {}, "1"), call("list_files", {}, "2"),
                                    call("finish", {"summary": "s"}, "3")))
    assert r.result.status == "failed" and r.result.stop_cause == "step_budget" and r.steps_used == 3


def test_no_tool_calls_nudges_twice_then_completes(task):
    r = run(task, "x", [], 0, model(AIMessage("thinking"), AIMessage("still"), AIMessage("I changed it.")))
    assert r.result.status == "completed" and r.result.summary == "I changed it."
    nudges = [m for m in r.messages if isinstance(m, HumanMessage) and "finish" in m.content]
    assert len(nudges) == 2


class RateLimited(ToolFake):
    def _generate(self, *a, **kw):
        err = RuntimeError("Error code: 429 - rate limit")
        err.status_code = 429
        raise err


def test_rate_limit_is_budget(task):
    r = run(task, "x", [], 0, RateLimited(messages=iter([])))
    assert r.result.status == "failed" and r.result.stop_cause == "budget"


def test_stop_flag_is_killed(task, monkeypatch):
    monkeypatch.setattr(stop, "requested", True)
    r = run(task, "x", [], 0, model(call("finish", {"summary": "s"})))
    assert r.result.status == "failed" and r.result.stop_cause == "killed"


def test_continues_a_conversation(task):
    first = run(task, "x", [], 0, model(call("finish", {"summary": "s"}, "1")))
    second = run(task, "fix the tests", first.messages, first.steps_used, model(call("finish", {"summary": "t"}, "2")))
    humans = [m.content for m in second.messages if isinstance(m, HumanMessage)]
    assert humans == ["x", "fix the tests"] and second.steps_used == 2


def test_trim_replaces_old_tool_outputs():
    msgs = []
    for i in range(30):
        msgs.append(AIMessage(content="", tool_calls=[{"name": "read_file", "args": {}, "id": str(i)}]))
        msgs.append(ToolMessage(content=f"big output {i}", tool_call_id=str(i)))
    out = trim(msgs, keep=20)
    tools = [m for m in out if isinstance(m, ToolMessage)]
    assert tools[0].content == "[output trimmed]" and tools[-1].content == "big output 29"
    assert len(out) == len(msgs)
```

- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement `coder/prompts.py` and `coder/agent.py`.**

```python
# coder/prompts.py
SYSTEM = """You are HiveDispatch's coding agent working in a git worktree. Make the change the user asks
for, using the tools: read and search before editing, edit with exact text, and run the relevant
checks before finishing. Paths are relative to the worktree.

You may run only these commands with run_command (word prefixes): {allowed}
These checks are always allowed, exactly as written: {checks}

When the change is done and checked, call finish with a short summary of what you changed and how
you verified it. If the ticket is unclear and you cannot proceed, call ask_human with one question.

Repository guidance:
{guidance}
"""

NUDGE = "Use the tools to continue. When you are done, call finish with a summary; if you are blocked, call ask_human."
```

```python
# coder/agent.py
"""HiveDispatch's own coding agent: a LangGraph loop of a chat model and the
workspace tools. It runs inside the workflow's code node when
code_with: langgraph, and returns the same AgentResult the CLIs do."""

from dataclasses import dataclass
from datetime import UTC, datetime
from typing import TypedDict

from langchain_core.messages import AIMessage, BaseMessage, HumanMessage, SystemMessage, ToolMessage
from langchain_core.tools import StructuredTool
from langgraph.graph import END, START, StateGraph
from langgraph.prebuilt import ToolNode

from .. import stop
from ..agentrun import AgentResult
from . import prompts
from .tools import Workspace

MAX_NUDGES = 2
KEEP = 20
STEP_OUT_CAP = 16 * 1024


@dataclass
class CoderRun:
    result: AgentResult
    messages: list
    steps_used: int


class _State(TypedDict):
    messages: list


def trim(messages: list, keep: int = KEEP) -> list:
    """Replaces tool outputs older than the last `keep` messages."""
    cut = len(messages) - keep
    out = []
    for i, m in enumerate(messages):
        if i < cut and isinstance(m, ToolMessage) and m.content != "[output trimmed]":
            m = ToolMessage(content="[output trimmed]", tool_call_id=m.tool_call_id, name=m.name)
        out.append(m)
    return out


def _is_rate_limit(exc: Exception) -> bool:
    return getattr(exc, "status_code", None) == 429 or "RateLimit" in type(exc).__name__


def _stamp(t: datetime) -> str:
    return t.astimezone(UTC).isoformat().replace("+00:00", "Z")


def run(task, prompt: str, messages: list, steps_used: int, model, now=lambda: datetime.now(UTC)) -> CoderRun:
    ws = Workspace(task.workspace, task.allowed_commands, task.checks, task.path, task.check_timeout_seconds)
    outcome: dict = {}
    steps: list[dict] = []
    usage = {"model": task.model or task.chat_model.model, "input_tokens": 0, "output_tokens": 0, "cost_usd": 0.0}
    used = [steps_used]

    def tool(fn, name, doc):
        # functools.wraps carries fn's signature (via __wrapped__), which
        # StructuredTool.from_function reads to build the args schema.
        @functools.wraps(fn)
        def wrapped(*args, **kwargs):
            start = now()
            if stop.requested:
                outcome.update(status="failed", stop_cause="killed", summary="Stopped by the worker.")
                return "stopped"
            if task.step_budget and used[0] >= task.step_budget:
                outcome.update(status="failed", stop_cause="step_budget",
                               summary=f"Step budget of {task.step_budget} tool calls exhausted.")
                return "step budget exhausted"
            used[0] += 1
            out = fn(*args, **kwargs)
            steps.append({"kind": "tool", "name": name, "input": json.dumps(kwargs)[:STEP_OUT_CAP],
                          "output": out[:STEP_OUT_CAP], "is_error": out.startswith("error:"),
                          "start": _stamp(start), "end": _stamp(now())})
            return out

        return StructuredTool.from_function(func=wrapped, name=name, description=doc)

    def finish(summary: str) -> str:
        outcome.update(status="completed", summary=summary)
        return "finished"

    def ask_human(question: str) -> str:
        outcome.update(status="needs_input", question=question, summary=question)
        return "asked"

    tools = [
        tool(ws.read_file, "read_file", "Read a file's numbered lines. Args: path, offset (0-based line), limit."),
        tool(ws.write_file, "write_file", "Create or overwrite a file with content."),
        tool(ws.edit_file, "edit_file", "Replace old with new in a file; old must match exactly once."),
        tool(ws.list_files, "list_files", "List files matching a glob (default **/*)."),
        tool(ws.search, "search", "Regex search over text files; optional glob."),
        tool(ws.run_command, "run_command", "Run an allowed command in the worktree."),
        tool(ask_human, "ask_human", "Ask the human one question and stop."),
        tool(finish, "finish", "Finish with a short summary of the change and how it was verified."),
    ]
    bound = model.bind_tools(tools)
    tool_node = ToolNode(tools)
    nudges = [0]
    system = SystemMessage(prompts.SYSTEM.format(
        allowed=", ".join(task.allowed_commands) or "(none)",
        checks=", ".join(task.checks) or "(none)", guidance=task.guidance or "(none)"))

    def model_node(state: _State) -> _State:
        if stop.requested:
            outcome.update(status="failed", stop_cause="killed", summary="Stopped by the worker.")
            return state
        try:
            reply = bound.invoke([system, *state["messages"]])
        except Exception as exc:
            cause = "budget" if _is_rate_limit(exc) else "error"
            outcome.update(status="failed", stop_cause=cause, summary=f"coding model failed: {exc}")
            return state
        u = getattr(reply, "usage_metadata", None) or {}
        usage["input_tokens"] += int(u.get("input_tokens", 0))
        usage["output_tokens"] += int(u.get("output_tokens", 0))
        msgs = [*state["messages"], reply]
        if not reply.tool_calls:
            if nudges[0] >= MAX_NUDGES:
                text = reply.content if isinstance(reply.content, str) else str(reply.content)
                outcome.update(status="completed", summary=text.strip() or "Run completed.")
            else:
                nudges[0] += 1
                msgs.append(HumanMessage(prompts.NUDGE))
        return {"messages": msgs}

    def tools_node(state: _State) -> _State:
        out = tool_node.invoke({"messages": state["messages"]})
        return {"messages": [*state["messages"], *out["messages"]]}

    def after_model(state: _State) -> str:
        if outcome:
            return END
        last = state["messages"][-1]
        return "tools" if isinstance(last, AIMessage) and last.tool_calls else "model"

    def after_tools(state: _State) -> str:
        return END if outcome else "model"

    g = StateGraph(_State)
    g.add_node("model", model_node)
    g.add_node("tools", tools_node)
    g.add_edge(START, "model")
    g.add_conditional_edges("model", after_model, ["tools", "model", END])
    g.add_conditional_edges("tools", after_tools, ["model", END])
    history = [*trim(list(messages)), HumanMessage(prompt)]
    final = g.compile().invoke({"messages": history}, {"recursion_limit": 10_000})
    if not outcome:
        outcome.update(status="failed", stop_cause="error", summary="coding agent stopped without an outcome")
    res = AgentResult(status=outcome["status"], stop_cause=outcome.get("stop_cause", ""),
                      summary=outcome.get("summary", ""), question=outcome.get("question", ""),
                      changed_files=list(ws.changed), steps=steps, usage=usage)
    return CoderRun(result=res, messages=final["messages"], steps_used=used[0])
```

  `agent.py` also imports `functools` and `json`.
- [ ] **Step 4: Run the tests and see them pass.** Run `graph/.venv/bin/pytest graph -q`, then ruff check and ruff format.
- [ ] **Step 5: Commit.** `feat(graph): HiveDispatch's own coding agent loop with budgets, stop and nudges`.

### Task 4: Python — the workflow's code node uses the coder

**Files:**
- Modify: `graph/hivegraph/graph.py` (State gains `coder_messages: list` and `coder_steps: int`; the code node branches; `build` takes `coder_model=None`)
- Modify: `graph/hivegraph/model.py` (`coder_model(task)`)
- Create: `graph/tests/test_coder_workflow.py`

- [ ] **Step 1: Failing tests.**

```python
import io
import json
import subprocess

from langchain_core.messages import AIMessage
from langgraph.checkpoint.memory import InMemorySaver
from test_coder_agent import ToolFake, call

from conftest import events
from hivegraph.checks import CheckFailure, CheckReport
from hivegraph.events import Emitter
from hivegraph.graph import run_task
from hivegraph.task import ChatModel, Task

APPROVE = AIMessage(json.dumps({"verdict": "approve", "findings": ""}))


def mk_task(tmp_path, **kw):
    subprocess.run(["git", "init", "-q", str(tmp_path)], check=True)
    (tmp_path / "a.txt").write_text("hello\n")
    t = Task(prompt="change hello", workspace=str(tmp_path), code_with="langgraph", allowed_commands=["echo"],
             checks=["true"], git_dir="", base_ref="HEAD", step_budget=50, chat_model=ChatModel(model="m"))
    for k, v in kw.items():
        setattr(t, k, v)
    return t


def test_code_with_langgraph_runs_the_coder(tmp_path):
    task = mk_task(tmp_path)
    coder = ToolFake(messages=iter([call("edit_file", {"path": "a.txt", "old": "hello", "new": "bye"}, "1"),
                                    call("finish", {"summary": "done by coder"}, "2")]))
    out = io.StringIO()
    run_task(task, Emitter(out), model=ToolFake(messages=iter([AIMessage("plan"), APPROVE])),
             coder_model=coder, checks=lambda t: CheckReport(True))
    evs = events(out)
    r = [e for e in evs if e["type"] == "result"][0]
    assert r["status"] == "completed" and "done by coder" in r["summary"] and r["changed_files"] == ["a.txt"]
    assert [e["name"] for e in evs if e["type"] == "step"] == ["edit_file", "finish"]
    assert any(e["type"] == "usage" for e in evs)


def test_fix_round_continues_the_conversation(tmp_path):
    task = mk_task(tmp_path)
    coder = ToolFake(messages=iter([call("finish", {"summary": "first"}, "1"), call("finish", {"summary": "second"}, "2")]))
    seen = []
    orig = coder.bind_tools

    def spy(tools, **kw):
        bound = orig(tools, **kw)
        real = bound.invoke

        def invoke(msgs, *a, **k):
            seen.append([m.content for m in msgs])
            return real(msgs, *a, **k)

        bound.invoke = invoke
        return bound

    object.__setattr__(coder, "bind_tools", spy)
    checks = iter([CheckReport(False, [CheckFailure("true", "FAIL x")]), CheckReport(True)])
    out = io.StringIO()
    run_task(task, Emitter(out), model=ToolFake(messages=iter([AIMessage("plan"), APPROVE])),
             coder_model=coder, checks=lambda t: next(checks))
    assert len(seen) == 2
    assert any("change hello" in c for c in seen[1]) and any("FAIL x" in c for c in seen[1])


def test_resume_keeps_the_coder_conversation(tmp_path):
    task = mk_task(tmp_path)
    saver = InMemorySaver()
    first = ToolFake(messages=iter([call("ask_human", {"question": "Which word?"}, "1")]))
    out = io.StringIO()
    run_task(task, Emitter(out), model=ToolFake(messages=iter([AIMessage("plan")])), coder_model=first,
             checks=lambda t: CheckReport(True), checkpointer=saver)
    r1 = [e for e in events(out) if e["type"] == "result"][0]
    assert r1["status"] == "needs_input" and r1["question"] == "Which word?"

    task.resume_token, task.prompt = r1["resume_token"], "Use bye."
    second = ToolFake(messages=iter([call("finish", {"summary": "used bye"}, "2")]))
    out2 = io.StringIO()
    run_task(task, Emitter(out2), model=ToolFake(messages=iter([APPROVE])), coder_model=second,
             checks=lambda t: CheckReport(True), checkpointer=saver)
    r2 = [e for e in events(out2) if e["type"] == "result"][0]
    assert r2["status"] == "completed" and "used bye" in r2["summary"]


def test_coder_model_choice(monkeypatch):
    from hivegraph import model as m

    seen = {}
    monkeypatch.setattr(m, "ChatOpenAI", lambda **kw: seen.update(kw) or object())
    t = Task(model="deepseek-pro", chat_model=ChatModel(model="deepseek-flash", base_url="u", api_key_env=""))
    m.coder_model(t)
    assert seen["model"] == "deepseek-pro" and seen["base_url"] == "u"
    t.model = ""
    m.coder_model(t)
    assert seen["model"] == "deepseek-flash"
```

- [ ] **Step 2: Run them and see them fail.** Expected: `run_task()` got an unexpected keyword `coder_model`.
- [ ] **Step 3: Implement.**
  - `model.py`:

```python
def coder_model(task):
    cm = task.chat_model
    key = os.environ.get(cm.api_key_env, "") if cm.api_key_env else ""
    return ChatOpenAI(model=task.model or cm.model, base_url=cm.base_url or None, api_key=key or "unused",
                      temperature=0)
```

  - In `graph.py`:
    - `State` gains `coder_messages: list` and `coder_steps: int`.
    - `build(task, emitter, model, agent=run_agent, checks=run_checks, coder_model=None)`.
    - `run_task(..., coder_model=None)` passes it through. When `task.code_with == "langgraph"` and `coder_model is None`, it builds `model.coder_model(task)` lazily, inside the try.
    - In `code`, replace `r = agent(task, prompt, state.get("cli_session", ""))` with:

```python
        if task.code_with == "langgraph":
            from .coder.agent import run as run_coder

            cr = run_coder(task, prompt, state.get("coder_messages", []), state.get("coder_steps", 0), coder_model)
            r = cr.result
            extra: State = {"coder_messages": cr.messages, "coder_steps": cr.steps_used}
        else:
            r = agent(task, prompt, state.get("cli_session", ""))
            extra = {}
```

      Then merge `extra` into the returned dict.
    - The first prompt is `CODE_FIRST` (ticket plus plan). On a resumed thread the prompt is the reply, the same as for the CLIs.
- [ ] **Step 4: Run all the Python tests, ruff check and ruff format.**
- [ ] **Step 5: Commit.** `feat(graph): code_with langgraph runs the coding agent in the workflow's code step`.

### Task 5: Website, docs, decision

**Files:**
- Modify: `web/src/components/AgentsPanel.vue` (`coders` gains `langgraph`, and the help text explains it is coding-only), `web/src/schemas.js` (policy "Graph workflows" section gains `executor.langgraph.allowed_commands`, type `list`)
- Rebuild: `web` → `internal/web/dist`
- Modify:
  - `docs/config.md`: the `agents[].code_with` row lists `langgraph` (coding only); the policy table gains `executor.langgraph.allowed_commands`; the "Graph workflows" section gets a paragraph on the coding agent: tools, allowlist rules, model choice.
  - `README.md`: one sentence in "Graph workflows".
  - `docs/decisions.md`: the new entry, with the rejected alternatives from the spec.
  - `internal/supervisor/prompt.go`: the executor description mentions `code_with langgraph`.
  - The spec's status line.
- Test: `internal/web/server_test.go`, a round-trip of an agent with `code_with: langgraph` (extend `TestAgentsSaveLangGraph`).

- [ ] **Step 1: Failing web test.** In `TestAgentsSaveLangGraph`, add a POST of `{Name: "d", Executor: "langgraph", CodeWith: "langgraph"}`, expecting 200, and a planning one expecting 400 with "coding" in the error. Run it: it already passes after Task 1 (server validation is `ParseAgents`). Keep it as a regression test and ledger that.
- [ ] **Step 2: Make the UI edits and run `cd web && npm run build`.**
- [ ] **Step 3: Write the docs and the decision entry.**
- [ ] **Step 4: Run `go test -race ./internal/web/ ./internal/supervisor/` and lint.**
- [ ] **Step 5: Commit.** `docs: the LangGraph coding agent — config, website, decision`.

### Task 6: Full gate and live smoke

- [ ] **Step 1:** Go: vet, `go test -race ./...` and golangci-lint across the module. Python: ruff check, ruff format check and pytest. Check every exit code.
- [ ] **Step 2: Live smoke.** Make a scratch Go module repo with `main.go` and `main_test.go` and policy `checks: ["go test ./..."]`, with `executor.langgraph.allowed_commands: [go test, go vet, gofmt -l]`. Write a task JSON with `code_with: langgraph`, `model: deepseek-flash`, and the DeepSeek chat model, and the prompt "Add func Add(a, b int) int to main.go and a table test for it in main_test.go". Run `hivegraph run`, then confirm:
  - the steps include `edit_file` or `write_file` and `run_command go test`;
  - the result is `completed`;
  - `go test ./...` passes in the scratch repo afterwards.
- [ ] **Step 3:** Report. Merging is the user's call.
