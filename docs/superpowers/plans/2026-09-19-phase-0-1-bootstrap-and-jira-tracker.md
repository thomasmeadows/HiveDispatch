# Phase 0–1: Bootstrap and Jira Tracker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Go module with config loading, a CLI skeleton, the `Tracker` interface, an in-memory fake, and a working Jira Cloud tracker that can poll, claim (with read-back), heartbeat, release, comment, and transition tickets — verified against a live instance by `hivedispatch check --jira`.

**Architecture:** `internal/tracker` defines the `Tracker` interface and domain types (`Ticket`, `Comment`, `Claim`, `State`). `internal/tracker/jira` implements it against Jira Cloud REST v3 (`/search/jql`, issue fields, comments, transitions), with ADF↔text rendering isolated in one file. `internal/tracker/fake` is an in-memory implementation with a race-injection hook used by later phases' dispatcher tests. `internal/config` loads the worker config (YAML + env secrets). `cmd/hivedispatch` is a stdlib-`flag` subcommand CLI.

**Tech Stack:** Go 1.27 (stdlib `net/http`, `encoding/json`, `flag`, `testing`, `net/http/httptest`), `gopkg.in/yaml.v3`, golangci-lint v2, GitHub Actions.

**Spec:** `docs/design-spec.md` (moved from `Docs/` in Task 1) and the MVP strategy in `docs/decisions.md`.

## Global Constraints

- Module path: `github.com/thomasmeadows/hivedispatch`.
- License: Apache-2.0. Every Go file starts with no license header (the LICENSE file covers the repo).
- Only two external dependencies allowed in this phase: `gopkg.in/yaml.v3`. Everything else is stdlib.
- Jira endpoint: `/rest/api/3/search/jql` (POST). Never `/rest/api/3/search` (returns 410). Pagination by `nextPageToken`; `fields` always passed explicitly.
- Secrets never live in YAML. `HIVE_JIRA_TOKEN` is the only Jira secret; `HIVE_GITHUB_TOKEN` is reserved for Phase 3.
- Every external call is behind an interface with a fake. No test in this phase touches the network.
- Commits: conventional-commit style (`feat:`, `test:`, `chore:`, `docs:`), one commit per task minimum. Commit messages end with `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.
- Run `go vet ./... && go test -race ./...` before every commit.

---

## File Structure

```
go.mod, go.sum
LICENSE                                   Apache-2.0 text
README.md                                 what/why/status/non-goals, quick start
CONTRIBUTING.md                           dev setup, test commands, decisions log rule
.gitignore
.golangci.yml
.github/workflows/ci.yml
docs/design-spec.md                       the spec, interview framing removed
docs/decisions.md                         decisions log (ADR-lite), seeded
docs/jira-setup.md                        custom fields, statuses, token
cmd/hivedispatch/main.go                  subcommand dispatch: version | check | init
cmd/hivedispatch/main_test.go
internal/config/config.go                 Config structs + Validate
internal/config/load.go                   Load(path) with ~ expansion and env secrets
internal/config/config_test.go
internal/tracker/tracker.go               Tracker interface, Ticket, Comment, Claim, State
internal/tracker/fake/fake.go             in-memory Tracker with BeforeReadBack hook
internal/tracker/fake/fake_test.go
internal/tracker/jira/client.go           Client, New, do(), APIError
internal/tracker/jira/client_test.go
internal/tracker/jira/adf.go              adfToText, textToADF
internal/tracker/jira/adf_test.go
internal/tracker/jira/issue.go            wire types, toTicket, Poll, Get
internal/tracker/jira/issue_test.go
internal/tracker/jira/claim.go            Claim, Heartbeat, Release
internal/tracker/jira/claim_test.go
internal/tracker/jira/comment.go          Comment, Transition
internal/tracker/jira/comment_test.go
internal/tracker/jira/admin.go            Check (live verification), EnsureFields (init)
internal/tracker/jira/admin_test.go
```

---

### Task 1: Repository bootstrap

**Files:**
- Create: `go.mod`, `LICENSE`, `.gitignore`, `README.md`, `CONTRIBUTING.md`, `docs/design-spec.md`, `docs/decisions.md`
- Delete: `Docs/` (three files; the `.md` moves, `.pdf` and `.docx` are dropped)

**Interfaces:**
- Produces: module path `github.com/thomasmeadows/hivedispatch` used by every later import.

- [ ] **Step 1: Confirm Go is installed**

Run: `go version`
Expected: `go version go1.27.x linux/amd64`. If missing, the user must run `sudo pacman -S --noconfirm go`.

- [ ] **Step 2: Initialise the module and fetch the license**

```bash
cd /mnt/nvme2/code/HiveDispatch
go mod init github.com/thomasmeadows/hivedispatch
curl -fsSL https://www.apache.org/licenses/LICENSE-2.0.txt -o LICENSE
head -3 LICENSE   # expect "Apache License / Version 2.0, January 2004"
```

- [ ] **Step 3: Move the spec and strip interview framing**

```bash
git mv "Docs/1) HiveDispatch — Design Spec.md" docs/design-spec.md 2>/dev/null || mv "Docs/1) HiveDispatch — Design Spec.md" docs/design-spec.md
rm -rf Docs
```

Edit `docs/design-spec.md`:
- Line 5 byline: replace `2026-09-17 · @Someone` with `2026-09-17 · Original design. Amendments are recorded in decisions.md.`
- In "Executor contract": replace `and it's worth doing before the interview specifically so you can say the abstraction survived contact with a second implementation.` with `— a second implementation is what proves the abstraction.`
- Replace the whole "Keep a decisions log" subsection body with:
  ```
  Every design choice and the alternative rejected, every failure hit and how it was diagnosed, every place the abstraction leaked — all go in `docs/decisions.md`. A repo plus a decisions log is something contributors can reason about; a clean repo with no scars is not.
  ```
- Delete the "Before you start" subsection entirely (npm name, hiring manager, yokai read).

- [ ] **Step 4: Write `.gitignore`**

```gitignore
/bin/
/dist/
*.test
*.out
coverage.html
.hivedispatch.local.yaml
```

- [ ] **Step 5: Write `README.md`**

```markdown
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
```

- [ ] **Step 6: Write `CONTRIBUTING.md`**

```markdown
# Contributing

## Dev setup

- Go 1.27+
- `golangci-lint` v2 (`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest`)

## Before every commit

```sh
go vet ./... && go test -race ./... && golangci-lint run
```

## Rules

- Every external system (tracker, git host, executor, model) sits behind an interface in `internal/` with a fake. Unit tests never touch the network.
- Design decisions go in `docs/decisions.md` with the alternative rejected. If you change a decision, add a new entry that supersedes the old one; do not edit history.
- Tests first. A task is not done until `go test -race ./...` passes.
- Conventional commit messages: `feat:`, `fix:`, `test:`, `docs:`, `chore:`.
```

- [ ] **Step 7: Seed `docs/decisions.md`**

```markdown
# Decisions log

Newest at the bottom. Each entry: what was decided, what was rejected, why.

## 2026-09-17 — Go, single binary

Decided: Go throughout. Rejected: Python (second runtime, no static binary), TypeScript (weaker subprocess supervision story). Why: the domain is concurrency and subprocess supervision, which goroutines and `context` are built for; self-hosted tools should be one file to install.

## 2026-09-17 — Jira owns claims, git owns history

Decided: claims are custom fields on the ticket, verified by read-back. Rejected: claiming via the state branch. Why: one-file-per-worker writes never collide, so git's push rejection never fires; two workers could both take a ticket cleanly. Jira gives one authoritative row per ticket.

## 2026-09-19 — Open source, Apache-2.0

Decided: Apache-2.0. Rejected: MIT (no patent grant), AGPL (deters adoption for a tool meant to be embedded in team workflows). The original spec's interview framing was removed; the decisions log is for contributors.

## 2026-09-19 — Tracker is an interface; Jira is the first implementation

Decided: `internal/tracker.Tracker` with `tracker/jira` as the MVP implementation and `tracker/fake` for tests. Rejected: hard-coding Jira as the spec did; building GitHub Issues first. Why: the user has a Jira instance with admin rights and the spec's claim protocol is designed around Jira fields, but most open-source adopters live on GitHub Issues; the interface makes that an adapter, not a rewrite.

## 2026-09-19 — Triage runs through the Claude Code CLI in read-only mode

Decided: `claude -p --restricted --tools "Read,Grep,Glob" --permission-mode plan --json-schema …`. Rejected: a hand-written tool loop against the Messages API. Why: the CLI gives triage its repo-inspection tools for free, is read-only by construction, and needs no second credential — users on a subscription plan have no API key. A direct-API `Triager` can be added behind the same interface.

## 2026-09-19 — Claim before triage

Decided: claim first, then triage. Rejected: the spec's Ready → Triaged → Claimed order. Why: everything after the claim is owned by exactly one worker, so two workers never triage the same ticket. Cost: one field write on tickets that end up rejected.

## 2026-09-19 — Five Jira statuses; phase lives in the run file

Decided: Jira statuses `Ready`, `In Progress`, `Needs Info`, `In Review`, `Needs Human`, mapped from HiveDispatch's `State` enum via config. Rejected: one Jira status per spec lifecycle state (Triaged, Claimed, Running…). Why: transient states would churn transitions on every poll; the run file on the state branch already records phase at the granularity the spec needs.

## 2026-09-19 — Step budget by counting tool calls

Decided: the Claude Code adapter counts `tool_use` events on `--output-format stream-json` and cancels the subprocess at the budget. Rejected: `--max-turns`. Why: Claude Code 2.1.278 has no `--max-turns`; counting on the stream is provider-agnostic anyway.

## 2026-09-19 — Stable worktree path per ticket

Decided: `<workroot>/<repo>/<TICKET-KEY>`. Why: Claude Code keys session storage on the working directory, so `--resume` only works when the path does not change between runs. The resume token stays opaque to the orchestrator; the path guarantee is the adapter's.

## 2026-09-19 — Orchestrator-side safety commit

Decided: on any executor exit, the dispatcher commits a dirty tree as `hive: WIP (<cause>)`. Rejected: relying on the prompt's commit-as-you-go instruction alone. Why: the prompt is advice; the dispatcher commit is the guarantee that every stop point is safe.

## 2026-09-19 — Two-level config

Decided: worker config in `~/.config/hivedispatch/config.yaml` (tracker, agent id, workroot, repos, windows); repo config in `.hivedispatch.yaml` inside the governed repo (executor settings, allowed tools, budgets, prompt). Secrets only via env. Why: the worker needs config before it can clone anything; per-repo policy belongs in the repo where it is reviewable.

## 2026-09-19 — Executor.Plan() implemented, not wired

Decided: the Claude Code adapter implements `Plan()`; the single-worker dispatcher skips it. Why: footprints only matter with concurrent workers. Implementing it now keeps the interface honest without paying a model call per run.

## 2026-09-19 — Headless default is `dontAsk` plus an allowlist

Decided: default `--permission-mode dontAsk` with tools from repo config. Rejected: `bypassPermissions` as default. Why: an unattended agent with the user's git credentials should fail closed. `bypassPermissions` is an explicit opt-in for sandboxed runs.
```

- [ ] **Step 8: Add remote, commit, push**

```bash
git add -A
git commit -m "chore: bootstrap module, license, docs, decisions log

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
git remote add origin git@github.com:thomasmeadows/HiveDispatch.git
git push -u origin main
```

Expected: push succeeds; `git log --oneline` shows one commit.

---

### Task 2: CI and lint configuration

**Files:**
- Create: `.golangci.yml`, `.github/workflows/ci.yml`

- [ ] **Step 1: Write `.golangci.yml`**

```yaml
version: "2"
linters:
  default: standard
  enable:
    - errorlint
    - gocritic
    - misspell
    - revive
    - unconvert
    - unparam
  settings:
    revive:
      rules:
        - name: exported
          arguments: ["checkPrivateReceivers", "disableStutteringCheck"]
formatters:
  enable:
    - gofmt
    - goimports
```

