# LangGraph workflow executor — design

Date: 2026-10-01. Status: approved in conversation; written spec pending review.

## Why

This is the second LangChain step. The first was LangSmith tracing (`2026-10-01-langsmith-tracing-design.md`).

Today a coding run is one call to Claude Code or Codex: the agent gets the ticket, works until it stops, and whatever it left is pushed. Nothing makes it run the repository's checks or look over its own diff, and a failure the agent didn't notice is found in review or CI.

This step replaces that one call with a LangGraph workflow:
1. plan;
2. code;
3. run the repository's checks, and fix until they pass;
4. self-review the diff, and fix what the review finds;
5. finish.

Every node can be seen in LangSmith and in LangGraph Studio.

## Scope decisions (made with the user)

- **Inside one run.** The graph is a new `executor.Executor` called `langgraph`. Go still claims tickets, prepares the worktree, commits, pushes, opens the PR, moves the ticket and runs the planning and review roles. The graph replaces only `Executor.Run`.
- **A subprocess per run.** The Go worker runs `hivegraph` in the worktree just as it runs `claude` or `codex`: the prompt on stdin, JSON lines on stdout, and the same timeout, step budget and kill handling. There is no service to keep running.
- **The CLI codes, a cheap model thinks.** Code and fix nodes run Claude Code or Codex, chosen per agent. The plan and self-review nodes call a chat model through LangChain. The default is DeepSeek through its OpenAI-compatible API.
- **The code node calls back into Go (approach A).** It runs `hivedispatch agent-run`, which uses the existing `claudecode` or `codex` executor and prints its `executor.Result` as JSON. The Python side never builds CLI arguments or parses CLI output.

## What the user configures

`.hive-dispatch/agents.yaml`, in the repository:

```yaml
agents:
  - name: careful
    executor: langgraph
    code_with: claude      # claude (default) or codex; which CLI the code node runs
    model: claude-opus-5-5 # passed to the code node's CLI, as today
```

- `code_with` is only valid with `executor: langgraph`, and validation rejects it on any other executor.
- `langgraph` joins `claude`, `codex` and `fake` as an allowed `executor`.

`.hive-dispatch/policy.yaml`, in the repository:

```yaml
checks:                    # run in the worktree after every code step; all must exit 0
  - go vet ./...
  - go test ./...
graph:
  max_fix_rounds: 3        # code → checks → fix loops before giving up on green
  max_review_rounds: 1     # self-review → fix loops
```

- `checks` run through `sh -c` in the worktree, each with a 10-minute timeout. The `executor.path` directories are added to `PATH` first, as they are for the agents.
- With no `checks`, the checks node passes trivially.

`config.yaml`, on the worker:

```yaml
graph:
  binary: hivegraph        # default: "hivegraph" on PATH
  provider: deepseek       # same presets as supervisor:; default: the supervisor's settings
  model: deepseek-flash
  base_url: ...            # optional, as in supervisor:
  api_key_env: ...         # optional, as in supervisor:
```

- Only OpenAI-compatible providers are allowed for now: `openai`, `deepseek`, `huggingface`, `ollama`, or a custom `base_url`. `anthropic` is rejected with a message that names the alternative.
- `hivedispatch check` reports whether `hivegraph` is installed and which model it would use. It never prints the key.

## Architecture

```
Go worker ──exec──▶ hivegraph run (Python, LangGraph)
  executor/langgraph   │ stdin: task JSON          stdout: JSONL events + result
                       ├─ plan node ──▶ ChatOpenAI(base_url, model)
                       ├─ code node ──exec──▶ hivedispatch agent-run ──▶ claude / codex
                       ├─ checks node ──▶ sh -c <each check>
                       ├─ review node ──▶ ChatOpenAI + `git diff <base>`
                       └─ SQLite checkpointer at <git dir>/hivegraph/checkpoints.sqlite
```

### Go side

