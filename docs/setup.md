# Setup

HiveDispatch needs: a Jira Cloud site (API token, two custom fields for the claim protocol, five workflow statuses), a GitHub token for opening pull requests, git credentials that can clone and push the repositories it works in, and a logged-in Claude Code CLI.

## 0. Start here

```sh
hivedispatch init
```

writes a commented starter config to `~/.config/hivedispatch/config.yaml` (or `-config PATH`) and never overwrites a non-empty file. Every field in it says where its value comes from. The sections below follow the same order as the comments in that file. If a later command reports `invalid config`, each line names the field and where to get it.

## 1. API token

Create one at https://id.atlassian.com/manage-profile/security/api-tokens. Export it:

```sh
export HIVE_JIRA_TOKEN=...
```

Put `base_url` and `email` in the worker config. The token never goes in YAML.

## 2. Claim fields

When a worker takes a ticket it has to tell every other worker — and every human looking at the board — that the ticket is taken and that the worker is still alive. HiveDispatch does that with two custom fields on the issue:

| Field in Jira | Config key | Meaning |
|---|---|---|
| **HiveDispatch Agent** | `jira.fields.agent_id` | the `agent_id` of the worker that holds the ticket; empty when unclaimed |
| **HiveDispatch Claimed At** | `jira.fields.claimed_at` | when that worker last checked in (its heartbeat, refreshed every `heartbeat_interval`). A claim older than `claim_timeout` is treated as abandoned — the worker probably crashed — and another worker may take the ticket over |

Two workers racing for one ticket both write the Agent field and then read it back; only the one whose name survives the read-back proceeds. That is the whole coordination mechanism, and it is why these are Jira fields rather than something in git.

Create them with:

```sh
hivedispatch init -jira
```

which adds both fields (if missing) and puts them on the default screen so they are writable. You do **not** need to copy anything into the config: the worker looks the fields up by name at startup. Set `jira.fields.agent_id` / `claimed_at` to explicit `customfield_NNNNN` ids only if you renamed the fields or run against several sites.

**Manual alternative:** Settings → Issues → Custom fields → Create a *Text field (single line)* named `HiveDispatch Agent` and a *Date time picker* named `HiveDispatch Claimed At`. Then add both to the edit screen of every project HiveDispatch works in. If a claim fails with "Field cannot be set. It is not on the appropriate screen", this step was missed.

**Team-managed projects** (Jira calls them "next-gen"; `hivedispatch check -jira` labels them) keep their own field list per issue type, and there is no API to attach a global field to them. After `init -jira`, open the project → **Project settings → Issue types**, pick each issue type HiveDispatch should handle (Task, Story, …), and in the *Fields* panel search for "HiveDispatch" and add both **HiveDispatch Agent** and **HiveDispatch Claimed At**. `check -jira` verifies the fields are editable on a real issue and tells you if this step is still missing.

**Duplicate fields.** If `check -jira` warns that several fields share a claim-field name, an earlier version created duplicates; the worker uses the lowest id. Delete the others under Settings → Issues → Custom fields (they go to the trash and can be restored).

## 3. Workflow statuses

Ready · In Progress · Needs Info · In Review · Needs Human

Any names work; map them under `jira.statuses`. The workflow must allow transitions between them from every state HiveDispatch uses (Ready → In Progress, In Progress → Needs Info/In Review/Needs Human/Ready, Needs Info → Ready). The simplest workflow allows all transitions.

## 4. Project key

`repos[].jira_project` is the Jira **project key** — the letters before the dash in ticket keys (`SCRUM` for `SCRUM-4`). Tickets in that project are dispatched into that repository. `check -jira` lists the keys on your site if the configured one does not exist.

## 4b. Trigger query

`jira.jql` selects what HiveDispatch may work on, e.g.

```
project = HIVE AND status = "Ready" AND labels = hive
```

Using a label as well as a status means a human explicitly opts each ticket in.

## Example worker config

`~/.config/hivedispatch/config.yaml`:

```yaml
agent_id: worker-a
workroot: ~/.local/share/hivedispatch
jira:
  base_url: https://yoursite.atlassian.net
  email: you@example.com
  jql: 'project = HIVE AND status = "Ready" AND labels = hive'
  fields:
    agent_id: customfield_10042     # from `hivedispatch init -jira`
    claimed_at: customfield_10043
repos:
  - name: yourorg/yourrepo
    url: git@github.com:yourorg/yourrepo.git
    jira_project: HIVE
```

