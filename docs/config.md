# Configuration reference

A worker config for the machine, two files in each repository, and a handful of environment variables.

| File | Where | Holds | Read from |
|---|---|---|---|
| Worker config | `~/.config/hivedispatch/config.yaml` | This machine: agent id, where to find repositories, schedule, executor, account names | disk |
| Repository settings | `.hive-dispatch/repo.yaml` in each repository | Ticket-key prefix, which tracker holds the queue, that tracker's settings | the local checkout |
| Agent policy | `.hive-dispatch/policy.yaml` in each repository | Model, tools, budget, guidance for the coding agent | the ticket's worktree (the committed copy) |

Each repository picks its own tracker, so one worker can serve a Jira project and several GitHub Issues queues at once.

## Worker config — `~/.config/hivedispatch/config.yaml`

Written by `hivedispatch init`; every command takes `-config PATH` to use another file.

| Key | Default | Meaning |
|---|---|---|
| `agent_id` | *(required)* | Name of this worker; written to tickets it claims |
| `code_dirs` | *(none)* | Folders scanned for repositories, e.g. `[~/code]`. Every git repository under one that has `.hive-dispatch/repo.yaml` is enrolled; `hivedispatch scan` lists them. Scanning skips hidden directories, `node_modules`, `vendor`, the workroot, and never descends into a repository |
| `scan_depth` | `4` | Directory levels below each code dir to look for repositories |
| `repos[].path` | *(none)* | A repository to enrol that is not under a code dir. Listed repositories come first; a second checkout of the same `owner/repo` is skipped (`check` names it) |
| `workroot` | `~/.local/share/hivedispatch` | Clones, worktrees, and state live under here |
| `max_concurrent` | `1` | Tickets this worker works at once. Each runs in its own git worktree on its own `hive/<KEY>` branch, so none of them — and none of your checkouts — see each other's changes. Each is also its own coding-agent session: several at once use your plan's limits several times faster. `run` keeps polling while tickets run and starts new ones as slots free up; `run -once` works every ready ticket, this many at a time |
| `poll_interval` | `60s` | Time between polls |
| `poll_jitter` | `10s` | Random extra wait per poll, so several workers do not poll in lockstep |
| `heartbeat_interval` | `60s` | How often a running worker refreshes its claim |
| `claim_timeout` | `2h` | A claim not refreshed for this long is abandoned; another worker may take it. Must exceed `heartbeat_interval` and any realistic run |
| `run_timeout` | `45m` | Wall-clock limit for one executor run |
| `step_budget` | `200` | Tool calls per executor run before it is stopped |
| `max_attempts` | `3` | Failed attempts before a ticket goes to Needs Human |
| `retention_days` | `30` | Raw run logs and finished run records older than this are pruned at startup; `-1` never prunes |
| `executor` | `claude` | `claude`, `codex`, or `fake` (no agent; useful for trying the pipeline) |
| `claude.binary` | `claude` | The Claude Code CLI to run |
| `claude.model` | *(CLI default)* | Model for the executor when the repo policy sets none |
| `codex.binary` | `codex` | The Codex CLI to run (`executor: codex`) |
| `codex.model` | *(CLI default)* | Model for the codex executor when the repo policy sets none |
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

Written by `hivedispatch init -github [DIR]` or `init -jira [DIR]` (DIR defaults to the repository you are in). Read from the local checkout — the path under `code_dirs` or in `repos[]` — so the worker knows the queue before it clones anything and an edit takes effect on the next start. Commit it, so every clone carries the same settings. Decoded strictly: an unknown key, or a worker-level key such as `jira.email`, is an error.

| Key | Default | Meaning |
|---|---|---|
| `project` | *(required)* | Ticket key prefix, unique across the worker's repositories. Jira: the project key (`SCRUM` for `SCRUM-4`). GitHub Issues: any short upper-case tag; issue #12 becomes `TAG-12` |
| `tracker` | *(required)* | `jira` or `github` (GitHub Issues: labels carry state, a hidden body marker carries the claim) |
| `name` | from `origin` | `owner/repo`, parsed from the origin remote's URL |
| `url` | `origin` remote | Clone URL your git credentials can push to |
| `default_branch` | `origin/HEAD`, else `main` | Base for ticket branches and PRs |
| `jira.base_url` | *(required for Jira)* | `https://<site>.atlassian.net` |
| `jira.jql` | *(required for Jira)* | Trigger query: which tickets the worker may take |
| `jira.fields.agent_id` | *(by name)* | `customfield_NNNNN` of "HiveDispatch Agent"; leave empty to resolve by name |
| `jira.fields.claimed_at` | *(by name)* | `customfield_NNNNN` of "HiveDispatch Claimed At" |
| `jira.statuses.ready` … `needs_human` | `Ready`, `In Progress`, `Needs Info`, `In Review`, `Needs Human` | Workflow status names in your project |
| `github.labels.ready` … `needs_human` | `hive:ready`, `hive:in-progress`, `hive:needs-info`, `hive:in-review`, `hive:needs-human` | State labels with `tracker: github` |
| `github.project.owner`, `.number` | *(none = no board)* | A GitHub Projects (v2) board mirroring the labels: `github.com/users/OWNER/projects/N` |
| `github.project.field` | `Status` | The board's single-select field |
| `github.project.columns.ready` … `needs_human` | `Ready`, `In Progress`, `Needs Info`, `In Review`, `Needs Human` | That field's option per state |

## Agent policy — `.hive-dispatch/policy.yaml`

Read from the ticket's worktree, so it is versioned with the code and can differ per branch. A repository that still has the old `.hivedispatch.yaml` at its root and no `policy.yaml` fails the run with a message to move it; the keys are unchanged.