- [ ] **Step 2: Write `.github/workflows/ci.yml`**

```yaml
name: ci
on:
  push:
    branches: [main]
  pull_request:
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: go vet ./...
      - run: go test -race -count=1 ./...
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - uses: golangci/golangci-lint-action@v8
        with:
          version: latest
```

- [ ] **Step 3: Verify the config parses locally (if golangci-lint is installed)**

Run: `golangci-lint config verify` (skip if not installed; CI will run it).

- [ ] **Step 4: Commit**

```bash
git add .golangci.yml .github/workflows/ci.yml
git commit -m "chore: add CI workflow and golangci-lint config

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 3: Worker config package

**Files:**
- Create: `internal/config/config.go`, `internal/config/load.go`, `internal/config/config_test.go`

**Interfaces:**
- Produces:
  - `type Config struct { AgentID string; Workroot string; PollInterval, HeartbeatInterval, ClaimTimeout time.Duration; Jira JiraConfig; Repos []RepoConfig }`
  - `type JiraConfig struct { BaseURL, Email, Token, JQL string; Fields JiraFields; Statuses JiraStatuses }`
  - `type JiraFields struct { AgentID, ClaimedAt string }`
  - `type JiraStatuses struct { Ready, InProgress, NeedsInfo, InReview, NeedsHuman string }`
  - `type RepoConfig struct { Name, URL, DefaultBranch, JiraProject string }`
  - `func Load(path string) (*Config, error)` — reads YAML, expands `~`, fills `Jira.Token` from `HIVE_JIRA_TOKEN`, applies defaults, validates.
  - `func (c *Config) Validate() error`
  - `func DefaultPath() string` → `~/.config/hivedispatch/config.yaml`

- [ ] **Step 1: Write the failing tests**

`internal/config/config_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validYAML = `
agent_id: worker-a
workroot: ~/hive-work
jira:
  base_url: https://example.atlassian.net
  email: me@example.com
  jql: 'project = HIVE AND status = "Ready"'
  fields:
    agent_id: customfield_10042
    claimed_at: customfield_10043
repos:
  - name: thomasmeadows/HiveDispatch
    url: git@github.com:thomasmeadows/HiveDispatch.git
    jira_project: HIVE
`

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadAppliesDefaultsAndEnvToken(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	t.Setenv("HOME", "/home/tester")
	cfg, err := Load(writeTemp(t, validYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Jira.Token != "secret" {
		t.Errorf("token = %q, want secret", cfg.Jira.Token)
	}
	if cfg.Workroot != "/home/tester/hive-work" {
		t.Errorf("workroot = %q, want ~ expanded", cfg.Workroot)
	}
	if cfg.PollInterval != 60*time.Second {
		t.Errorf("poll_interval default = %v, want 60s", cfg.PollInterval)
	}
	if cfg.HeartbeatInterval != 60*time.Second {
		t.Errorf("heartbeat_interval default = %v, want 60s", cfg.HeartbeatInterval)
	}
	if cfg.ClaimTimeout != 2*time.Hour {
		t.Errorf("claim_timeout default = %v, want 2h", cfg.ClaimTimeout)
	}
	if cfg.Jira.Statuses.Ready != "Ready" || cfg.Jira.Statuses.NeedsHuman != "Needs Human" {
		t.Errorf("status defaults not applied: %+v", cfg.Jira.Statuses)
	}
	if cfg.Repos[0].DefaultBranch != "main" {
		t.Errorf("default_branch default = %q, want main", cfg.Repos[0].DefaultBranch)
	}
}

func TestLoadParsesDurations(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	body := validYAML + "poll_interval: 90s\nclaim_timeout: 3h\n"
	cfg, err := Load(writeTemp(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PollInterval != 90*time.Second || cfg.ClaimTimeout != 3*time.Hour {
		t.Errorf("durations = %v/%v", cfg.PollInterval, cfg.ClaimTimeout)
	}
}

func TestLoadRejectsMissingToken(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "")
	_, err := Load(writeTemp(t, validYAML))
	if err == nil || !strings.Contains(err.Error(), "HIVE_JIRA_TOKEN") {
		t.Fatalf("err = %v, want mention of HIVE_JIRA_TOKEN", err)
	}
}

func TestValidateReportsEveryMissingField(t *testing.T) {
	c := &Config{}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"agent_id", "jira.base_url", "jira.email", "jira.jql", "jira.fields.agent_id", "jira.fields.claimed_at", "repos"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestValidateRejectsClaimTimeoutShorterThanHeartbeat(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	body := validYAML + "heartbeat_interval: 5m\nclaim_timeout: 1m\n"
	_, err := Load(writeTemp(t, body))
	if err == nil || !strings.Contains(err.Error(), "claim_timeout") {
		t.Fatalf("err = %v, want claim_timeout complaint", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/`
Expected: FAIL — `undefined: Load`, `undefined: Config`.

- [ ] **Step 3: Add the yaml dependency**

Run: `go get gopkg.in/yaml.v3@latest`

- [ ] **Step 4: Write `internal/config/config.go`**

```go
// Package config loads and validates the HiveDispatch worker configuration.
//
// The worker config lives outside any governed repository (by default
// ~/.config/hivedispatch/config.yaml) because the worker needs it before it
// can clone anything. Secrets are never read from YAML; they come from the
// environment.
package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Config is the worker-level configuration.
type Config struct {
	AgentID           string        `yaml:"agent_id"`
	Workroot          string        `yaml:"workroot"`
	PollInterval      time.Duration `yaml:"poll_interval"`
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`
	ClaimTimeout      time.Duration `yaml:"claim_timeout"`
	Jira              JiraConfig    `yaml:"jira"`
	Repos             []RepoConfig  `yaml:"repos"`
}

// JiraConfig describes the Jira Cloud site and the fields the claim protocol uses.
type JiraConfig struct {
	BaseURL  string       `yaml:"base_url"`
	Email    string       `yaml:"email"`
	Token    string       `yaml:"-"` // from HIVE_JIRA_TOKEN
	JQL      string       `yaml:"jql"`
	Fields   JiraFields   `yaml:"fields"`
	Statuses JiraStatuses `yaml:"statuses"`
}

// JiraFields holds the custom field IDs (customfield_NNNNN) used for claims.
type JiraFields struct {
	AgentID   string `yaml:"agent_id"`
	ClaimedAt string `yaml:"claimed_at"`
}

// JiraStatuses maps HiveDispatch states to Jira workflow status names.
type JiraStatuses struct {
	Ready      string `yaml:"ready"`
	InProgress string `yaml:"in_progress"`
	NeedsInfo  string `yaml:"needs_info"`
	InReview   string `yaml:"in_review"`
	NeedsHuman string `yaml:"needs_human"`
}

// RepoConfig is one repository the worker may dispatch work into.
type RepoConfig struct {
	Name          string `yaml:"name"`           // owner/repo
	URL           string `yaml:"url"`            // clone URL
	DefaultBranch string `yaml:"default_branch"` // default "main"
	JiraProject   string `yaml:"jira_project"`   // tickets in this project map to this repo
}

func (c *Config) applyDefaults() {
	if c.PollInterval == 0 {
		c.PollInterval = 60 * time.Second
	}
	if c.HeartbeatInterval == 0 {
		c.HeartbeatInterval = 60 * time.Second
	}
	if c.ClaimTimeout == 0 {
		c.ClaimTimeout = 2 * time.Hour
	}
	s := &c.Jira.Statuses
	def := func(p *string, v string) {
		if *p == "" {
			*p = v
		}
	}
	def(&s.Ready, "Ready")
	def(&s.InProgress, "In Progress")
	def(&s.NeedsInfo, "Needs Info")
	def(&s.InReview, "In Review")
	def(&s.NeedsHuman, "Needs Human")
	for i := range c.Repos {
		def(&c.Repos[i].DefaultBranch, "main")
	}
}

// Validate returns an error listing every missing or inconsistent field.
func (c *Config) Validate() error {
	var problems []string
	need := func(v, name string) {
		if strings.TrimSpace(v) == "" {
			problems = append(problems, name+" is required")
		}
	}
	need(c.AgentID, "agent_id")
	need(c.Jira.BaseURL, "jira.base_url")
	need(c.Jira.Email, "jira.email")
	need(c.Jira.JQL, "jira.jql")
	need(c.Jira.Fields.AgentID, "jira.fields.agent_id")
	need(c.Jira.Fields.ClaimedAt, "jira.fields.claimed_at")
	if c.Jira.Token == "" {
		problems = append(problems, "HIVE_JIRA_TOKEN environment variable is required")
	}
	if len(c.Repos) == 0 {
		problems = append(problems, "repos must list at least one repository")
	}
	for i, r := range c.Repos {
		need(r.Name, fmt.Sprintf("repos[%d].name", i))
		need(r.URL, fmt.Sprintf("repos[%d].url", i))
		need(r.JiraProject, fmt.Sprintf("repos[%d].jira_project", i))
	}
	if c.ClaimTimeout > 0 && c.HeartbeatInterval > 0 && c.ClaimTimeout <= c.HeartbeatInterval {
		problems = append(problems, "claim_timeout must be longer than heartbeat_interval")
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New("invalid config:\n  - " + strings.Join(problems, "\n  - "))
}
```

- [ ] **Step 5: Write `internal/config/load.go`**

```go
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultPath is where Load looks when no path is given.
func DefaultPath() string {
	return filepath.Join(homeDir(), ".config", "hivedispatch", "config.yaml")
}

// Load reads a YAML worker config, fills secrets from the environment,
// applies defaults, and validates.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	c.Jira.Token = os.Getenv("HIVE_JIRA_TOKEN")
	c.Workroot = expandHome(c.Workroot)
	if c.Workroot == "" {
		c.Workroot = filepath.Join(homeDir(), ".local", "share", "hivedispatch")
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		return filepath.Join(homeDir(), strings.TrimPrefix(p, "~"))
	}
	return p
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test -race ./internal/config/`
Expected: PASS (6 tests).

- [ ] **Step 7: Commit**

```bash
go mod tidy
git add go.mod go.sum internal/config
git commit -m "feat(config): worker config loading with env secrets and validation

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 4: CLI skeleton

**Files:**
- Create: `cmd/hivedispatch/main.go`, `cmd/hivedispatch/main_test.go`

**Interfaces:**
- Consumes: `config.Load`, `config.DefaultPath`.
- Produces: `func run(args []string, stdout, stderr io.Writer) int` (tested); subcommands `version`, `check` (config only for now; Task 12 adds `--jira`), `init` (registered, prints "not implemented" until Task 12).

- [ ] **Step 1: Write the failing tests**

`cmd/hivedispatch/main_test.go`:

```go
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"version"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errb.String())
	}
	if !strings.HasPrefix(out.String(), "hivedispatch ") {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestNoArgsPrintsUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(nil, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "usage") {
		t.Errorf("stderr = %q", errb.String())
	}
}

func TestCheckValidConfig(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte(`
agent_id: w
jira:
  base_url: https://x.atlassian.net
  email: a@b.c
  jql: project = X
  fields: {agent_id: customfield_1, claimed_at: customfield_2}
repos:
  - {name: o/r, url: git@github.com:o/r.git, jira_project: X}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"check", "-config", p}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "config ok") {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestCheckInvalidConfig(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "")
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte("agent_id: w\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"check", "-config", p}, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "invalid config") {
		t.Errorf("stderr = %q", errb.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/hivedispatch/`
Expected: FAIL — `undefined: run`.

- [ ] **Step 3: Write `cmd/hivedispatch/main.go`**

```go
// Command hivedispatch is the HiveDispatch worker CLI.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/thomasmeadows/hivedispatch/internal/config"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: hivedispatch <command> [flags]

commands:
  version              print the version
  check [-config P]    validate the worker config
  init  [-config P]    set up tracker fields (not implemented yet)
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "hivedispatch %s\n", version)
		return 0
	case "check":
		return runCheck(args[1:], stdout, stderr)
	case "init":
		fmt.Fprintln(stderr, "init: not implemented yet")
		return 1
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

func runCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "path to worker config")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "config ok: agent %s, %d repo(s), jira %s\n", cfg.AgentID, len(cfg.Repos), cfg.Jira.BaseURL)
	return 0
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./cmd/hivedispatch/ && go build -o /dev/null ./cmd/hivedispatch`
Expected: PASS (4 tests), build succeeds.

- [ ] **Step 5: Commit**

```bash
git add cmd/hivedispatch
git commit -m "feat(cli): version and check subcommands

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 5: Tracker interface and domain types

**Files:**
- Create: `internal/tracker/tracker.go`, `internal/tracker/tracker_test.go`

**Interfaces:**
- Produces (used by every later task):

```go
type State string
const (
    StateReady      State = "ready"
    StateInProgress State = "in_progress"
    StateNeedsInfo  State = "needs_info"
    StateInReview   State = "in_review"
    StateNeedsHuman State = "needs_human"
)
type Claim struct { AgentID string; At time.Time }
func (c *Claim) Fresh(now time.Time, timeout time.Duration) bool   // nil-safe: nil claim is not fresh
type Comment struct { ID, AuthorID, Author, Body string; Created time.Time }
type Ticket struct { Key, Summary, Description, Status, URL string; Labels []string; Comments []Comment; Claim *Claim; Updated time.Time }
type Tracker interface { Poll; Get; Claim; Heartbeat; Release; Comment; Transition }   // exact signatures below
var ErrNotFound = errors.New("tracker: ticket not found")
var ErrNotClaimHolder = errors.New("tracker: not the claim holder")
```

- [ ] **Step 1: Write the failing test**

`internal/tracker/tracker_test.go`:

```go
package tracker

import (
	"testing"
	"time"
)

func TestClaimFresh(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	var nilClaim *Claim
	if nilClaim.Fresh(now, time.Hour) {
		t.Error("nil claim must not be fresh")
	}
	c := &Claim{AgentID: "a", At: now.Add(-30 * time.Minute)}
	if !c.Fresh(now, time.Hour) {
		t.Error("30m-old claim with 1h timeout should be fresh")
	}
	if c.Fresh(now, 10*time.Minute) {
		t.Error("30m-old claim with 10m timeout should be stale")
	}
}

func TestStateValid(t *testing.T) {
	for _, s := range []State{StateReady, StateInProgress, StateNeedsInfo, StateInReview, StateNeedsHuman} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	if State("bogus").Valid() {
		t.Error("bogus should be invalid")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tracker/`
Expected: FAIL — `undefined: Claim`.

- [ ] **Step 3: Write `internal/tracker/tracker.go`**

```go
// Package tracker defines the issue-tracker boundary.
//
// The tracker owns the queue and the claims. Implementations must make
// Claim an optimistic write followed by a read-back so that two workers
// racing for the same ticket resolve to exactly one winner.
package tracker

import (
	"context"
	"errors"
	"time"
)

// State is a HiveDispatch lifecycle state. Implementations map it to their
// own status vocabulary.
type State string

// Lifecycle states. Transient states (triaged, claimed, running) are not
// tracker states; they live in the run file.
const (
	StateReady      State = "ready"
	StateInProgress State = "in_progress"
	StateNeedsInfo  State = "needs_info"
	StateInReview   State = "in_review"
	StateNeedsHuman State = "needs_human"
)

// Valid reports whether s is one of the defined states.
func (s State) Valid() bool {
	switch s {
	case StateReady, StateInProgress, StateNeedsInfo, StateInReview, StateNeedsHuman:
		return true
	}
	return false
}

// Claim records which agent holds a ticket and when it last heartbeat.
type Claim struct {
	AgentID string
	At      time.Time
}

// Fresh reports whether the claim was heartbeat within timeout of now.
// A nil claim is never fresh.
func (c *Claim) Fresh(now time.Time, timeout time.Duration) bool {
	if c == nil || c.AgentID == "" {
		return false
	}
	return now.Sub(c.At) < timeout
}

// Comment is one entry in a ticket's thread, body rendered as plain text.
type Comment struct {
	ID       string
	AuthorID string
	Author   string
	Body     string
	Created  time.Time
}

// Ticket is a tracker-agnostic view of an issue.
type Ticket struct {
	Key         string
	Summary     string
	Description string // plain text
	Status      string // raw tracker status name
	URL         string
	Labels      []string
	Comments    []Comment
	Claim       *Claim // nil when unclaimed
	Updated     time.Time
}

// Sentinel errors returned by implementations.
var (
	ErrNotFound       = errors.New("tracker: ticket not found")
	ErrNotClaimHolder = errors.New("tracker: not the claim holder")
)

// Tracker is the issue-tracker boundary. All methods are safe for
// concurrent use.
type Tracker interface {
	// Poll returns every ticket matching the configured trigger query.
	Poll(ctx context.Context) ([]Ticket, error)
	// Get returns one ticket by key, or ErrNotFound.
	Get(ctx context.Context, key string) (Ticket, error)
	// Claim writes agentID and at to the ticket, then re-reads it. It
	// returns won=true only if the read-back still shows agentID.
	Claim(ctx context.Context, key, agentID string, at time.Time) (won bool, err error)
	// Heartbeat refreshes the claim timestamp. Returns ErrNotClaimHolder
	// if agentID no longer holds the claim.
	Heartbeat(ctx context.Context, key, agentID string) error
	// Release clears the claim if agentID holds it. Releasing a claim
	// held by someone else returns ErrNotClaimHolder.
	Release(ctx context.Context, key, agentID string) error
	// Comment posts body (plain text; implementations render it) to the ticket.
	Comment(ctx context.Context, key, body string) error
	// Transition moves the ticket to the tracker status mapped from to.
	Transition(ctx context.Context, key string, to State) error
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/tracker/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tracker
git commit -m "feat(tracker): Tracker interface and domain types

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 6: In-memory fake tracker with race injection

**Files:**
- Create: `internal/tracker/fake/fake.go`, `internal/tracker/fake/fake_test.go`

**Interfaces:**
- Consumes: everything in `internal/tracker`.
- Produces:
  - `func New() *Tracker`
  - `func (f *Tracker) Add(t tracker.Ticket)` — seed a ticket (Status defaults to `"ready"`).
  - `func (f *Tracker) BeforeReadBack func(key string)` — field; called between the claim write and the read-back so tests can inject a competing claim.
  - `func (f *Tracker) Comments(key string) []string` — bodies posted via Comment.
  - `func (f *Tracker) Transitions(key string) []tracker.State`
  - `Poll` returns tickets whose `Status == string(tracker.StateReady)`.
  - `Transition` sets `Status = string(to)`.

- [ ] **Step 1: Write the failing tests**

`internal/tracker/fake/fake_test.go`:

```go
package fake

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

var now = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

func seeded() *Tracker {
	f := New()
	f.Add(tracker.Ticket{Key: "HIVE-1", Summary: "one"})
	f.Add(tracker.Ticket{Key: "HIVE-2", Summary: "two", Status: "in_review"})
	return f
}

func TestPollReturnsOnlyReady(t *testing.T) {
	f := seeded()
	got, err := f.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Key != "HIVE-1" {
		t.Fatalf("got %+v", got)
	}
}

func TestGetUnknownIsNotFound(t *testing.T) {
	_, err := seeded().Get(context.Background(), "HIVE-99")
	if !errors.Is(err, tracker.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestClaimWon(t *testing.T) {
	f := seeded()
	won, err := f.Claim(context.Background(), "HIVE-1", "worker-a", now)
	if err != nil || !won {
		t.Fatalf("won=%v err=%v", won, err)
	}
	tk, _ := f.Get(context.Background(), "HIVE-1")
	if tk.Claim == nil || tk.Claim.AgentID != "worker-a" || !tk.Claim.At.Equal(now) {
		t.Fatalf("claim = %+v", tk.Claim)
	}
}

func TestClaimLostToRace(t *testing.T) {
	f := seeded()
	f.BeforeReadBack = func(key string) {
		// Simulate worker-b writing between our write and our read-back.
		f.overwriteClaim(key, "worker-b", now.Add(time.Second))
	}
	won, err := f.Claim(context.Background(), "HIVE-1", "worker-a", now)
	if err != nil {
		t.Fatal(err)
	}
	if won {
		t.Fatal("worker-a should have lost the race")
	}
}

func TestHeartbeatAndReleaseRequireHolder(t *testing.T) {
	f := seeded()
	ctx := context.Background()
	if _, err := f.Claim(ctx, "HIVE-1", "worker-a", now); err != nil {
		t.Fatal(err)
	}
	if err := f.Heartbeat(ctx, "HIVE-1", "worker-b"); !errors.Is(err, tracker.ErrNotClaimHolder) {
		t.Errorf("heartbeat by non-holder: %v", err)
	}
	if err := f.Release(ctx, "HIVE-1", "worker-b"); !errors.Is(err, tracker.ErrNotClaimHolder) {
		t.Errorf("release by non-holder: %v", err)
	}
	if err := f.Heartbeat(ctx, "HIVE-1", "worker-a"); err != nil {
		t.Errorf("heartbeat by holder: %v", err)
	}
	tk, _ := f.Get(ctx, "HIVE-1")
	if !tk.Claim.At.After(now) {
		t.Errorf("heartbeat did not advance At: %v", tk.Claim.At)
	}
	if err := f.Release(ctx, "HIVE-1", "worker-a"); err != nil {
		t.Errorf("release by holder: %v", err)
	}
	tk, _ = f.Get(ctx, "HIVE-1")
	if tk.Claim != nil {
		t.Errorf("claim not cleared: %+v", tk.Claim)
	}
}

func TestCommentAndTransitionRecorded(t *testing.T) {
	f := seeded()
	ctx := context.Background()
	if err := f.Comment(ctx, "HIVE-1", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := f.Transition(ctx, "HIVE-1", tracker.StateInReview); err != nil {
		t.Fatal(err)
	}
	if got := f.Comments("HIVE-1"); len(got) != 1 || got[0] != "hello" {
		t.Errorf("comments = %v", got)
	}
	if got := f.Transitions("HIVE-1"); len(got) != 1 || got[0] != tracker.StateInReview {
		t.Errorf("transitions = %v", got)
	}
	tk, _ := f.Get(ctx, "HIVE-1")
	if tk.Status != "in_review" {
		t.Errorf("status = %q", tk.Status)
	}
	if len(tk.Comments) != 1 || tk.Comments[0].Body != "hello" {
		t.Errorf("ticket comments = %+v", tk.Comments)
	}
}

func TestGetReturnsCopy(t *testing.T) {
	f := seeded()
	tk, _ := f.Get(context.Background(), "HIVE-1")
	tk.Summary = "mutated"
	again, _ := f.Get(context.Background(), "HIVE-1")
	if again.Summary != "one" {
		t.Error("Get must return a copy")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tracker/fake/`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Write `internal/tracker/fake/fake.go`**

```go
// Package fake is an in-memory tracker.Tracker for tests.
//
// BeforeReadBack lets a test inject a competing write between Claim's write
// and its read-back, which is how the claim race is exercised without a
// real tracker.
package fake

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// Tracker is an in-memory tracker.Tracker.
type Tracker struct {
	// BeforeReadBack, if set, runs between Claim's write and read-back.
	BeforeReadBack func(key string)
	// Now returns the current time; defaults to time.Now.
	Now func() time.Time

	mu          sync.Mutex
	tickets     map[string]*tracker.Ticket
	comments    map[string][]string
	transitions map[string][]tracker.State
	nextID      int
}

var _ tracker.Tracker = (*Tracker)(nil)

// New returns an empty fake tracker.
func New() *Tracker {
	return &Tracker{
		Now:         time.Now,
		tickets:     map[string]*tracker.Ticket{},
		comments:    map[string][]string{},
		transitions: map[string][]tracker.State{},
	}
}

// Add seeds a ticket. An empty Status defaults to "ready".
func (f *Tracker) Add(t tracker.Ticket) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t.Status == "" {
		t.Status = string(tracker.StateReady)
	}
	c := t
	f.tickets[t.Key] = &c
}

// Comments returns the bodies posted to key, in order.
func (f *Tracker) Comments(key string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.comments[key]...)
}

// Transitions returns the states key was transitioned to, in order.
func (f *Tracker) Transitions(key string) []tracker.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]tracker.State(nil), f.transitions[key]...)
}

// overwriteClaim sets a claim unconditionally; used by race tests.
func (f *Tracker) overwriteClaim(key, agentID string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.tickets[key]; ok {
		t.Claim = &tracker.Claim{AgentID: agentID, At: at}
	}
}

func copyTicket(t *tracker.Ticket) tracker.Ticket {
	c := *t
	c.Labels = append([]string(nil), t.Labels...)
	c.Comments = append([]tracker.Comment(nil), t.Comments...)
	if t.Claim != nil {
		cl := *t.Claim
		c.Claim = &cl
	}
	return c
}

// Poll returns tickets in the ready state.
func (f *Tracker) Poll(_ context.Context) ([]tracker.Ticket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tracker.Ticket
	for _, t := range f.tickets {
		if t.Status == string(tracker.StateReady) {
			out = append(out, copyTicket(t))
		}
	}
	return out, nil
}

// Get returns a copy of the ticket.
func (f *Tracker) Get(_ context.Context, key string) (tracker.Ticket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tickets[key]
	if !ok {
		return tracker.Ticket{}, tracker.ErrNotFound
	}
	return copyTicket(t), nil
}

// Claim writes the claim, runs BeforeReadBack, then re-reads.
func (f *Tracker) Claim(_ context.Context, key, agentID string, at time.Time) (bool, error) {
	f.mu.Lock()
	t, ok := f.tickets[key]
	if !ok {
		f.mu.Unlock()
		return false, tracker.ErrNotFound
	}
	t.Claim = &tracker.Claim{AgentID: agentID, At: at}
	f.mu.Unlock()

	if f.BeforeReadBack != nil {
		f.BeforeReadBack(key)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	return t.Claim != nil && t.Claim.AgentID == agentID, nil
}

func (f *Tracker) holder(key, agentID string) (*tracker.Ticket, error) {
	t, ok := f.tickets[key]
	if !ok {
		return nil, tracker.ErrNotFound
	}
	if t.Claim == nil || t.Claim.AgentID != agentID {
		return nil, tracker.ErrNotClaimHolder
	}
	return t, nil
}

// Heartbeat refreshes the claim timestamp for the holder.
func (f *Tracker) Heartbeat(_ context.Context, key, agentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, err := f.holder(key, agentID)
	if err != nil {
		return err
	}
	t.Claim.At = f.Now()
	return nil
}

// Release clears the claim for the holder.
func (f *Tracker) Release(_ context.Context, key, agentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, err := f.holder(key, agentID)
	if err != nil {
		return err
	}
	t.Claim = nil
	return nil
}

// Comment appends body to the ticket thread.
func (f *Tracker) Comment(_ context.Context, key, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tickets[key]
	if !ok {
		return tracker.ErrNotFound
	}
	f.nextID++
	t.Comments = append(t.Comments, tracker.Comment{
		ID:      fmt.Sprint(f.nextID),
		Author:  "fake",
		Body:    body,
		Created: f.Now(),
	})
	f.comments[key] = append(f.comments[key], body)
	return nil
}

// Transition sets the ticket status to the state's string form.
func (f *Tracker) Transition(_ context.Context, key string, to tracker.State) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tickets[key]
	if !ok {
		return tracker.ErrNotFound
	}
	if !to.Valid() {
		return fmt.Errorf("fake: invalid state %q", to)
	}
	t.Status = string(to)
	f.transitions[key] = append(f.transitions[key], to)
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/tracker/...`
Expected: PASS (7 tests in fake).

- [ ] **Step 5: Commit**

```bash
git add internal/tracker/fake
git commit -m "feat(tracker): in-memory fake with claim-race injection

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 7: Jira client core

**Files:**
- Create: `internal/tracker/jira/client.go`, `internal/tracker/jira/client_test.go`

**Interfaces:**
- Consumes: `config.JiraConfig`.
- Produces:
  - `type Client struct { cfg config.JiraConfig; http *http.Client; base *url.URL }`
  - `func New(cfg config.JiraConfig, opts ...Option) (*Client, error)`; `func WithHTTPClient(*http.Client) Option`
  - `func (c *Client) do(ctx, method, path string, body, out any) error` — JSON in/out, Basic auth, maps non-2xx to `*APIError`.
  - `type APIError struct { Status int; Method, Path string; Messages []string }` with `Error()`; `func (e *APIError) Is(target error) bool` returning true for `tracker.ErrNotFound` when Status is 404.
  - `var _ tracker.Tracker = (*Client)(nil)` is added in Task 11 once all methods exist.

- [ ] **Step 1: Write the failing tests**

`internal/tracker/jira/client_test.go`:

```go
package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

func testCfg(baseURL string) config.JiraConfig {
	return config.JiraConfig{
		BaseURL: baseURL,
		Email:   "me@example.com",
		Token:   "tok",
		JQL:     `project = HIVE AND status = "Ready"`,
		Fields:  config.JiraFields{AgentID: "customfield_10042", ClaimedAt: "customfield_10043"},
		Statuses: config.JiraStatuses{
			Ready: "Ready", InProgress: "In Progress", NeedsInfo: "Needs Info",
			InReview: "In Review", NeedsHuman: "Needs Human",
		},
	}
}

// newTestClient returns a client pointed at a mux-backed test server.
func newTestClient(t *testing.T, mux *http.ServeMux) *Client {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := New(testCfg(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDoSendsBasicAuthAndJSON(t *testing.T) {
	mux := http.NewServeMux()
	var gotAuth, gotAccept, gotCT string
	var gotBody map[string]any
	mux.HandleFunc("POST /rest/api/3/echo", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotCT = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	c := newTestClient(t, mux)
	var out struct{ OK bool }
	if err := c.do(context.Background(), http.MethodPost, "/rest/api/3/echo", map[string]string{"a": "b"}, &out); err != nil {
		t.Fatal(err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("me@example.com:tok"))
	if gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}
	if gotAccept != "application/json" || gotCT != "application/json" {
		t.Errorf("headers accept=%q ct=%q", gotAccept, gotCT)
	}
	if gotBody["a"] != "b" {
		t.Errorf("body = %v", gotBody)
	}
	if !out.OK {
		t.Error("response not decoded")
	}
}

func TestDoMapsErrorsToAPIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rest/api/3/issue/NOPE-1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errorMessages":["Issue does not exist or you do not have permission to see it."],"errors":{}}`))
	})
	mux.HandleFunc("PUT /rest/api/3/issue/HIVE-1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errorMessages":[],"errors":{"customfield_10042":"Field 'customfield_10042' cannot be set. It is not on the appropriate screen, or unknown."}}`))
	})
	c := newTestClient(t, mux)

	err := c.do(context.Background(), http.MethodGet, "/rest/api/3/issue/NOPE-1", nil, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Fatalf("err = %v", err)
	}
	if !errors.Is(err, tracker.ErrNotFound) {
		t.Error("404 should satisfy errors.Is(err, tracker.ErrNotFound)")
	}

	err = c.do(context.Background(), http.MethodPut, "/rest/api/3/issue/HIVE-1", map[string]any{}, nil)
	if !errors.As(err, &apiErr) || apiErr.Status != 400 {
		t.Fatalf("err = %v", err)
	}
	if len(apiErr.Messages) != 1 || apiErr.Messages[0] != "customfield_10042: Field 'customfield_10042' cannot be set. It is not on the appropriate screen, or unknown." {
		t.Errorf("messages = %v", apiErr.Messages)
	}
}

func TestDoHandlesNoContent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /rest/api/3/issue/HIVE-1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	c := newTestClient(t, mux)
	if err := c.do(context.Background(), http.MethodPut, "/rest/api/3/issue/HIVE-1", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestNewRejectsBadURL(t *testing.T) {
	cfg := testCfg("://bad")
	if _, err := New(cfg); err == nil {
		t.Fatal("expected error")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tracker/jira/`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Write `internal/tracker/jira/client.go`**

```go
// Package jira implements tracker.Tracker against Jira Cloud REST API v3.
//
// It calls REST directly rather than through a client library because the
// /search endpoint was removed (410) in favour of /search/jql, and most
// libraries have not migrated.
package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// Client is a Jira Cloud tracker.
type Client struct {
	cfg  config.JiraConfig
	http *http.Client
	base *url.URL
	now  func() time.Time
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient replaces the default HTTP client.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// withNow overrides the clock (tests).
func withNow(f func() time.Time) Option {
	return func(c *Client) { c.now = f }
}

// New returns a Client for cfg.
func New(cfg config.JiraConfig, opts ...Option) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(cfg.BaseURL, "/"))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("jira: invalid base_url %q", cfg.BaseURL)
	}
	c := &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 30 * time.Second},
		base: base,
		now:  time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// APIError is a non-2xx response from Jira.
type APIError struct {
	Status   int
	Method   string
	Path     string
	Messages []string
}

func (e *APIError) Error() string {
	msg := strings.Join(e.Messages, "; ")
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("jira: %s %s: %d: %s", e.Method, e.Path, e.Status, msg)
}

// Is lets a 404 satisfy errors.Is(err, tracker.ErrNotFound).
func (e *APIError) Is(target error) bool {
	return target == tracker.ErrNotFound && e.Status == http.StatusNotFound
}

// jiraErrorBody is Jira's standard error envelope.
type jiraErrorBody struct {
	ErrorMessages []string          `json:"errorMessages"`
	Errors        map[string]string `json:"errors"`
}

// do performs one JSON request. body may be nil; out may be nil.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("jira: encode %s %s: %w", method, path, err)
		}
		rdr = bytes.NewReader(buf)
	}
	u := c.base.ResolveReference(&url.URL{Path: path})
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rdr)
	if err != nil {
		return fmt.Errorf("jira: build %s %s: %w", method, path, err)
	}
	req.SetBasicAuth(c.cfg.Email, c.cfg.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("jira: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("jira: read %s %s: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.apiError(method, path, resp.StatusCode, raw)
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("jira: decode %s %s: %w", method, path, err)
	}
	return nil
}

func (c *Client) apiError(method, path string, status int, raw []byte) error {
	e := &APIError{Status: status, Method: method, Path: path}
	var body jiraErrorBody
	if json.Unmarshal(raw, &body) == nil {
		e.Messages = append(e.Messages, body.ErrorMessages...)
		keys := make([]string, 0, len(body.Errors))
		for k := range body.Errors {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			e.Messages = append(e.Messages, k+": "+body.Errors[k])
		}
	} else if s := strings.TrimSpace(string(raw)); s != "" {
		e.Messages = []string{truncate(s, 200)}
	}
	return e
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// issuePath returns /rest/api/3/issue/{key}[suffix].
func issuePath(key, suffix string) string {
	return "/rest/api/3/issue/" + url.PathEscape(key) + suffix
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/tracker/jira/`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/tracker/jira
git commit -m "feat(jira): REST client core with auth and error mapping

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 8: ADF rendering

Jira Cloud v3 returns descriptions and comment bodies as Atlassian Document Format (a JSON tree). HiveDispatch needs plain text in and out.

**Files:**
- Create: `internal/tracker/jira/adf.go`, `internal/tracker/jira/adf_test.go`

**Interfaces:**
- Produces:
  - `type adfNode struct { Type string; Text string; Content []adfNode; Attrs map[string]any }` (json tags `type`, `text`, `content`, `attrs`)
  - `func adfToText(n *adfNode) string` — nil-safe; paragraphs and headings separated by blank lines; list items prefixed `- `; code blocks fenced; hard breaks as `\n`; mentions rendered as `@Name` from `attrs.text`; inline cards as their URL.
  - `func textToADF(s string) adfNode` — one `doc` node with a `paragraph` per line (empty lines produce empty paragraphs).

- [ ] **Step 1: Write the failing tests**

`internal/tracker/jira/adf_test.go`:

```go
package jira

import (
	"encoding/json"
	"testing"
)

func TestADFToTextRendersCommonNodes(t *testing.T) {
	raw := `{
	  "type":"doc","version":1,"content":[
	    {"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Title"}]},
	    {"type":"paragraph","content":[
	      {"type":"text","text":"Hello "},
	      {"type":"mention","attrs":{"id":"abc","text":"@Thomas"}},
	      {"type":"text","text":", see "},
	      {"type":"inlineCard","attrs":{"url":"https://example.com/x"}},
	      {"type":"hardBreak"},
	      {"type":"text","text":"second line"}
	    ]},
	    {"type":"bulletList","content":[
	      {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"one"}]}]},
	      {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"two"}]}]}
	    ]},
	    {"type":"codeBlock","attrs":{"language":"go"},"content":[{"type":"text","text":"fmt.Println(1)"}]}
	  ]}`
	var n adfNode
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	got := adfToText(&n)
	want := "Title\n\nHello @Thomas, see https://example.com/x\nsecond line\n\n- one\n- two\n\n```go\nfmt.Println(1)\n```"
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestADFToTextNilAndEmpty(t *testing.T) {
	if adfToText(nil) != "" {
		t.Error("nil should render empty")
	}
	if adfToText(&adfNode{Type: "doc"}) != "" {
		t.Error("empty doc should render empty")
	}
}

func TestTextToADFRoundTrip(t *testing.T) {
	n := textToADF("line one\n\nline three")
	if n.Type != "doc" || len(n.Content) != 3 {
		t.Fatalf("doc = %+v", n)
	}
	if n.Content[0].Type != "paragraph" || n.Content[0].Content[0].Text != "line one" {
		t.Errorf("para 0 = %+v", n.Content[0])
	}
	if len(n.Content[1].Content) != 0 {
		t.Errorf("empty line should be empty paragraph, got %+v", n.Content[1])
	}
	if adfToText(&n) != "line one\n\nline three" {
		t.Errorf("round trip = %q", adfToText(&n))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tracker/jira/ -run ADF`
Expected: FAIL — `undefined: adfNode`.

- [ ] **Step 3: Write `internal/tracker/jira/adf.go`**

```go
package jira

import (
	"fmt"
	"strings"
)

// adfNode is one node of an Atlassian Document Format tree.
type adfNode struct {
	Type    string         `json:"type"`
	Version int            `json:"version,omitempty"`
	Text    string         `json:"text,omitempty"`
	Content []adfNode      `json:"content,omitempty"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}

// adfToText renders an ADF tree as plain text. Block nodes are separated by
// blank lines; inline formatting is dropped.
func adfToText(n *adfNode) string {
	if n == nil {
		return ""
	}
	var blocks []string
	for i := range n.Content {
		if b := renderBlock(&n.Content[i]); b != "" {
			blocks = append(blocks, b)
		}
	}
	return strings.Join(blocks, "\n\n")
}

func renderBlock(n *adfNode) string {
	switch n.Type {
	case "paragraph", "heading":
		return renderInline(n.Content)
	case "bulletList", "orderedList":
		var items []string
		for i := range n.Content {
			items = append(items, "- "+renderListItem(&n.Content[i]))
		}
		return strings.Join(items, "\n")
	case "codeBlock":
		lang, _ := n.Attrs["language"].(string)
		return "```" + lang + "\n" + renderInline(n.Content) + "\n```"
	case "blockquote", "panel", "expand", "nestedExpand", "tableCell", "tableHeader":
		return adfToText(n)
	case "table":
		var rows []string
		for i := range n.Content {
			var cells []string
			for j := range n.Content[i].Content {
				cells = append(cells, strings.ReplaceAll(adfToText(&n.Content[i].Content[j]), "\n", " "))
			}
			rows = append(rows, "| "+strings.Join(cells, " | ")+" |")
		}
		return strings.Join(rows, "\n")
	case "rule":
		return "---"
	case "mediaSingle", "mediaGroup":
		return "[attachment]"
	default:
		if len(n.Content) > 0 {
			return adfToText(n)
		}
		return renderInline([]adfNode{*n})
	}
}

func renderListItem(n *adfNode) string {
	// A list item is a sequence of blocks; join with a newline and indent.
	var parts []string
	for i := range n.Content {
		parts = append(parts, renderBlock(&n.Content[i]))
	}
	return strings.ReplaceAll(strings.Join(parts, "\n"), "\n", "\n  ")
}

func renderInline(nodes []adfNode) string {
	var sb strings.Builder
	for i := range nodes {
		n := &nodes[i]
		switch n.Type {
		case "text":
			sb.WriteString(n.Text)
		case "hardBreak":
			sb.WriteString("\n")
		case "mention":
			if t, ok := n.Attrs["text"].(string); ok {
				sb.WriteString(t)
			} else {
				sb.WriteString("@unknown")
			}
		case "inlineCard", "blockCard", "embedCard":
			if u, ok := n.Attrs["url"].(string); ok {
				sb.WriteString(u)
			}
		case "emoji":
			if s, ok := n.Attrs["shortName"].(string); ok {
				sb.WriteString(s)
			}
		case "date":
			if ts, ok := n.Attrs["timestamp"].(string); ok {
				sb.WriteString(ts)
			}
		default:
			if len(n.Content) > 0 {
				sb.WriteString(renderInline(n.Content))
			} else if n.Text != "" {
				sb.WriteString(n.Text)
			} else {
				sb.WriteString(fmt.Sprintf("[%s]", n.Type))
			}
		}
	}
	return sb.String()
}

// textToADF wraps plain text in a minimal ADF document, one paragraph per line.
func textToADF(s string) adfNode {
	doc := adfNode{Type: "doc", Version: 1}
	for _, line := range strings.Split(s, "\n") {
		p := adfNode{Type: "paragraph"}
		if line != "" {
			p.Content = []adfNode{{Type: "text", Text: line}}
		}
		doc.Content = append(doc.Content, p)
	}
	return doc
}
```

Note on the round-trip test: `adfToText` joins blocks with `"\n\n"` and skips empty blocks, so `"line one\n\nline three"` → three paragraphs (middle empty) → renders `"line one\n\nline three"`. Consecutive non-empty lines will re-render separated by a blank line; that is acceptable for comments HiveDispatch writes (they are short and paragraph-oriented).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/tracker/jira/ -run ADF`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/tracker/jira/adf.go internal/tracker/jira/adf_test.go
git commit -m "feat(jira): ADF to text rendering and back

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 9: Issue mapping, Poll, and Get

**Files:**
- Create: `internal/tracker/jira/issue.go`, `internal/tracker/jira/issue_test.go`, `internal/tracker/jira/testdata/issue.json`, `internal/tracker/jira/testdata/search_page1.json`, `internal/tracker/jira/testdata/search_page2.json`

**Interfaces:**
- Consumes: `do`, `adfToText`, `issuePath`.
- Produces:
  - `func (c *Client) Poll(ctx) ([]tracker.Ticket, error)` — POST `/rest/api/3/search/jql`, follows `nextPageToken`.
  - `func (c *Client) Get(ctx, key) (tracker.Ticket, error)` — GET issue with explicit `fields`.
  - `func (c *Client) fields() []string` — the field list both use.
  - `func (c *Client) toTicket(raw issueJSON) tracker.Ticket`
  - `const jiraTime = "2006-01-02T15:04:05.000-0700"`

- [ ] **Step 1: Write fixtures**

`internal/tracker/jira/testdata/issue.json` (a single issue as returned by GET; custom field IDs match `testCfg`):

```json
{
  "id": "10001",
  "key": "HIVE-1",
  "self": "https://example.atlassian.net/rest/api/3/issue/10001",
  "fields": {
    "summary": "Add a --version flag",
    "description": {"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"Print the build version."}]}]},
    "status": {"name": "Ready"},
    "labels": ["hive"],
    "updated": "2026-09-19T10:15:00.000+0000",
    "customfield_10042": "worker-b",
    "customfield_10043": "2026-09-19T10:00:00.000+0000",
    "comment": {
      "comments": [
        {
          "id": "20001",
          "author": {"accountId": "acc-1", "displayName": "Thomas"},
          "created": "2026-09-19T09:00:00.000+0000",
          "body": {"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"Please also update the README."}]}]}
        }
      ]
    }
  }
}
```

`internal/tracker/jira/testdata/search_page1.json`:

```json
{
  "issues": [
    {"id":"10001","key":"HIVE-1","fields":{"summary":"one","status":{"name":"Ready"},"labels":[],"updated":"2026-09-19T10:15:00.000+0000","customfield_10042":null,"customfield_10043":null,"comment":{"comments":[]}}}
  ],
  "nextPageToken": "tok2",
  "isLast": false
}
```

`internal/tracker/jira/testdata/search_page2.json`:

```json
{
  "issues": [
    {"id":"10002","key":"HIVE-2","fields":{"summary":"two","status":{"name":"Ready"},"labels":["hive"],"updated":"2026-09-19T10:16:00.000+0000","customfield_10042":null,"customfield_10043":null,"comment":{"comments":[]}}}
  ],
  "isLast": true
}
```

- [ ] **Step 2: Write the failing tests**

`internal/tracker/jira/issue_test.go`:

```go
package jira

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGetMapsIssueToTicket(t *testing.T) {
	mux := http.NewServeMux()
	var gotFields string
	mux.HandleFunc("GET /rest/api/3/issue/HIVE-1", func(w http.ResponseWriter, r *http.Request) {
		gotFields = r.URL.Query().Get("fields")
		_, _ = w.Write(fixture(t, "issue.json"))
	})
	c := newTestClient(t, mux)
	tk, err := c.Get(context.Background(), "HIVE-1")
	if err != nil {
		t.Fatal(err)
	}
	if gotFields != "summary,description,status,labels,updated,comment,customfield_10042,customfield_10043" {
		t.Errorf("fields param = %q", gotFields)
	}
	if tk.Key != "HIVE-1" || tk.Summary != "Add a --version flag" || tk.Description != "Print the build version." {
		t.Errorf("basic fields: %+v", tk)
	}
	if tk.Status != "Ready" || len(tk.Labels) != 1 || tk.Labels[0] != "hive" {
		t.Errorf("status/labels: %+v", tk)
	}
	if tk.URL != "https://example.atlassian.net/browse/HIVE-1" {
		t.Errorf("url = %q", tk.URL)
	}
	if tk.Claim == nil || tk.Claim.AgentID != "worker-b" {
		t.Fatalf("claim = %+v", tk.Claim)
	}
	wantAt := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	if !tk.Claim.At.Equal(wantAt) {
		t.Errorf("claim.At = %v, want %v", tk.Claim.At, wantAt)
	}
	if len(tk.Comments) != 1 {
		t.Fatalf("comments = %+v", tk.Comments)
	}
	cm := tk.Comments[0]
	if cm.ID != "20001" || cm.AuthorID != "acc-1" || cm.Author != "Thomas" || cm.Body != "Please also update the README." {
		t.Errorf("comment = %+v", cm)
	}
	if !cm.Created.Equal(time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("comment.Created = %v", cm.Created)
	}
}

func TestGetNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rest/api/3/issue/HIVE-9", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"errorMessages":["Issue does not exist"],"errors":{}}`))
	})
	c := newTestClient(t, mux)
	_, err := c.Get(context.Background(), "HIVE-9")
	if !errors.Is(err, tracker.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestPollFollowsNextPageToken(t *testing.T) {
	mux := http.NewServeMux()
	var bodies []map[string]any
	mux.HandleFunc("POST /rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		if body["nextPageToken"] == "tok2" {
			_, _ = w.Write(fixture(t, "search_page2.json"))
			return
		}
		_, _ = w.Write(fixture(t, "search_page1.json"))
	})
	c := newTestClient(t, mux)
	got, err := c.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Key != "HIVE-1" || got[1].Key != "HIVE-2" {
		t.Fatalf("got %+v", got)
	}
	if got[0].Claim != nil {
		t.Errorf("null custom fields should mean no claim, got %+v", got[0].Claim)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d", len(bodies))
	}
	if bodies[0]["jql"] != `project = HIVE AND status = "Ready"` {
		t.Errorf("jql = %v", bodies[0]["jql"])
	}
	if _, has := bodies[0]["nextPageToken"]; has {
		t.Error("first request must not send nextPageToken")
	}
	if f, ok := bodies[0]["fields"].([]any); !ok || len(f) != 8 {
		t.Errorf("fields = %v", bodies[0]["fields"])
	}
}

