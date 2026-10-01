# LangGraph coding agent (`code_with: langgraph`) — design

Date: 2026-10-01. Status: approved in conversation; written spec pending review.

## Why

This is the third and last LangChain step that was planned. The first two were tracing and the graph workflow. The fleet supervisor was dropped because LangGraph's supervisor is deprecated.

Today the workflow's code step always drives a CLI, Claude Code or Codex. This step adds a coding agent HiveDispatch owns:
- it runs on any OpenAI-compatible model, such as DeepSeek or a local Ollama;
- it uses no Claude or Codex quota;
- LangSmith traces it step by step.

Like the rest of LangChain in HiveDispatch, it is optional.

## Decisions (made with the user)

- **A `code_with` choice, not a new executor.** `executor: langgraph` with `code_with: langgraph` swaps only the workflow's code step. Plan, checks, fix rounds and self-review are unchanged.
- **Shell access through an allowlist from the policy.**
- **Model:** the agent's `model:` on the graph's provider, falling back to `graph.model`.
- **An agent loop of our own:** a LangGraph `StateGraph` with a model node and LangGraph's `ToolNode`. It uses neither the deprecated `create_react_agent` nor a new `langchain` dependency.

## Configuration

`agents.yaml`:

```yaml
agents:
  - name: deep
    executor: langgraph
    code_with: langgraph   # new: HiveDispatch's own coding agent
    model: deepseek-pro    # optional: the coder's model on graph.provider; default graph.model
```

`code_with: langgraph` is valid only for `role: coding`. Planning and review are read-only runs through a CLI, so validation rejects it for those roles and names the reason.

`.hive-dispatch/policy.yaml`:

```yaml
executor:
  langgraph:
    allowed_commands:      # word prefixes run_command may use; the checks: are always allowed
      - go test
      - go vet
      - gofmt -l
      - git diff
      - git status
```

Validation rejects an empty entry.

## The agent: `graph/hivegraph/coder/`

### Tools

All paths are relative to the worktree. Each path is resolved, and anything outside the worktree, through a symlink, or under `.git/` is refused with a message.

| Tool | Does |
|---|---|
| `read_file(path, offset=0, limit=2000)` | Numbered lines; output capped at 16 KiB |
| `write_file(path, content)` | Creates or overwrites; creates parent directories |
| `edit_file(path, old, new)` | Replaces `old` exactly once. Zero matches or more than one is an error that says how many |
| `list_files(glob="**/*")` | Paths, excluding `.git/`; at most 500 |
| `search(pattern, glob="**/*")` | Python regex over text files; at most 200 `path:line: text` hits |
| `run_command(command)` | See the allowlist below. Timeout 10 minutes; output is the last 8 KiB plus the exit code |
| `ask_human(question)` | Ends the run as `needs_input` with the question |
| `finish(summary)` | Ends the run as `completed` with the summary |

### The allowlist (`run_command`)

1. If `command` exactly equals one of the policy's `checks:` (after trimming), it runs through `sh -c` in the worktree, as the checks node runs it.
2. Otherwise it is split with `shlex.split`. Unbalanced quotes are refused. It is run as an argv without a shell, so `;`, `&&`, `|`, `$(…)`, backticks and redirects are just literal arguments and cannot chain or redirect.
3. It runs only if its argv starts with the words of an `allowed_commands` entry. For example, `go test ./...` matches `go test`, and `gotest` doesn't match `go test`.
4. Anything else is refused with a message listing what is allowed. A refusal counts as a step and is shown to the agent, which can carry on.

`PATH` includes the policy's `executor.path` directories, as it does for the checks and the CLIs.

### The loop

- **State:** `messages`, plus how many tool steps it has taken.
- **Nodes:**
  1. `model` calls the chat model with the tools bound.
  2. If there are no tool calls, a nudge is appended asking the agent to call `finish` or `ask_human`. The nudge happens at most twice; after that, the last text is taken as the summary and the run completes.
  3. `tools` is LangGraph's `ToolNode`. `finish` and `ask_human` end the loop with their outcome.
