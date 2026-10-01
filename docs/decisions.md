# Decisions log

Newest at the bottom. Each entry: what was decided, what was rejected, why.

## 2026-09-17 — Go, single binary

Decided: Go throughout. Rejected: Python (second runtime, no static binary), TypeScript (weaker subprocess supervision story). Why: the domain is concurrency and subprocess supervision, which goroutines and `context` are built for; self-hosted tools should be one file to install.

## 2026-09-17 — Jira owns claims, git owns history

Decided: claims are custom fields on the ticket, verified by read-back. Rejected: claiming via the state branch. Why: one-file-per-worker writes never collide, so git's push rejection never fires; two workers could both take a ticket cleanly. Jira gives one authoritative row per ticket.

## 2026-09-19 — Open source, Apache-2.0

Decided: Apache-2.0. Rejected: MIT (no patent grant), AGPL (deters adoption for a tool meant to be embedded in team workflows). The original spec's interview framing was removed; the decisions log is for contributors.

## 2026-09-19 — Tracker is an interface; Jira is the first implementation

Decided: `internal/tracker.Tracker` with `tracker/jira` as the MVP implementation and `tracker/fake` for tests. Rejected: hard-coding Jira as the spec did; building GitHub Issues first. Why: the maintainer has a Jira instance with admin rights and the spec's claim protocol is designed around Jira fields, but most open-source adopters live on GitHub Issues; the interface makes that an adapter, not a rewrite.

## 2026-09-19 — Triage runs through the Claude Code CLI in read-only mode

Decided: `claude -p --restricted --tools "Read,Grep,Glob" --permission-mode plan --json-schema …`. Rejected: a hand-written tool loop against the Messages API. Why: the CLI gives triage its repo-inspection tools for free, is read-only by construction, and needs no second credential — users on a subscription plan have no API key. A direct-API `Triager` can be added behind the same interface.

## 2026-09-19 — Claim before triage

Decided: claim first, then triage. Rejected: the spec's Ready → Triaged → Claimed order. Why: everything after the claim is owned by exactly one worker, so two workers never triage the same ticket. Cost: one field write on tickets that end up rejected.

## 2026-09-19 — Five Jira statuses; phase lives in the run file

Decided: Jira statuses `Ready`, `In Progress`, `Needs Info`, `In Review`, `Needs Human`, mapped from HiveDispatch's `State` enum via config. Rejected: one Jira status per spec lifecycle state (Triaged, Claimed, Running…). Why: transient states would churn transitions on every poll; the run file on the state branch already records phase at the granularity the spec needs.

## 2026-09-19 — Step budget by counting tool calls

Decided: the Claude Code adapter counts `tool_use` events on `--output-format stream-json` and cancels the subprocess at the budget. Rejected: `--max-turns`. Why: Claude Code 2.1.278 has no `--max-turns`; counting on the stream is provider-agnostic anyway.

## 2026-09-19 — Stable worktree path per ticket

Decided: `<workroot>/<repo>/<TICKET-KEY>`. Why: Claude Code keys session storage on the working directory, so `--resume` only works when the path does not change between runs. The resume token stays opaque to the orchestrator; the path guarantee is the adapter's.

## 2026-09-19 — Orchestrator-side safety commit

Decided: on any executor exit, the dispatcher commits a dirty tree as `hive: WIP (<cause>)`. Rejected: relying on the prompt's commit-as-you-go instruction alone. Why: the prompt is advice; the dispatcher commit is the guarantee that every stop point is safe.

## 2026-09-19 — Two-level config

Decided: worker config in `~/.config/hivedispatch/config.yaml` (tracker, agent id, workroot, repos, windows); repo config in `.hivedispatch.yaml` inside the governed repo (executor settings, allowed tools, budgets, prompt). Secrets only via env. Why: the worker needs config before it can clone anything; per-repo policy belongs in the repo where it is reviewable.

## 2026-09-19 — Executor.Plan() implemented, not wired

Decided: the Claude Code adapter implements `Plan()`; the single-worker dispatcher skips it. Why: footprints only matter with concurrent workers. Implementing it now keeps the interface honest without paying a model call per run.

## 2026-09-19 — Headless default is `dontAsk` plus an allowlist

Decided: default `--permission-mode dontAsk` with tools from repo config. Rejected: `bypassPermissions` as default. Why: an unattended agent with the user's git credentials should fail closed. `bypassPermissions` is an explicit opt-in for sandboxed runs.

## 2026-09-19 — Dispatcher comments carry a marker

Decided: every comment the dispatcher posts starts with `[HiveDispatch]`. Why: with a personal API token the dispatcher's comments are authored by the same Jira user as the human's, so author identity cannot separate questions from answers. The marker can.

## 2026-09-19 — Humans move a ticket back to Ready after answering

Decided: after `needs_info`, the ticket sits in Needs Info until a human answers and transitions it back to Ready; the next poll detects the reply and resumes. Rejected: polling Needs Info tickets for new comments. Why: an explicit transition is a deliberate "go" from a phone, and it keeps the trigger JQL the single definition of "work I may take". Automatic resume can be added later without changing the resume path.

## 2026-09-19 — Two-context shutdown: drain, then interrupt

Decided: the first Ctrl-C stops polling and lets the run in flight finish; the second cancels the run, and cleanup (safety commit, comment, release) still happens on a background context. Why: a safe stop beats a punctual one, but an operator must always be able to stop a runaway run without leaving a claimed ticket behind.

## 2026-09-19 — Dispatcher returns OutcomeFailed with an error on PR failure

Decided: if the branch pushed but the PR could not be opened, the ticket returns to Ready with a comment and the call returns an error. Why: the branch exists, so the next attempt finds or opens the PR without redoing the work; Ready is the only state the poller will pick up again.

## 2026-09-19 — Base clone is --no-checkout; one worktree per ticket