func TestPollStopsAtPageLimit(t *testing.T) {
	mux := http.NewServeMux()
	calls := 0
	mux.HandleFunc("POST /rest/api/3/search/jql", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"issues":[],"nextPageToken":"again","isLast":false}`))
	})
	c := newTestClient(t, mux)
	_, err := c.Poll(context.Background())
	if err == nil {
		t.Fatal("expected error when pagination never terminates")
	}
	if calls != maxPollPages {
		t.Errorf("calls = %d, want %d", calls, maxPollPages)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/tracker/jira/ -run 'Get|Poll'`
Expected: FAIL — `c.Get undefined`.

- [ ] **Step 4: Write `internal/tracker/jira/issue.go`**

```go
package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// jiraTime is the timestamp layout Jira Cloud uses for datetime fields.
const jiraTime = "2006-01-02T15:04:05.000-0700"

// maxPollPages bounds pagination so a misbehaving server cannot spin us.
const maxPollPages = 20

const pollPageSize = 50

// issueJSON is the subset of a Jira issue HiveDispatch reads. Custom fields
// are decoded from the raw map because their IDs are configuration.
type issueJSON struct {
	ID     string                 `json:"id"`
	Key    string                 `json:"key"`
	Fields map[string]any         `json:"fields"`
}

type searchResponse struct {
	Issues        []issueJSON `json:"issues"`
	NextPageToken string      `json:"nextPageToken"`
	IsLast        bool        `json:"isLast"`
}

// fields is the explicit field list. Jira's /search/jql defaults to id only.
func (c *Client) fields() []string {
	return []string{
		"summary", "description", "status", "labels", "updated", "comment",
		c.cfg.Fields.AgentID, c.cfg.Fields.ClaimedAt,
	}
}

// Poll runs the configured JQL and returns every matching ticket.
func (c *Client) Poll(ctx context.Context) ([]tracker.Ticket, error) {
	var out []tracker.Ticket
	token := ""
	for page := 0; page < maxPollPages; page++ {
		body := map[string]any{
			"jql":        c.cfg.JQL,
			"fields":     c.fields(),
			"maxResults": pollPageSize,
		}
		if token != "" {
			body["nextPageToken"] = token
		}
		var resp searchResponse
		if err := c.do(ctx, http.MethodPost, "/rest/api/3/search/jql", body, &resp); err != nil {
			return nil, err
		}
		for i := range resp.Issues {
			out = append(out, c.toTicket(resp.Issues[i]))
		}
		if resp.IsLast || resp.NextPageToken == "" {
			return out, nil
		}
		token = resp.NextPageToken
	}
	return nil, fmt.Errorf("jira: poll exceeded %d pages without isLast", maxPollPages)
}

// Get fetches one issue.
func (c *Client) Get(ctx context.Context, key string) (tracker.Ticket, error) {
	q := url.Values{"fields": {strings.Join(c.fields(), ",")}}
	var raw issueJSON
	if err := c.do(ctx, http.MethodGet, issuePath(key, "")+"?"+q.Encode(), nil, &raw); err != nil {
		return tracker.Ticket{}, err
	}
	return c.toTicket(raw), nil
}

// toTicket maps a Jira issue onto the tracker-agnostic Ticket.
func (c *Client) toTicket(raw issueJSON) tracker.Ticket {
	f := raw.Fields
	t := tracker.Ticket{
		Key:     raw.Key,
		Summary: str(f["summary"]),
		Status:  nested(f["status"], "name"),
		URL:     c.base.String() + "/browse/" + raw.Key,
		Updated: parseTime(str(f["updated"])),
	}
	if d, ok := f["description"].(map[string]any); ok {
		t.Description = adfToText(toADF(d))
	}
	if ls, ok := f["labels"].([]any); ok {
		for _, l := range ls {
			t.Labels = append(t.Labels, str(l))
		}
	}
	if agent := str(f[c.cfg.Fields.AgentID]); agent != "" {
		t.Claim = &tracker.Claim{AgentID: agent, At: parseTime(str(f[c.cfg.Fields.ClaimedAt]))}
	}
	if cm, ok := f["comment"].(map[string]any); ok {
		if list, ok := cm["comments"].([]any); ok {
			for _, item := range list {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				comment := tracker.Comment{
					ID:       str(m["id"]),
					AuthorID: nested(m["author"], "accountId"),
					Author:   nested(m["author"], "displayName"),
					Created:  parseTime(str(m["created"])),
				}
				if b, ok := m["body"].(map[string]any); ok {
					comment.Body = adfToText(toADF(b))
				}
				t.Comments = append(t.Comments, comment)
			}
		}
	}
	return t
}

// toADF converts a generic JSON map into an adfNode tree.
func toADF(m map[string]any) *adfNode {
	n := &adfNode{Type: str(m["type"]), Text: str(m["text"])}
	if attrs, ok := m["attrs"].(map[string]any); ok {
		n.Attrs = attrs
	}
	if kids, ok := m["content"].([]any); ok {
		for _, k := range kids {
			if km, ok := k.(map[string]any); ok {
				n.Content = append(n.Content, *toADF(km))
			}
		}
	}
	return n
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func nested(v any, key string) string {
	m, _ := v.(map[string]any)
	return str(m[key])
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(jiraTime, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -race ./internal/tracker/jira/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tracker/jira
git commit -m "feat(jira): poll via /search/jql with pagination, get, issue mapping

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 10: Claim, Heartbeat, Release

**Files:**
- Create: `internal/tracker/jira/claim.go`, `internal/tracker/jira/claim_test.go`
- Modify: `internal/tracker/jira/client.go` (add `beforeReadBack` hook)

**Interfaces:**
- Consumes: `do`, `Get`, `jiraTime`, `c.now`.
- Produces:
  - `func (c *Client) Claim(ctx, key, agentID string, at time.Time) (bool, error)` — PUT both fields, then `Get`, compare.
  - `func (c *Client) Heartbeat(ctx, key, agentID string) error` — `Get`; if not holder → `tracker.ErrNotClaimHolder`; else PUT `claimed_at = now`.
  - `func (c *Client) Release(ctx, key, agentID string) error` — `Get`; if not holder → `ErrNotClaimHolder`; else PUT both fields `null`.
  - `func (c *Client) setClaimFields(ctx, key string, agentID any, at any) error`
  - Screen-mapping errors (400 containing "not on the appropriate screen") are wrapped with a hint pointing at `docs/jira-setup.md`.
  - Test hook: `withBeforeReadBack(func(key string)) Option` in `client.go`.

- [ ] **Step 1: Write the failing tests**

`internal/tracker/jira/claim_test.go`:

```go
package jira

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// claimServer simulates the two claim fields on one issue.
type claimServer struct {
	mu     sync.Mutex
	agent  any
	at     any
	puts   []map[string]any
	screen bool // when true, PUT fails with the "not on screen" error
}

func (s *claimServer) mux(t *testing.T) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /rest/api/3/issue/HIVE-1", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Fields map[string]any `json:"fields"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.screen {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"errorMessages":[],"errors":{"customfield_10042":"Field 'customfield_10042' cannot be set. It is not on the appropriate screen, or unknown."}}`))
			return
		}
		s.puts = append(s.puts, body.Fields)
		if v, ok := body.Fields["customfield_10042"]; ok {
			s.agent = v
		}
		if v, ok := body.Fields["customfield_10043"]; ok {
			s.at = v
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /rest/api/3/issue/HIVE-1", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		resp := map[string]any{"id": "1", "key": "HIVE-1", "fields": map[string]any{
			"summary": "x", "status": map[string]any{"name": "Ready"}, "labels": []any{},
			"updated": "2026-09-19T10:15:00.000+0000",
			"customfield_10042": s.agent, "customfield_10043": s.at,
			"comment": map[string]any{"comments": []any{}},
		}}
		_ = json.NewEncoder(w).Encode(resp)
	})
	return mux
}