**`internal/executor/langgraph`** implements `executor.Executor`.
- `Run` launches `hivegraph run` with the task as JSON on stdin:
  - `ticket`, `prompt`, `workspace`, `resume_token`, `step_budget`, `code_with`, `model`;
  - the `checks` and `graph` limits read from the worktree's policy;
  - `base_ref`, the commit the run starts from, for the review diff;
  - `hivedispatch`, the path of the running binary;
  - the chat model's `provider`, `model`, `base_url` and `api_key_env`.
- `Run` reads JSONL events into a `Transcript`, as `codexcli` does:
  - a `step` event becomes an `executor.Step`, and its `start`/`end` come from the event;
  - a `usage` event adds to `executor.Usage`;
  - the last `result` event becomes the `executor.Result`.
- If the process exits without a `result` line, the run fails with `CauseError` and its stderr, as `claudecli` handles it.
- Timeouts map to `CauseTimeout`. The step budget counts code-node `step` events and maps to `CauseStepBudget`.
- `Plan` and `Advise` delegate to the `code_with` executor, so a `langgraph` agent can also be a planner or reviewer.
- **Process handling:**
  - The child gets its own process group.
  - When it has to be stopped, the group gets SIGTERM, a 15-second grace period, then SIGKILL.
  - The inner `hivedispatch agent-run` traps SIGTERM through its `signal.NotifyContext`, and its `claudecli`/`codexcli` then kill the CLI's own process group. A plain SIGKILL would orphan the CLI, because that runs in a separate group.
- `<git dir>` comes from `git rev-parse --git-dir` in the worktree, so checkpoints are never committed and are removed with the worktree.

**`hivedispatch agent-run`** is a new hidden subcommand, not listed in `-help`.
- Flags: `-executor claude|codex`, `-workspace`, `-model`, `-resume`, `-step-budget`.
- It reads the prompt on stdin, runs that executor's `Run`, and prints one JSON object on stdout: the `executor.Result` fields, including `steps` and `usage`, without `Log`.
- It exits 0 whenever it printed a result, failed runs included. A non-zero exit means it could not run at all.

**Tracing:**
- When the run's context carries a span, `executor/langgraph` sets `LANGSMITH_PARENT` (a new variable) to that span's dotted order and trace ID.
- `hivegraph` reads it and starts its LangGraph trace under that parent. This uses the LangSmith SDK's `langsmith-trace` header form, through `RunTree.from_headers`.
- `trace.Span` gains `Header() string`, which returns `dotted_order`.
- The graph's own nodes then appear inside the ticket's `run langgraph` span. The `step` events also become Go child spans, so with tracing on, the node steps appear twice:
  - as the LangGraph tree, which is detailed;
  - as Go steps, which are coarse.

  To avoid the duplicate, `executor/langgraph` marks its result `steps_traced: true` when the graph traced itself, and `tracedExecutor` then skips the Go step spans.

### Python side: `graph/`, the package `hivegraph`

- **Packaging:** `graph/pyproject.toml`.
  - Runtime dependencies: `langgraph`, `langgraph-checkpoint-sqlite`, `langchain-openai`, `langsmith`.
  - Dev dependencies: `pytest`, `ruff`.
  - The console script is `hivegraph`, and Python ≥ 3.11 is required.
  - Install with `uv tool install ./graph` or `pipx install ./graph`.
- **`hivegraph run`** reads the task JSON, builds the graph, and runs it with `thread_id = resume_token or a new uuid`. It writes events to stdout as JSONL, flushing each line.
- **State:** `TypedDict` with:
  - the task and the plan;
  - `cli_session` (the resume token of the code CLI);
  - `fix_round`, `review_round`, the last checks report and the last review findings;
  - the `outcome`: status, cause, summary, question, changed files;
  - token totals.