| Key | Default | Meaning |
|---|---|---|
| `executor.model` | worker `claude.model` | Model for Claude Code runs in this repo |
| `executor.permission_mode` | `dontAsk` | Claude Code permission mode: `acceptEdits`, `auto`, `bypassPermissions`, `manual`, `dontAsk`, `plan`. `dontAsk` fails closed |
| `executor.tools` | `[default]` | Claude Code built-in tool set, or a list to restrict |
| `executor.allowed_tools` | *(none)* | Claude Code pre-approved patterns for `dontAsk`, e.g. `Edit`, `"Bash(go test:*)"` |
| `executor.max_budget_usd` | *(none)* | Claude Code per-run spend cap (API-billed accounts) |
| `executor.path` | *(none)* | Directories prepended to the agent's `PATH` (`~` and `$VAR` expand), e.g. `["~/go/bin"]` so `golangci-lint` resolves. Applies to every executor |
| `executor.codex.model` | worker `codex.model` | Model for Codex runs in this repo |
| `executor.codex.sandbox` | `workspace-write` | Codex sandbox for runs: `read-only`, `workspace-write`, `danger-full-access`. Approvals are always off (`approval_policy=never`); a command the sandbox refuses fails |
| `executor.codex.network` | `false` | Allow outbound network inside `workspace-write` (e.g. for `go mod download`) |
| `guidance` | *(none)* | Text appended to every prompt for this repo: conventions, required checks, where decisions are recorded. Applies to every executor |

The `executor.model` / `permission_mode` / `tools` / `allowed_tools` / `max_budget_usd` keys are Claude Code vocabulary and are ignored by the codex executor; `executor.codex.*` is ignored by the claude executor. A repo can carry both so any worker can run it.

## Environment

| Variable | Required | Meaning |
|---|---|---|
| `HIVE_JIRA_TOKEN` | when a repository uses Jira | Atlassian API token for `jira.email` |
| `HIVE_GITHUB_TOKEN` | when a repository uses GitHub Issues | Token for opening PRs and, for repositories with `tracker: github`, for reading and writing issues. Classic or fine-grained, interchangeably — see the token table in `docs/setup.md` §5 for which permissions each needs; a user-owned Projects board requires a classic token. If unset, `gh auth token` and the git credential helper are tried; with none and Jira, branches are pushed and the ticket asks a human to open the PR |
| `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` / `DEEPSEEK_API_KEY` / `HF_TOKEN` | with `hivedispatch supervisor` | the supervisor's model key; which one is read is `api_key_env` in the supervisor config (see below) |

## Commands

| Command | What it does |
|---|---|
| `init` | Write the starter worker config (never overwrites a non-empty file) |
| `init -github [DIR]` | In the repository at DIR (default: the one you are in): write `.hive-dispatch/repo.yaml` and `policy.yaml` starters the first time; once filled in, create the repository's state labels. Says how to enrol DIR if the worker would not pick it up |
| `init -jira [DIR]` | The same for a Jira repository; once filled in, create the two claim fields on its Jira site |
| `scan [DIR…]` | List git repositories under DIR (default `code_dirs`, else your home directory): path, enrolled or not, project, tracker, `owner/repo` |
| `check [-live]` | Validate the worker config and every enrolled repository, and list them; with `-live`, verify each repository's tracker (Jira: credentials, fields, editability, statuses, project, trigger query; GitHub: token, repo, labels, board) and PR access |
| `run [-once]` | Preflight, then poll and dispatch (once, or until Ctrl-C: first drains, second interrupts) |
| `once KEY` | Handle one ticket by key, ignoring the trigger query and run windows |
| `status [-json]` | List run records from the state branch(es) |
| `supervisor [-config P] [-provider anthropic\|openai\|deepseek\|huggingface\|ollama] [-model M] [-resume \| -session FILE]` | Run the built-in assistant that helps configure and run HiveDispatch. `-session` takes either a file name looked up under `sessions/` in the supervisor directory, or a path to use as-is |
| `version` | Print the version |

## Supervisor config — `~/.config/hivedispatch/supervisor/config.yaml`

Settings for `hivedispatch supervisor`, the built-in assistant. Optional: with no file, the provider is chosen from the environment (`ANTHROPIC_API_KEY` → anthropic, else `OPENAI_API_KEY` → openai, else `DEEPSEEK_API_KEY` → deepseek, else `HF_TOKEN` → huggingface, else a local Ollama). The file lives beside the worker config, so `-config PATH` moves it to `<dir of PATH>/supervisor/config.yaml`; the assistant's notes (`memory.md`) and session transcripts (`sessions/`) are in the same directory.

| Key | Default | Meaning |
|---|---|---|
| `provider` | *(from environment)* | `anthropic`, `openai`, `deepseek`, `huggingface` or `ollama`. The last four share the OpenAI-style `chat/completions` API |
| `model` | *(per provider)* | `claude-sonnet-5` · `gpt-5-mini` · `deepseek-flash` · `Qwen/Qwen3-32B` · `qwen3`. Best-effort names: check your provider's catalogue and set this explicitly |
| `base_url` | *(per provider)* | `https://api.anthropic.com` · `https://api.openai.com/v1` · `https://api.deepseek.com/v1` · `https://router.huggingface.co/v1` · `http://localhost:11434/v1`. Any OpenAI-compatible server works under `openai` |
| `api_key_env` | *(per provider)* | `ANTHROPIC_API_KEY` · `OPENAI_API_KEY` · `DEEPSEEK_API_KEY` · `HF_TOKEN` · none for Ollama. The variable that holds the key; the key itself never goes in YAML |
| `max_tokens` | `4096` | Reply length limit per model call |
| `step_budget` | `20` | Tool calls the assistant may make per message before it stops and asks to continue |

`-provider` and `-model` on the command line override the file for one session.
