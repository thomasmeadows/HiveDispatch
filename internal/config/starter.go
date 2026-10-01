package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// starterPlaceholders are values in the starters that must be replaced;
// Validate rejects a config that still contains them.
var starterPlaceholders = []string{"YOURTEAM", "you@example.com", "project = KEY"}

// Starter is the commented worker config written by `hivedispatch init`.
// Every value that must come from somewhere else says where.
const Starter = `# HiveDispatch worker configuration: settings for this machine.
# Each repository's own settings (its tracker, labels, Jira query) live in that
# repository, in .hive-dispatch/repo.yaml — create it with
#   cd /path/to/repo && hivedispatch init -github     (or: init -jira)
# Secrets never go in either file.
#
#   export HIVE_JIRA_TOKEN=...    # needed when any repository uses Jira.
#                                 # https://id.atlassian.com/manage-profile/security/api-tokens
#
#   GitHub: opening a pull request needs a token even on public repositories.
#   The worker looks, in order, for HIVE_GITHUB_TOKEN, "gh auth token" (if you use
#   the GitHub CLI), then your git credential helper for github.com. Without one it
#   still pushes the branch and asks you on the ticket to open the PR yourself;
#   repositories that use GitHub Issues need one.
#   export HIVE_GITHUB_TOKEN=...  # https://github.com/settings/personal-access-tokens
#                                 # fine-grained: Pull requests + Issues read/write, Contents read
#                                 # classic: repo scope (or public_repo for public repos)
#
#   Cloning and pushing use your own git credentials (ssh key or credential helper),
#   not the token: "git clone <url>" must work non-interactively.
#
# Setup order:
#   1. Set code_dirs or repos below (and machine_id, if the hostname will not do).
#   2. In each repository: hivedispatch init -github   (or -jira); fill in the
#      .hive-dispatch/repo.yaml it writes, then run the same command again.
#   3. hivedispatch scan          (lists repositories found, and which are enrolled)
#   4. hivedispatch check -live
#   5. hivedispatch run -once
# Full walkthrough: docs/setup.md in the HiveDispatch repository.

# Names this machine on the tickets it claims and the pull requests it opens.
# Default: the hostname. Set it when two machines share a hostname.
# machine_id: laptop-1

# Where to find repositories. Every git repository under a code dir that has a
# .hive-dispatch/repo.yaml is enrolled automatically; "hivedispatch scan" shows them.
# code_dirs: [~/code]
# scan_depth: 4            # directory levels below each code dir
#
# Repositories outside the code dirs can be listed by path.
# repos:
#   - path: ~/work/api

# Where clones, worktrees, and run state live. Default: ~/.local/share/hivedispatch
# workroot: ~/.local/share/hivedispatch

# Jira account, when any repository uses Jira: the Atlassian account email the
# HIVE_JIRA_TOKEN belongs to.
# jira:
#   email: you@example.com

# Optional. Defaults shown.
# poll_interval: 60s
# heartbeat_interval: 60s
# claim_timeout: 2h        # a claim older than this is considered abandoned
# run_timeout: 45m
# step_budget: 200         # tool calls per run
# max_attempts: 3
# max_concurrent: 1        # tickets worked at once, each in its own git worktree
#                          # (and its own coding-agent session: mind your plan's limits)
# claude:
#   binary: claude         # the Claude Code CLI; which agents use it is in each
# codex:                   # repository's .hive-dispatch/agents.yaml
#   binary: codex
# state_store: branch      # or local
# triage:
#   kind: claude           # or passthrough
#   step_budget: 40
#   timeout: 5m
# run_windows:             # only start new work inside these windows
#   timezone: America/New_York
#   windows:
#     - days: [mon, tue, wed, thu, fri]
#       start: "22:00"
#       end: "06:00"

# The built-in assistant (hivedispatch supervisor, and the chat in hivedispatch website).
# Optional: with nothing set, the provider follows whichever key is in the environment
# (ANTHROPIC_API_KEY, OPENAI_API_KEY, DEEPSEEK_API_KEY, HF_TOKEN), else a local Ollama.
# supervisor:
#   provider: anthropic    # anthropic, openai, deepseek, huggingface or ollama
#   model: claude-sonnet-5 # check the provider's catalogue
#   api_key_env: ANTHROPIC_API_KEY   # the variable holding the key, never the key
#   max_tokens: 4096
#   step_budget: 20        # tool calls per reply

# Graph workflows (agents with executor: langgraph). Optional, and only read when such
# an agent exists; needs hivegraph installed (pipx install ./graph from a HiveDispatch
# checkout). The chat model plans and reviews; empty uses the supervisor's model.
# graph:
#   binary: hivegraph
#   provider: deepseek     # openai, deepseek, huggingface or ollama (not anthropic)
#   model: deepseek-flash
`

const repoStarterHead = `# HiveDispatch settings for this repository, read by the worker from this checkout.
# Commit it: every clone then carries the same tracker settings. The agents that
# work this repository's tickets are in agents.yaml, and the policy they all
# follow (tools, model, budget) in policy.yaml, both next to this file.
# Reference: docs/config.md in the HiveDispatch repository.

`

// repoStarterOrigin follows the tracker lines of every repo.yaml starter.
const repoStarterOrigin = `
# name, url and default_branch default to the origin remote; set them only to override.
# name: owner/repo
# url: git@github.com:owner/repo.git
# default_branch: main

`