var claimAt = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

func TestClaimWon(t *testing.T) {
	s := &claimServer{}
	c := newTestClient(t, s.mux(t))
	won, err := c.Claim(context.Background(), "HIVE-1", "worker-a", claimAt)
	if err != nil || !won {
		t.Fatalf("won=%v err=%v", won, err)
	}
	if len(s.puts) != 1 {
		t.Fatalf("puts = %v", s.puts)
	}
	if s.puts[0]["customfield_10042"] != "worker-a" || s.puts[0]["customfield_10043"] != "2026-09-19T12:00:00.000+0000" {
		t.Errorf("put fields = %v", s.puts[0])
	}
}

func TestClaimLostOnReadBack(t *testing.T) {
	s := &claimServer{}
	mux := s.mux(t)
	srvClient := newTestClientWith(t, mux, withBeforeReadBack(func(string) {
		s.mu.Lock()
		s.agent, s.at = "worker-b", "2026-09-19T12:00:01.000+0000"
		s.mu.Unlock()
	}))
	won, err := srvClient.Claim(context.Background(), "HIVE-1", "worker-a", claimAt)
	if err != nil {
		t.Fatal(err)
	}
	if won {
		t.Fatal("should have lost")
	}
}

func TestClaimScreenErrorHasHint(t *testing.T) {
	s := &claimServer{screen: true}
	c := newTestClient(t, s.mux(t))
	_, err := c.Claim(context.Background(), "HIVE-1", "worker-a", claimAt)
	if err == nil || !strings.Contains(err.Error(), "jira-setup.md") {
		t.Fatalf("err = %v, want setup hint", err)
	}
}

