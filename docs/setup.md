# Setup

HiveDispatch needs: a Jira Cloud site (API token, two custom fields for the claim protocol, five workflow statuses), a GitHub token for opening pull requests, and git credentials that can clone and push the repositories it works in.

## 1. API token

Create one at https://id.atlassian.com/manage-profile/security/api-tokens. Export it:

```sh
export HIVE_JIRA_TOKEN=...
```

Put `base_url` and `email` in the worker config. The token never goes in YAML.

## 2. Custom fields

Two fields hold the claim: who holds the ticket and when they last heartbeat.

```sh
hivedispatch init -jira
```

creates `HiveDispatch Agent` (text) and `HiveDispatch Claimed At` (datetime) if missing, adds them to the default screen, and prints the IDs to paste into `jira.fields`.

**Manual alternative:** Settings → Issues → Custom fields → Create. Then add both to the edit screen of every project HiveDispatch works in. If a claim fails with "Field cannot be set. It is not on the appropriate screen", this step was missed.

**Team-managed projects** manage fields per project: add the two fields under Project settings → Issue types.

## 3. Workflow statuses

Ready · In Progress · Needs Info · In Review · Needs Human

Any names work; map them under `jira.statuses`. The workflow must allow transitions between them from every state HiveDispatch uses (Ready → In Progress, In Progress → Needs Info/In Review/Needs Human/Ready, Needs Info → Ready). The simplest workflow allows all transitions.

## 4. Trigger query

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

Create a fine-grained personal access token with **Pull requests: read and write** and **Contents: read** on the repositories HiveDispatch works in, and export it:

```sh
export HIVE_GITHUB_TOKEN=...
```

The token is used only for the pull-request API. Cloning and pushing use your own git credentials (ssh keys or a credential helper), so make sure `git clone <repo url>` works non-interactively as the user running the worker.

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

## 8. Claude Code

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