const repoStarterJira = repoStarterHead + `# Where this repository's tickets come from: jira, or github (its GitHub Issues).
ticket_tracker: jira

# First part of every ticket's name: prefix-board-number-title, e.g.
# jira-scrum-4-create-website, where SCRUM-4 is the Jira key. Default: JIRA. Set a
# different one when two repositories on this machine use the same tracker.
# ticket_prefix: JIRA
` + repoStarterOrigin + `
jira:
  # Your Jira Cloud site. The account (jira.email) is in the worker config.
  base_url: https://YOURTEAM.atlassian.net
  # Which tickets the worker may take. Using a label as well as a status means a
  # human opts each ticket in.
  jql: 'project = KEY AND labels = hive'
  # Claim fields. HiveDispatch marks a ticket it is working on by writing two custom
  # fields on the issue:
  #   agent_id    which worker holds the ticket ("HiveDispatch Agent")
  #   claimed_at  when that worker last checked in ("HiveDispatch Claimed At");
  #               a claim older than claim_timeout is treated as abandoned and
  #               another worker may take the ticket over.
  # "hivedispatch init -jira" creates both fields. Leave the ids empty and the worker
  # finds the fields by name; set them (customfield_NNNNN) only if you renamed them.
  # fields:
  #   agent_id: ""
  #   claimed_at: ""
  # Workflow status names in your Jira project. Defaults shown; change to match yours.
  # statuses:
  #   ready: Ready
  #   in_progress: In Progress
  #   needs_info: Needs Info
  #   in_review: In Review
  #   needs_human: Needs Human
`

const repoStarterGitHub = repoStarterHead + `# The queue is this repository's GitHub Issues: state is carried by labels and the
# claim by a hidden marker in the issue body. "hivedispatch init -github" creates the
# labels; the GitHub token needs Issues read/write.
ticket_tracker: github

# First part of every ticket's name: prefix-board-number-title, e.g.
# github-myrepo-12-create-website for issue #12 of this repository. Default: GITHUB.
# Set a different one when two repositories on this machine use the same tracker.
# ticket_prefix: GITHUB
` + repoStarterOrigin + `
# github:
#   # State labels. Defaults shown.
#   labels:
#     ready: hive:ready
#     in_progress: hive:in-progress
#     needs_info: hive:needs-info
#     in_review: hive:in-review
#     needs_human: hive:needs-human
#   # Optional: also move each issue's card on a GitHub Projects board as its state
#   # changes. Labels stay the source of truth; the board mirrors them. Take owner and
#   # number from the board's URL: github.com/users/OWNER/projects/N (or
#   # github.com/orgs/OWNER/projects/N). The token then also needs project access: for
#   # a user-owned board a classic PAT with the project scope (or
#   # "gh auth refresh -s project"); for an organisation board a fine-grained token
#   # with Projects: read and write also works.
#   project:
#     owner: OWNER
#     number: 1
#     field: Status        # single-select field; every option below must exist on it
#     columns:
#       ready: Ready
#       in_progress: In Progress
#       needs_info: Needs Info
#       in_review: In Review
#       needs_human: Needs Human
`

// PolicyStarter is the policy.yaml written next to a new repo.yaml.
const PolicyStarter = `# HiveDispatch agent policy for this repository. The worker reads it from the
# ticket's worktree, so the committed copy on the default branch is what applies.
# Reference: docs/config.md in the HiveDispatch repository.
executor:
  permission_mode: dontAsk
  tools: [default]
  # Tools the agent may use without asking. Keep it to what the repo needs.
  # allowed_tools: [Read, Edit, Write, Glob, Grep, "Bash(go test:*)"]
  # model: sonnet
  # max_budget_usd: 5
  # path: ["~/go/bin"]     # prepended to the agent's PATH
  # codex:                 # used by the codex executor instead of the keys above
  #   sandbox: workspace-write
  #   network: false
# guidance: |
#   House rules for the agent: how to build, test, and lint before finishing.
`

// AgentsStarter is the agents.yaml written next to a new repo.yaml.
const AgentsStarter = `# The agents that work this repository's tickets, read by the worker from this
# checkout. Each works one board column by its role:
#   planning   Planning:  posts a plan, moves the ticket to Ready
#   coding     Ready:     implements it and opens a pull request (the default)
#   review     In Review: reviews the pull request; passes it or sends it back
# Each works one ticket at a time (the worker's max_concurrent caps them all), and
# a ticket labelled hive:agent:<name> waits for that agent in its column. Every
# agent follows policy.yaml; model overrides its model.
agents:
  - name: default
    role: coding
    executor: claude       # claude, codex, or fake (no agent; for trying the pipeline)
    # model: sonnet
  # - name: planner
  #   role: planning
  # - name: reviewer
  #   role: review
`

// RepoStarter is the commented repo.yaml for tracker ("jira" or "github").
func RepoStarter(tracker string) string {
	if tracker == "jira" {
		return repoStarterJira
	}
	return repoStarterGitHub
}

// WriteStarter writes Starter to path unless a non-empty file already exists
// there. It returns true when a file was written.
func WriteStarter(path string) (bool, error) {
	return writeIfAbsent(path, Starter)
}

// WriteRepoStarter writes .hive-dispatch/repo.yaml for tracker, and a
// policy.yaml and agents.yaml, into the repository at dir; existing files
// are kept. It
// returns true when repo.yaml was written.
func WriteRepoStarter(dir, tracker string) (bool, error) {
	written, err := writeIfAbsent(repoFilePath(dir), RepoStarter(tracker))
	if err != nil {
		return false, err
	}
	if _, err := writeIfAbsent(filepath.Join(dir, RepoDir, "policy.yaml"), PolicyStarter); err != nil {
		return written, err
	}
	if _, err := writeIfAbsent(agentsFilePath(dir), AgentsStarter); err != nil {
		return written, err
	}
	return written, nil
}

func writeIfAbsent(path, body string) (bool, error) {
	if fi, err := os.Stat(path); err == nil {
		if fi.Size() > 0 {
			return false, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("create directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return false, err
	}
	return true, nil
}
