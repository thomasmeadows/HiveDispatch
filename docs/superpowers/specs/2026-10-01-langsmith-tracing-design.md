# LangSmith tracing — design

Date: 2026-10-01. Status: approved.

## Why

The user wants LangGraph (LangChain) in HiveDispatch, for the supervisor and, where possible, for the coding agents. Its uses are graph workflows, a fleet supervisor, a model-agnostic coding agent of HiveDispatch's own, and the LangChain tooling around them. Tracing comes first because it is the cheapest of these and the others build on it: every later LangGraph graph reports to the same LangSmith project.

LangGraph exists only for Python and JS, and HiveDispatch is Go with the stdlib plus yaml.v3. LangSmith, though, takes traces over plain HTTP. So this step adds no Python and no Go dependency.

## What the user sees

With `LANGSMITH_TRACING=true` and `LANGSMITH_API_KEY` set, the LangSmith project (default `hivedispatch`) shows two kinds of run tree:

- **Supervisor turn** (`chain`). It holds one `llm` run per model call, with the messages, the reply, token counts, provider and model. It also holds one `tool` run per tool call, with arguments and result, including cancelled calls.
- **Ticket** (`chain`, named `<role> <ticket key>`, tagged with ticket, repo, agent, role and machine id). Under it:
  - a `triage` run (`chain`) when triage runs;
  - the executor's `run` or `advise` call (`chain`), whose inputs are the task or advice and whose outputs are the status, cause, summary and changed files;
  - under a `run`, the CLI's own steps as children: tool calls and assistant messages. These come from Claude Code `stream-json` or Codex `--json`, and each is timed by when its line arrived.

With tracing off, nothing changes and nothing is sent.

## Configuration

Environment variables only, under LangChain's names, so an existing LangSmith setup carries over:

| Variable | Default | Meaning |
|---|---|---|
| `LANGSMITH_TRACING` | off | `true` turns tracing on (needs a key) |
| `LANGSMITH_API_KEY` | — | sent as `x-api-key` |
| `LANGSMITH_PROJECT` | `hivedispatch` | sent as each run's `session_name` |
| `LANGSMITH_ENDPOINT` | `https://api.smith.langchain.com` | the EU, APAC or self-hosted API base |
| `LANGSMITH_WORKSPACE_ID` | — | sent as `x-tenant-id` when a key spans several workspaces |
| `LANGSMITH_HIDE_INPUTS` / `LANGSMITH_HIDE_OUTPUTS` | off | `true` sends `{}` instead of that side's content |

`hivedispatch check` says whether tracing is on and which project it goes to. It never prints the key.

## Architecture

`internal/trace` depends only on the stdlib:

- `Tracer` has `Start(ctx, name, kind, inputs) (ctx, *Span)` and `Close(ctx) error`. The context carries the current span, so a child started from it gets the right parent.
- `*Span` has `End(outputs, err)`, `SetMetadata(k, v)` and `SetUsage(model, in, out)`. A nil span and a nil tracer are no-ops, so call sites need no checks.
- `Noop` is the default. `FromEnv(getenv)` builds a `Noop` or a LangSmith tracer.
- `trace/fake` records spans with their parent links for tests. `trace/langsmith` is the exporter.

Decorators live in `internal/trace`:

- `trace.Model(model.Model, Tracer)` puts an `llm` span around each `Chat`.
- `trace.Executor(executor.Executor, Tracer, meta)` wraps `Plan`, `Run` and `Advise`. It turns `Result.Steps` into child spans.

`executor.Step` is a plain record: kind, name, input, output, error flag, start and end, model, and tokens. `claudecli.Parser` and `codexcli.Parser` collect steps live as lines arrive, and the adapters copy them into `Result.Steps`. That keeps `executor`, `claudecli` and `codexcli` free of `trace`.

### Exporter

- **Endpoint:** `POST {endpoint}/runs/batch` with body `{"post":[…],"patch":[…]}`. This is what LangSmith's own Python SDK sends.
- **IDs:** each run's `id` is a UUIDv7.
- **Ordering:** `dotted_order` is the parent's `dotted_order`, a dot, then the start time as `%Y%m%dT%H%M%S%fZ` (microseconds, UTC) followed by the id, and `trace_id` is the root's id.
- **Start and end:** `Start` queues a `post` with the inputs, and `End` queues a `patch` with the outputs, error and end time. A run that starts and ends within one flush interval goes as a single `post`. Long ticket runs therefore appear in LangSmith while they work.
- **LLM runs:** token usage goes in `outputs.usage_metadata` (`input_tokens`, `output_tokens`, `total_tokens`) and in `extra.metadata.usage_metadata`. `ls_provider` and `ls_model_name` come from the model's `vendor/model` name, and LangSmith needs `ls_model_name` to price a run.
- **Tracing never slows or fails HiveDispatch:**
  - `Start` and `End` only append to a bounded queue (10 000 operations). When the queue is full, operations are dropped and counted.
  - A background goroutine flushes every second, or as soon as 100 operations are waiting.
  - A failed POST is retried once, then logged at Warn with the drop count. A 409 conflict counts as success.
  - `Close` flushes what is left, bounded by its context; the CLI gives it 5 s.
- **Size cap:** every string in inputs, outputs and metadata is cut to 64 KiB with a `…[truncated]` marker.

## Instrumentation

- `supervisor.Agent` gets a `Tracer` field:
  - `Turn` opens a `chain` span named `supervisor turn`, with the user message as input and the reply as output.
  - `Chat` goes through `trace.Model`.
  - `call` and `cancelled` open `tool` spans.
  - `SessionOptions.Tracer` (and `Options.Tracer`) carries it in, and a nil value means `Noop`.
- `dispatch.Dispatcher` gets a `Tracer` field:
  - `handle` opens the ticket span and ends it with the outcome.
  - `handleCoding` puts a `triage` span around `Triager.Decide`.
  - `executorFor` returns the executor wrapped with `trace.Executor`.
- **CLI wiring:**
  - `run`, `website` and `supervisor` each build one tracer with `trace.FromEnv(os.Getenv)` and close it on the way out.
  - `run` passes it to the dispatcher. `website` passes it to the dispatcher it starts and to the chat sessions. `supervisor` passes it to its session.

## Error handling

Tracing errors are never returned to callers. The exporter logs through `slog`, since HiveDispatch already logs that way. A span left un-ended (for example on a panic) simply stays open in LangSmith.

## Testing

- **Unit tests** cover:
  - context parenting, nil safety and `FromEnv` cases;
  - the exporter against `httptest`: payload shape, `dotted_order`, `trace_id`, headers, project, truncation, hiding, post/patch merging, overflow dropping, retry, 409, and `Close` flushing;
  - the decorators, through the supervisor and executor fakes;
  - the step collection in both CLI parsers, with an injected clock;
  - the span tree a dispatch test produces through the fake tracer.
- **Live:** run the supervisor on DeepSeek and `run -once` with Claude Code, then inspect the trees in LangSmith. This needs the user's LangSmith key.

## Not in this step

LangGraph itself, as a Python service behind a Go interface with a fake: workflows, a fleet supervisor, and a LangGraph coding agent as a third executor. Each gets its own spec.
