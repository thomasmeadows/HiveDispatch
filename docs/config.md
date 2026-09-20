# Configuration reference

Two files and two environment variables.

## Worker config — `~/.config/hivedispatch/config.yaml`

Written by `hivedispatch init`; every command takes `-config PATH` to use another file.

| Key | Default | Meaning |
|---|---|---|
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
| `executor` | `claude` | `claude` or `fake` (no agent; useful for trying the pipeline) |
| `claude.binary` | `claude` | The Claude Code CLI to run |
| `claude.model` | *(CLI default)* | Model for the executor when the repo policy sets none |
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
| `repos[].jira_project` | *(required)* | Jira project key (`SCRUM` for `SCRUM-4`); tickets in it go to this repo |
| `run_windows.timezone` | local | IANA zone for the windows |
| `run_windows.windows[]` | *(none = always)* | `{days: [mon, …], start: "22:00", end: "06:00"}`; `start` after `end` spans midnight. Windows gate the start of new work only |

## Repo policy — `.hivedispatch.yaml` at the repository root

Read from the ticket's worktree, so it is versioned with the code and can differ per branch.

| Key | Default | Meaning |
|---|---|---|
| `executor.model` | worker `claude.model` | Model for runs in this repo |
| `executor.permission_mode` | `dontAsk` | Claude Code permission mode: `acceptEdits`, `auto`, `bypassPermissions`, `manual`, `dontAsk`, `plan`. `dontAsk` fails closed |
| `executor.tools` | `[default]` | Built-in tool set, or a list to restrict |
| `executor.allowed_tools` | *(none)* | Pre-approved patterns for `dontAsk`, e.g. `Edit`, `"Bash(go test:*)"` |
| `executor.max_budget_usd` | *(none)* | Per-run spend cap (API-billed accounts) |
| `guidance` | *(none)* | Text appended to every prompt for this repo: conventions, required checks, where decisions are recorded |

## Environment

| Variable | Required | Meaning |
|---|---|---|
| `HIVE_JIRA_TOKEN` | yes | Atlassian API token for `jira.email` |
| `HIVE_GITHUB_TOKEN` | no | Token for opening PRs. If unset, `gh auth token` and the git credential helper are tried; with none, branches are pushed and the ticket asks a human to open the PR |

## Commands

| Command | What it does |
|---|---|
| `init` | Write the starter worker config (never overwrites a non-empty file) |
| `init -jira` | Create the two claim fields in Jira |
| `check [-jira]` | Validate the config; with `-jira`, verify credentials, fields, editability, statuses, projects, and the trigger query |
| `run [-once]` | Preflight, then poll and dispatch (once, or until Ctrl-C: first drains, second interrupts) |
| `once KEY` | Handle one ticket by key, ignoring the trigger query and run windows |
| `status [-json]` | List run records from the state branch(es) |
| `version` | Print the version |