- **System prompt:** the coding agent's role, the tools, the allowlist, the repository guidance, and "make the change, run the relevant checks, then call finish".
- **Limits:**
  - **Step budget:** the run's `step_budget` counts tool calls across all of the workflow's code rounds, the same rule the CLIs follow under the graph. When it runs out, the run fails with `step_budget`.
  - **Stop:** the SIGTERM stop flag (`stop.requested`) is checked before every model call and every tool, and ends the run as `killed`.
  - **Model errors:** HTTP 429 or a rate-limit error fails the run with cause `budget`; anything else fails it with `error`. The code node's result carries the cause into the workflow, as `agent-run` failures do today.
- **Memory:** the agent's messages live in the workflow state under `coder_messages`, so the SQLite checkpoint keeps them.
  - A fix round, a review round, or a resume after a human reply appends the new instruction as a human message and continues the conversation.
  - Before each code round, tool outputs older than the last 20 messages are replaced with `[output trimmed]`, which bounds the context.
- **Results:**
  - Every tool call becomes a `step` event, with its input, output (capped), error flag and times.
  - Each model call's token usage becomes a `usage` event.
  - The files written or edited are the run's `changed_files`.

### Workflow integration

- The `code` node branches on `task.code_with == "langgraph"`. It calls `coder.run(task, prompt, state)` instead of `run_agent(...)`. Both return the same `AgentResult` shape, so routing, summaries and events stay unchanged.
- The coder model is `ChatOpenAI` on the graph provider's `base_url` and key, with `model = task.model or task.chat_model.model`.

### Go side

- `config.ParseAgents`: `code_with` also accepts `langgraph`, and is rejected unless `role: coding`.
- `repoconfig`: `executor.langgraph.allowed_commands []string` (`ExecutorConfig.LangGraph`). Blank entries are rejected.
- `executor/langgraph`: the task JSON gains `allowed_commands`. `Plan` and `Advise` with `code_with: langgraph` return a clear error, which validation already prevents.
- The website: the agent form's "Code with" offers `langgraph`, and the policy form gets "Allowed commands".

## Testing

- **Python** (pytest, no network). The tests use LangChain's `GenericFakeChatModel`, scripting `AIMessage`s that carry `tool_calls`, against a temporary git repo:
  - edits and writes land, and `changed_files` lists them;
  - `edit_file` with zero or two matches gives an error the agent sees;
  - a path outside the worktree, through a symlink, or in `.git` is refused;
  - an allowed command runs; a disallowed command, `go test; rm -rf x` and unbalanced quotes are refused, and nothing outside runs;
  - an exact `checks:` command runs through the shell;
  - the step budget trips and maps to `step_budget`;
  - `ask_human` maps to `needs_input` and `finish` to `completed`;
  - a model 429 maps to `budget`;
  - the stop flag maps to `killed`;
  - in a workflow run with `code_with: langgraph`, a fix round continues the same messages, and a resumed thread keeps the conversation;
  - trimming replaces old tool outputs.
- **Go:** validation of `code_with: langgraph` (coding only), `allowed_commands`, and the task JSON field.
- **Live:** on a scratch repo with `checks: [go test ./...]`, `code_with: langgraph` and DeepSeek make a small real change (add a function and its test). The checks pass, and the summary and events look right.

## Docs

- `docs/config.md`: the `code_with: langgraph` row and `executor.langgraph.allowed_commands`.
- README: one paragraph in "Graph workflows".
- `docs/decisions.md`: "HiveDispatch's own coding agent is a code_with choice", with these rejected alternatives:
  - a separate executor;
  - unrestricted shell;
  - `create_react_agent`, which is deprecated;
  - LangChain's `create_agent`, which is a new dependency and offers less direct control of budgets and stopping;
  - a separate coder model block.

## Not in this step

- Planning and review on the LangGraph agent.
- Sandboxing beyond the allowlist and worktree confinement, such as containers or bubblewrap.
- MCP tools.