Decided: each repo gets one `--no-checkout` base clone and one worktree per ticket at a stable path. Rejected: a fresh clone per ticket (slow, no session resume), a single working clone with branch switching (one ticket at a time, and Claude Code's session store is keyed by path). Why: worktrees share objects, keep the per-ticket path stable for `--resume`, and let several tickets sit on disk at once.

## 2026-09-19 — One state branch per governed repo, routed by Jira project

Decided: `hive/state` lives in each repo HiveDispatch works in; a router picks the store by ticket prefix. Rejected: one central state repo. Why: the spec keeps state with the code it describes, and per-repo state means a repo's history and its run log travel together. The cost is one push per state write; acceptable at MVP scale.

## 2026-09-19 — Safety commits use a HiveDispatch identity, set via environment

Decided: the dispatcher's own commits (`hive: checkpoint`, `hive: WIP (...)`, state writes) are authored as `HiveDispatch <hivedispatch@localhost>`, set through `GIT_AUTHOR_*`/`GIT_COMMITTER_*` on the subprocess. Rejected: `-c user.name=...`. Why: reviewers can tell orchestrator housekeeping from agent work in `git log`; and environment variables win over `-c`, so a user who exports `GIT_AUTHOR_NAME` would otherwise silently override the identity.

## 2026-09-19 — State-branch conflict check is on the files we push

Decided: when a push is rejected, the store fetches and compares the incoming diff against the files it is about to push; any overlap is `ErrClaimInvariant`, otherwise it rebases. Discovered in testing: this catches a second worker's *first* write to a file another worker already created, not only later overwrites — earlier than the spec's wording implied, which is the safer side.

## 2026-09-19 — Never pass --bare to Claude Code

Decided: the adapter never uses `--bare`. Discovered: on 2.1.278, `--bare` skips loading stored credentials, so headless runs fail with "Not logged in". Hooks and plugins are constrained instead through the repo's tool allowlist.

## 2026-09-19 — Rate-limit events are the budget signal

Decided: `rate_limit_event` messages on the stream (status, resetsAt, per-window utilization) are the primary budget signal; `api_error_status == 429` and "usage limit" text are fallbacks. The spec assumed subscription plans expose no quota data; the stream does. The reset time goes into the recovery comment, and the utilization is the input for a later pre-flight check.

## 2026-09-19 — Per-repo policy is read by the executor from the worktree

Decided: the Claude Code adapter reads `.hivedispatch.yaml` from the ticket worktree at run time. Rejected: threading repo config through `executor.Task`. Why: keeps the executor contract unchanged and the policy versioned with the code it governs — the branch the agent works on carries the rules it works under.

## 2026-09-19 — Plan() prefers structured_output

Decided: with `--json-schema`, Claude Code returns both `structured_output` (object) and `result` (JSON string); the adapter uses the former and falls back to parsing the latter. Verified against 2.1.278.

## 2026-09-19 — Edited paths are relativised against the init cwd as well as the workspace

Decided: the stream parser relativises `Edit`/`Write` paths against both the workspace it was given and the `cwd` the CLI reported in its `init` event. Why: the two normally agree, but recorded transcripts and symlinked work roots do not, and `ChangedFiles` should be repo-relative either way.

## 2026-09-19 — Worktree is prepared before triage

Decided: the dispatcher prepares the ticket worktree first and hands its path to the triager. Rejected: triaging against the `--no-checkout` base clone (nothing to read) or a separate read-only checkout (a second copy per repo). Why: a worktree is cheap, the triager needs real files to inspect, and on dispatch the same worktree is used for the run.

## 2026-09-19 — Triage notes, not a triage-written prompt

Decided: the triager returns `notes` that are appended to the standard executor prompt, rather than authoring the whole prompt. Why: the standard prompt carries the invariants (commit as you go, no merging, HIVE_NEEDS_INPUT protocol); letting the triage model rewrite it would let a bad triage decision remove a guardrail.

## 2026-09-19 — One shared CLI runner

Decided: `internal/claudecli` owns process supervision and stream parsing for both executor and triager. Why: the step budget, timeout, process-group kill and log capture are the same problem in both places; the second user is what proved the extraction.

## 2026-09-19 — GitHub token is optional and discovered

Decided: the PR token is looked up as `HIVE_GITHUB_TOKEN` → `gh auth token` → git credential helper; with none found the worker degrades to "push the branch, ask a human to open the PR" rather than refusing to run. Rejected: making the token required (the first-run experience asked for a credential most users already have somewhere), and skipping PRs silently. Clarified: GitHub requires authentication to create a pull request even on public repositories, so "not needed for public repos" is true only for cloning and reading, not for opening PRs.

## 2026-09-19 — Claim field ids are resolved by name

Decided: `jira.fields.*` are optional; at startup the worker looks up "HiveDispatch Agent" and "HiveDispatch Claimed At" by name and uses their ids. Rejected: requiring the user to paste `customfield_NNNNN` ids after `init -jira`. Why: the ids are an API detail with no meaning to a person setting the tool up; the first-run feedback was that the field was unexplained. Explicit ids remain as an override for renamed fields or multiple sites.

## 2026-09-19 — Field listing uses /field/search; check verifies editability

Discovered on the first live run: `GET /rest/api/3/field` did not return the freshly created claim fields at all (they appeared only in `GET /rest/api/3/field/search`), so `init -jira` created a second pair and `run` could not find any. Decided: all field listing goes through `/field/search?type=custom` with pagination; `check -jira` warns about duplicate names and uses the lowest id. Also discovered: on a team-managed project, globally created fields are not editable on issues until added to the project's issue types by hand, and there is no public API for that. Decided: `check -jira` reads `editmeta` on a real issue and prints the exact click path when the fields are not editable, rather than letting the first claim fail at run time. `check` also verifies `repos[].jira_project` against the site's project keys, because the first live config had `1` where `SCRUM` was needed.

## 2026-09-20 — First live end-to-end run

SCRUM-5 on a team-managed Jira project → claimed by `worker-1` → passthrough triage → Claude Code in a worktree → one commit on `hive/SCRUM-5` → GitHub PR #1 → ticket commented and moved to In Review → claim released. Every phase was pushed to `hive/state` before the next action. The ticket was against HiveDispatch's own repository.

What the first run taught, all fixed before it succeeded: `/rest/api/3/field` hides new custom fields (use `/field/search`); team-managed projects need the claim fields added to each issue type by hand, and the Jira UI can freeze when adding a global date-time field (it eventually took — if it does not, the fallback is to collapse both fields into one text field, which the code does not yet do); `jira_project` is a key, not an id; `--bare` breaks headless auth; a GitHub token is required to open PRs even on public repos.

## 2026-09-20 — Ticket lifecycle is logged; polling is shown, not logged

Decided: the dispatcher logs one line per lifecycle step of a ticket it works — `picked up ticket`, `work started` (with attempt `n/max`), then `work finished` / `work needs info` / `work needs human` / `work failed` with the result the ticket was told about. The per-poll `handled` line is gone: it fired for every skipped ticket on every poll and said nothing. Rejected: logging each poll (the first-run feedback was explicitly "I don't want polling logged"). Instead `run` keeps a status line at the bottom of the terminal — `last poll <time> (<n>s ago)`, refreshed every second — via a `Polled` hook on the dispatcher, so a quiet worker still visibly has a pulse. The logger is routed through the status line so log output scrolls above it rather than tearing it; the line is only drawn when stderr is a terminal and the worker is looping (`-once` and pipes get plain logs). Rejected: a TUI or a third-party terminal library — one erase-and-redraw escape sequence in the stdlib is all the feature needs.

## 2026-09-20 — The ticket thread is the review channel (verified)

PR #2 failed lint in CI. A comment on the ticket naming the failures, plus moving it back to Ready, made the worker re-run the ticket with `--resume` on the stored session: the agent fixed the three errcheck findings in context and pushed; the PR was merged. Decided: keep the resume token on completed runs (not only after needs_info) so a ticket can be iterated from its thread without re-explaining. Rejected for now: watching PR check runs and commenting automatically — the manual loop works and automating it belongs to the Review capability on the roadmap.

## 2026-09-20 — Retention ages raw logs by mtime; events are never pruned

Decided: `retention_days` (default 30) removes raw executor logs older than the cutoff and run records at phase `done` not updated since; `events.jsonl` stays as the audit trail. git does not preserve mtimes, so on a fresh clone every log looks new and is kept — the safe side of a retention error. Rejected: dating logs from their filenames (fragile) or pruning events (destroys the "what did the swarm do" answer).

## 2026-09-20 — status reads local state; once bypasses the queue by design

`status` opens the state stores and nothing else: no Jira call, no token discovery, no executor, so it is safe to run anywhere and answers from the last push. `once KEY` deliberately ignores the trigger JQL and run windows — it is the operator saying "this one, now" — but still runs the Jira preflight and the normal claim protocol, so it cannot collide with a running worker.

## 2026-09-20 — GitHub Issues tracker: labels for state, a body marker for the claim

Decided: with `tracker: github`, one `hive:*` label at a time carries the lifecycle state and a hidden `<!-- hivedispatch-claim: agent time -->` comment at the end of the issue body carries the claim; keys are `<project>-<number>`. Rejected: the assignee as the claim (all workers share one account, so it cannot tell them apart), a per-worker label (litters the label list), GitHub Projects fields (a second system to set up, and no phone-tap equivalent to a label). Why: labels are visible and one tap on a phone; the body marker is invisible to readers, survives edits, and supports write-then-read-back exactly like the Jira fields. This is the second `Tracker` implementation the interface was waiting for; the dispatcher did not change.

## 2026-09-20 — AGENTS.md is the agent instruction file; CLAUDE.md imports it

Decided: `AGENTS.md` at the repo root is the single instruction set for coding agents, and `CLAUDE.md` is a one-line `@AGENTS.md` import so Claude Code loads the same text. It points at `CONTRIBUTING.md` for conventions rather than restating them, and makes the pre-commit / pre-push check (`go vet`, `go test -race`, `golangci-lint run`, scoped to touched packages per commit and module-wide before push) an explicit required step. Rejected: two copies of the same content (they drift), a symlink (not portable across every checkout), and `CLAUDE.md` as the canonical file (`AGENTS.md` is the vendor-neutral name other agent CLIs read).

## 2026-09-20 — The session-limit scenario, live

The first GitHub Issues run hit the Claude session limit mid-work. What held: the run stopped with `cause=budget`, the comment carried the reset time, the claim was released, and after the reset the next attempt resumed the same session and pushed. What did not: while the quota was exhausted the loop kept claiming and re-triaging every poll (four times in four minutes, each a wasted CLI call), and after the push the PR failed on a token without pull-request write, which sent the ticket back through triage — which, reading the failure comment, sensibly asked a human rather than re-running the agent.

Decided:
- A budget stop from the executor or the triager pauses the whole worker until the provider's reset time plus a minute (15 minutes when no reset time is known). A rate limit is a fleet condition, not a ticket condition. Rejected: per-ticket backoff (every other ticket would hit the same wall).
- `Result.RetryAfter` and `claudecli.BudgetError` carry the reset time as data; `LooksLikeBudget` is the one place that decides what counts as a budget stop, and now includes "session limit".
- A run that completed and pushed but has no PR is finished on the next poll without triage or the agent: the artifacts say exactly where it stopped, as the spec argues. The run stays at phase `pushed` when the PR fails so this path is taken.
- `check -live` and the run preflight probe pull-request write access with a POST whose head branch does not exist (422 with permission, 403 without). GitHub exposes no read-only way to learn a fine-grained token's permissions.

## 2026-09-20 — Repo policy can extend the agent's PATH

Decided: `executor.path` in `.hivedispatch.yaml` prepends directories to the agent's `PATH`. Rejected: allowing absolute tool paths in `allowed_tools` (Claude Code matches whole tokens by prefix, so `Bash(golangci-lint:*)` never matches `/home/me/go/bin/golangci-lint`), and the `go run …@latest` fallback (its command token is `github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest`, which the allowlist entry did not match either, and it recompiles the linter every run). Observed on PR #4: the agent tried all three, was denied each time, and stopped rather than routing around the policy — the right behaviour, and the reason the fix belongs in the environment.

## 2026-09-20 — Ticket keys are the identity; reused worktrees fast-forward

Observed: the GitHub repo was configured with `project: SCRUM`, so issue #5 became `SCRUM-5` — the same key as the Jira ticket whose PR #1 had merged the night before. The dispatcher reused that ticket's worktree (cut from a main that predated the GitHub Issues tracker) and its run record, and triage correctly rejected the issue against code that no longer existed. Decided: `Prepare` fast-forwards an existing worktree to the default branch when the branch has no commits of its own (a merged branch), and never touches a diverged one; config rejects two repos sharing a project key; docs say to pick a distinct prefix per tracker. Rejected: namespacing keys by tracker automatically — the key is meant to be what a human reads on the branch and the PR, and the collision is a configuration choice they can see.

## 2026-09-20 — A GitHub Projects board mirrors the labels; it never owns the queue

Decided: with `tracker: github`, an optional `github.project` block names a Projects (v2) board, and every `Transition` — after the label swap — adds the issue to the board if needed and sets a single-select field (`Status` by default) to the option configured for the new state. Labels remain the queue, the claim marker remains the claim, and a board failure is logged and reported against the label that did land, never rolled back. `check -live` resolves the board and lists the columns it lacks; columns are not created automatically. This narrows the "GitHub Projects fields" rejection in the GitHub Issues entry above: the board is a mirror, still not where state lives.

Rejected: polling the board's column instead of the label (Projects has no REST API and no cheap "items in column X" query; the label is one tap on a phone and the board is not), rolling the label back when the board move fails (two writes that can each fail is worse than one authoritative write and a visible mirror lag), an `owner_type: user|org` setting (`repositoryOwner(login:)` with a `ProjectV2Owner` fragment resolves either in one query), and creating missing options with `updateProjectV2Field` (it replaces the whole option list and can drop what a human set up). Why GraphQL: Projects v2 has no REST surface at all; the client posts to `graphql` resolved against `api_url`, which lands on `/graphql` for github.com and `/api/graphql` on GHES.

## 2026-09-20 — Token kinds are interchangeable to the code; not to GitHub's permission model

Both classic and fine-grained personal access tokens are sent as `Authorization: Bearer` and nothing inspects the prefix, so either works everywhere HiveDispatch talks to GitHub. Learned while wiring the Projects board: fine-grained tokens have no account-level Projects permission, so a **user-owned** board needs a classic token with `project` (organisation boards accept either). Documented as a table by need rather than by token kind, because that is the question people actually have.

## 2026-09-20 — A run record remembers its ticket's URL

Observed: with the GitHub repo still keyed `SCRUM`, issue #N loaded Jira ticket SCRUM-N's run record and resumed *its* Claude session — the agent carried on a conversation about a different ticket. Decided: the record stores the ticket URL; when a key resolves to a ticket with a different URL, the record is discarded (attempts, resume token, PR) and the run starts fresh with a warning. The correct configuration is still a distinct `project` per tracker; this makes the wrong one safe.

## 2026-09-20 — Codex is the second executor; it gets its own repo-policy block

Decided: `executor: codex` runs tickets with the Codex CLI through a new `internal/executor/codex` adapter on a new `internal/codexcli` runner. Codex speaks its own JSONL (`codex exec --json`: `thread.started` carries the thread id, `item.*` events carry commands and file changes, `turn.completed` / `turn.failed` / `error` end the run), so it gets its own parser rather than a second dialect bolted onto `claudecli`; process supervision (process-group kill, step budget, bounded logs) is duplicated deliberately — about eighty lines — instead of extracting a shared runner that would have to know about both stream formats. The thread id is the resume token and later runs pass `codex exec resume <id>`; the orchestrator did not change. Plan runs `--sandbox read-only --ephemeral --output-schema` and reads the footprint from the final message. `--full-auto` is not used (the installed CLI, 0.154, no longer has it); approvals are forced off with `-c approval_policy=never` because a headless run has nobody to answer, and the sandbox mode is the repo policy.

The repo policy fork: `.hivedispatch.yaml`'s `executor.model` / `permission_mode` / `tools` / `allowed_tools` / `max_budget_usd` are Claude Code vocabulary — permission modes and tool allowlists have no Codex equivalent, Codex has no per-run USD cap, and a model name like `sonnet` would break `codex -m`. Decided: a separate `executor.codex` block (`model`, `sandbox`, `network`), and the codex executor ignores the Claude keys entirely; `executor.path` and `guidance` are shared because they are about the repo, not the agent. Rejected: mapping `permission_mode` onto sandbox modes (`dontAsk` fails closed per tool call while a sandbox denies by capability — the semantics do not line up and a wrong guess would be silent), and making the Claude keys executor-agnostic by renaming them (breaks every existing repo policy for no gain). A repo can carry both blocks so any worker can run it.

Not done: budget stops from Codex carry no reset time (the CLI reports quota only as error text, matched by `codexcli.LooksLikeBudget`), so the worker pauses for the 15-minute default; triage still runs on Claude Code (`triage.kind: passthrough` avoids it) — a Codex triager is a separate ticket. The adapter was verified against the fake CLI and the flag and event names embedded in the installed `codex` binary, not against a live run.

## 2026-09-21 — The supervisor is HiveDispatch's own agent loop

Decided: `hivedispatch supervisor` runs its own agent loop against a model interface (`internal/supervisor`), not a wrapper around Claude Code or Codex in interactive mode. Rejected: `claude --append-system-prompt` wrapping the CLI — less code, but the point is an agent HiveDispatch owns, with memory and tools it controls, that can grow into the fleet-level decisions the design spec reserves for a supervisor.

## 2026-09-21 — OpenAI-compatible chat/completions is the second provider shape

Decided: one `internal/supervisor/model/openai` adapter speaks the OpenAI `chat/completions` wire format, reused for Hugging Face's router, Ollama, OpenAI itself, Groq and vLLM; Anthropic gets its own adapter for its `messages` API. Rejected: a per-vendor adapter each. Why: the wire format is shared across every OpenAI-compatible provider — only the base URL, key, and vendor label differ.

## 2026-09-21 — Tool subcommands re-exec the binary

Decided: the supervisor's `run_hivedispatch` tool re-execs `os.Executable()` with an allowlisted argv (`check`, `init -jira`, `status -json`, `run -once -executor fake`, …) rather than calling `check`/`init` as a library. Rejected: moving those subcommands into an internal package `main.go` could call directly. Why: that refactor buys no user-visible gain, and re-exec shows the operator exactly the output they would see running the command themselves; the allowlist is a list of argv shapes, easy to read and to extend.

## 2026-09-21 — Confirm before mutate, in code

Decided: every supervisor tool that changes something outside its own notes file — writing the config, `init`, `init -jira`/`-github`, `run -once` — shows the change and asks `[y/N]` in the terminal before acting, enforced by the tool's Go code. Rejected: trusting the system prompt to ask first, as with triage and the executor allowlist. Why: the guardrail has to be code the model cannot talk its way around.

## 2026-09-21 — Notes file plus transcripts, no compaction

Decided: the supervisor keeps a plain-text `memory.md` it appends to via a `remember` tool, plus one JSON transcript per session — `-resume` reloads the newest, `-session FILE` a specific one — and rebuilds the system prompt every turn from the current notes and config rather than replaying history. Rejected: replaying every past transcript into context (grows without bound) and a model-written summary between sessions (an extra call whose failure mode is silent, undetectable loss).

## 2026-09-21 — Segregated: one directory, its own config file

Decided: the supervisor's settings live in `<config dir>/supervisor/config.yaml`, alongside its `memory.md` and `sessions/`, entirely separate from the worker config it edits; the default models in that file's table are best-effort names, and the docs tell the operator to check the provider's catalogue and set them explicitly. Rejected: a `supervisor:` block inside the worker config. Why: that would put the assistant's own settings inside the file it is meant to write and diff for the operator, and would spread supervisor code through `config`, `starter`, and the docs table for no benefit.

## 2026-09-21 — Supervisor: what diverged from the spec during the build

Decided: `write_config` writes a config that parses but is still incomplete and reports the loader's remaining problems back through the tool result, rather than refusing anything short of a fully valid config. Rejected: requiring a complete, `config.Load`-clean file before writing. Why: a Jira config needs `HIVE_JIRA_TOKEN` set before it validates, and that token often does not exist yet at the point the operator wants the rest of the file saved; the operator would otherwise be stuck unable to save partial progress.

Decided: `run_hivedispatch`'s output cap is 32 KiB per stream (stdout and stderr each), not the 64 KiB combined the design spec's tool table describes. Rejected: reconciling the two by shrinking the cap or rewriting the spec's table. Why: 32 KiB per stream was what the plan built and shipped with; this entry is the record of the divergence rather than a silent edit to either the spec or the code.

Decided (this wave): the spinner is scoped to the model call in flight — started on `Agent.Turn`'s `model_start` event, stopped on `model_done` — instead of wrapping the whole turn. Rejected: leaving it wrapping the turn. Why: a tool's own `[y/N]` confirmation prompt happens inside a turn, and the ticker was overwriting it mid-wait.

Decided (this wave): `hivedispatch check` is captured once per turn, in `REPL.turn`, with the turn's own context, rather than re-run by `Agent.System` before every model call in the turn. Rejected: leaving it in `System`. Why: a 20-step turn re-executed the binary up to 21 times, and each `check` shells out to `gh`/the git credential helper with its own timeouts when no GitHub token is configured.

Decided (this wave): in `internal/supervisor/model/openai`, vendor `openai` sends `max_completion_tokens` instead of `max_tokens`. Rejected: sending both, or always sending `max_tokens`. Why: OpenAI's current models reject `max_tokens` outright, while the router, Ollama and vLLM want `max_tokens` and do not recognise `max_completion_tokens`.

## 2026-09-21 — Supervisor, live

Verified end to end against DeepSeek (`deepseek-flash`, through the OpenAI-compatible adapter — the preset was added for this run after the catalogue showed it is DeepSeek's only model). Piped one-shot with no worker config: two tool calls (`check`, `read_doc setup`), a correct diagnosis, and the model asked which tracker before writing anything — 4.5 s. `-resume` picked up the same session file; in piped mode `write_config` was declined by the non-tty rule and the model recovered by listing what it had guessed. Interactive, against the real `~/.config/hivedispatch/config.yaml`: read the config, read the reference, rewrote a file that had been generated twice (stale `jira:` block, `jira_project` alias) into a clean GitHub-Issues config, the operator approved the diff, `config.yaml.bak` was kept, `check` still passed, and `remember` recorded the setup for the next session. The `[y/N]` prompt stayed on screen — the spinner fix from the final review held.

Not verified live: the Anthropic adapter (no key available — it is covered by the httptest round trips only) and Ollama. Not built: anything fleet-level; the notes file already reads like the fleet memory the design spec anticipates, which is the point of starting here.

## 2026-09-27 — Repository settings live in the repository; one worker, many trackers

Supersedes "Two-level config" (2026-09-19) and the file name in "Per-repo policy is read by the executor from the worktree" (2026-09-19); the policy is still read from the worktree.

Decided: each governed repository carries a `.hive-dispatch/` folder. `repo.yaml` holds the ticket-key prefix, which tracker holds the queue (`jira` or `github`) and that tracker's settings — Jira site, JQL, claim-field ids, statuses; GitHub labels and Projects board. `policy.yaml` is the old `.hivedispatch.yaml`, unchanged in content. The worker config keeps what belongs to the machine — agent id, workroot, schedule, executor, the account names `jira.email` and `github.api_url` — and says where repositories are: `code_dirs` to scan and explicit `repos: - path:` entries. A git repository under a code dir with a `repo.yaml` is enrolled by its presence; `hivedispatch scan` lists what is found. `name`, `url` and `default_branch` default to the checkout's `origin` remote. `init -github|-jira [DIR]` acts on one repository: the first run writes the starters, the second creates that tracker's labels or claim fields.

Two sources, split by purpose. `repo.yaml` is read from the local checkout, because the worker needs the queue before it has cloned anything and `init` edits should apply without a commit round-trip. `policy.yaml` is still read from the ticket worktree, so the rules the agent runs under are the reviewed, committed ones. Rejected: reading both from the local checkout (uncommitted policy edits would govern the agent unreviewed), and both from the default branch of the worker's clone (every tracker tweak would need a commit and push before `check` could see it).

Trackers are per repository and joined by a router (`internal/tracker/router`) keyed by project, like the state router. Its `Poll` keeps each ticket only from its own project's tracker, so two repositories sharing a JQL do not double-dispatch, and it tolerates a failing tracker: it is logged and skipped, and `Poll` errors only if every tracker fails. Rejected: failing the whole poll, which would let one unreachable Jira site stall every GitHub repository on the worker.

Presence opts in. Rejected: `scan` only listing and a `repo add` command writing paths into the worker config — one more step for no extra safety, since `repo.yaml` is itself the deliberate act. A second checkout of the same `owner/repo` (a clone in two places) is skipped and named by `check`, rather than being an error; two different repositories claiming the same project key are an error.

Clean break, no migration command. A worker config that still has `tracker:`, `jira.base_url`/`jql`/`fields`/`statuses`, `github.labels`/`project` or `repos[]` entries with anything but `path` is rejected with the list of moved keys; a repository with `.hivedispatch.yaml` and no `policy.yaml` fails the run with a message to move it. Rejected: reading both layouts with a deprecation warning — two layouts to maintain for a single-operator alpha. Rejected: a `migrate` command, at the operator's call.

One Jira account (`jira.email` + `HIVE_JIRA_TOKEN`) serves every Jira repository; per-site credentials wait until someone needs two Atlassian accounts on one worker. `repo.yaml` is decoded strictly, so `jira.email` there — a personal identity in a committed file — is an error rather than silently ignored.

Multiple workers: this change is configuration only. One process serves many repositories and trackers, tickets still run one at a time, and `max_concurrent` is reserved (only `1` is accepted) for the concurrency step in the design spec.

Found along the way: the supervisor's startup `check` re-execs its own binary, which under `go test` is the test binary — `TestSupervisorOneShotFake` was re-running the whole suite until the 30 s timeout. The CLI tests now have a `TestMain` that runs the CLI when `HIVEDISPATCH_TEST_AS_CLI=1`.

## 2026-09-27 — Tickets run in parallel, one worktree each

Supersedes the "Multiple workers" paragraph of the entry above: `max_concurrent` is no longer reserved.

Decided: one worker process works up to `max_concurrent` tickets at once (default 1). Each ticket already had its own git worktree on `hive/<KEY>` off the worker's base clone, so parallel runs never see each other's files, and the operator's own checkout — from which only `.hive-dispatch/repo.yaml` is read — is never touched. The dispatcher now starts each ticket in a goroutine holding one of N slots and skips tickets it is already working. `run` keeps polling while tickets run, starts new ones as slots free up, stops polling on the first signal and returns once in-flight runs finish (the second signal still cancels them). `run -once` works every ready ticket, N at a time, and waits. A budget stop still pauses new work; runs already in flight continue to their own end.

Git does not wait for its own locks: two fetches, a `worktree add` and a push on one `.git` fail with "cannot lock ref" or "index.lock exists". Every git operation on a repository's shared clone — clone/fetch, worktree add, the safety commit and push, and the state branch's commit and push — now takes a per-repository lock (`internal/gitops/repolock`); the agent's own work inside its worktree does not. Reproduced first: six parallel Prepare+Finalize calls on one clone failed without the lock and pass with it under `-race`. Rejected: one base clone per ticket (a full clone per ticket costs disk and time, and loses the shared fetch), and retrying on lock errors (hides the contention instead of removing it).

Rejected: several worker processes on one machine sharing a workroot. The lock is in-process; two processes would race on the same clone. Separate processes need separate workroots, which already works.

## 2026-09-27 — A local web UI: `hivedispatch website`

Decided: `hivedispatch website` serves a Vue 3 single-page app and a JSON API from the one binary. The pages are a Dashboard (config summary, `check` output, run records), Repos (everything `scan` finds, enrolment, each repository's `repo.yaml` and `policy.yaml`) and Configuration (worker and supervisor config). A supervisor chat sits in the right sidebar.

The front end is Vue single-file components built by Vite from `web/`. The build output is committed to `internal/web/dist` and embedded with `go:embed`, so `go build` and `go install` still need only Go, and `go.mod` gains nothing. CI rebuilds it and fails if the committed copy is stale. Rejected: building in CI or a Makefile before `go build`, because it breaks `go install …@latest` and puts Node on every contributor's path. Also rejected: a no-build app on a vendored `vue.esm-browser.js`, which avoids Node entirely but gives up SFCs and tooling for a UI that will keep growing.

Every file edit goes through one path, `internal/yamlfile`, which the supervisor's `write_config` now uses too. The new content must parse, and the file's own loader runs on a temporary copy; whatever it objects to is shown next to a diff before anything is written. The previous file is kept as `.bak`. Form edits are applied as key patches on the YAML node tree, so the starter files' comments survive. Rejected: re-marshalling a struct on every form save, which would strip the comments that double as the config's documentation. Files that do not load can still be saved, with their problems listed, for the same reason as `write_config` (partial progress beats being stuck).

The server trusts nobody but the operator's own browser. It answers only requests whose `Host` is loopback, which blocks DNS rebinding. Every state-changing request must be `application/json` from the same origin, which blocks cross-site forms and fetches. Repository files are served and written only for paths the scan found. Rejected: a login or a per-launch token in the URL. Those are more ceremony than a single-operator tool on loopback needs, and binding a non-loopback address is possible but prints a warning.

The supervisor keeps its guardrail in code. The conversation was split from the terminal REPL into `supervisor.Session`, which takes the confirmation function as a dependency. In the browser, a tool that would change something blocks until the operator presses Approve or Decline on a card showing the diff. Stopping the turn, or closing the server, declines. Rejected: auto-declining every change from the web chat, as piped mode does. That would make the chat read-only in the one front end where approving is easiest to show.

## 2026-09-27 — Two website modes, one front door

Decided: `hivedispatch website` serves the embedded production build. `hivedispatch website -dev` starts Vite's dev server as a child process on a private loopback port, and the Go server proxies every non-API request to it, including the HMR websocket. The browser talks only to the Go server in both modes. The host and origin checks apply in development too, and so will a future login gate, because it belongs in the same `ServeHTTP` that every request already passes through. `-dev` finds `web/` from the working directory upward, or takes `-web DIR`. Stopping the server stops Vite.

Rejected: running `vite` as the front server and proxying `/api` to Go. Development would then sit behind a different server than production, and auth would have to be duplicated in Node or left out of dev. Also rejected: a watch build served from disk (`vite build --watch` plus Go serving the files). Go stays in front, but you lose hot module replacement, and every edit costs a full page reload and the page's state.

## 2026-09-28 — Supervisor settings back in the worker config

Supersedes "Segregated: one directory, its own config file" (2026-09-21), for the settings only. The notes and session transcripts stay in `<config dir>/supervisor/`.

Decided: the supervisor's provider, model, key variable and limits are a `supervisor:` block in `config.yaml`. The operator asked for one file to manage, and the website's Configuration page is now a single form. The old `supervisor/config.yaml` is rejected with a message naming the file and where its keys go. Rejected: reading both locations, for the same reason as the tracker-settings move (two layouts to maintain for a single operator). The earlier worry, that the assistant's own settings would sit in the file it rewrites, is covered by the confirmation step: every `write_config` shows the operator the diff, including any change to `supervisor:`.

## 2026-09-28 — Agents: a pool per repository

Decided: each repository lists its coding agents in `.hive-dispatch/agents.yaml`, each with a name, an executor (claude/codex/fake) and an optional model. A repository without the file has one `default` Claude agent. Agents are pool slots: each works one ticket at a time, and `max_concurrent` still caps the machine. A `hive:agent:<name>` label pins a ticket to one agent. The agent's model wins over the policy's. The worker-level `executor`, `claude.model` and `codex.model` moved into agents and are rejected in `config.yaml`. The binaries stay machine settings.

`agents.yaml` is read from the local checkout, like `repo.yaml`, and `policy.yaml` is unchanged: one policy per repository, read from the worktree, which every agent follows. An earlier draft put a policy in each agent. It was dropped at the operator's call, because a second place to define what an agent may do risks the two drifting apart. Since permissions stay in the reviewed policy, the agent list can apply on save.

Claims on the tracker stay under the worker's `agent_id`, so the claim protocol and every tracker are untouched. The agent shows up as `<agent_id>/<name>` on run records and pull requests. Run records now say which executor made a session, and a question/answer resume continues only on an agent with the same executor, because a Claude session id means nothing to Codex. Rejected: routing tickets only by label (several agents would bring no parallelism), and a pool with no way to choose an agent.

## 2026-09-28 — machine_id, defaulting to the hostname

Decided: the worker config's `agent_id` is renamed `machine_id`, because "agent" now means one of a repository's coding agents. It defaults to the hostname, so a new install needs no identity set by hand. It is still what claims, run records (`<machine_id>/<agent>`) and pull requests carry. A config that still says `agent_id` is rejected with a hint to rename it, the same clean break as earlier moves. The claim protocol's field names (the Jira "HiveDispatch Agent" field, `jira.fields.agent_id`) are unchanged: they name the claim holder, and renaming them would orphan existing Jira fields.

Rejected: the OS machine ID (`/etc/machine-id`, macOS `IOPlatformUUID`, Windows `MachineGuid`). It is unique, but the value is written on tickets, which can be public GitHub issues, and systemd says to keep it private. It would also read as 32 opaque characters on every claim. A hash of it would be safe to publish but just as unreadable. Two machines with the same hostname would collide, so the docs and starter say to set `machine_id` in that case.

## 2026-09-28 — Ticket names: prefix-board-number-title

Supersedes the key format in "Repository settings live in the repository" (2026-09-27): `project` was the ticket key prefix, and GitHub issue #12 was `PROJECT-12`.

Decided: `repo.yaml` names its tracker `ticket_tracker` and its prefix `ticket_prefix`. The prefix defaults to the tracker's name (`GITHUB`, `JIRA`). A ticket's key is `PREFIX-BOARD-NUMBER`:
- **Jira:** the board is the Jira project, so Jira's own key is kept whole inside ours (`JIRA-SCRUM-4`).
- **GitHub:** the board is `ISSUES`, or `PROJECT<N>` when the repository mirrors to Projects board number N (`GITHUB-ISSUES-12`, `GITHUB-PROJECT2-12`).

Each tracker still works in its own keys, and a wrapper (`internal/tracker/prefixed`) adds and strips the prefix. That is why neither tracker needs a key translation table.

A ticket also gets a readable name: the key and the title in kebab case (`github-issues-12-create-website`). It names the branch and the pull request, and shows in `status` and the website, so people can find the ticket they filed. The name is fixed in the run record when the ticket is first worked: a retitled ticket keeps its branch and PR.

The key, not the name, is the identity, because titles change. `once` takes either form; a board can itself contain a number segment, so it tries each possible key in turn.

Rejected:
- **Remapping Jira keys to the prefix** (`SCRUM-4` → `JIRA-4`): it needed a per-repository Jira project setting and allowed only one Jira project per repository.
- **The repository name as the GitHub board:** the operator preferred `ISSUES`/`PROJECT<N>`. That is what GitHub actually works from, and it keeps names short.

The prefix must still be unique per worker, so two repositories using the same tracker cannot both keep the default; `check` says so.

Consequence for existing installs: keys changed shape. Run records and branches from before this change are not picked up; a ticket in flight starts over under its new name. The old `project`/`tracker` keys are rejected with a rename hint, the same clean break as earlier moves.

## 2026-09-28 — Planning, coding and review agents, one board column each

Decided: an agent in `agents.yaml` has a `role`, and each role works one column of the board:
- **`planning` works Planning.** This is a new state, with the label `hive:planning`, the board column `Planning`, and the Jira status `Planning`. A read-only run posts a plan on the ticket and moves it to Ready, or asks one question and moves it to Needs Info.
- **`coding` works Ready.** This is the default and today's agent. It skips triage for a planned ticket, because the plan in the thread is its brief.
- **`review` works In Review.** A read-only run reviews each new version of the PR once, from its diff. An approval leaves the ticket In Review for a human to merge. A request for changes sends it back to Ready, and the coding agent resumes its session with the findings, up to `max_review_rounds` (default 2), then Needs Human.

The column decides the role. `hive:agent:<name>` picks an agent within that role's column, so one ticket can name both its coder and its reviewer.

A column no repository has agents for is not polled. Planning is optional: a ticket may start in Ready as before, and triage still runs there. `check` requires the Planning label, column or status only where a planning agent exists.

Reviews are posted as COMMENT reviews. GitHub refuses APPROVE and REQUEST_CHANGES from the account that opened the pull request, which is the worker's own. The verdict is in the text, and the ticket's column carries it.

Planning and review are read-only runs through a new `Executor.Advise`: Claude Code in plan mode, or Codex in its read-only sandbox, answering in a JSON schema. That is the same mechanism as triage and `Plan`, so either CLI can serve any role. The review agent's tools are read-only file tools, so the diff is put in its prompt, capped at 100 KiB.

`jira.jql` becomes the scope only: HiveDispatch adds `status = "<column>"` for each stage from `jira.statuses`. A query that still names a status is rejected rather than silently matching nothing. This supersedes "trigger JQL" in the setup docs.

Rejected:
- **Tracker assignee fields for assigning agents:** that needs a real account per agent on every tracker.
- **Planning replacing triage:** tickets that need no plan would pay for one.
- **Review comments that never move the ticket:** the send-back loop is what makes a review agent more than a linter.
- **Posting REQUEST_CHANGES:** GitHub would reject it from the PR's own author.

## 2026-09-29 — A single password for a hosted website

Decided: when `HIVE_WEBSITE_PASSWORD` is set, `hivedispatch website` requires it on every request through HTTP Basic auth, in the same `ServeHTTP` front door as the host and origin checks. The user name is ignored. A request without the password gets 401 before any route runs, and that covers the API, the assets, the chat event stream and the `-dev` Vite proxy.

With a password set, the loopback-only `Host` check is lifted, so a hosted site can be reached by its DNS name or public IP. That check exists to stop DNS rebinding, and a rebinding page is a different origin, so the browser never sends it the credentials. The JSON and same-origin checks on writes still apply.

It is documented as one shared password for one operator, not a multi-user login. The docs also say to put TLS in front, because Basic auth over plain HTTP exposes the password.

Rejected:
- **A login page with session cookies:** it needs front-end work, cookie and CSRF handling, and a session store, all to serve one person. The browser's Basic prompt works with `fetch`, `EventSource` and the HMR websocket unchanged.
- **A key in `config.yaml`:** the website and the supervisor rewrite that file and show its diffs, so a secret there would leak into both. Every other secret is already an environment variable.
- **User accounts:** out of scope. Someone who needs several users should put an authenticating reverse proxy in front.

## 2026-10-01 — Tracing goes to LangSmith over its runs API, from Go

Decided: HiveDispatch traces supervisor turns and ticket runs to LangSmith. It posts to the `/runs/batch` endpoint that LangSmith's own SDKs use, from a stdlib-only `internal/trace` package. The settings are LangChain's own environment variables (`LANGSMITH_TRACING`, `LANGSMITH_API_KEY`, `LANGSMITH_PROJECT`, `LANGSMITH_ENDPOINT`, `LANGSMITH_HIDE_INPUTS`/`_OUTPUTS`).

Inputs and outputs are sent in full, cut to 64 KiB a string, unless the hide variables are set. Tracing never blocks or fails a run: runs are queued, flushed in the background, and dropped with a log line when LangSmith cannot keep up.

The coding CLIs' steps (tool calls and assistant messages) become child runs. Their parsers record them live, because Claude Code's `stream-json` carries no timestamps.

This is the first step toward LangGraph. Later steps run LangGraph as a Python service behind a Go interface with a fake, and they trace to the same project.

Rejected:
- **OTLP export:** portable to other backends, but LangSmith renders its native run types, token usage and costs better than mapped `gen_ai` attributes.
- **A Python LangChain sidecar just to trace:** it adds a runtime and a process for something a few hundred lines of Go do.
- **A third-party Go LangSmith SDK:** AGENTS.md allows no new dependencies.
- **Parsing the saved run log after the fact:** the log has no per-event times, so every step would get a made-up duration.

## 2026-10-01 — Graph workflows run as a Python subprocess

Decided: an `executor: langgraph` agent runs `hivegraph`, a LangGraph workflow in the Python package `graph/`. The workflow is plan → code → checks ⇄ fix → self-review ⇄ fix → finish.

The worker runs it once per ticket run, as it runs `claude` or `codex`: the task goes in as JSON on stdin, and events come back as JSON lines on stdout. The code step calls back into `hivedispatch agent-run`, which uses the existing Claude Code or Codex executor. Plan and self-review use a cheap OpenAI-compatible chat model through LangChain. Checkpoints go to SQLite in the worktree's git directory, so a ticket resumed after a reply continues the same thread.

Stopping a run sends SIGTERM to the group and waits 15 s before SIGKILL. `agent-run` turns the SIGTERM into killing the CLI's own process group, which a plain SIGKILL would orphan.

Go keeps its rule of stdlib plus yaml.v3. The Python dependencies live only in `graph/pyproject.toml`, and CI checks them in their own job. LangChain stays optional: a worker with no `langgraph` agent needs no Python, and prints nothing about the graph.

Rejected:
- **A long-lived LangGraph Server:** one more service to deploy, secure and keep alive on every worker, for persistence that SQLite in the worktree already gives.
- **Python reimplementing the CLI runners:** the argument building, stream parsing, budget detection and process supervision would be written twice and drift apart.
- **Go orchestrating with Python only for model calls:** no graph state, no checkpoints, nothing to see in Studio. That would be LangGraph in name only.
- **A Go port of LangGraph:** it would be an unofficial third-party dependency, which AGENTS.md rules out, and it lags the real thing.
- **Anthropic as the graph's chat model, for now:** `langchain-openai` covers every provider the supervisor has except Anthropic, and the cheap model's job doesn't need it.

## 2026-10-01 — HiveDispatch's own coding agent is a code_with choice

Decided: `code_with: langgraph` makes the graph workflow's code step run a coding agent inside `hivegraph` instead of Claude Code or Codex. It is a LangGraph `StateGraph` of a model node and LangGraph's `ToolNode`, running on the graph's OpenAI-compatible provider with the agent's `model:` (default `graph.model`).

Its file tools are confined to the worktree: they refuse paths outside it, through symlinks, or under `.git`.

`run_command` splits a command into words and runs it without a shell. It runs a command only if it starts with an entry of the policy's `executor.langgraph.allowed_commands`, or exactly equals one of its `checks`, which run through `sh -c` as the checks step runs them.

Every tool call counts toward the run's step budget. The conversation is kept in the workflow's checkpointed state, so fix rounds and resumes continue it. Older tool outputs are trimmed to bound the context. Rate limits map to `budget`, so the worker backs off as it does for the CLIs.

It may only be a coding agent, because planning and review are read-only runs through a CLI. Like the rest of LangChain here, it is optional.

Rejected:
- **A separate executor:** the workflow's plan, checks and review are what make a cheap model's code worth opening a pull request for. A bare agent would have none of that.
- **An unrestricted shell:** the CLIs come with their own permission systems, and this agent has only what HiveDispatch gives it. Allowlisted prefixes without a shell are the smallest grant that lets it run the tests.
- **LangGraph's `create_react_agent`:** deprecated in LangGraph 1.0, and removed in 2.0.
- **LangChain's `create_agent`:** it adds the `langchain` package, and budgets and stopping would go through middleware rather than a loop of our own.
- **A separate `graph.coder` model block:** the agent's own `model:` on the graph provider covers the case of a stronger model for coding without a new block.

## 2026-10-01 — DeepCode is the third coding CLI

Decided: `executor: deepcode` runs [DeepCode](https://api-docs.deepseek.com/quick_start/agent_integrations/deepcode), DeepSeek's open-source terminal coding agent (`@vegamo/deepcode-cli`), headless as `deepcode -x --prompt=…`. A resume after a human reply passes `-r <sessionId>`, and the session id is the resume token. It is also a `code_with` choice for the graph workflow, through `agent-run`.

DeepCode prints only its final reply. The steps, edited files, token usage and outcome (completed, failed, `ask_permission`, `waiting_for_user`) come from the session it saves under `~/.deepcode/projects`. A run's session is the new file whose recorded root path is the worktree, so parallel tickets stay apart without depending on how DeepCode names its project folders. The step budget is enforced by polling that file during the run.

The operator owns DeepCode's configuration, as with Claude Code and Codex. Its API key and model are in `~/.deepcode/settings.json`, and DeepCode reads no environment variables for them.

- An agent running DeepCode may not set `model:`, because DeepCode has no model flag.
- It must be a coding agent, because DeepCode cannot be held to read-only from its command line.
- Its shell is limited by DeepCode's own `permissions`. In `-x` mode an `ask` fails the run with DeepCode's message rather than hanging, and the summary says to fix the settings.

Rejected:
- **HiveDispatch writing DeepCode's settings** (a project-level `.deepcode/settings.json` in the worktree, or a private HOME): the first would put the API key in the worktree, where the safety commit could pick it up. A private HOME would hide the user's git and tool credentials from DeepCode's shell.
- **Passing the key through the environment:** DeepCode ignores it.
- **Planning and review on DeepCode:** without a read-only mode, a "read-only" run could still change the tree.
- **HiveDispatch's own command allowlist around DeepCode:** DeepCode runs its own tools, so the only lever is its settings. That is documented rather than half-enforced.


## 2026-10-01 — Grok Build is a coding executor

Decided: `executor: grok` runs xAI's [Grok Build](https://github.com/xai-org/grok-build) CLI headlessly in the prepared worktree. It also works through the graph's `code_with: grok` via `agent-run`. The worker's `grok.binary` selects the executable, and the agent's `model` passes `--model`; credentials and permissions remain in the operator's Grok setup.

Use `--prompt-file` with a private temporary file, deleted after the run, and `--output-format streaming-messages-json`. Grok documents this as the Messages wire format, so reuse the existing `claudecli` parser and process supervision. Grok's `search_replace` file paths and terminal `errors` array are recognized by that parser. Grok-specific outcome mapping handles native turn exhaustion, quota errors, incomplete responses and nonzero exit status. The returned session ID is the resume token. Tool calls enforce the step budget, with `--max-turns` additionally bounding model turns; cancellation kills the process group.

Only coding roles are accepted for now. The adapter does not establish a read-only sandbox for planning or review. It leaves Grok's permission policy in place and never supplies `--yolo`; headless permission requests are cancelled by Grok. The shared PATH policy and repository guidance still apply.

Rejected:
- **A separate parser for Grok's native `streaming-json`:** its supported Messages format already carries sessions, tool calls, results and usage that HiveDispatch consumes. Sharing that parser avoids duplicating process supervision and tracing.
- **Automatically bypassing all permissions:** the operator should configure Grok's allowed operations, as with the other executors.
- **Planning and review via a prompt that says read-only:** a prompt does not enforce isolation. Add those roles when the adapter can guarantee it.
- **Putting prompts in argv:** a private temporary file avoids command-line length limits and exposing ticket content in process listings.


## 2026-10-01 — Antigravity CLI runs through its native JSON event stream

Decided: `executor: antigravity` runs Google's `agy` CLI in the prepared worktree; `code_with: antigravity` uses the same executor via `agent-run`. The worker's `antigravity.binary` selects the executable (default `agy`), and the agent's model passes `--model`. Authentication and permissions stay in Antigravity's own configuration.

Send one `user` message on stdin with `--input-format stream-json --output-format stream-json`, then close stdin. Antigravity completes that turn before exiting. Parse its native `init`, `step_update` and `result` envelopes in `internal/antigravitycli`; its wire format differs from Claude's Messages stream. Resume with the saved conversation ID via `--conversation`. Count distinct tool steps by conversation ID and step index, and record their progress for tracing. Sum per-step usage because result totals can cover the entire resumed conversation. When a resumed run has no per-step usage, report unknown usage rather than counting historical totals again.

Timeout and step-budget cancellation kill the CLI's process group. The CLI's print timeout follows the caller's deadline, or defaults to 45 minutes when called without one. Logs and trace output are capped, and malformed or oversized output stops the process with a visible error. The adapter does not infer changed paths from undocumented tool argument schemas; the worker's normal git commit flow still captures changes.

Only coding roles are accepted. Read-only isolation is not implemented. Do not pass `--dangerously-skip-permissions`; the operator grants the commands needed through Antigravity's own permission rules. Headless soft denials can still produce SUCCESS, so retain diagnostic output in the run log and document this behavior.

Rejected:
- **Reusing the Claude/Grok parser:** Antigravity has different envelopes, statuses and step identities.
- **Passing ticket text through `-p`:** JSON stdin avoids argv exposure and command-line length limits.
- **Using the result's cumulative usage on resume:** this would charge earlier work to the new run.
- **Bypassing permissions or treating a read-only prompt as isolation:** neither is appropriate for a worker honoring operator policy.


## 2026-10-01 — The website checks and installs the executor CLIs

Decided: the website has an **Agent Configuration** page backed by `GET /api/executors` and `POST /api/executors/install`. Each executor except `fake` is checked by running `<binary> --version`, where the binary comes from the worker config's `<executor>.binary` key (`graph.binary` for langgraph). The page also lists which agents in the scanned repositories use each executor, counting a langgraph agent's `code_with`. If a bare binary is missing from PATH but present in `~/.local/bin`, where the curl installers put it, the page says to fix PATH or the config instead of offering a reinstall.

The install request names an executor. The command it runs comes from a fixed table in `internal/web/executors.go`, using each vendor's documented installer: the Claude Code, Grok Build and Antigravity curl scripts, and npm for Codex and DeepCode. For hivegraph it is the same pipx command `check` prints for this version. It runs as `sh -c` with a ten-minute timeout, one install at a time, and the page asks for confirmation and shows the exact command first. Authentication stays with each CLI.

Rejected:
- **A command field in the request:** that would make the website a remote shell. The executor's name selects from the table instead.
- **Logging in to the CLIs from the page:** their logins are interactive and keep credentials in each tool's own files, which HiveDispatch never handles.
- **Installing automatically when an agent needs a missing CLI:** running an installer is a choice for the operator, so it waits for a click and a confirmation.