func TestHeartbeatRequiresHolder(t *testing.T) {
	s := &claimServer{agent: "worker-b", at: "2026-09-19T11:00:00.000+0000"}
	c := newTestClientWith(t, s.mux(t), withNow(func() time.Time { return claimAt }))
	err := c.Heartbeat(context.Background(), "HIVE-1", "worker-a")
	if !errors.Is(err, tracker.ErrNotClaimHolder) {
		t.Fatalf("err = %v", err)
	}
	if len(s.puts) != 0 {
		t.Error("non-holder must not write")
	}
	if err := c.Heartbeat(context.Background(), "HIVE-1", "worker-b"); err != nil {
		t.Fatal(err)
	}
	if len(s.puts) != 1 || s.puts[0]["customfield_10043"] != "2026-09-19T12:00:00.000+0000" {
		t.Errorf("puts = %v", s.puts)
	}
	if _, has := s.puts[0]["customfield_10042"]; has {
		t.Error("heartbeat must not rewrite agent id")
	}
}

func TestReleaseClearsBothFields(t *testing.T) {
	s := &claimServer{agent: "worker-a", at: "2026-09-19T11:00:00.000+0000"}
	c := newTestClient(t, s.mux(t))
	if err := c.Release(context.Background(), "HIVE-1", "worker-b"); !errors.Is(err, tracker.ErrNotClaimHolder) {
		t.Fatalf("non-holder release err = %v", err)
	}
	if err := c.Release(context.Background(), "HIVE-1", "worker-a"); err != nil {
		t.Fatal(err)
	}
	if len(s.puts) != 1 || s.puts[0]["customfield_10042"] != nil || s.puts[0]["customfield_10043"] != nil {
		t.Errorf("puts = %v", s.puts)
	}
}
```

Add to `client_test.go`:

```go
// newTestClientWith is newTestClient with extra options.
func newTestClientWith(t *testing.T, mux *http.ServeMux, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := New(testCfg(srv.URL), opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tracker/jira/ -run 'Claim|Heartbeat|Release'`
Expected: FAIL — `c.Claim undefined`, `undefined: withBeforeReadBack`.

- [ ] **Step 3: Add the hook to `client.go`**

In the `Client` struct add `beforeReadBack func(key string)`, and add:

```go
// withBeforeReadBack runs f between Claim's write and read-back (tests).
func withBeforeReadBack(f func(key string)) Option {
	return func(c *Client) { c.beforeReadBack = f }
}
```

- [ ] **Step 4: Write `internal/tracker/jira/claim.go`**

```go
package jira

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// Claim writes agentID and at to the claim fields, then re-reads the issue.
// It reports won=true only when the read-back still shows agentID.
func (c *Client) Claim(ctx context.Context, key, agentID string, at time.Time) (bool, error) {
	if err := c.setClaimFields(ctx, key, agentID, at.UTC().Format(jiraTime)); err != nil {
		return false, err
	}
	if c.beforeReadBack != nil {
		c.beforeReadBack(key)
	}
	t, err := c.Get(ctx, key)
	if err != nil {
		return false, fmt.Errorf("jira: claim read-back %s: %w", key, err)
	}
	return t.Claim != nil && t.Claim.AgentID == agentID, nil
}

// Heartbeat refreshes claimed_at if agentID holds the claim.
func (c *Client) Heartbeat(ctx context.Context, key, agentID string) error {
	if err := c.requireHolder(ctx, key, agentID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPut, issuePath(key, ""), map[string]any{
		"fields": map[string]any{c.cfg.Fields.ClaimedAt: c.now().UTC().Format(jiraTime)},
	}, nil)
}

// Release clears both claim fields if agentID holds the claim.
func (c *Client) Release(ctx context.Context, key, agentID string) error {
	if err := c.requireHolder(ctx, key, agentID); err != nil {
		return err
	}
	return c.setClaimFields(ctx, key, nil, nil)
}

func (c *Client) requireHolder(ctx context.Context, key, agentID string) error {
	t, err := c.Get(ctx, key)
	if err != nil {
		return err
	}
	if t.Claim == nil || t.Claim.AgentID != agentID {
		return tracker.ErrNotClaimHolder
	}
	return nil
}

// setClaimFields writes both claim fields; nil values clear them.
func (c *Client) setClaimFields(ctx context.Context, key string, agentID, at any) error {
	err := c.do(ctx, http.MethodPut, issuePath(key, ""), map[string]any{
		"fields": map[string]any{
			c.cfg.Fields.AgentID:   agentID,
			c.cfg.Fields.ClaimedAt: at,
		},
	}, nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusBadRequest &&
		strings.Contains(err.Error(), "not on the appropriate screen") {
		return fmt.Errorf("%w\n  hint: the claim custom fields must be on the issue's edit screen — see docs/jira-setup.md", err)
	}
	return err
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -race ./internal/tracker/jira/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tracker/jira
git commit -m "feat(jira): claim with read-back, heartbeat, release

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 11: Comment and Transition

**Files:**
- Create: `internal/tracker/jira/comment.go`, `internal/tracker/jira/comment_test.go`
- Modify: `internal/tracker/jira/client.go` — add `var _ tracker.Tracker = (*Client)(nil)`

**Interfaces:**
- Consumes: `do`, `textToADF`, `config.JiraStatuses`.
- Produces:
  - `func (c *Client) Comment(ctx, key, body string) error` — POST `/issue/{key}/comment` with ADF body.
  - `func (c *Client) Transition(ctx, key string, to tracker.State) error` — GET transitions, match `to.name` case-insensitively against the configured status name, POST the transition id.
  - `func (c *Client) statusName(s tracker.State) (string, error)`

- [ ] **Step 1: Write the failing tests**

`internal/tracker/jira/comment_test.go`:

```go
package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

func TestCommentPostsADF(t *testing.T) {
	mux := http.NewServeMux()
	var got map[string]any
	mux.HandleFunc("POST /rest/api/3/issue/HIVE-1/comment", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"1"}`))
	})
	c := newTestClient(t, mux)
	if err := c.Comment(context.Background(), "HIVE-1", "first line\nsecond"); err != nil {
		t.Fatal(err)
	}
	body, _ := got["body"].(map[string]any)
	if body["type"] != "doc" || body["version"] != float64(1) {
		t.Fatalf("body = %v", got)
	}
	content, _ := body["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("paragraphs = %v", content)
	}
}

