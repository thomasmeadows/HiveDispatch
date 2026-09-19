# Jira setup

HiveDispatch needs three things from a Jira Cloud site: an API token, two custom fields for the claim protocol, and five workflow statuses.

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

## Verify

```sh
hivedispatch check -jira
```
