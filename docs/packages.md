# Package guide

- [Testing guide (fakes, helpers and suites)](testing.md)

## Table of contents

- [Package guide](#package-guide)
  - [Table of contents](#table-of-contents)
  - [Scope and conventions](#scope-and-conventions)
  - [End-to-end operation](#end-to-end-operation)
  - [cmd/hivedispatch](#cmdhivedispatch)
  - [docs](#docs)
  - [graph/hivegraph](#graphhivegraph)
  - [graph/hivegraph/coder](#graphhivegraphcoder)
  - [internal/command-line-interfaces/antigravitycli](#internalcommand-line-interfacesantigravitycli)
  - [internal/command-line-interfaces/claudecli](#internalcommand-line-interfacesclaudecli)
  - [internal/command-line-interfaces/codexcli](#internalcommand-line-interfacescodexcli)
  - [internal/command-line-interfaces/deepcodecli](#internalcommand-line-interfacesdeepcodecli)
  - [internal/command-line-interfaces/graphcli](#internalcommand-line-interfacesgraphcli)
  - [internal/command-line-interfaces/openclawcli](#internalcommand-line-interfacesopenclawcli)
  - [internal/config](#internalconfig)
  - [internal/discover](#internaldiscover)
  - [internal/dispatch](#internaldispatch)
  - [internal/executor](#internalexecutor)
  - [internal/executor/antigravity](#internalexecutorantigravity)
  - [internal/executor/claudecode](#internalexecutorclaudecode)
  - [internal/executor/codex](#internalexecutorcodex)
  - [internal/executor/deepcode](#internalexecutordeepcode)
  - [internal/executor/grok](#internalexecutorgrok)
  - [internal/executor/langgraph](#internalexecutorlanggraph)
  - [internal/executor/openclaw](#internalexecutoropenclaw)
  - [internal/githost](#internalgithost)
  - [internal/githost/github](#internalgithostgithub)
  - [internal/githost/none](#internalgithostnone)
  - [internal/gitops](#internalgitops)
  - [internal/gitops/git](#internalgitopsgit)
  - [internal/gitops/repolock](#internalgitopsrepolock)
  - [internal/prompt](#internalprompt)
  - [internal/repoconfig](#internalrepoconfig)
  - [internal/schedule](#internalschedule)
  - [internal/state](#internalstate)
  - [internal/state/gitbranch](#internalstategitbranch)
  - [internal/state/localdir](#internalstatelocaldir)
  - [internal/state/router](#internalstaterouter)
  - [internal/statusline](#internalstatusline)
  - [internal/supervisor](#internalsupervisor)
  - [internal/supervisor/model](#internalsupervisormodel)
  - [internal/supervisor/model/anthropic](#internalsupervisormodelanthropic)
  - [internal/supervisor/model/openai](#internalsupervisormodelopenai)
  - [internal/trace](#internaltrace)
  - [internal/trace/langsmith](#internaltracelangsmith)
  - [internal/tracker](#internaltracker)
  - [internal/tracker/ghissues](#internaltrackerghissues)
  - [internal/tracker/jira](#internaltrackerjira)
  - [internal/tracker/prefixed](#internaltrackerprefixed)
  - [internal/tracker/router](#internaltrackerrouter)
  - [internal/triage](#internaltriage)
  - [internal/triage/claudecode](#internaltriageclaudecode)
  - [internal/triage/passthrough](#internaltriagepassthrough)
  - [internal/web](#internalweb)
  - [internal/yamlfile](#internalyamlfile)
  - [web](#web)
  - [Update protocol](#update-protocol)

## Scope and conventions

This guide describes the current implementation, with one section per Go package (excluding fake and test-helper packages), Python import package, and the `web/` npm package. Directory headings use repository-relative paths; file links open the source. Each file table is local to that section, and child packages have their own sections.

Individual tests (`*_test.go`, `graph/tests/`), test scripts/fixtures, installed dependencies, caches and generated frontend asset hashes are excluded. Fake implementations and reusable test-helper packages are documented separately in [Testing guide](testing.md), along with test suites and fixture locations. `internal/web/dist/` is documented as a generated tree. `graph/pyproject.toml` defines the Python distribution, dependencies, CLI entry point and check settings; `go.mod` and `go.sum` define/lock the Go module. Root instructions, CI configuration, images and other documentation are repository support files, not additional code packages.

For architecture and rationale, see [design-spec.md](design-spec.md) and [decisions.md](decisions.md). For development requirements, see [CONTRIBUTING.md](../CONTRIBUTING.md).

## End-to-end operation

1. **Startup:** `cmd/hivedispatch` loads `config`, discovers repositories, and wires tracker/state routers, Git workspaces, triage, executors and optional tracing.
2. **Dispatch:** `dispatch` checks `schedule`, polls configured role columns and reserves a free agent/slot. Tracker adapters own claims and heartbeats; state adapters preserve the run's progress.
3. **Planning and coding:** planning agents post a plan and move tickets to Ready. Coding claims a Ready ticket, prepares or resumes its worktree, triages unless the saved state permits skipping it, and runs the chosen executor under policy and limits.
4. **Publication and review:** Git finalization safety-commits remaining edits and pushes. The host finds/opens a PR; dispatch reports the outcome and transitions the ticket. Review agents inspect a new PR head, post a review and either leave it for human review or request another coding round. HiveDispatch never merges.
5. **Optional graph coding:** the Go LangGraph adapter sends a task to `hivegraph` through `graphcli`; Python runs plan → code → checks/fixes → review/fixes. CLI coding calls back through `hivedispatch agent-run`; built-in coding uses `hivegraph.coder`. Results return through JSON/JSONL, and SQLite checkpoints support resume.
6. **Operator interaction:** the terminal supervisor and Vue website use the same Go supervisor sessions. The website also calls config/discovery/status helpers, stages YAML edits through `yamlfile`, and streams supervisor progress and confirmations.

<a id="package-cmd-hivedispatch"></a>

## cmd/hivedispatch

The executable entry point and composition root. It connects configuration to concrete tracker, executor, git, state, tracing, supervisor and website implementations.

**Files (excluding tests)**

| File                                               | Responsibility                                                                                |
| -------------------------------------------------- | --------------------------------------------------------------------------------------------- |
| [main.go](../cmd/hivedispatch/main.go)             | CLI dispatch; init, scan, check, run and once commands; preflight checks and worker shutdown. |
| [wire.go](../cmd/hivedispatch/wire.go)             | Constructs repository trackers, state stores, executors, workspaces and the dispatcher.       |
| [status.go](../cmd/hivedispatch/status.go)         | Reads and displays persisted runs without tracker or executor access.                         |
| [supervisor.go](../cmd/hivedispatch/supervisor.go) | Starts terminal or piped supervisor conversations.                                            |
| [website.go](../cmd/hivedispatch/website.go)       | Starts the HTTP server, resolves the development frontend and optionally opens a browser.     |
| [tracing.go](../cmd/hivedispatch/tracing.go)       | Creates optional tracing from the environment and bounds its final flush.                     |
| [agentrun.go](../cmd/hivedispatch/agentrun.go)     | Reads one JSON task, runs an executor and writes the normalized JSON result for hivegraph.    |

**Flow:** Parse the subcommand and flags → load the applicable configuration → wire dependencies → run the worker, one ticket, an operator command, or a server → cancel and flush on shutdown. The internal `agent-run` command bridges Python back to the Go executors.

[Back to contents](#table-of-contents)

<a id="package-docs"></a>

## docs

Embeds operator Markdown documentation in the Go binary.

**Files (excluding tests)**

| File                       | Responsibility                                                                                                                                                  |
| -------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [docs.go](../docs/docs.go) | Declares the embedded filesystem with `//go:embed *.md`. Top-level Markdown, including this guide, is embedded; nested design plans and images are not matched. |

**Flow:** The Go build embeds top-level Markdown files → a consumer reads `docs.FS` at runtime. The supervisor currently exposes only the documents named in its `read_doc` allowlist; embedding a new document does not automatically add a tool choice.

[Back to contents](#table-of-contents)

<a id="package-graph-hivegraph"></a>

## graph/hivegraph

Python package `hivegraph`: an optional checkpointed plan/code/check/review workflow, distributed by graph/pyproject.toml.

**Files (excluding tests)**

| File                                          | Responsibility                                                                                   |
| --------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| [__init__.py](../graph/hivegraph/__init__.py) | Package marker and workflow description.                                                         |
| [cli.py](../graph/hivegraph/cli.py)           | hivegraph run entry point, stdin task decoding, signal setup and workflow invocation.            |
| [task.py](../graph/hivegraph/task.py)         | Task/chat-model data classes and JSON task decoding.                                             |
| [graph.py](../graph/hivegraph/graph.py)       | StateGraph nodes/routes, diff collection, fix/review budgets, SQLite resume and tracing context. |
| [agentrun.py](../graph/hivegraph/agentrun.py) | Invokes hivedispatch agent-run and decodes its result for CLI-backed coding.                     |
| [checks.py](../graph/hivegraph/checks.py)     | Runs policy check commands with repository environment, timeouts and capped failure output.      |
| [model.py](../graph/hivegraph/model.py)       | Builds OpenAI-compatible LangChain models for planning/review and coding.                        |
| [prompts.py](../graph/hivegraph/prompts.py)   | Planning, first-code, fix, review and revision prompts.                                          |
| [events.py](../graph/hivegraph/events.py)     | Flushes JSONL node/step/result events to the Go runner.                                          |
| [stop.py](../graph/hivegraph/stop.py)         | Shared SIGTERM flag used to stop new nodes without orphaning CLI children.                       |

**Flow:** Read a JSON task from Go → load or create a SQLite checkpoint thread → plan → code via agent-run or the built-in coder → run repository checks and bounded fix rounds → self-review and bounded revisions → emit the final result/resume token. SIGTERM stops new work while child cleanup completes; planning/review model failures can degrade gracefully with an explanation.

[Back to contents](#table-of-contents)

<a id="package-graph-hivegraph-coder"></a>

## graph/hivegraph/coder

Python subpackage `hivegraph.coder`: the built-in coding agent selected by code_with: langgraph.

**Files (excluding tests)**

| File                                                | Responsibility                                                                                                              |
| --------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------- |
| [__init__.py](../graph/hivegraph/coder/__init__.py) | Package marker and coding-agent scope.                                                                                      |
| [agent.py](../graph/hivegraph/coder/agent.py)       | Model/tool StateGraph, conversation trimming, budgets, stop handling and normalized results.                                |
| [prompts.py](../graph/hivegraph/coder/prompts.py)   | Coder system instructions, allowed commands and finish/needs-input guidance.                                                |
| [tools.py](../graph/hivegraph/coder/tools.py)       | Confined file read/search/edit operations and command execution restricted to allowed word prefixes or exact policy checks. |

**Flow:** Build tools confined to the worktree → alternate model and ToolNode calls → count each tool against the budget → retain/trim conversation across fix rounds → finish, ask for input or report a stop through the same AgentResult shape as CLI coding.

[Back to contents](#table-of-contents)

<a id="package-internal-command-line-interfaces-antigravitycli"></a>

## internal/command-line-interfaces/antigravitycli

Runs agy and parses its init, step_update and result envelopes.

**Files (excluding tests)**

| File                                                                      | Responsibility                                                                       |
| ------------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| [run.go](../internal/command-line-interfaces/antigravitycli/run.go)       | Process-group supervision, JSON stdin, bounds, deadline and step-budget handling.    |
| [stream.go](../internal/command-line-interfaces/antigravitycli/stream.go) | Native event parsing, conversation IDs, distinct steps and current-invocation usage. |

**Flow:** Send the prompt as JSON stdin → parse events and deduplicate tool-step updates → track per-step usage → stop on deadline/budget or invalid output → return the transcript and bounded logs.

[Back to contents](#table-of-contents)

<a id="package-internal-command-line-interfaces-claudecli"></a>

## internal/command-line-interfaces/claudecli

Runs Claude Code headlessly and parses its Messages stream, also used by Grok Build.

**Files (excluding tests)**

| File                                                                 | Responsibility                                                                             |
| -------------------------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| [run.go](../internal/command-line-interfaces/claudecli/run.go)       | Process setup, stdin/environment, cancellation, step limits and bounded log capture.       |
| [stream.go](../internal/command-line-interfaces/claudecli/stream.go) | Messages parser, session/result/usage data, edited paths, timed steps and quota detection. |

**Flow:** Start a process group → consume stream-json → count tool calls and capture edits/usage/steps → stop on cancellation or budget → return transcript, exit details and bounded logs.

[Back to contents](#table-of-contents)

<a id="package-internal-command-line-interfaces-codexcli"></a>

## internal/command-line-interfaces/codexcli

Runs codex exec and parses its native JSONL protocol.

**Files (excluding tests)**

| File                                                                | Responsibility                                                                       |
| ------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| [run.go](../internal/command-line-interfaces/codexcli/run.go)       | Codex process execution and supervision with bounded logs.                           |
| [stream.go](../internal/command-line-interfaces/codexcli/stream.go) | Native event parser, thread/session data, changed paths, steps and budget detection. |

**Flow:** Start a process group → consume item and turn events → count tools and collect changes/usage → enforce cancellation or step limits → return transcript and exit/log details.

[Back to contents](#table-of-contents)

<a id="package-internal-command-line-interfaces-deepcodecli"></a>

## internal/command-line-interfaces/deepcodecli

Runs DeepCode and observes session files because stdout contains only the final reply.

**Files (excluding tests)**

| File                                                                     | Responsibility                                                                                                                   |
| ------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------- |
| [run.go](../internal/command-line-interfaces/deepcodecli/run.go)         | Argument building, process supervision, session discovery/polling and bounded logs.                                              |
| [session.go](../internal/command-line-interfaces/deepcodecli/session.go) | Reads session messages and index metadata; extracts steps, edits, usage and status, limiting resumed accounting to new messages. |

**Flow:** Snapshot existing sessions → start the CLI → locate the worktree session or resume the named session → poll new tool calls for budget enforcement → parse session/index data and return the invocation result.

[Back to contents](#table-of-contents)

<a id="package-internal-command-line-interfaces-graphcli"></a>

## internal/command-line-interfaces/graphcli

Runs hivegraph and parses the Go/Python JSONL event protocol.

**Files (excluding tests)**

| File                                                                | Responsibility                                                                            |
| ------------------------------------------------------------------- | ----------------------------------------------------------------------------------------- |
| [run.go](../internal/command-line-interfaces/graphcli/run.go)       | Subprocess execution, task stdin, bounded output and graceful process-group cancellation. |
| [stream.go](../internal/command-line-interfaces/graphcli/stream.go) | Graph protocol parser and normalized node, step, usage and final-result data.             |

**Flow:** Send a serialized task to hivegraph → parse workflow node/step/result events → retain logs and transcript → on cancellation send SIGTERM, allow cleanup, then SIGKILL if needed.

[Back to contents](#table-of-contents)

<a id="package-internal-command-line-interfaces-openclawcli"></a>

## internal/command-line-interfaces/openclawcli

Runs OpenClaw agent exec and reads its final JSON envelope.

**Files (excluding tests)**

| File                                                             | Responsibility                                                                                                      |
| ---------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| [run.go](../internal/command-line-interfaces/openclawcli/run.go) | Invocation flags, prompt transport, default timeout, process-group cancellation, output bounds and result decoding. |

**Flow:** Start agent exec with worktree/cwd and stdin prompt → bound the invocation by its deadline → capture capped stdout/stderr → decode the envelope and return exit information. There is no live tool stream or resumable session.

[Back to contents](#table-of-contents)

<a id="package-internal-config"></a>

## internal/config

Loads machine configuration and enrolled repository settings, applies defaults and validates agent pools and tracker configuration.

**Files (excluding tests)**

| File                                        | Responsibility                                                                                            |
| ------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| [config.go](../internal/config/config.go)   | Configuration types, defaults, account/repository/worker validation and repository lookup.                |
| [load.go](../internal/config/load.go)       | Config paths and worker/full/unvalidated loading, including legacy-format checks.                         |
| [repo.go](../internal/config/repo.go)       | Repository YAML parsing, account resolution, discovery, deduplication and scan rows.                      |
| [agents.go](../internal/config/agents.go)   | Agent defaults and strict agents.yaml parsing; validates executor/role combinations and renamed settings. |
| [starter.go](../internal/config/starter.go) | Commented starter YAML and write-if-absent helpers for worker and repository enrollment.                  |

**Flow:** Read worker YAML → expand paths and defaults → discover or load explicit repositories → combine account settings with repo.yaml and agents.yaml → validate the resolved configuration. Starter writers support initial enrollment.

[Back to contents](#table-of-contents)

<a id="package-internal-discover"></a>

## internal/discover

Finds local Git repositories and reads their identity without enrolling them.

**Files (excluding tests)**

| File                                            | Responsibility                                                                              |
| ----------------------------------------------- | ------------------------------------------------------------------------------------------- |
| [discover.go](../internal/discover/discover.go) | Filesystem scanning, repository inspection, Git root/origin lookup and remote-name parsing. |

**Flow:** Scan configured roots with depth and exclusion limits → inspect candidate repositories → read origin and branch through Git → return facts for configuration and UI callers.

[Back to contents](#table-of-contents)

<a id="package-internal-dispatch"></a>

## internal/dispatch

Owns deterministic ticket orchestration, concurrency, role scheduling, retries and reporting.

**Files (excluding tests)**

| File                                            | Responsibility                                                                                                 |
| ----------------------------------------------- | -------------------------------------------------------------------------------------------------------------- |
| [dispatch.go](../internal/dispatch/dispatch.go) | Dispatcher configuration, polling loop, worker slots, in-flight ticket tracking and quota backoff.             |
| [agents.go](../internal/dispatch/agents.go)     | Role-to-column mapping, label-based agent selection, agent reservations and executor lookup.                   |
| [handle.go](../internal/dispatch/handle.go)     | Ticket handling, coding lifecycle, claims/heartbeats, resume decisions, finalization and state/report helpers. |
| [stages.go](../internal/dispatch/stages.go)     | Read-only planning and PR review, structured verdicts and review-round limits.                                 |
| [report.go](../internal/dispatch/report.go)     | Human-readable tracker comments and PR review bodies for every outcome.                                        |
| [trace.go](../internal/dispatch/trace.go)       | Executor wrapper that records runs, usage and CLI steps as child trace spans.                                  |

**Flow:** Check schedule and quota backoff → poll Planning, In Review and Ready for configured roles → reserve a ticket, agent and worker slot → claim the ticket → run its role → persist state and report. Coding prepares/resumes a worktree, triages when needed, executes, safety-commits and pushes, then finds/opens a PR. Planning posts a plan or question; review checks each new PR head and either leaves it for a human or sends it back with a bounded review loop. Claims and slots are released after handling.

[Back to contents](#table-of-contents)

<a id="package-internal-executor"></a>

## internal/executor

Defines the common boundary for coding agents and read-only planning/advice.

**Files (excluding tests)**

| File                                            | Responsibility                                                                                          |
| ----------------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| [executor.go](../internal/executor/executor.go) | Executor interface; task, advice, footprint, result, usage and step types; stop causes and output caps. |

**Flow:** The dispatcher builds a Task or Advice → calls the selected implementation → receives a Footprint, structured advice or Result → interprets the normalized stop cause, resume token, usage and steps.

[Back to contents](#table-of-contents)

<a id="package-internal-executor-antigravity"></a>

## internal/executor/antigravity

Adapts Antigravity CLI to the Executor interface.

**Files (excluding tests)**

| File                                                              | Responsibility                                                                                        |
| ----------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------- |
| [antigravity.go](../internal/executor/antigravity/antigravity.go) | Coding adapter for agy, invocation options and normalized outcomes, including resumed usage handling. |

**Flow:** Load policy → pass the prompt to agy as JSON stdin → run antigravitycli with the saved conversation ID when resuming → map steps, current-run usage and stop status. Planning and advice are unsupported.

[Back to contents](#table-of-contents)

<a id="package-internal-executor-claudecode"></a>

## internal/executor/claudecode

Adapts Claude Code to the Executor interface.

**Files (excluding tests)**

| File                                                           | Responsibility                                                                   |
| -------------------------------------------------------------- | -------------------------------------------------------------------------------- |
| [claudecode.go](../internal/executor/claudecode/claudecode.go) | Implements Run, Plan and Advise, policy loading, guidance and PATH handling.     |
| [command.go](../internal/executor/claudecode/command.go)       | Builds Claude Code arguments from policy and invocation settings.                |
| [outcome.go](../internal/executor/claudecode/outcome.go)       | Maps transcript/exit conditions and needs-input responses into executor results. |

**Flow:** Load worktree policy → build permission/model/resume arguments and guidance → invoke claudecli → normalize the result. Plan and Advise run read-only with structured output.

[Back to contents](#table-of-contents)

<a id="package-internal-executor-codex"></a>

## internal/executor/codex

Adapts Codex to the Executor interface.

**Files (excluding tests)**

| File                                                | Responsibility                                                              |
| --------------------------------------------------- | --------------------------------------------------------------------------- |
| [codex.go](../internal/executor/codex/codex.go)     | Implements Run, Plan and Advise and applies repository policy and guidance. |
| [command.go](../internal/executor/codex/command.go) | Builds codex exec arguments, sandbox settings, resume and schema options.   |
| [outcome.go](../internal/executor/codex/outcome.go) | Normalizes quota, timeout, error and needs-input outcomes.                  |

**Flow:** Load worktree policy → build sandbox/model/resume arguments → invoke codexcli → map the transcript and exit. Plan and Advise use a read-only sandbox and structured output.

[Back to contents](#table-of-contents)

<a id="package-internal-executor-deepcode"></a>

## internal/executor/deepcode

Adapts DeepCode to the Executor interface.

**Files (excluding tests)**

| File                                                     | Responsibility                                                                     |
| -------------------------------------------------------- | ---------------------------------------------------------------------------------- |
| [deepcode.go](../internal/executor/deepcode/deepcode.go) | Coding adapter, policy/PATH handling, session resume and DeepCode outcome mapping. |

**Flow:** Load policy → invoke deepcodecli with an optional saved session ID → collect the session transcript → map final status, question, usage and stop cause. Planning and advice are unsupported.

[Back to contents](#table-of-contents)

<a id="package-internal-executor-grok"></a>

## internal/executor/grok

Adapts Grok Build to the Executor interface.

**Files (excluding tests)**

| File                                         | Responsibility                                                                                                     |
| -------------------------------------------- | ------------------------------------------------------------------------------------------------------------------ |
| [grok.go](../internal/executor/grok/grok.go) | Coding adapter, temporary prompt, model/turn settings, shared runner invocation and Grok-specific outcome mapping. |

**Flow:** Load policy and guidance → write the prompt to a private temporary file → invoke Grok using the claudecli Messages parser → map tool/turn/quota/exit outcomes → remove the prompt file. Planning and advice are unsupported.

[Back to contents](#table-of-contents)

<a id="package-internal-executor-langgraph"></a>

## internal/executor/langgraph

Adapts the optional Python hivegraph workflow to the Executor interface.

**Files (excluding tests)**

| File                                                        | Responsibility                                                                                                         |
| ----------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| [langgraph.go](../internal/executor/langgraph/langgraph.go) | Task wire format, policy/checkpoint setup, graph subprocess invocation, outcome mapping and inner-executor delegation. |

**Flow:** Load policy and Git checkpoint locations → serialize a task to graphcli → consume workflow events → normalize the final result and resume token. Plan/Advise delegate to a selected inner CLI executor; coding may use a CLI or the Python coder.

[Back to contents](#table-of-contents)

<a id="package-internal-executor-openclaw"></a>

## internal/executor/openclaw

Adapts OpenClaw to the Executor interface.

**Files (excluding tests)**

| File                                                     | Responsibility                                                                        |
| -------------------------------------------------------- | ------------------------------------------------------------------------------------- |
| [openclaw.go](../internal/executor/openclaw/openclaw.go) | Coding adapter, guidance/PATH settings, budget limitation notice and outcome mapping. |

**Flow:** Load policy → invoke openclawcli in the worktree → interpret the final JSON envelope and process exit. Every turn starts a fresh temporary session; ticket history supplies follow-up context. No step-budget enforcement or read-only planning/advice is available.

[Back to contents](#table-of-contents)

<a id="package-internal-githost"></a>

## internal/githost

Defines hosting operations for pull requests, separate from issue tracking.

**Files (excluding tests)**

| File                                         | Responsibility                                                       |
| -------------------------------------------- | -------------------------------------------------------------------- |
| [githost.go](../internal/githost/githost.go) | GitHost interface and PR/request data shared by real and fake hosts. |

**Flow:** The dispatcher asks for a PR by head branch → opens one if missing → reads its head for review and posts review text through the same host boundary.

[Back to contents](#table-of-contents)

<a id="package-internal-githost-github"></a>

## internal/githost/github

Implements pull-request hosting with the GitHub REST API.

**Files (excluding tests)**

| File                                                  | Responsibility                                                                                |
| ----------------------------------------------------- | --------------------------------------------------------------------------------------------- |
| [github.go](../internal/githost/github/github.go)     | REST client, PR conversion, find/open/review operations and write-permission probe.           |
| [discover.go](../internal/githost/github/discover.go) | Token discovery from the configured environment, gh authentication and Git credential helper. |

**Flow:** Discover credentials during wiring → construct the client → find or open a PR → post COMMENT reviews. Preflight can probe PR write access before starting work.

[Back to contents](#table-of-contents)

<a id="package-internal-githost-none"></a>

## internal/githost/none

Supports workers without credentials for automatic PR creation.

**Files (excluding tests)**

| File                                        | Responsibility                          |
| ------------------------------------------- | --------------------------------------- |
| [none.go](../internal/githost/none/none.go) | Credential-free GitHost implementation. |

**Flow:** FindPR returns no PR → OpenPR returns an actionable error → dispatcher reports the pushed branch for manual PR creation; Review is a no-op.

[Back to contents](#table-of-contents)

<a id="package-internal-gitops"></a>

## internal/gitops

Defines the workspace boundary between dispatch and Git.

**Files (excluding tests)**

| File                                      | Responsibility                               |
| ----------------------------------------- | -------------------------------------------- |
| [gitops.go](../internal/gitops/gitops.go) | Workspaces interface and Workspace metadata. |

**Flow:** Prepare a ticket workspace → let an executor edit it → optionally read its diff → Finalize remaining edits and publish the branch.

[Back to contents](#table-of-contents)

<a id="package-internal-gitops-git"></a>

## internal/gitops/git

Implements ticket workspaces with base clones and Git worktrees.

**Files (excluding tests)**

| File                                    | Responsibility                                                                                  |
| --------------------------------------- | ----------------------------------------------------------------------------------------------- |
| [git.go](../internal/gitops/git/git.go) | Base clone preparation, ticket worktree creation/resume, finalization/push and diff generation. |
| [run.go](../internal/gitops/git/run.go) | Git subprocess helpers, explicit commit identity and ref-existence checks.                      |

**Flow:** Ensure/fetch the base clone → create or resume the ticket branch/worktree → execute outside the Git adapter → safety-commit remaining changes and push. Diff compares ticket work with the base for review; repository locks serialize shared mutations.

[Back to contents](#table-of-contents)

<a id="package-internal-gitops-repolock"></a>

## internal/gitops/repolock

Shares a per-repository lock between workspace and state-branch Git operations.

**Files (excluding tests)**

| File                                                   | Responsibility                          |
| ------------------------------------------------------ | --------------------------------------- |
| [repolock.go](../internal/gitops/repolock/repolock.go) | Repository-keyed shared mutex registry. |

**Flow:** Resolve a lock by repository identity → acquire it before shared Git mutations → release it afterwards so concurrent tickets cannot race on repository metadata.

[Back to contents](#table-of-contents)

<a id="package-internal-prompt"></a>

## internal/prompt

Renders ticket context into coding, planning and review prompts.

**Files (excluding tests)**

| File                                      | Responsibility                                                                        |
| ----------------------------------------- | ------------------------------------------------------------------------------------- |
| [prompt.go](../internal/prompt/prompt.go) | Prompt templates and render helpers for coding, resumed work, planning and PR review. |

**Flow:** Combine ticket description, comments and repository context → select the prompt for the role or resume → include triage/review guidance and diff where applicable → pass text to an executor.

[Back to contents](#table-of-contents)

<a id="package-internal-repoconfig"></a>

## internal/repoconfig

Reads the committed agent policy from the ticket worktree.

**Files (excluding tests)**

| File                                                  | Responsibility                                                                                               |
| ----------------------------------------------------- | ------------------------------------------------------------------------------------------------------------ |
| [repoconfig.go](../internal/repoconfig/repoconfig.go) | Policy types, parser/loader, permission and sandbox validation, graph loop limits and command/PATH settings. |

**Flow:** Load .hive-dispatch/policy.yaml → reject obsolete policy location when applicable → apply and validate executor/check/graph defaults → expand extra PATH entries for the executor.

[Back to contents](#table-of-contents)

<a id="package-internal-schedule"></a>

## internal/schedule

Evaluates configured time windows for starting work.

**Files (excluding tests)**

| File                                            | Responsibility                          |
| ----------------------------------------------- | --------------------------------------- |
| [schedule.go](../internal/schedule/schedule.go) | Run-window parsing and Open evaluation. |

**Flow:** Parse run windows and timezone → evaluate the current time, including windows spanning midnight → allow or defer polling. A nil schedule permits work at any time.

[Back to contents](#table-of-contents)

<a id="package-internal-state"></a>

## internal/state

Defines persisted ticket runs and their event/log storage boundary.

**Files (excluding tests)**

| File                                   | Responsibility                                                   |
| -------------------------------------- | ---------------------------------------------------------------- |
| [state.go](../internal/state/state.go) | Run phases, Run and LogEntry records and the RunStore interface. |

**Flow:** Dispatch loads or creates a run → records its phase before the next action → saves outcome, resume and review metadata → readers list runs or prune old records through RunStore.

[Back to contents](#table-of-contents)

<a id="package-internal-state-gitbranch"></a>

## internal/state/gitbranch

Persists run state on the orphan hive/state branch.

**Files (excluding tests)**

| File                                                     | Responsibility                                                                                       |
| -------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| [gitbranch.go](../internal/state/gitbranch/gitbranch.go) | State-branch initialization, RunStore operations, repository locking and conflict-aware commit/push. |
| [run.go](../internal/state/gitbranch/run.go)             | Git command, commit-identity and reference helpers.                                                  |

**Flow:** Open or create the state worktree → write a run/event/log → commit and push before continuing. On push rejection, fetch and rebase disjoint changes; fail with a claim-invariant error if another worker changed the same owned files.

[Back to contents](#table-of-contents)

<a id="package-internal-state-localdir"></a>

## internal/state/localdir

Stores runs and logs in a local directory; used by tests and local-state callers.

**Files (excluding tests)**

| File                                                  | Responsibility                                                             |
| ----------------------------------------------------- | -------------------------------------------------------------------------- |
| [localdir.go](../internal/state/localdir/localdir.go) | RunStore implementation with JSON runs and per-ticket log directories.     |
| [tree.go](../internal/state/localdir/tree.go)         | Shared run-listing and pruning helpers, also used by the Git-backed store. |

**Flow:** Load/save runs as JSON with atomic replacement → append ticket events or raw logs → list records → prune old raw logs and finished runs while retaining event logs.

[Back to contents](#table-of-contents)

<a id="package-internal-state-router"></a>

## internal/state/router

Presents per-repository state stores as one RunStore.

**Files (excluding tests)**

| File                                            | Responsibility                                     |
| ----------------------------------------------- | -------------------------------------------------- |
| [router.go](../internal/state/router/router.go) | State routing and aggregate list/prune operations. |

**Flow:** Select the store by ticket-key prefix for load/save/log operations → fan out List and Prune across stores → combine results and errors.

[Back to contents](#table-of-contents)

<a id="package-internal-statusline"></a>

## internal/statusline

Keeps a transient worker status line alongside normal terminal output.

**Files (excluding tests)**

| File                                                  | Responsibility                                          |
| ----------------------------------------------------- | ------------------------------------------------------- |
| [statusline.go](../internal/statusline/statusline.go) | Synchronized terminal status-line writer and lifecycle. |

**Flow:** Set the latest status → clear/redraw around ordinary writes → clear the line when work ends. The CLI uses it to display time since the last poll.

[Back to contents](#table-of-contents)

<a id="package-internal-supervisor"></a>

## internal/supervisor

Implements the operator assistant used by both the terminal and website.

**Files (excluding tests)**

| File                                                    | Responsibility                                                                                      |
| ------------------------------------------------------- | --------------------------------------------------------------------------------------------------- |
| [supervisor.go](../internal/supervisor/supervisor.go)   | Connects a supervisor Session to the terminal REPL and its confirmation/progress callbacks.         |
| [config.go](../internal/supervisor/config.go)           | Supervisor settings, provider presets/overrides, validation, model construction and Ollama probing. |
| [graphconfig.go](../internal/supervisor/graphconfig.go) | Loads optional graph model/binary settings, inheriting compatible supervisor settings.              |
| [agent.go](../internal/supervisor/agent.go)             | Model/tool loop, bounded turns, conversation history, cancellation events and tracing.              |
| [session.go](../internal/supervisor/session.go)         | Frontend-independent session creation, resume, per-turn checks and transcript persistence.          |
| [repl.go](../internal/supervisor/repl.go)               | Terminal input, confirmations, progress/spinner output and interrupt handling.                      |
| [memory.go](../internal/supervisor/memory.go)           | Persistent notes, prompt-sized note selection and session transcript storage.                       |
| [prompt.go](../internal/supervisor/prompt.go)           | System-prompt construction and hivedispatch check output capture.                                   |
| [tools.go](../internal/supervisor/tools.go)             | Read/write config, read embedded docs, remember notes and supervisor-config tools.                  |
| [tools_run.go](../internal/supervisor/tools_run.go)     | Allowlisted hivedispatch command tool, argument validation, confirmation and subprocess execution.  |

**Flow:** Load provider settings and memory → create a session with tools and a fresh system prompt/check output → call the model → execute requested tools (confirming writes/actions where required) → repeat within the turn budget → save the conversation and emit progress to the frontend.

[Back to contents](#table-of-contents)

<a id="package-internal-supervisor-model"></a>

## internal/supervisor/model

Defines the supervisor chat-model boundary independently of any provider.

**Files (excluding tests)**

| File                                              | Responsibility                                                                                 |
| ------------------------------------------------- | ---------------------------------------------------------------------------------------------- |
| [model.go](../internal/supervisor/model/model.go) | Model interface, message/tool/request/response/usage types and HTTP/rate-limit error decoding. |

**Flow:** Build a Request from system text, conversation and tool definitions → call Model.Chat → process normalized text/tool calls, stop reason and usage. HTTP errors and rate limits have shared representations.

[Back to contents](#table-of-contents)

<a id="package-internal-supervisor-model-anthropic"></a>

## internal/supervisor/model/anthropic

Implements the Anthropic Messages protocol for the supervisor.

**Files (excluding tests)**

| File                                                                | Responsibility                                                |
| ------------------------------------------------------------------- | ------------------------------------------------------------- |
| [anthropic.go](../internal/supervisor/model/anthropic/anthropic.go) | Messages client, wire conversion and response/error handling. |

**Flow:** Validate settings → convert history into alternating roles with tool results → send a Messages request → normalize text, tool use, stop reason, usage or errors.

[Back to contents](#table-of-contents)

<a id="package-internal-supervisor-model-openai"></a>

## internal/supervisor/model/openai

Implements the OpenAI-compatible chat/completions protocol for configured providers.

**Files (excluding tests)**

| File                                                       | Responsibility                                                                   |
| ---------------------------------------------------------- | -------------------------------------------------------------------------------- |
| [openai.go](../internal/supervisor/model/openai/openai.go) | Client configuration, request conversion and chat/completions response handling. |

**Flow:** Validate endpoint/model settings → convert messages and tools to the provider wire format → make an HTTP request → normalize assistant text, tool calls, stop reason, usage or errors.

[Back to contents](#table-of-contents)

<a id="package-internal-trace"></a>

## internal/trace

Records nested ticket, model and tool activity behind an exporter interface.

**Files (excluding tests)**

| File                                   | Responsibility                                                                                |
| -------------------------------------- | --------------------------------------------------------------------------------------------- |
| [trace.go](../internal/trace/trace.go) | Run/exporter types, tracer/span lifecycle, trace IDs, hierarchy, usage and data sanitization. |

**Flow:** Start a span under the context parent → attach metadata/usage → finish with outputs/error → hand snapshots to the exporter → flush on Close. Nil tracers/spans are no-ops; strings are capped and configured inputs/outputs hidden.

[Back to contents](#table-of-contents)

<a id="package-internal-trace-langsmith"></a>

## internal/trace/langsmith

Exports traces asynchronously to LangSmith without making ticket success depend on telemetry.

**Files (excluding tests)**

| File                                                     | Responsibility                                                                               |
| -------------------------------------------------------- | -------------------------------------------------------------------------------------------- |
| [env.go](../internal/trace/langsmith/env.go)             | Environment-based enablement, endpoint/project/key settings and input/output hiding options. |
| [langsmith.go](../internal/trace/langsmith/langsmith.go) | Bounded exporter queue, background batches, HTTP delivery and shutdown.                      |

**Flow:** Read tracing environment settings → enqueue span starts/ends → batch them to the runs API in the background → drop/log unsendable work → flush within the caller deadline on close.

[Back to contents](#table-of-contents)

<a id="package-internal-tracker"></a>

## internal/tracker

Defines issue-tracker operations and a provider-independent ticket lifecycle.

**Files (excluding tests)**

| File                                         | Responsibility                                                                                       |
| -------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| [tracker.go](../internal/tracker/tracker.go) | Tracker interface, lifecycle states, ticket/comment/claim data, claim freshness and sentinel errors. |
| [name.go](../internal/tracker/name.go)       | Readable ticket/branch naming and candidate key extraction from names.                               |

**Flow:** Poll a lifecycle state → fetch normalized ticket/thread data → claim with write/read-back semantics → heartbeat while working → comment/transition → release ownership.

[Back to contents](#table-of-contents)

<a id="package-internal-tracker-ghissues"></a>

## internal/tracker/ghissues

Implements issue tracking with GitHub labels, body claim markers and optional Projects v2 mirroring.

**Files (excluding tests)**

| File                                                  | Responsibility                                                                              |
| ----------------------------------------------------- | ------------------------------------------------------------------------------------------- |
| [client.go](../internal/tracker/ghissues/client.go)   | Authenticated REST transport, configuration and GitHub API errors.                          |
| [issues.go](../internal/tracker/ghissues/issues.go)   | Paginated polling, issue/comment reads, normalized tickets, comments and label transitions. |
| [claim.go](../internal/tracker/ghissues/claim.go)     | Hidden body marker encoding, ownership verification, heartbeat and release.                 |
| [project.go](../internal/tracker/ghissues/project.go) | GraphQL board/column resolution and issue-card status updates.                              |
| [admin.go](../internal/tracker/ghissues/admin.go)     | Credential/repository/label/board checks and missing-label creation.                        |

**Flow:** Poll open issues with a lifecycle label → load comments and normalize ticket data → claim through a body marker and read-back → heartbeat/release through that marker → post comments and swap lifecycle labels. When configured, mirror the resulting state to a project board after changing labels.

[Back to contents](#table-of-contents)

<a id="package-internal-tracker-jira"></a>

## internal/tracker/jira

Implements ticket tracking with Jira Cloud REST API v3.

**Files (excluding tests)**

| File                                              | Responsibility                                                                     |
| ------------------------------------------------- | ---------------------------------------------------------------------------------- |
| [client.go](../internal/tracker/jira/client.go)   | HTTP client setup, authentication, JSON transport and API error mapping.           |
| [issue.go](../internal/tracker/jira/issue.go)     | Paginated JQL polling, issue reads, ticket conversion and status-specific queries. |
| [claim.go](../internal/tracker/jira/claim.go)     | Claim write/read-back, holder checks, heartbeats and release.                      |
| [comment.go](../internal/tracker/jira/comment.go) | Posting comments and resolving/executing lifecycle transitions.                    |
| [adf.go](../internal/tracker/jira/adf.go)         | Atlassian Document Format to plain text and plain text to comment ADF.             |
| [admin.go](../internal/tracker/jira/admin.go)     | Claim-field discovery/creation, screen setup and read-only configuration checks.   |

**Flow:** Resolve configured claim fields → poll scoped JQL narrowed to the requested status → translate ADF and fields into tickets → claim by writing and reading back ownership → heartbeat, comment, transition and release. Admin helpers support enrollment and live checks.

[Back to contents](#table-of-contents)

<a id="package-internal-tracker-prefixed"></a>

## internal/tracker/prefixed

Adds each repository ticket_prefix to native tracker keys.

**Files (excluding tests)**

| File                                                    | Responsibility                                                      |
| ------------------------------------------------------- | ------------------------------------------------------------------- |
| [prefixed.go](../internal/tracker/prefixed/prefixed.go) | Tracker decorator forwarding all operations while translating keys. |

**Flow:** Wrap the provider tracker → add the prefix to returned tickets → strip and validate it before incoming Get/Claim/Comment/Transition calls → leave the provider working with native keys.

[Back to contents](#table-of-contents)

<a id="package-internal-tracker-router"></a>

## internal/tracker/router

Combines repository trackers into one queue for dispatch.

**Files (excluding tests)**

| File                                              | Responsibility                                                                    |
| ------------------------------------------------- | --------------------------------------------------------------------------------- |
| [router.go](../internal/tracker/router/router.go) | Tracker selection, multi-tracker polling and forwarding of individual operations. |

**Flow:** Poll trackers in project order → keep tickets belonging to each route and tolerate individual tracker failures → route ticket-specific calls by key prefix. Poll reports an error when every tracker fails.

[Back to contents](#table-of-contents)

<a id="package-internal-triage"></a>

## internal/triage

Defines the decision-only boundary before coding starts.

**Files (excluding tests)**

| File                                      | Responsibility                                        |
| ----------------------------------------- | ----------------------------------------------------- |
| [triage.go](../internal/triage/triage.go) | Triager interface, Input, Decision and verdict kinds. |

**Flow:** Supply ticket, repository and workspace context → ask Triager.Decide → receive dispatch, needs-info or reject → let the dispatcher perform the actual action.

[Back to contents](#table-of-contents)

<a id="package-internal-triage-claudecode"></a>

## internal/triage/claudecode

Uses Claude Code read-only to decide whether and how to attempt a ticket.

**Files (excluding tests)**

| File                                                   | Responsibility                                                              |
| ------------------------------------------------------ | --------------------------------------------------------------------------- |
| [triager.go](../internal/triage/claudecode/triager.go) | Triager configuration, read-only CLI arguments and decision execution.      |
| [prompt.go](../internal/triage/claudecode/prompt.go)   | Decision schema, triage prompt and validated conversion to triage.Decision. |

**Flow:** Build a constrained prompt/schema → run claudecli with read-only tools → decode and validate the structured verdict → render the coding prompt or return a question/rejection.

[Back to contents](#table-of-contents)

<a id="package-internal-triage-passthrough"></a>

## internal/triage/passthrough

Provides a rules-based baseline that dispatches every ticket.

**Files (excluding tests)**

| File                                                            | Responsibility                        |
| --------------------------------------------------------------- | ------------------------------------- |
| [passthrough.go](../internal/triage/passthrough/passthrough.go) | Unconditional Triager implementation. |

**Flow:** Receive triage input → render a coding prompt → return dispatch without calling a model.

[Back to contents](#table-of-contents)

<a id="package-internal-web"></a>

## internal/web

Serves the operator API, supervisor chat and embedded Vue app.

**Files (excluding tests)**

| File                                         | Responsibility                                                                                                                                                |
| -------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [server.go](../internal/web/server.go)       | Server options/lifecycle, routes, request guards and JSON helpers.                                                                                            |
| [api.go](../internal/web/api.go)             | Overview, checks, runs, repositories, enrollment, editable files and agent-pool endpoints.                                                                    |
| [chat.go](../internal/web/chat.go)           | Serialized supervisor conversation, event history/SSE, confirmations, cancellation and reset.                                                                 |
| [executors.go](../internal/web/executors.go) | Executor CLI version/usage discovery and fixed installer commands with bounded execution.                                                                     |
| [embed.go](../internal/web/embed.go)         | Embeds the committed dist frontend so Go builds do not need Node.                                                                                             |
| [vite.go](../internal/web/vite.go)           | Starts a private Vite process and proxies HTTP/HMR traffic through the Go server.                                                                             |
| [dist/](../internal/web/dist/)               | Generated index.html and hashed JavaScript/CSS bundles built from web/; tracked as one generated artifact tree rather than enumerating changing asset hashes. |

**Flow:** Apply authentication/host/origin checks → route API requests to config, discovery, status, CLI or supervisor helpers → stage YAML edits and return a diff before applying them → stream chat events over SSE. Non-API requests serve the committed frontend or proxy Vite in development.

[Back to contents](#table-of-contents)

<a id="package-internal-yamlfile"></a>

## internal/yamlfile

Stages and applies operator YAML edits while preserving comments and keeping backups.

**Files (excluding tests)**

| File                                            | Responsibility                                                                     |
| ----------------------------------------------- | ---------------------------------------------------------------------------------- |
| [yamlfile.go](../internal/yamlfile/yamlfile.go) | Comment-preserving dotted-key patches, staged validation and backup/atomic writes. |
| [diff.go](../internal/yamlfile/diff.go)         | Line-based diff rendering for operator review.                                     |

**Flow:** Patch the YAML node tree or supply replacement text → Stage parses and runs the supplied validator against a temporary sibling → show the diff/problems → caller authorizes application → Commit backs up and atomically replaces the file.

[Back to contents](#table-of-contents)

<a id="package-web"></a>

## web

The npm package for the Vue 3 operator frontend. Components and views are modules within this package, not separate npm packages.

**Files (excluding tests)**

| File                                                                        | Responsibility                                                                     |
| --------------------------------------------------------------------------- | ---------------------------------------------------------------------------------- |
| [package.json](../web/package.json)                                         | Frontend dependencies and development/build scripts.                               |
| [package-lock.json](../web/package-lock.json)                               | Locked npm dependency graph for reproducible installation.                         |
| [vite.config.js](../web/vite.config.js)                                     | Vue/Vite configuration and output directory.                                       |
| [index.html](../web/index.html)                                             | Browser document and app mount point.                                              |
| [src/main.js](../web/src/main.js)                                           | Vue bootstrap, router registration and global stylesheet import.                   |
| [src/App.vue](../web/src/App.vue)                                           | Application shell, navigation and supervisor sidebar layout.                       |
| [src/router.js](../web/src/router.js)                                       | Hash routes for dashboard, repositories, detail, configuration and executor setup. |
| [src/api.js](../web/src/api.js)                                             | JSON request/error wrapper and API operation functions.                            |
| [src/schemas.js](../web/src/schemas.js)                                     | Form schemas mapping UI fields to YAML keys.                                       |
| [src/markdown.js](../web/src/markdown.js)                                   | Escaped Markdown subset for supervisor replies.                                    |
| [src/style.css](../web/src/style.css)                                       | Shared layout, typography, controls and responsive styling.                        |
| [src/components/AgentsPanel.vue](../web/src/components/AgentsPanel.vue)     | Repository agent-pool listing and whole-list edits.                                |
| [src/components/ChatSidebar.vue](../web/src/components/ChatSidebar.vue)     | Chat history/SSE, messages, confirmations, cancellation and reset.                 |
| [src/components/ConfigEditor.vue](../web/src/components/ConfigEditor.vue)   | Schema forms and raw YAML editing with diff/validation preview before save.        |
| [src/components/DiffView.vue](../web/src/components/DiffView.vue)           | Visual rendering of additions, deletions and context in server diffs.              |
| [src/views/Dashboard.vue](../web/src/views/Dashboard.vue)                   | Worker overview, checks and recent runs.                                           |
| [src/views/Repos.vue](../web/src/views/Repos.vue)                           | Discovered repositories, filtering and navigation.                                 |
| [src/views/RepoDetail.vue](../web/src/views/RepoDetail.vue)                 | Enrollment/setup and repository settings, agents and policy tabs.                  |
| [src/views/Configuration.vue](../web/src/views/Configuration.vue)           | Worker/supervisor configuration editor.                                            |
| [src/views/AgentConfiguration.vue](../web/src/views/AgentConfiguration.vue) | Executor availability/version/usage and confirmed installation UI.                 |

**Flow:** index.html loads main.js → mount App and hash router → a view fetches JSON through api.js → forms stage/show/apply edits through the Go API → the chat sidebar subscribes to SSE and submits confirmation answers. Vite builds the production app into internal/web/dist.

[Back to contents](#table-of-contents)

## Update protocol

The contributor requirement lives in [CONTRIBUTING.md — Package guide maintenance](../CONTRIBUTING.md#package-guide-maintenance), and [AGENTS.md](../AGENTS.md) points coding agents to it. Update this guide and/or [testing.md](testing.md), according to the changed files, in the same change as the code; do not leave it for a later documentation task.

1. **New package:** add a section with a unique explicit anchor, purpose, non-test file table and operation flow; add its link to the alphabetically ordered table of contents. Cover Go packages, Python import packages and first-party npm packages, placing fake/helper packages in [testing.md](testing.md) instead.
2. **New file:** add a linked row to its owning package explaining its responsibility. For a test/fixture, update the relevant suite or support entry in [testing.md](testing.md) instead of this guide. Document a new generated tree and its source/build process as a group rather than enumerating generated hashes.
3. **Rename, move or deletion:** update/remove the old row or section, TOC entry, source links and affected flow descriptions. Check other package sections that name the moved responsibility.
4. **Behavior or boundary change:** update the purpose/flow even if no file was added. Describe the entry point, ordered operations, collaborating packages and material persistence, error, resume or cancellation behavior. Record architectural choices separately in the append-only decisions log.
5. **Verify before committing:** compare both guides with `go list ./...`, review non-test Go files with `rg --files cmd internal docs -g '*.go' -g '!**/*_test.go'`, inspect Python files with `rg --files graph/hivegraph -g '*.py'`, and inspect frontend source/package manifests with `rg --files web -g '!**/node_modules/**'`. Follow the scope exclusions above. Open/check the TOC anchors and source links; make sure every included file has a responsibility and every package has a flow. Run the checks required by CONTRIBUTING.md.
6. **Review:** mention the guide update in the PR description when the package/file layout or operation flow changed. Reviewers check coverage and accuracy alongside the code. This is a contributor protocol, not an automated CI inventory gate.