func transitionsMux(t *testing.T, posted *string) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rest/api/3/issue/HIVE-1/transitions", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"transitions":[
		  {"id":"11","name":"Start work","to":{"name":"In Progress"}},
		  {"id":"21","name":"Ask","to":{"name":"needs info"}},
		  {"id":"31","name":"Review","to":{"name":"In Review"}}
		]}`))
	})
	mux.HandleFunc("POST /rest/api/3/issue/HIVE-1/transitions", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Transition struct{ ID string } `json:"transition"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		*posted = body.Transition.ID
		w.WriteHeader(204)
	})
	return mux
}

func TestTransitionMatchesStatusNameCaseInsensitively(t *testing.T) {
	var posted string
	c := newTestClient(t, transitionsMux(t, &posted))
	if err := c.Transition(context.Background(), "HIVE-1", tracker.StateNeedsInfo); err != nil {
		t.Fatal(err)
	}
	if posted != "21" {
		t.Errorf("posted transition %q, want 21", posted)
	}
}

func TestTransitionUnavailableListsOptions(t *testing.T) {
	var posted string
	c := newTestClient(t, transitionsMux(t, &posted))
	err := c.Transition(context.Background(), "HIVE-1", tracker.StateNeedsHuman)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), `"Needs Human"`) || !strings.Contains(err.Error(), "In Progress") {
		t.Errorf("err = %v, want target and available names", err)
	}
	if posted != "" {
		t.Error("must not post when no transition matches")
	}
}

