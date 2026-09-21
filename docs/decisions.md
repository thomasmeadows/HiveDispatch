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
