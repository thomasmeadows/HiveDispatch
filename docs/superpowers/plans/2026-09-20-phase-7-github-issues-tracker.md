# Phase 7: GitHub Issues Tracker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A second `tracker.Tracker` — GitHub Issues — selectable with `tracker: github`, so HiveDispatch works for people without Jira and the `Tracker` interface is proven by a second implementation. Labels carry state, a hidden body marker carries the claim, and the ticket key `PROJECT-N` maps to `owner/repo#N`.

**Architecture:** `internal/tracker/ghissues` implements `tracker.Tracker` over the GitHub REST API (issues, comments, labels). It is configured from `repos[]` (each repo's `project` key and `name`) plus `github.labels`. `config.Config.Tracker` selects the implementation; Jira settings are validated only when Jira is selected. The CLI grows a `Preflight`/`Setup` seam so `check`, `init` and `run` work for either tracker. The dispatcher is untouched.

**Tech Stack:** Go 1.27 stdlib; GitHub REST API 2022-11-28 (`/repos/{o}/{r}/issues`, `/issues/{n}`, `/issues/{n}/comments`, `/issues/{n}/labels`, `/labels`).

**Spec:** `docs/design-spec.md` — Lifecycle and claiming (the claim protocol must survive a second tracker unchanged); decisions log "Tracker is an interface".

## Global Constraints

- `tracker.Tracker` does not change. The claim protocol is still write → read-back.
- Exactly one `hive:*` state label on an issue at a time; `Transition` swaps them atomically enough (remove old, add new; a failure between the two is logged and the next poll sees the truth).
- The claim marker is `<!-- hivedispatch-claim: <agent> <RFC3339 UTC> -->` on its own line at the end of the body; `Description` never includes it.
- Ticket keys are `<PROJECT>-<number>`; `PROJECT` comes from `repos[].project`.
- No new external dependencies. `go vet ./... && go test -race ./... && golangci-lint run` green before every commit; commit messages end with `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.

---

## File Structure

```
internal/config/config.go                 + Tracker ("jira"|"github"), RepoConfig.Project (jira_project alias), GitHubConfig.Labels
internal/tracker/ghissues/client.go       Client, New, do(), token, error mapping, key ⇄ repo#number
internal/tracker/ghissues/claim.go        marker parse/format, Claim, Heartbeat, Release
internal/tracker/ghissues/issues.go       Poll, Get, toTicket, Comment, Transition
internal/tracker/ghissues/admin.go        Check (token, repos, labels), EnsureLabels
internal/tracker/ghissues/*_test.go       httptest
cmd/hivedispatch/wire.go                  tracker selection; preflight per tracker
cmd/hivedispatch/main.go                  check/init/-github; usage
internal/config/starter.go                tracker choice explained
docs/setup.md, docs/config.md, README.md, docs/decisions.md
```

---

### Task 1: Config — tracker selection and project keys

**Files:**
- Modify: `internal/config/config.go`, `config_test.go`, `starter.go`

**Interfaces:**
- Produces:

```go
Config.Tracker string            `yaml:"tracker"`   // "jira" (default) | "github"
RepoConfig.Project string        `yaml:"project"`   // ticket key prefix; `jira_project` still accepted and copied here
GitHubConfig.Labels GitHubLabels `yaml:"labels"`
type GitHubLabels struct { Ready, InProgress, NeedsInfo, InReview, NeedsHuman string } // defaults hive:ready …
```

Rules: when `tracker: github`, `jira.*` is not required and `HIVE_GITHUB_TOKEN` (or discovery) is required at wire time — validation only insists on the token when `tracker: github` **and** the env var is empty, with the hint that `gh auth login` also works. `repos[].project` is required; `jira_project` populates it when `project` is empty (and `RepoConfig.JiraProject` is removed from code — every user is `Project`).

- [ ] **Step 1: Tests** — `TestTrackerGithubSkipsJiraValidation` (config with `tracker: github`, no `jira:` block, `HIVE_GITHUB_TOKEN=gh` → valid; defaults labels), `TestJiraProjectAliasPopulatesProject`, `TestTrackerUnknownRejected`.
- [ ] **Step 2: Implement.** Rename `JiraProject` → `Project` throughout (`config`, `dispatch.repoFor`, `wire.go`, `main.go`, `router` key). Keep the YAML alias by unmarshalling both tags into a private struct and merging in `Load`.
- [ ] **Step 3: Starter** — add a `tracker: jira` line at the top with a comment: "or github: state is carried by labels, the claim by a hidden marker in the issue body; jira.* is then ignored and HIVE_GITHUB_TOKEN must have issues: write". Rename the comment on `jira_project` → `project`.
- [ ] **Step 4:** `go vet ./... && go test -race ./... && golangci-lint run` → commit `feat(config): tracker selection; repos[].project (jira_project alias); github labels`.

---

### Task 2: GitHub Issues client core, key mapping, ticket mapping

**Files:**
- Create: `internal/tracker/ghissues/client.go`, `issues.go`, `client_test.go`, `issues_test.go`

**Interfaces:**
- Produces:

```go
func New(cfg config.GitHubConfig, repos []config.RepoConfig, opts ...Option) (*Client, error)   // token from cfg.Token; opts: WithHTTPClient, withNow
func (c *Client) keyToRef(key string) (repo string, number int, err error)   // "HIVE-12" → "o/r", 12
func (c *Client) refToKey(repo string, number int) (string, bool)
func (c *Client) Poll(ctx) ([]tracker.Ticket, error)    // per repo: GET /repos/{r}/issues?state=open&labels=<ready>&per_page=100 (paginate via Link header, max 5 pages), skipping pull requests (issues with "pull_request" key)
func (c *Client) Get(ctx, key) (tracker.Ticket, error)   // GET issue + GET comments (paginated)
func (c *Client) Comment(ctx, key, body) error           // POST comments
func (c *Client) Transition(ctx, key, to tracker.State) error  // DELETE other hive labels, POST target label
// Ticket mapping: Key "<P>-<n>", Summary title, Description body without marker, Status = the hive:* label present or "open", URL html_url, Labels all names, Comments (author login), Claim from marker, Updated updated_at
```

- [ ] **Step 1: Tests** with `httptest` and a mux keyed on `/repos/o/r/...`: key mapping both ways (unknown project → error), Poll paginates via `Link: <...>; rel="next"` and skips PRs, Get maps everything (fixture issue JSON with a body ending in the marker → `Claim` parsed, `Description` clean), Comment posts `{body}`, Transition removes `hive:ready` and adds `hive:in-progress` and does not touch `bug`.
- [ ] **Step 2: Implement.** `do` mirrors `githost/github`'s (Bearer token, Accept `application/vnd.github+json`, API version header, error message extraction); `APIError.Is(tracker.ErrNotFound)` on 404.
- [ ] **Step 3:** commit `feat(ghissues): GitHub Issues client — poll, get, comment, label transitions`.

---

### Task 3: Claim protocol on the issue body

**Files:**
- Create: `internal/tracker/ghissues/claim.go`, `claim_test.go`

**Interfaces:**
- Produces:

```go
const markerPrefix = "<!-- hivedispatch-claim:"
func splitMarker(body string) (clean string, claim *tracker.Claim)
func withMarker(clean string, agent string, at time.Time) string     // clean + "\n\n<!-- hivedispatch-claim: agent 2026-...Z -->"
func (c *Client) Claim(ctx, key, agent string, at time.Time) (bool, error)   // GET body → PATCH body with marker → GET → compare agent
func (c *Client) Heartbeat(ctx, key, agent string) error                    // GET; if holder → PATCH marker with now
func (c *Client) Release(ctx, key, agent string) error                      // GET; if holder → PATCH clean body
```

- [ ] **Step 1: Tests**: `splitMarker` round-trips and tolerates no marker / trailing whitespace / marker mid-body (last occurrence wins, everything after it dropped); Claim won (PATCH body contains marker, read-back returns it); Claim lost (`withBeforeReadBack` hook overwrites the served body with another agent's marker); Heartbeat and Release refuse non-holders and never PATCH; Release leaves the clean body.
- [ ] **Step 2: Implement** with a `beforeReadBack` hook like the Jira client. Body PATCH is `{"body": ...}` only — never touch title or labels here.
- [ ] **Step 3:** add `var _ tracker.Tracker = (*Client)(nil)`; commit `feat(ghissues): claim protocol via a hidden body marker with read-back`.

---

### Task 4: Check, EnsureLabels, CLI wiring, docs

**Files:**
- Create: `internal/tracker/ghissues/admin.go`, `admin_test.go`
- Modify: `cmd/hivedispatch/wire.go`, `main.go`, `main_test.go`, `docs/setup.md`, `docs/config.md`, `README.md`, `docs/decisions.md`

**Interfaces:**
- Produces:

```go
type CheckReport struct { User string; Repos []string; MissingLabels map[string][]string; SampleTickets int }
func (r CheckReport) OK() bool
func (c *Client) Check(ctx) (CheckReport, error)     // GET /user; per repo GET /repos/{r} (must exist, permissions.push or has_issues); GET /repos/{r}/labels (paginated) vs the five configured; Poll count
func (c *Client) EnsureLabels(ctx) (created int, err error)   // POST /repos/{r}/labels for each missing, with colours and descriptions
```

CLI:
- `newWorker`: `switch cfg.Tracker` → Jira path as today, or `ghissues.New(cfg.GitHub, cfg.Repos)` after token discovery (moved before tracker construction; for `github` a missing token is an error, not a warning).
- `check`: `-jira` becomes `-live` (keep `-jira` as a hidden alias) and runs whichever tracker's preflight; `init -github` calls `EnsureLabels`.
- `githubPreflight` prints: user, repos ok, missing labels (with "run `hivedispatch init -github`"), matched tickets.

Docs: `docs/setup.md` gets a "GitHub Issues instead of Jira" section (token permissions: Issues read/write, Pull requests read/write, Contents read; labels; how to trigger from the phone: add `hive:ready`); `docs/config.md` rows for `tracker`, `repos[].project`, `github.labels.*`; README states both trackers; decisions entry.

- [ ] **Step 1: Tests** for Check (missing labels reported per repo; PR-typed issues not counted) and EnsureLabels (creates only missing; `httptest`), plus a `cmd` test that `check` with `tracker: github` and no token fails with a hint mentioning `gh auth login` and `HIVE_GITHUB_TOKEN`.
- [ ] **Step 2: Implement** and wire.
- [ ] **Step 3:** `go vet ./... && go test -race ./... && golangci-lint run ./... && go build -o bin/hivedispatch ./cmd/hivedispatch`; commit `feat: GitHub Issues tracker wired end to end; check/init for either tracker`; push.
- [ ] **Step 4: Manual E2E (user):** in a scratch repo (or this one): `tracker: github`, `repos[0].project: HD`; `hivedispatch init -github`; open an issue, add label `hive:ready`; `hivedispatch run -once -executor fake -placeholder` → issue gets `hive:in-review`, a comment, a branch `hive/HD-<n>` and a PR. Then a real run.

---

## Self-review

**Spec coverage:** second `Tracker` implementation ✔; claim protocol identical in shape (write, read-back, heartbeat, stale reclaim) ✔; phone-driven steering (labels) ✔; setup automation (`init -github`) ✔; preflight parity with Jira ✔.

**Type consistency:** `RepoConfig.Project` replaces `JiraProject` everywhere in Task 1 before Tasks 2–4 use it; `tracker.State` → label mapping lives in `ghissues` only; `githost/github` and `tracker/ghissues` share nothing but the token (kept separate on purpose — PRs and issues are different capabilities).

**Placeholder scan:** none.
