# Configuration reference

A worker config for the machine, three files in each repository, and a handful of environment variables.

| File | Where | Holds | Read from |
|---|---|---|---|
| Worker config | `~/.config/hivedispatch/config.yaml` | This machine: agent id, where to find repositories, schedule, CLI binaries, account names, the supervisor's settings | disk |
| Repository settings | `.hive-dispatch/repo.yaml` in each repository | Ticket-key prefix, which tracker holds the queue, that tracker's settings | the local checkout |
| Agents | `.hive-dispatch/agents.yaml` in each repository | The pool of coding agents that work its tickets: name, executor, model | the local checkout |
| Agent policy | `.hive-dispatch/policy.yaml` in each repository | Model, tools, budget, guidance that every agent of the repository follows | the ticket's worktree (the committed copy) |

Each repository picks its own tracker, so one worker can serve a Jira project and several GitHub Issues queues at once.

## Worker config — `~/.config/hivedispatch/config.yaml`

Written by `hivedispatch init`; every command takes `-config PATH` to use another file.

| Key | Default | Meaning |
|---|---|---|
| `machine_id` | *(hostname)* | Names this machine on the tickets it claims (the claim protocol's owner) and the pull requests it opens. Set it when two machines share a hostname. Configs that still say `agent_id` are rejected with a hint to rename it |
| `code_dirs` | *(none)* | Folders scanned for repositories, e.g. `[~/code]`. Every git repository under one that has `.hive-dispatch/repo.yaml` is enrolled; `hivedispatch scan` lists them. Scanning skips hidden directories, `node_modules`, `vendor`, the workroot, and never descends into a repository |
| `scan_depth` | `4` | Directory levels below each code dir to look for repositories |
| `repos[].path` | *(none)* | A repository to enrol that is not under a code dir. Listed repositories come first; a second checkout of the same `owner/repo` is skipped (`check` names it) |
| `workroot` | `~/.local/share/hivedispatch` | Clones, worktrees, and state live under here |
| `max_concurrent` | `1` | Tickets this worker works at once. Each runs in its own git worktree on its own `hive/<name>` branch, so none of them — and none of your checkouts — see each other's changes. Each is also its own coding-agent session: several at once use your plan's limits several times faster. `run` keeps polling while tickets run and starts new ones as slots free up; `run -once` works every ready ticket, this many at a time |
| `poll_interval` | `60s` | Time between polls |
| `poll_jitter` | `10s` | Random extra wait per poll, so several workers do not poll in lockstep |
| `heartbeat_interval` | `60s` | How often a running worker refreshes its claim |
| `claim_timeout` | `2h` | A claim not refreshed for this long is abandoned; another worker may take it. Must exceed `heartbeat_interval` and any realistic run |
| `run_timeout` | `45m` | Wall-clock limit for one executor run |
| `step_budget` | `200` | Tool calls per executor run before it is stopped |
| `max_attempts` | `3` | Failed attempts before a ticket goes to Needs Human |
| `max_review_rounds` | `2` | Times a review agent may send a ticket back for changes before it goes to Needs Human |
| `retention_days` | `30` | Raw run logs and finished run records older than this are pruned at startup; `-1` never prunes |
| `claude.binary` | `claude` | The Claude Code CLI to run |
| `codex.binary` | `codex` | The Codex CLI to run (for agents with `executor: codex`) |
| `triage.kind` | `claude` | `claude` (read-only model triage) or `passthrough` (dispatch everything) |
| `triage.step_budget` | `40` | Tool calls the triager may make |
| `triage.timeout` | `5m` | Wall-clock limit for triage |
| `triage.model` | *(CLI default)* | Model for triage |
| `state_store` | `branch` | `branch` keeps run state on `hive/state` in each repo; `local` keeps it under `workroot/state` |
| `jira.email` | *(required if a repository uses Jira)* | Atlassian account `HIVE_JIRA_TOKEN` belongs to. One account serves every Jira repository |
| `github.api_url` | `https://api.github.com` | Change for GitHub Enterprise |
| `run_windows.timezone` | local | IANA zone for the windows |
| `run_windows.windows[]` | *(none = always)* | `{days: [mon, …], start: "22:00", end: "06:00"}`; `start` after `end` spans midnight. Windows gate the start of new work only |

A worker config written before `.hive-dispatch/` existed — with `tracker:`, `jira.base_url`/`jql`/`fields`/`statuses`, `github.labels`/`project`, or `repos[]` entries with `name`/`url`/`project` — is rejected with a list of the keys that moved. Move them into each repository's `repo.yaml` (`hivedispatch init -github DIR` or `-jira DIR` writes a starter) and leave `repos[]` with only `path`.

## Repository settings — `.hive-dispatch/repo.yaml`

Written by `hivedispatch init -github [DIR]` or `init -jira [DIR]` (DIR defaults to the repository you are in). Read from the local checkout — the path under `code_dirs` or in `repos[]` — so the worker knows the queue before it clones anything and an edit takes effect on the next start. Commit it, so every clone carries the same settings. Decoded strictly: an unknown key, or a worker-level key such as `jira.email`, is an error. The earlier `project` and `tracker` keys are rejected with a hint to rename them `ticket_prefix` and `ticket_tracker`.

Every ticket has a key, `PREFIX-BOARD-NUMBER`, and a readable name, the key and the title in kebab case:

| Tracker | Key | Name |
|---|---|---|
| Jira (the board is the Jira project) | `JIRA-SCRUM-4` | `jira-scrum-4-create-website` |
| GitHub Issues | `GITHUB-ISSUES-12` | `github-issues-12-create-website` |
| GitHub Issues with `github.project.number: 2` | `GITHUB-PROJECT2-12` | `github-project2-12-create-website` |

The key is the ticket's identity (run records, worktrees, routing to a repository). The name is fixed when the ticket is first worked, so retitling the ticket later changes neither: it names the branch (`hive/github-issues-12-create-website`), the pull request, and the ticket in `status` and the website. `hivedispatch once` takes either.

| Key | Default | Meaning |
|---|---|---|
| `ticket_tracker` | *(required)* | `jira` or `github` (GitHub Issues: labels carry state, a hidden body marker carries the claim) |
| `ticket_prefix` | the tracker's name: `GITHUB` or `JIRA` | First part of every ticket key, unique across the worker's repositories: letters, digits or underscores, no dashes. Set one when two repositories on the machine use the same tracker |
| `name` | from `origin` | `owner/repo`, parsed from the origin remote's URL |
| `url` | `origin` remote | Clone URL your git credentials can push to |
| `default_branch` | `origin/HEAD`, else `main` | Base for ticket branches and PRs |
| `jira.base_url` | *(required for Jira)* | `https://<site>.atlassian.net` |
| `jira.jql` | *(required for Jira)* | Scope: which tickets are HiveDispatch's, e.g. `project = SCRUM AND labels = hive`. No status clause: each column's agents add the status from `jira.statuses` (a query that names a status is rejected) |
| `jira.fields.agent_id` | *(by name)* | `customfield_NNNNN` of "HiveDispatch Agent"; leave empty to resolve by name |
| `jira.fields.claimed_at` | *(by name)* | `customfield_NNNNN` of "HiveDispatch Claimed At" |
| `jira.statuses.planning` … `needs_human` | `Planning`, `Ready`, `In Progress`, `Needs Info`, `In Review`, `Needs Human` | Workflow status names in your project. `Planning` must exist only when the repository has a planning agent |
| `github.labels.planning` … `needs_human` | `hive:planning`, `hive:ready`, `hive:in-progress`, `hive:needs-info`, `hive:in-review`, `hive:needs-human` | State labels with `ticket_tracker: github` |
| `github.project.owner`, `.number` | *(none = no board)* | A GitHub Projects (v2) board mirroring the labels: `github.com/users/OWNER/projects/N` |
| `github.project.field` | `Status` | The board's single-select field |
| `github.project.columns.planning` … `needs_human` | `Planning`, `Ready`, `In Progress`, `Needs Info`, `In Review`, `Needs Human` | That field's option per state (`Planning` only needed with a planning agent) |

## Agents — `.hive-dispatch/agents.yaml`

The coding agents that work this repository's tickets, read from the local checkout like `repo.yaml` (commit it so other clones have the same pool). Without the file a repository has one agent, `default`, running Claude Code. The website's Agents tab edits it.

```yaml
agents:
  - name: claude-1
    executor: claude
  - name: codex-1
    executor: codex
    model: gpt-5-codex
```

| Key | Default | Meaning |
|---|---|---|
| `agents[].name` | *(required)* | Unique within the repository (case-insensitive); letters, digits, `.`, `-`, `_`. Shown on run records and pull requests as `<machine_id>/<name>` |
| `agents[].role` | `coding` | Which board column the agent works: `planning`, `coding` or `review` (below) |
| `agents[].executor` | `claude` | `claude` (Claude Code), `codex`, or `fake` (no agent; useful for trying the pipeline) |
| `agents[].model` | *(policy's model, then the CLI's)* | Model for this agent's runs; wins over the policy's `executor.model` / `executor.codex.model` |

Each agent works one column of the board:

| Role | Column | What it does |
|---|---|---|
| `planning` | Planning | Reads the repository without changing it and posts an implementation plan on the ticket, then moves it to Ready; its coding agent skips triage and reads the plan in the ticket thread. If the ticket is unclear it asks one question instead (Needs Info) |
| `coding` | Ready | Triages the ticket (unless it was planned), implements it, pushes and opens the pull request, and moves the ticket to In Review. This is the only role that writes code |
| `review` | In Review | Reviews each new version of the pull request once, from its diff, and posts the review on the PR (as a comment: GitHub will not let the account that opened a PR approve it). Approved: the ticket stays In Review for a human to merge. Changes requested: the ticket goes back to Ready and its coding agent continues its session with the findings, up to `max_review_rounds`, then Needs Human |

Columns without an agent of that role are not polled: a repository with only the default agent works Ready as before, and tickets can skip Planning by starting in Ready. Planning and review runs are read-only (Claude Code in plan mode, Codex in its read-only sandbox) and follow the policy's model and guidance.

Agents of a role are a pool. Each works one ticket at a time, and the worker's `max_concurrent` caps all of them together, so one repository runs at most as many tickets of a column at once as it has agents for it. A ticket labelled `hive:agent:<name>` waits for that agent in that agent's column; the other columns still use their whole pool, so one ticket can name both its coder and its reviewer. A label naming an agent the repository does not have is logged and the ticket skipped. Claims on the tracker are made under the worker's `machine_id`. A ticket paused on a question resumes its earlier session only on an agent with the same executor; any other agent starts it afresh.

`run -executor fake` (or `claude`, `codex`) makes every agent use that executor for one run.

## Agent policy — `.hive-dispatch/policy.yaml`

Every agent of the repository follows it. Read from the ticket's worktree, so it is versioned with the code and can differ per branch. A repository that still has the old `.hivedispatch.yaml` at its root and no `policy.yaml` fails the run with a message to move it; the keys are unchanged.

| Key | Default | Meaning |
|---|---|---|
| `executor.model` | *(CLI default)* | Model for Claude Code runs in this repo, unless the agent sets its own |
| `executor.permission_mode` | `dontAsk` | Claude Code permission mode: `acceptEdits`, `auto`, `bypassPermissions`, `manual`, `dontAsk`, `plan`. `dontAsk` fails closed |
| `executor.tools` | `[default]` | Claude Code built-in tool set, or a list to restrict |
| `executor.allowed_tools` | *(none)* | Claude Code pre-approved patterns for `dontAsk`, e.g. `Edit`, `"Bash(go test:*)"` |
| `executor.max_budget_usd` | *(none)* | Claude Code per-run spend cap (API-billed accounts) |
| `executor.path` | *(none)* | Directories prepended to the agent's `PATH` (`~` and `$VAR` expand), e.g. `["~/go/bin"]` so `golangci-lint` resolves. Applies to every executor |
| `executor.codex.model` | *(CLI default)* | Model for Codex runs in this repo, unless the agent sets its own |
| `executor.codex.sandbox` | `workspace-write` | Codex sandbox for runs: `read-only`, `workspace-write`, `danger-full-access`. Approvals are always off (`approval_policy=never`); a command the sandbox refuses fails |
| `executor.codex.network` | `false` | Allow outbound network inside `workspace-write` (e.g. for `go mod download`) |
| `guidance` | *(none)* | Text appended to every prompt for this repo: conventions, required checks, where decisions are recorded. Applies to every executor |

The `executor.model` / `permission_mode` / `tools` / `allowed_tools` / `max_budget_usd` keys are Claude Code vocabulary and are ignored by the codex executor; `executor.codex.*` is ignored by the claude executor. A repo can carry both so any worker can run it.

## Environment

| Variable | Required | Meaning |
|---|---|---|
| `HIVE_JIRA_TOKEN` | when a repository uses Jira | Atlassian API token for `jira.email` |
| `HIVE_GITHUB_TOKEN` | when a repository uses GitHub Issues | Token for opening PRs and, for repositories with `tracker: github`, for reading and writing issues. Classic or fine-grained, interchangeably — see the token table in `docs/setup.md` §5 for which permissions each needs; a user-owned Projects board requires a classic token. If unset, `gh auth token` and the git credential helper are tried; with none and Jira, branches are pushed and the ticket asks a human to open the PR |
| `HIVE_WEBSITE_PASSWORD` | no | When set, `hivedispatch website` asks every request for this password (HTTP Basic auth, any user name), declines the rest with 401, and answers requests for any host name instead of loopback only. One shared password for one operator — not a multi-user login; see the README's hosted-hardware notes |
| `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` / `DEEPSEEK_API_KEY` / `HF_TOKEN` | with `hivedispatch supervisor` | the supervisor's model key; which one is read is `supervisor.api_key_env` (see below) |

## Commands

| Command | What it does |
|---|---|
| `init` | Write the starter worker config (never overwrites a non-empty file) |
| `init -github [DIR]` | In the repository at DIR (default: the one you are in): write `.hive-dispatch/repo.yaml`, `agents.yaml` and `policy.yaml` starters the first time; once filled in, create the repository's state labels. Says how to enrol DIR if the worker would not pick it up |
| `init -jira [DIR]` | The same for a Jira repository; once filled in, create the two claim fields on its Jira site |
| `scan [DIR…]` | List git repositories under DIR (default `code_dirs`, else your home directory): path, enrolled or not, project, tracker, `owner/repo` |
| `check [-live]` | Validate the worker config and every enrolled repository, and list them; with `-live`, verify each repository's tracker (Jira: credentials, fields, editability, statuses, project, trigger query; GitHub: token, repo, labels, board) and PR access |
| `run [-once]` | Preflight, then poll and dispatch (once, or until Ctrl-C: first drains, second interrupts) |
| `once KEY` | Handle one ticket by key, ignoring the trigger query and run windows |
| `status [-json]` | List run records from the state branch(es) |
| `supervisor [-config P] [-provider anthropic\|openai\|deepseek\|huggingface\|ollama] [-model M] [-resume \| -session FILE]` | Run the built-in assistant that helps configure and run HiveDispatch. `-session` takes either a file name looked up under `sessions/` in the supervisor directory, or a path to use as-is |
| `version` | Print the version |

## Supervisor — `supervisor:` in the worker config

Settings for the built-in assistant (`hivedispatch supervisor` and the chat in `hivedispatch website`), under a `supervisor:` key in `config.yaml`. Optional: without them, the provider is chosen from the environment (`ANTHROPIC_API_KEY` → anthropic, else `OPENAI_API_KEY` → openai, else `DEEPSEEK_API_KEY` → deepseek, else `HF_TOKEN` → huggingface, else a local Ollama). The assistant's notes (`memory.md`) and session transcripts (`sessions/`) live in a `supervisor/` directory beside the worker config. The separate `supervisor/config.yaml` of earlier versions is no longer read: the supervisor refuses to start until its keys are moved under `supervisor:`.

| Key | Default | Meaning |
|---|---|---|
| `supervisor.provider` | *(from environment)* | `anthropic`, `openai`, `deepseek`, `huggingface` or `ollama`. The last four share the OpenAI-style `chat/completions` API |
| `supervisor.model` | *(per provider)* | `claude-sonnet-5` · `gpt-5-mini` · `deepseek-flash` · `Qwen/Qwen3-32B` · `qwen3`. Best-effort names: check your provider's catalogue and set this explicitly |
| `supervisor.base_url` | *(per provider)* | `https://api.anthropic.com` · `https://api.openai.com/v1` · `https://api.deepseek.com/v1` · `https://router.huggingface.co/v1` · `http://localhost:11434/v1`. Any OpenAI-compatible server works under `openai` |
| `supervisor.api_key_env` | *(per provider)* | `ANTHROPIC_API_KEY` · `OPENAI_API_KEY` · `DEEPSEEK_API_KEY` · `HF_TOKEN` · none for Ollama. The variable that holds the key; the key itself never goes in YAML |
| `supervisor.max_tokens` | `4096` | Reply length limit per model call |
| `supervisor.step_budget` | `20` | Tool calls the assistant may make per message before it stops and asks to continue |

`-provider` and `-model` on the command line override these for one session.
