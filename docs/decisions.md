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