- **Nodes:**
  1. **`plan`** (chat model): a short plan and a checklist, from the ticket prompt and the repository guidance. On a resumed thread it is skipped, because the thread already has a plan.
  2. **`code`** runs `hivedispatch agent-run` with a prompt built from what came before:
     - first time: ticket + plan;
     - after failed checks: the failing commands and the last 8 KiB of their output;
     - after review: the findings;
     - on a resumed thread: the human's reply.

     It passes `-resume <cli_session>` so the CLI keeps its context across rounds. It emits the CLI's own steps as `step` events, and its usage as a `usage` event. A `needs_input` or `failed` result ends the graph with that outcome.
  3. **`checks`** runs each check. If all pass, it goes to `review`. If one fails and `fix_round < max_fix_rounds`, it goes back to `code`. Otherwise the graph finishes `completed`, with a summary saying the checks are still red and listing them. The ticket still gets its PR, and a human decides.
  4. **`review`** (chat model) reads the plan and `git diff <base_ref>` (capped at 100 KiB, as the review role is) and returns structured `{verdict: approve|changes, findings}`.
     - On `changes` with `review_round < max_review_rounds`, the graph goes back to `code`.
     - Otherwise it goes to `finish`.
  5. **`finish`** writes the summary: the CLI's last summary, the checks status and any unresolved review findings. It emits `result`.
- **Errors:**
  - Any exception in a node is caught at the top level, and `result{status: failed, cause: error, summary: <exception>}` is written.
  - A chat-model failure in `plan` falls back to coding without a plan.
  - A chat-model failure in `review` skips the review and notes it in the summary. The cheap model is never allowed to stop a run on its own.
- **Event shapes**, one per line:
  - `step`: `{"type":"step","kind":"tool|message","name","input","output","is_error","start","end"}`. Times are RFC 3339.
  - `usage`: `{"type":"usage","model","input_tokens","output_tokens","cost_usd"}`.
  - `result`: `{"type":"result","status","stop_cause","summary","question","resume_token","changed_files","steps_traced"}`.
  - Each node emits a `node` event, `{"type":"node","name","start","end"}`. Go counts these toward progress and logs them, but does not trace them.

## Testing

- **Go:**
  - `executor/langgraph` against a fake `hivegraph` shell script that replays JSONL fixtures. Cases: success, needs_input, failure, no result line, timeout, step budget, and SIGTERM reaching a child before SIGKILL.
  - `agent-run` with the fake executor.
  - Config validation for `code_with`, `checks`, `graph:` and providers.
  - A dispatch test showing a `langgraph` agent's steps under the run span.
- **Python** (pytest, no network):
  - The graph runs with LangChain's `FakeListChatModel` / `GenericFakeChatModel` and a fake `agent-run` script.
  - Routes covered: checks pass first time; fail, fix and pass; fail past `max_fix_rounds`; review asks for changes once; needs_input stops the graph.
  - Plan and review model failures fall back without stopping the run.
  - The event output matches the shapes above.
  - A resumed thread skips `plan` and passes `-resume`.
- **CI:** a `graph` job on `ubuntu-latest` with Python 3.12 runs `pip install ./graph[dev]`, then `ruff check graph`, `ruff format --check graph` and `pytest graph`.
- **Live:**
  - Install `hivegraph`.
  - Add a `langgraph` agent with `checks` to a test repository.
  - Run `hivedispatch run -once` on a small ticket with DeepSeek as the chat model.
  - Confirm the PR and the summary, and with tracing on, the LangGraph tree under the ticket in LangSmith.

## Documentation and repo rules

- `docs/decisions.md` gets a new entry, "Graph workflows run as a Python subprocess", with these rejected alternatives:
  - a long-lived LangGraph Server;
  - Python reimplementing the CLI runners;
  - Go orchestrating with Python only for model calls;
  - a Go port of LangGraph.

  It states that Go keeps its stdlib-only rule and the Python package has its own dependencies.
- AGENTS.md and CONTRIBUTING.md get a "Python (`graph/`)" section: set up a venv and install with `pip install -e './graph[dev]'`, then run `ruff check`, `ruff format --check` and `pytest` before committing changes under `graph/`. Fakes, not network, apply there too.
- `docs/config.md` documents `graph:`, `checks:`, `graph.max_*_rounds` and `code_with`. The README gets a short "Graph workflows" section.

## Not in this step

- Anthropic as the graph's chat model.
- A graph for the planning or review roles.
- The fleet supervisor and the LangGraph coding agent.
- Human-in-the-loop interrupts inside the graph: a question still ends the run, as today.
