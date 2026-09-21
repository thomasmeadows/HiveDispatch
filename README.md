# HiveDispatch

Ticket-driven orchestration for autonomous coding agents.

HiveDispatch turns tickets into pull requests. It polls an issue tracker (Jira Cloud or GitHub Issues), triages each ticket, claims it, branches, runs a coding-agent CLI (Claude Code or Codex) in an isolated worktree, commits, opens a PR, and reports back on the ticket — so steering development work needs nothing but a ticket and a comment thread, including from a phone.

**v0.1.0 — alpha.** The single-worker MVP is complete and has run end to end on a real Jira project, GitHub repository, and Claude Code — including this repository's own tickets: a Jira ticket is triaged by Claude Code (dispatch / ask / reject), claimed, implemented by Claude Code in an isolated worktree, pushed, and opened as a GitHub PR — with questions posted back to the ticket and the run resumed when a human answers.

## How a ticket flows

1. **Poll** — the worker runs your trigger JQL (e.g. `status = Ready AND labels = hive`), or lists GitHub issues labelled `hive:ready`.
2. **Claim** — it writes its agent id to the ticket and reads it back; two workers racing resolve to one winner.
3. **Triage** — Claude Code, read-only, inspects the repo and decides: dispatch with notes, ask one question, or reject.
4. **Branch** — a git worktree on `hive/<KEY>`, resumed if it already exists.
5. **Run** — Claude Code (or Codex, with `executor: codex`) implements the ticket under the repo's `.hivedispatch.yaml` policy, with a step budget and a wall-clock timeout.
6. **Commit and push** — the agent commits as it goes; the worker safety-commits anything left and pushes.
7. **PR and report** — a pull request is opened (or found), and the ticket gets a comment and a status change. Every stop, including budget and timeout, leaves the ticket in a state a human understands.

## What it is not

- It never merges. Agents branch and open PRs; a human merges. Always.
- It does not replace the coding agent. Claude Code and Codex already ship memory, session resume, and context management; HiveDispatch wraps them.
- It is not a hosted service. Self-hosted, runs from a laptop, no infrastructure beyond git and an agent CLI.

## Design

Read [`docs/design-spec.md`](docs/design-spec.md) for the architecture and [`docs/decisions.md`](docs/decisions.md) for every choice made along the way and the alternatives rejected. To build and test the project locally, see [`CONTRIBUTING.md`](CONTRIBUTING.md).

The control plane is deterministic. Model discretion is confined to two places: triage (is this ticket worth attempting, and how?) and code generation inside the executor. Everything between — polling, claiming, branching, reporting — is ordinary code with ordinary failure modes.

## Quick start

```sh
go install github.com/thomasmeadows/hivedispatch/cmd/hivedispatch@latest

hivedispatch init                 # writes ~/.config/hivedispatch/config.yaml, fully commented
hivedispatch supervisor           # or: chat with the built-in assistant, which edits the config and runs the checks for you
$EDITOR ~/.config/hivedispatch/config.yaml

export HIVE_JIRA_TOKEN=...        # Jira: https://id.atlassian.com/manage-profile/security/api-tokens
hivedispatch init -jira           # Jira: creates the two claim fields
hivedispatch init -github         # GitHub Issues (tracker: github): creates the hive:* labels

# GitHub token for opening PRs: HIVE_GITHUB_TOKEN, or `gh auth login`, or your git
# credential helper — found automatically. Without one, branches are pushed and you
# open the PR yourself. (GitHub needs auth to open PRs even on public repos.)
hivedispatch check -live          # verifies the tracker, reports the GitHub token source

hivedispatch run -once -executor fake -placeholder   # dry run: ticket → branch → PR, no agent
hivedispatch run                                     # the real thing
hivedispatch once SCRUM-42                           # drive one ticket by hand
hivedispatch status                                  # what has run, from the state branch
```

Prebuilt binaries for Linux and macOS are on the [releases page](https://github.com/thomasmeadows/HiveDispatch/releases). Every step's prerequisites are in [`docs/setup.md`](docs/setup.md); every key in [`docs/config.md`](docs/config.md).

## License

Apache-2.0. See [LICENSE](LICENSE).
