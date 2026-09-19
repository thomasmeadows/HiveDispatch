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