func TestTransitionRejectsInvalidState(t *testing.T) {
	c := newTestClient(t, http.NewServeMux())
	if err := c.Transition(context.Background(), "HIVE-1", tracker.State("bogus")); err == nil {
		t.Fatal("expected error")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tracker/jira/ -run 'Comment|Transition'`
Expected: FAIL — `c.Comment undefined`.

- [ ] **Step 3: Write `internal/tracker/jira/comment.go`**

```go
package jira

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

// Comment posts body as a plain-text comment (rendered to ADF).
func (c *Client) Comment(ctx context.Context, key, body string) error {
	return c.do(ctx, http.MethodPost, issuePath(key, "/comment"), map[string]any{
		"body": textToADF(body),
	}, nil)
}

type transitionsResponse struct {
	Transitions []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		To   struct {
			Name string `json:"name"`
		} `json:"to"`
	} `json:"transitions"`
}

// Transition moves the issue to the Jira status configured for state.
func (c *Client) Transition(ctx context.Context, key string, to tracker.State) error {
	target, err := c.statusName(to)
	if err != nil {
		return err
	}
	var resp transitionsResponse
	if err := c.do(ctx, http.MethodGet, issuePath(key, "/transitions"), nil, &resp); err != nil {
		return err
	}
	var available []string
	for _, tr := range resp.Transitions {
		if strings.EqualFold(tr.To.Name, target) {
			return c.do(ctx, http.MethodPost, issuePath(key, "/transitions"), map[string]any{
				"transition": map[string]string{"id": tr.ID},
			}, nil)
		}
		available = append(available, tr.To.Name)
	}
	return fmt.Errorf("jira: %s has no transition to %q (available: %s)", key, target, strings.Join(available, ", "))
}

// statusName maps a HiveDispatch state to the configured Jira status name.
func (c *Client) statusName(s tracker.State) (string, error) {
	st := c.cfg.Statuses
	switch s {
	case tracker.StateReady:
		return st.Ready, nil
	case tracker.StateInProgress:
		return st.InProgress, nil
	case tracker.StateNeedsInfo:
		return st.NeedsInfo, nil
	case tracker.StateInReview:
		return st.InReview, nil
	case tracker.StateNeedsHuman:
		return st.NeedsHuman, nil
	}
	return "", fmt.Errorf("jira: unknown state %q", s)
}
```

Add to `client.go` after the `Client` type:

```go
var _ tracker.Tracker = (*Client)(nil)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go vet ./... && go test -race ./...`
Expected: PASS; the interface assertion compiles, proving `Client` satisfies `tracker.Tracker`.

- [ ] **Step 5: Commit**

```bash
git add internal/tracker/jira
git commit -m "feat(jira): comment and transition; Client satisfies tracker.Tracker

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 12: Live verification (`check --jira`) and field setup (`init --jira`)

**Files:**
- Create: `internal/tracker/jira/admin.go`, `internal/tracker/jira/admin_test.go`, `docs/jira-setup.md`
- Modify: `cmd/hivedispatch/main.go` (add `-jira` to `check`; implement `init`), `cmd/hivedispatch/main_test.go`

**Interfaces:**
- Consumes: `do`, `config.JiraConfig`.
- Produces:
  - `type CheckReport struct { User string; MissingFields []string; MissingStatuses []string; SampleTickets int }`
  - `func (c *Client) Check(ctx) (CheckReport, error)` — GET `/rest/api/3/myself`, GET `/rest/api/3/field` (verify both configured IDs exist), GET `/rest/api/3/status` (verify all five names exist, case-insensitive), then `Poll` and count.
  - `func (r CheckReport) OK() bool`
  - `type EnsuredFields struct { AgentID, ClaimedAt string }`
  - `func (c *Client) EnsureFields(ctx) (EnsuredFields, error)` — finds fields named `HiveDispatch Agent` / `HiveDispatch Claimed At` in GET `/rest/api/3/field`; creates missing ones via POST `/rest/api/3/field`; calls POST `/rest/api/3/screens/addToDefault/{id}` for each created field; returns the IDs.

- [ ] **Step 1: Write the failing tests**

`internal/tracker/jira/admin_test.go`:

```go
package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func adminMux(t *testing.T, fields []map[string]any, created *[]map[string]any, addedToScreen *[]string) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rest/api/3/myself", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"accountId":"acc-1","displayName":"Thomas","emailAddress":"me@example.com"}`))
	})
	mux.HandleFunc("GET /rest/api/3/field", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(fields)
	})
	mux.HandleFunc("GET /rest/api/3/status", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"To Do"},{"name":"Ready"},{"name":"In Progress"},{"name":"Needs Info"},{"name":"In Review"},{"name":"Done"}]`))
	})
	mux.HandleFunc("POST /rest/api/3/search/jql", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"issues":[{"key":"HIVE-1","fields":{"summary":"x","status":{"name":"Ready"},"comment":{"comments":[]}}}],"isLast":true}`))
	})
	mux.HandleFunc("POST /rest/api/3/field", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		*created = append(*created, body)
		id := "customfield_2000" + string(rune('0'+len(*created)))
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "name": body["name"]})
	})
	mux.HandleFunc("POST /rest/api/3/screens/addToDefault/{id}", func(w http.ResponseWriter, r *http.Request) {
		*addedToScreen = append(*addedToScreen, r.PathValue("id"))
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`[]`))
	})
	return mux
}

