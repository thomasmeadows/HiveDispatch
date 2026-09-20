package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// starterPlaceholders are values in Starter that must be replaced; Validate
// rejects a config that still contains them.
var starterPlaceholders = []string{"YOURTEAM", "you@example.com", "yourorg", "yourrepo", "project = KEY"}

// Starter is the commented config written by `hivedispatch init`. Every
// value that must come from somewhere else says where.
const Starter = `# HiveDispatch worker configuration.
# Secrets never go in this file.
#
#   export HIVE_JIRA_TOKEN=...    # required. https://id.atlassian.com/manage-profile/security/api-tokens
#
#   GitHub: opening a pull request needs a token even on public repositories
#   (GitHub does not accept anonymous PR creation). The worker looks, in order, for
#   HIVE_GITHUB_TOKEN, "gh auth token" (if you use the GitHub CLI), then your git
#   credential helper for github.com. If none is found it still pushes the branch
#   and asks you on the ticket to open the PR yourself.
#   export HIVE_GITHUB_TOKEN=...  # optional. https://github.com/settings/personal-access-tokens
#                                 # fine-grained: Pull requests read/write, Contents read
#                                 # classic: repo scope (or public_repo for public repos)
#
#   Cloning and pushing use your own git credentials (ssh key or credential helper),
#   not the token: "git clone <url>" must work non-interactively.
#
# Setup order:
#   1. Fill in agent_id, jira.*, and repos below.
#   2. export HIVE_JIRA_TOKEN, then run:  hivedispatch init -jira
#      It creates the two claim fields in Jira (see jira.fields below).
#   3. hivedispatch check -jira   (also reports where the GitHub token came from)
#   4. hivedispatch run -once
# Full walkthrough: docs/setup.md in the repository.

# Any short name for this worker. Shown on tickets it claims.
agent_id: worker-1

# Where clones, worktrees, and run state live. Default: ~/.local/share/hivedispatch
# workroot: ~/.local/share/hivedispatch

jira:
  # Your Jira Cloud site.
  base_url: https://YOURTEAM.atlassian.net
  # The Atlassian account the API token belongs to.
  email: you@example.com
  # Which tickets the worker may take. Using a label as well as a status means a
  # human opts each ticket in.
  jql: 'project = KEY AND status = "Ready" AND labels = hive'
  # Claim fields. HiveDispatch marks a ticket it is working on by writing two custom
  # fields on the issue:
  #   agent_id    which worker holds the ticket ("HiveDispatch Agent")
  #   claimed_at  when that worker last checked in ("HiveDispatch Claimed At");
  #               a claim older than claim_timeout is treated as abandoned and
  #               another worker may take the ticket over.
  # "hivedispatch init -jira" creates both fields. Leave the ids empty and the worker
  # finds the fields by name; set them (customfield_NNNNN) only if you renamed the
  # fields or use several Jira sites.
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

repos:
    # owner/repo as shown on GitHub (used for the pull-request API).
  - name: yourorg/yourrepo
    # Clone URL your own git credentials can push to (ssh key or credential helper).
    url: git@github.com:yourorg/yourrepo.git
    # The Jira project KEY: the letters before the dash in ticket keys (SCRUM for
    # SCRUM-4). Tickets in this project are dispatched into this repo.
    jira_project: KEY
    # default_branch: main

# Optional. Defaults shown.
# poll_interval: 60s
# heartbeat_interval: 60s
# claim_timeout: 2h        # a claim older than this is considered abandoned
# run_timeout: 45m
# step_budget: 200         # tool calls per run
# max_attempts: 3
# executor: claude         # or fake (no agent; useful for trying the pipeline)
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
`

// WriteStarter writes Starter to path unless a non-empty file already exists
// there. It returns true when a file was written.
func WriteStarter(path string) (bool, error) {
	if fi, err := os.Stat(path); err == nil {
		if fi.Size() > 0 {
			return false, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("create config directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(Starter), 0o600); err != nil {
		return false, err
	}
	return true, nil
}
