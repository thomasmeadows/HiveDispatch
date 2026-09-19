# HiveDispatch

Ticket-driven orchestration for autonomous coding agents.

HiveDispatch turns tickets into pull requests. It polls an issue tracker, triages each ticket, claims it, branches, runs a coding-agent CLI (Claude Code first) in an isolated worktree, commits, opens a PR, and reports back on the ticket — so steering development work needs nothing but a ticket and a comment thread, including from a phone.

**Status: pre-alpha.** Phase 0–1 of the MVP (config, CLI, Jira tracker) is under construction. Nothing dispatches yet.

## What it is not

- It never merges. Agents branch and open PRs; a human merges. Always.
- It does not replace the coding agent. Claude Code and Codex already ship memory, session resume, and context management; HiveDispatch wraps them.
- It is not a hosted service. Self-hosted, runs from a laptop, no infrastructure beyond git and an agent CLI.

## Design

Read [`docs/design-spec.md`](docs/design-spec.md) for the architecture and [`docs/decisions.md`](docs/decisions.md) for every choice made along the way and the alternatives rejected.

The control plane is deterministic. Model discretion is confined to two places: triage (is this ticket worth attempting, and how?) and code generation inside the executor. Everything between — polling, claiming, branching, reporting — is ordinary code with ordinary failure modes.

## Quick start

```sh
go install github.com/thomasmeadows/hivedispatch/cmd/hivedispatch@latest
hivedispatch version
```

Jira setup: see [`docs/jira-setup.md`](docs/jira-setup.md).

## License

Apache-2.0. See [LICENSE](LICENSE).