func TestCheckReportsMissingFieldAndStatus(t *testing.T) {
	fields := []map[string]any{
		{"id": "customfield_10042", "name": "HiveDispatch Agent"},
		{"id": "summary", "name": "Summary"},
	}
	var created []map[string]any
	var added []string
	c := newTestClient(t, adminMux(t, fields, &created, &added))
	rep, err := c.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.User != "Thomas" {
		t.Errorf("user = %q", rep.User)
	}
	if len(rep.MissingFields) != 1 || rep.MissingFields[0] != "customfield_10043" {
		t.Errorf("missing fields = %v", rep.MissingFields)
	}
	if len(rep.MissingStatuses) != 1 || rep.MissingStatuses[0] != "Needs Human" {
		t.Errorf("missing statuses = %v", rep.MissingStatuses)
	}
	if rep.SampleTickets != 1 {
		t.Errorf("sample tickets = %d", rep.SampleTickets)
	}
	if rep.OK() {
		t.Error("report with missing items must not be OK")
	}
}

func TestEnsureFieldsCreatesOnlyMissing(t *testing.T) {
	fields := []map[string]any{
		{"id": "customfield_10042", "name": "HiveDispatch Agent"},
	}
	var created []map[string]any
	var added []string
	c := newTestClient(t, adminMux(t, fields, &created, &added))
	got, err := c.EnsureFields(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentID != "customfield_10042" {
		t.Errorf("agent id = %q, want existing field reused", got.AgentID)
	}
	if got.ClaimedAt != "customfield_20001" {
		t.Errorf("claimed at = %q", got.ClaimedAt)
	}
	if len(created) != 1 || created[0]["name"] != "HiveDispatch Claimed At" ||
		created[0]["type"] != "com.atlassian.jira.plugin.system.customfieldtypes:datetime" {
		t.Errorf("created = %v", created)
	}
	if len(added) != 1 || added[0] != "customfield_20001" {
		t.Errorf("added to screen = %v", added)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tracker/jira/ -run 'Check|Ensure'`
Expected: FAIL — `c.Check undefined`.

- [ ] **Step 3: Write `internal/tracker/jira/admin.go`**

```go
package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Field names HiveDispatch creates when asked to set up a site.
const (
	fieldNameAgent     = "HiveDispatch Agent"
	fieldNameClaimedAt = "HiveDispatch Claimed At"
)

// CheckReport is the result of a live configuration check.
type CheckReport struct {
	User            string
	MissingFields   []string
	MissingStatuses []string
	SampleTickets   int
}

// OK reports whether nothing is missing.
func (r CheckReport) OK() bool {
	return len(r.MissingFields) == 0 && len(r.MissingStatuses) == 0
}

type fieldJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Check verifies credentials, custom field IDs, status names, and that the
// trigger JQL runs. It performs only reads.
func (c *Client) Check(ctx context.Context) (CheckReport, error) {
	var rep CheckReport
	var me struct {
		DisplayName string `json:"displayName"`
	}
	if err := c.do(ctx, http.MethodGet, "/rest/api/3/myself", nil, &me); err != nil {
		return rep, fmt.Errorf("authentication failed: %w", err)
	}
	rep.User = me.DisplayName

	fields, err := c.listFields(ctx)
	if err != nil {
		return rep, err
	}
	have := map[string]bool{}
	for _, f := range fields {
		have[f.ID] = true
	}
	for _, id := range []string{c.cfg.Fields.AgentID, c.cfg.Fields.ClaimedAt} {
		if !have[id] {
			rep.MissingFields = append(rep.MissingFields, id)
		}
	}

	var statuses []struct {
		Name string `json:"name"`
	}
	if err := c.do(ctx, http.MethodGet, "/rest/api/3/status", nil, &statuses); err != nil {
		return rep, err
	}
	st := c.cfg.Statuses
	for _, want := range []string{st.Ready, st.InProgress, st.NeedsInfo, st.InReview, st.NeedsHuman} {
		found := false
		for _, s := range statuses {
			if strings.EqualFold(s.Name, want) {
				found = true
				break
			}
		}
		if !found {
			rep.MissingStatuses = append(rep.MissingStatuses, want)
		}
	}

	tickets, err := c.Poll(ctx)
	if err != nil {
		return rep, fmt.Errorf("trigger JQL failed: %w", err)
	}
	rep.SampleTickets = len(tickets)
	return rep, nil
}

// EnsuredFields are the custom field IDs after EnsureFields.
type EnsuredFields struct {
	AgentID   string
	ClaimedAt string
}

// EnsureFields creates the two claim custom fields if they do not exist and
// adds newly created ones to the default screen so they are writable.
func (c *Client) EnsureFields(ctx context.Context) (EnsuredFields, error) {
	fields, err := c.listFields(ctx)
	if err != nil {
		return EnsuredFields{}, err
	}
	byName := map[string]string{}
	for _, f := range fields {
		byName[f.Name] = f.ID
	}
	var out EnsuredFields
	out.AgentID, err = c.ensureField(ctx, byName, fieldNameAgent,
		"com.atlassian.jira.plugin.system.customfieldtypes:textfield",
		"com.atlassian.jira.plugin.system.customfieldtypes:textsearcher")
	if err != nil {
		return out, err
	}
	out.ClaimedAt, err = c.ensureField(ctx, byName, fieldNameClaimedAt,
		"com.atlassian.jira.plugin.system.customfieldtypes:datetime",
		"com.atlassian.jira.plugin.system.customfieldtypes:datetimerange")
	return out, err
}

func (c *Client) listFields(ctx context.Context) ([]fieldJSON, error) {
	var fields []fieldJSON
	if err := c.do(ctx, http.MethodGet, "/rest/api/3/field", nil, &fields); err != nil {
		return nil, fmt.Errorf("list fields: %w", err)
	}
	return fields, nil
}

func (c *Client) ensureField(ctx context.Context, byName map[string]string, name, typ, searcher string) (string, error) {
	if id, ok := byName[name]; ok {
		return id, nil
	}
	var created fieldJSON
	err := c.do(ctx, http.MethodPost, "/rest/api/3/field", map[string]any{
		"name":        name,
		"description": "Managed by HiveDispatch. Do not edit by hand.",
		"type":        typ,
		"searcherKey": searcher,
	}, &created)
	if err != nil {
		return "", fmt.Errorf("create field %q: %w", name, err)
	}
	if err := c.do(ctx, http.MethodPost, "/rest/api/3/screens/addToDefault/"+url.PathEscape(created.ID), nil, nil); err != nil {
		return created.ID, fmt.Errorf("field %s created but not added to default screen: %w", created.ID, err)
	}
	return created.ID, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/tracker/jira/ -run 'Check|Ensure'`
Expected: PASS.

- [ ] **Step 5: Wire the CLI**

Replace `runCheck` and the `init` case in `cmd/hivedispatch/main.go`:

```go
func runCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "path to worker config")
	live := fs.Bool("jira", false, "also verify against the live Jira site")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "config ok: agent %s, %d repo(s), jira %s\n", cfg.AgentID, len(cfg.Repos), cfg.Jira.BaseURL)
	if !*live {
		return 0
	}
	client, err := jira.New(cfg.Jira)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	rep, err := client.Check(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, "jira check failed:", err)
		return 1
	}
	fmt.Fprintf(stdout, "jira ok: authenticated as %s; trigger JQL matches %d ticket(s)\n", rep.User, rep.SampleTickets)
	for _, f := range rep.MissingFields {
		fmt.Fprintf(stderr, "missing custom field: %s (run `hivedispatch init -jira`)\n", f)
	}
	for _, s := range rep.MissingStatuses {
		fmt.Fprintf(stderr, "missing workflow status: %q (see docs/jira-setup.md)\n", s)
	}
	if !rep.OK() {
		return 1
	}
	return 0
}

func runInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "path to worker config")
	doJira := fs.Bool("jira", false, "create the claim custom fields in Jira")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*doJira {
		fmt.Fprintln(stderr, "init: nothing to do (pass -jira)")
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	client, err := jira.New(cfg.Jira)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ids, err := client.EnsureFields(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, "init failed:", err)
		return 1
	}
	fmt.Fprintf(stdout, "jira fields ready. Put this in your config:\n\njira:\n  fields:\n    agent_id: %s\n    claimed_at: %s\n", ids.AgentID, ids.ClaimedAt)
	return 0
}
```

Update the `switch` so `case "init": return runInit(args[1:], stdout, stderr)`, add imports `context` and `github.com/thomasmeadows/hivedispatch/internal/tracker/jira`, and update `usage`:

```
  check [-config P] [-jira]   validate the worker config; -jira verifies against the live site
  init  -jira [-config P]     create the claim custom fields in Jira and print their IDs
```

Note: `config.Load` requires `jira.fields.*` to be set, which is circular for a first `init`. Resolve it in `runInit` by loading with placeholder-tolerant validation: before calling `config.Load`, set `HIVE_INIT=1`; in `config.Validate`, skip the two `jira.fields.*` checks when `os.Getenv("HIVE_INIT") == "1"`. Add a test in `config_test.go`:

```go
func TestValidateSkipsFieldsDuringInit(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	t.Setenv("HIVE_INIT", "1")
	body := strings.ReplaceAll(validYAML, "    agent_id: customfield_10042\n    claimed_at: customfield_10043\n", "")
	if _, err := Load(writeTemp(t, body)); err != nil {
		t.Fatalf("Load during init: %v", err)
	}
}
```

- [ ] **Step 6: Add CLI tests**

Append to `cmd/hivedispatch/main_test.go`:

```go
func TestInitWithoutJiraFlag(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"init"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}
```

- [ ] **Step 7: Write `docs/jira-setup.md`**

```markdown
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

## Verify

```sh
hivedispatch check -jira
```
```

- [ ] **Step 8: Run everything**

Run: `go vet ./... && go test -race ./... && go build -o bin/hivedispatch ./cmd/hivedispatch`
Expected: all PASS; binary builds.

- [ ] **Step 9: Live verification against the user's Jira (manual)**

With a real config and `HIVE_JIRA_TOKEN` set:

```sh
HIVE_INIT=1 ./bin/hivedispatch init -jira      # prints field IDs; paste into config
./bin/hivedispatch check -jira                 # expect "jira ok" and no "missing" lines
```

Record any surprises (field screen errors, status names, team-managed quirks) in `docs/decisions.md`.

- [ ] **Step 10: Commit**

```bash
git add -A
git commit -m "feat(jira): live check and field setup; init/check CLI; jira-setup docs

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
git push
```

---

## Self-review

**Spec coverage (Phase 0–1 of the MVP strategy):**
- Bootstrap (module, license, docs, decisions log, CI) — Tasks 1–2. ✔
- Worker config with env secrets — Task 3. ✔
- CLI `version` / `check` / `init` — Tasks 4, 12. ✔
- `Tracker` interface + domain types — Task 5. ✔
- Fake with race injection (needed by Phase 2's race test) — Task 6. ✔
- Jira: `/search/jql` + `nextPageToken` + explicit `fields` (spec gotcha) — Task 9. ✔
- Claim protocol: write, read-back, heartbeat, release, stale detection via `Claim.Fresh` — Tasks 5, 10. ✔
- Comment (ADF) and Transition (status mapping) — Tasks 8, 11. ✔
- `jira-setup.md` with the screen gotcha — Task 12. ✔
- Not in this phase by design: dispatcher loop, run windows, state branch, git, executor (Phases 2–5).

**Type consistency:** `config.JiraConfig` fields used in `jira.Client` match Task 3 (`Fields.AgentID`, `Fields.ClaimedAt`, `Statuses.*`, `JQL`, `Email`, `Token`, `BaseURL`). `tracker.State` constants used in Tasks 6, 11, 12 match Task 5. `newTestClient` / `newTestClientWith` / `testCfg` defined in Task 7 and reused in 9–12. `withNow` defined in Task 7, used in Task 10. `fixture()` defined in Task 9, used only there.

**Placeholder scan:** none.