## 5. GitHub

Two separate things talk to GitHub, with two separate credentials:

| Purpose | Credential | Needed when |
|---|---|---|
| Clone and push branches | your own git credentials — an ssh key or a credential helper | always; `git clone <url>` must work non-interactively as the worker's user |
| Open pull requests via the API | a GitHub token | to open PRs — **even on public repositories**, GitHub does not accept anonymous PR creation. Without one the worker still pushes the branch and asks on the ticket for a human to open the PR |

The token is found automatically, in this order:

1. `HIVE_GITHUB_TOKEN` in the environment
2. `gh auth token` — if you use the GitHub CLI and have run `gh auth login`, nothing else is needed
3. your git credential helper for `github.com` (`git credential fill`) — if you push over HTTPS with a stored token, that token is reused

`hivedispatch check` prints which source it found.

To create a token explicitly: https://github.com/settings/personal-access-tokens → *Fine-grained tokens* → *Generate new token*, choose the repositories HiveDispatch works in, and grant **Pull requests: Read and write** and **Contents: Read**. (A classic token needs the `repo` scope, or `public_repo` for public repositories only.) Then:

```sh
export HIVE_GITHUB_TOKEN=github_pat_...
```

If you use GitHub Enterprise, set `github.api_url` in the worker config (default `https://api.github.com`).

## 6. Work directory layout

```
<workroot>/repos/<owner>__<repo>/repo      base clone (no checkout)
<workroot>/repos/<owner>__<repo>/<KEY>     worktree for one ticket, branch hive/<KEY>
<workroot>/repos/<owner>__<repo>/.state    worktree of the hive/state branch
```

Set `state_store: local` to keep run state in `<workroot>/state/` instead of the `hive/state` branch (useful for trials; not shared between workers).

## 7. Security

The worker runs with your git credentials and can push any branch your credentials allow. Scope the deploy key or token to branch creation and PR opening where your host supports it, never to the default branch, and run the worker under a dedicated account when you can.

## Verify

```sh
hivedispatch check -jira
hivedispatch run -once -placeholder   # takes one Ready ticket to a PR with a placeholder commit
```

## 8. GitHub Issues instead of Jira

Set `tracker: github` in the worker config and skip sections 1–4. The GitHub token (section 5) then also needs **Issues: read and write**. State lives in labels and the claim in a hidden marker in the issue body, so nothing else has to exist in the repository.

```sh
hivedispatch init -github     # creates hive:ready, hive:in-progress, hive:needs-info, hive:in-review, hive:needs-human in each repo
hivedispatch check -live
```

To queue an issue, add the **`hive:ready`** label (one tap in the GitHub mobile app). The worker swaps the label as the ticket moves: `hive:in-progress` while it works, `hive:needs-info` when it has a question — answer in the thread and put `hive:ready` back — `hive:in-review` when a pull request is open, `hive:needs-human` when it will not attempt the issue. Ticket keys are `<project>-<issue number>` with `project` from `repos[].project`, so issue #12 in a repo with `project: HD` is `HD-12` and its branch is `hive/HD-12`.

Label names are configurable under `github.labels`.

## 9. Claude Code

The worker shells out to the `claude` CLI. Log in once as the user that runs the worker (`claude` then `/login`) — headless runs reuse the stored credentials. Do not set `--bare` anywhere; it skips credential loading.

Per-repo policy lives in `.hivedispatch.yaml` at the repository root:

```yaml
executor:
  model: sonnet                 # optional; default is the CLI's default
  permission_mode: dontAsk      # acceptEdits | auto | bypassPermissions | manual | dontAsk | plan
  tools: [default]              # built-in tool set; use a list to restrict
  allowed_tools:                # pre-approved patterns for dontAsk mode
    - Edit
    - "Bash(go test:*)"
  max_budget_usd: 5             # optional; API-billing accounts only
guidance: |
  Free text appended to every prompt for this repo.
```

`dontAsk` fails closed: anything not in `allowed_tools` is denied and the agent must work around it or ask. `bypassPermissions` is for sandboxed runs only.

The run log for each attempt is saved to the state branch under `logs/<KEY>/<timestamp>.log`.

### Triage

Triage runs the same CLI read-only (`--restricted`, Read/Grep/Glob, plan mode) in the ticket worktree with its own bounds:

```yaml
triage:
  kind: claude          # or passthrough: dispatch every ticket without a model call
  step_budget: 40       # tool calls
  timeout: 5m
  model: sonnet         # optional
```
