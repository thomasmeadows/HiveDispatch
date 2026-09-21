# Configuration reference

Three files and a handful of environment variables.

## Worker config — `~/.config/hivedispatch/config.yaml`

Written by `hivedispatch init`; every command takes `-config PATH` to use another file.

| Key | Default | Meaning |
|---|---|---|
| `tracker` | `jira` | `jira` or `github` (GitHub Issues: labels carry state, a hidden body marker carries the claim) |
| `agent_id` | *(required)* | Name of this worker; written to tickets it claims |
| `workroot` | `~/.local/share/hivedispatch` | Clones, worktrees, and state live under here |
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
| `jira.base_url` | *(required)* | `https://<site>.atlassian.net` |
| `jira.email` | *(required)* | Atlassian account the token belongs to |
| `jira.jql` | *(required)* | Trigger query: which tickets the worker may take |
| `jira.fields.agent_id` | *(by name)* | `customfield_NNNNN` of "HiveDispatch Agent"; leave empty to resolve by name |
| `jira.fields.claimed_at` | *(by name)* | `customfield_NNNNN` of "HiveDispatch Claimed At" |
| `jira.statuses.ready` … `needs_human` | `Ready`, `In Progress`, `Needs Info`, `In Review`, `Needs Human` | Workflow status names in your project |
| `github.api_url` | `https://api.github.com` | Change for GitHub Enterprise |
| `repos[].name` | *(required)* | `owner/repo` |
| `repos[].url` | *(required)* | Clone URL your git credentials can push to |
| `repos[].default_branch` | `main` | Base for ticket branches and PRs |
| `repos[].project` | *(required)* | Ticket key prefix. Jira: the project key (`SCRUM` for `SCRUM-4`). GitHub Issues: any short upper-case tag; issue #12 becomes `TAG-12`. `jira_project` is accepted as an alias |
| `github.labels.ready` … `needs_human` | `hive:ready`, `hive:in-progress`, `hive:needs-info`, `hive:in-review`, `hive:needs-human` | State labels when `tracker: github` |
| `run_windows.timezone` | local | IANA zone for the windows |
| `run_windows.windows[]` | *(none = always)* | `{days: [mon, …], start: "22:00", end: "06:00"}`; `start` after `end` spans midnight. Windows gate the start of new work only |

## Repo policy — `.hivedispatch.yaml` at the repository root

Read from the ticket's worktree, so it is versioned with the code and can differ per branch.

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
| `HIVE_JIRA_TOKEN` | yes | Atlassian API token for `jira.email` |
| `HIVE_GITHUB_TOKEN` | with `tracker: github` | Token for opening PRs and, with `tracker: github`, for reading and writing issues. Classic or fine-grained, interchangeably — see the token table in `docs/setup.md` §5 for which permissions each needs; a user-owned Projects board requires a classic token. If unset, `gh auth token` and the git credential helper are tried; with none and Jira, branches are pushed and the ticket asks a human to open the PR |
| `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` / `HF_TOKEN` | with `hivedispatch supervisor` | the supervisor's model key; which one is read is `api_key_env` in the supervisor config (see below) |

## Commands

| Command | What it does |
|---|---|
| `init` | Write the starter worker config (never overwrites a non-empty file) |
| `init -jira` | Create the two claim fields in Jira |
| `init -github` | Create the state labels in each GitHub repository |
| `check [-live]` | Validate the config; with `-live`, verify the tracker (Jira: credentials, fields, editability, statuses, projects, trigger query; GitHub: token, repos, labels) |
| `run [-once]` | Preflight, then poll and dispatch (once, or until Ctrl-C: first drains, second interrupts) |
| `once KEY` | Handle one ticket by key, ignoring the trigger query and run windows |
| `status [-json]` | List run records from the state branch(es) |
| `version` | Print the version |

## Supervisor config — `~/.config/hivedispatch/supervisor/config.yaml`

Settings for `hivedispatch supervisor`, the built-in assistant. Optional: with no file, the provider is chosen from the environment (`ANTHROPIC_API_KEY` → anthropic, else `OPENAI_API_KEY` → openai, else `HF_TOKEN` → huggingface, else a local Ollama). The file lives beside the worker config, so `-config PATH` moves it to `<dir of PATH>/supervisor/config.yaml`; the assistant's notes (`memory.md`) and session transcripts (`sessions/`) are in the same directory.

| Key | Default | Meaning |
|---|---|---|
| `provider` | *(from environment)* | `anthropic`, `openai`, `huggingface` or `ollama`. The last three share the OpenAI-style `chat/completions` API |
| `model` | *(per provider)* | `claude-sonnet-5` · `gpt-5-mini` · `Qwen/Qwen3-32B` · `qwen3`. Best-effort names: check your provider's catalogue and set this explicitly |
| `base_url` | *(per provider)* | `https://api.anthropic.com` · `https://api.openai.com/v1` · `https://router.huggingface.co/v1` · `http://localhost:11434/v1`. Any OpenAI-compatible server works under `openai` |
| `api_key_env` | *(per provider)* | `ANTHROPIC_API_KEY` · `OPENAI_API_KEY` · `HF_TOKEN` · none for Ollama. The variable that holds the key; the key itself never goes in YAML |
| `max_tokens` | `4096` | Reply length limit per model call |
| `step_budget` | `20` | Tool calls the assistant may make per message before it stops and asks to continue |

`-provider` and `-model` on the command line override the file for one session.
