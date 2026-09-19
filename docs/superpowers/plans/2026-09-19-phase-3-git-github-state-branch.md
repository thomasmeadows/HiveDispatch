# Phase 3: Git Plumbing, GitHub PRs, State Branch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace three Phase 2 fakes with real implementations — `gitops` via git worktrees, `githost` via the GitHub REST API, `state` via the `hive/state` orphan branch — so that `hivedispatch run` takes a real Jira ticket to a real GitHub PR with a placeholder commit, and a crash-restart resumes on the same branch and PR.

**Architecture:** `internal/gitops/git` shells out to `git`: one `--no-checkout` base clone per repo, one worktree per ticket on `hive/<KEY>`, a safety commit and push in `Finalize`. `internal/githost/github` is a small REST client. `internal/state/gitbranch` keeps run files on an orphan branch checked out as a worktree, pushing after every write and failing loudly if another worker touched our file. `internal/state/router` picks a store per Jira project so the dispatcher still sees one `RunStore`. The dispatcher does not change.

**Tech Stack:** Go 1.27 stdlib (`os/exec`, `net/http`), git ≥ 2.42 (`worktree add --orphan`), GitHub REST API 2022-11-28.

**Spec:** `docs/design-spec.md` — Lifecycle (idempotent branch names), State in git, Failure handling (push rejected repeatedly → fail loudly), Build order step 3.

## Global Constraints

- Module path `github.com/thomasmeadows/hivedispatch`; no new external dependencies.
- Git credentials for clone/push are the user's own (ssh or credential helper). `HIVE_GITHUB_TOKEN` is used only for the PR API.
- Branch name is always `gitops.BranchName(key)` = `hive/<KEY>`; a restarted run targets the same branch and PR.
- The state branch is `hive/state`; one file per ticket; a push conflict on our own file is a bug, never merged.
- All git tests run against a local bare repo in a temp dir with `GIT_CONFIG_GLOBAL=/dev/null` and `GIT_CONFIG_NOSYSTEM=1`; no network.
- `go vet ./... && go test -race ./... && golangci-lint run` green before every commit; commit messages end with `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.

---

## File Structure

```
internal/gitops/gitops.go              + Workspace.Base (upstream ref the branch was cut from)
internal/gitops/git/run.go             exec helper: run(ctx, dir, args...) (stdout, error), refExists
internal/gitops/git/git.go             Workspaces: RepoDir, EnsureBase, Prepare, Finalize
internal/gitops/git/git_test.go        bare-repo fixture (gittest helpers live here, reused by state tests via copy)
internal/gitops/git/gittest/gittest.go NewRemote(t) (bare repo path with main); Env(t) sets isolated git env
internal/githost/github/github.go      Client: New, FindPR, OpenPR
internal/githost/github/github_test.go httptest
internal/state/gitbranch/gitbranch.go  Store: Open, Load, Save, AppendLog, WriteLog, commitAndPush with conflict check
internal/state/gitbranch/gitbranch_test.go
internal/state/router/router.go        Store routing by Jira project prefix
internal/state/router/router_test.go
internal/config/config.go              + GitHub {APIURL, Token}, StateStore "branch"|"local"
internal/executor/fake/fake.go         + Placeholder: write a file into the workspace
cmd/hivedispatch/main.go               run wires real gitops/githost/state; -placeholder flag
docs/decisions.md, README.md, docs/jira-setup.md → docs/setup.md (rename, add GitHub + git sections)
```

---

### Task 1: Git test fixture and workspace preparation

**Files:**
- Create: `internal/gitops/git/gittest/gittest.go`, `internal/gitops/git/run.go`, `internal/gitops/git/git.go`, `internal/gitops/git/git_test.go`
- Modify: `internal/gitops/gitops.go` (add `Base` to `Workspace`), `internal/gitops/fake/fake.go` (set `Base: "origin/" + repo.DefaultBranch`)

**Interfaces:**
- Produces:

```go
// gittest
func Env(t *testing.T)                       // isolates git config and sets an identity via env
func NewRemote(t *testing.T) string          // bare repo path whose main has one commit containing README.md
func Clone(t *testing.T, remote string) string  // a working clone with identity set, for simulating "someone else"
// git
func New(root string) *Workspaces
func (w *Workspaces) RepoDir(repo config.RepoConfig) string          // <root>/<owner>__<name>
func (w *Workspaces) EnsureBase(ctx, repo) (string, error)           // <RepoDir>/repo, --no-checkout clone, fetched
func (w *Workspaces) Prepare(ctx, repo, key) (gitops.Workspace, error)
// run.go
func run(ctx, dir string, args ...string) (string, error)            // trimmed stdout; error includes stderr
func refExists(ctx, dir, ref string) bool
```

- [ ] **Step 1: Add `Base` to the workspace type**

In `internal/gitops/gitops.go` change `Workspace` to:

```go
// Workspace is a prepared checkout for one ticket.
type Workspace struct {
	Path   string
	Branch string
	Base   string // ref the branch was cut from, e.g. "origin/main"
}
```

In `internal/gitops/fake/fake.go` `Prepare`, return `gitops.Workspace{Path: dir, Branch: gitops.BranchName(key), Base: "origin/" + repo.DefaultBranch}` (rename the `_` parameter to `repo`).

- [ ] **Step 2: Write `internal/gitops/git/gittest/gittest.go`**

```go
// Package gittest builds throwaway git remotes for tests.
package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Env isolates git from the user's configuration and sets an identity.
func Env(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
}

// Git runs git in dir and fails the test on error.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

// NewRemote returns a bare repository whose main branch has one commit.
func NewRemote(t *testing.T) string {
	t.Helper()
	Env(t)
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	Git(t, root, "init", "-q", "--bare", "-b", "main", remote)
	seed := filepath.Join(root, "seed")
	Git(t, root, "init", "-q", "-b", "main", seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("# seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	Git(t, seed, "add", "README.md")
	Git(t, seed, "commit", "-q", "-m", "init")
	Git(t, seed, "remote", "add", "origin", remote)
	Git(t, seed, "push", "-q", "origin", "main")
	return remote
}

// Clone returns a fresh working clone of remote.
func Clone(t *testing.T, remote string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	Git(t, t.TempDir(), "clone", "-q", remote, dir)
	return dir
}
```

- [ ] **Step 3: Write the failing tests**

`internal/gitops/git/git_test.go`:

```go
package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/gitops/git/gittest"
)

func repoCfg(remote string) config.RepoConfig {
	return config.RepoConfig{Name: "o/r", URL: remote, DefaultBranch: "main", JiraProject: "HIVE"}
}

func TestPrepareCreatesWorktreeOnTicketBranch(t *testing.T) {
	remote := gittest.NewRemote(t)
	w := New(t.TempDir())
	ws, err := w.Prepare(context.Background(), repoCfg(remote), "HIVE-1")
	if err != nil {
		t.Fatal(err)
	}
	if ws.Branch != "hive/HIVE-1" || ws.Base != "origin/main" {
		t.Errorf("ws = %+v", ws)
	}
	if got := gittest.Git(t, ws.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "hive/HIVE-1\n" {
		t.Errorf("branch = %q", got)
	}
	if _, err := os.Stat(filepath.Join(ws.Path, "README.md")); err != nil {
		t.Error("worktree should contain the default branch's files")
	}
	again, err := w.Prepare(context.Background(), repoCfg(remote), "HIVE-1")
	if err != nil || again.Path != ws.Path {
		t.Errorf("second Prepare = %+v, %v", again, err)
	}
}

func TestPrepareResumesRemoteBranchFromFreshRoot(t *testing.T) {
	remote := gittest.NewRemote(t)
	other := gittest.Clone(t, remote)
	gittest.Git(t, other, "checkout", "-q", "-b", "hive/HIVE-2")
	if err := os.WriteFile(filepath.Join(other, "wip.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, other, "add", "wip.txt")
	gittest.Git(t, other, "commit", "-q", "-m", "wip")
	gittest.Git(t, other, "push", "-q", "origin", "hive/HIVE-2")

	w := New(t.TempDir())
	ws, err := w.Prepare(context.Background(), repoCfg(remote), "HIVE-2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws.Path, "wip.txt")); err != nil {
		t.Error("worktree should resume from the pushed branch")
	}
}

func TestFinalizeCommitsDirtyTreeAndPushes(t *testing.T) {
	remote := gittest.NewRemote(t)
	w := New(t.TempDir())
	ctx := context.Background()
	ws, err := w.Prepare(ctx, repoCfg(remote), "HIVE-3")
	if err != nil {
		t.Fatal(err)
	}
	pushed, err := w.Finalize(ctx, ws, "hive: checkpoint")
	if err != nil || pushed {
		t.Fatalf("clean tree: pushed=%v err=%v", pushed, err)
	}
	if err := os.WriteFile(filepath.Join(ws.Path, "new.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	pushed, err = w.Finalize(ctx, ws, "hive: WIP (timeout)")
	if err != nil || !pushed {
		t.Fatalf("dirty tree: pushed=%v err=%v", pushed, err)
	}
	if got := gittest.Git(t, ws.Path, "log", "-1", "--format=%s %an"); got != "hive: WIP (timeout) HiveDispatch\n" {
		t.Errorf("last commit = %q", got)
	}
	if got := gittest.Git(t, ws.Path, "status", "--porcelain"); got != "" {
		t.Errorf("tree still dirty: %q", got)
	}
	remoteLog := gittest.Git(t, remote, "log", "-1", "--format=%s", "hive/HIVE-3")
	if remoteLog != "hive: WIP (timeout)\n" {
		t.Errorf("remote branch log = %q", remoteLog)
	}
	// Idempotent: nothing new → still reports the branch as pushed.
	pushed, err = w.Finalize(ctx, ws, "hive: checkpoint")
	if err != nil || !pushed {
		t.Fatalf("no-op finalize: pushed=%v err=%v", pushed, err)
	}
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `go test ./internal/gitops/...`
Expected: FAIL — `undefined: New`.

- [ ] **Step 5: Write `internal/gitops/git/run.go`**

```go
package git

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// run executes git in dir and returns trimmed stdout.
func run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// refExists reports whether a fully qualified ref exists in dir.
func refExists(ctx context.Context, dir, ref string) bool {
	_, err := run(ctx, dir, "show-ref", "--verify", "--quiet", ref)
	return err == nil
}
```

- [ ] **Step 6: Write `internal/gitops/git/git.go`**

```go
// Package git implements gitops.Workspaces with git worktrees.
//
// Layout under Root:
//
//	<owner>__<name>/repo      --no-checkout base clone, fetched on every Prepare
//	<owner>__<name>/<KEY>     worktree on hive/<KEY>
//	<owner>__<name>/.state    reserved for the state branch worktree
package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/gitops"
)

// Identity used for safety commits, so they are distinguishable from the
// agent's own commits.
const (
	commitName  = "HiveDispatch"
	commitEmail = "hivedispatch@localhost"
)

// Workspaces manages base clones and per-ticket worktrees under Root.
type Workspaces struct {
	Root string
}

var _ gitops.Workspaces = (*Workspaces)(nil)

// New returns a Workspaces rooted at root.
func New(root string) *Workspaces {
	return &Workspaces{Root: root}
}

// RepoDir is the directory holding everything for one repo.
func (w *Workspaces) RepoDir(repo config.RepoConfig) string {
	return filepath.Join(w.Root, strings.ReplaceAll(repo.Name, "/", "__"))
}

// EnsureBase clones the repo if needed and fetches. It returns the base
// clone path.
func (w *Workspaces) EnsureBase(ctx context.Context, repo config.RepoConfig) (string, error) {
	base := filepath.Join(w.RepoDir(repo), "repo")
	if _, err := os.Stat(filepath.Join(base, ".git")); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(w.RepoDir(repo), 0o755); err != nil {
			return "", err
		}
		if _, err := run(ctx, w.RepoDir(repo), "clone", "-q", "--no-checkout", repo.URL, base); err != nil {
			return "", fmt.Errorf("clone %s: %w", repo.Name, err)
		}
	}
	if _, err := run(ctx, base, "fetch", "-q", "--prune", "origin"); err != nil {
		return "", fmt.Errorf("fetch %s: %w", repo.Name, err)
	}
	return base, nil
}

// Prepare returns the ticket's worktree, creating it from the remote ticket
// branch if one exists, else from the default branch.
func (w *Workspaces) Prepare(ctx context.Context, repo config.RepoConfig, key string) (gitops.Workspace, error) {
	base, err := w.EnsureBase(ctx, repo)
	if err != nil {
		return gitops.Workspace{}, err
	}
	ws := gitops.Workspace{
		Path:   filepath.Join(w.RepoDir(repo), key),
		Branch: gitops.BranchName(key),
		Base:   "origin/" + repo.DefaultBranch,
	}
	if _, err := os.Stat(filepath.Join(ws.Path, ".git")); err == nil {
		return ws, nil
	}
	if _, err := run(ctx, base, "worktree", "prune"); err != nil {
		return gitops.Workspace{}, err
	}
	var args []string
	switch {
	case refExists(ctx, base, "refs/heads/"+ws.Branch):
		args = []string{"worktree", "add", "-q", ws.Path, ws.Branch}
	case refExists(ctx, base, "refs/remotes/origin/"+ws.Branch):
		args = []string{"worktree", "add", "-q", "--track", "-b", ws.Branch, ws.Path, "origin/" + ws.Branch}
	default:
		args = []string{"worktree", "add", "-q", "-b", ws.Branch, ws.Path, ws.Base}
	}
	if _, err := run(ctx, base, args...); err != nil {
		return gitops.Workspace{}, fmt.Errorf("worktree %s: %w", key, err)
	}
	return ws, nil
}

// Finalize commits a dirty tree under the HiveDispatch identity and pushes
// the branch when it has commits beyond Base.
func (w *Workspaces) Finalize(ctx context.Context, ws gitops.Workspace, message string) (bool, error) {
	status, err := run(ctx, ws.Path, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	if status != "" {
		if _, err := run(ctx, ws.Path, "add", "-A"); err != nil {
			return false, err
		}
		if _, err := run(ctx, ws.Path, "-c", "user.name="+commitName, "-c", "user.email="+commitEmail,
			"commit", "-q", "-m", message); err != nil {
			return false, err
		}
	}
	ahead, err := run(ctx, ws.Path, "rev-list", "--count", ws.Base+"..HEAD")
	if err != nil {
		return false, err
	}
	if ahead == "0" {
		return false, nil
	}
	if _, err := run(ctx, ws.Path, "push", "-q", "-u", "origin", ws.Branch); err != nil {
		return false, fmt.Errorf("push %s: %w", ws.Branch, err)
	}
	return true, nil
}
```

- [ ] **Step 7: Run tests, lint, commit**

Run: `go vet ./... && go test -race ./internal/gitops/... ./internal/dispatch/ && ~/go/bin/golangci-lint run ./...`
Expected: PASS, 0 issues.

```bash
git add internal/gitops
git commit -m "feat(gitops): worktree-based Workspaces with safety commit and push

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 2: GitHub PR client and config

**Files:**
- Create: `internal/githost/github/github.go`, `internal/githost/github/github_test.go`
- Modify: `internal/config/config.go`, `internal/config/config_test.go`, `cmd/hivedispatch/main_test.go` (set `HIVE_GITHUB_TOKEN` in tests that load config)

**Interfaces:**
- Produces:

```go
// config
type GitHubConfig struct { APIURL string `yaml:"api_url"`; Token string `yaml:"-"` }   // default https://api.github.com; token from HIVE_GITHUB_TOKEN, required
Config.GitHub GitHubConfig `yaml:"github"`
Config.StateStore string  `yaml:"state_store"`   // "branch" (default) | "local"
// github
func New(cfg config.GitHubConfig, opts ...Option) (*Client, error); func WithHTTPClient(*http.Client) Option
func (c *Client) FindPR(ctx, repo, head string) (*githost.PR, error)   // GET /repos/{repo}/pulls?state=open&head={owner}:{head}
func (c *Client) OpenPR(ctx, repo string, req githost.Request) (*githost.PR, error)  // POST /repos/{repo}/pulls
```

- [ ] **Step 1: Write the failing tests**

Append to `internal/config/config_test.go` and add `t.Setenv("HIVE_GITHUB_TOKEN", "gh")` to every existing test that calls `Load` expecting success (`TestLoadAppliesDefaultsAndEnvToken`, `TestLoadParsesDurations`, `TestValidateSkipsFieldsDuringInit`, `TestLoadRunDefaultsAndWindows`, and the two that expect other specific errors: `TestValidateRejectsClaimTimeoutShorterThanHeartbeat`, `TestValidateRejectsBadWindow`):

```go
func TestLoadGitHubAndStateDefaults(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	t.Setenv("HIVE_GITHUB_TOKEN", "gh")
	cfg, err := Load(writeTemp(t, validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHub.APIURL != "https://api.github.com" || cfg.GitHub.Token != "gh" || cfg.StateStore != "branch" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestLoadRejectsMissingGitHubTokenAndBadStateStore(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	t.Setenv("HIVE_GITHUB_TOKEN", "")
	_, err := Load(writeTemp(t, validYAML+"state_store: cloud\n"))
	if err == nil || !strings.Contains(err.Error(), "HIVE_GITHUB_TOKEN") || !strings.Contains(err.Error(), "state_store") {
		t.Fatalf("err = %v", err)
	}
}
```

In `cmd/hivedispatch/main_test.go`, add `t.Setenv("HIVE_GITHUB_TOKEN", "gh")` to `TestCheckValidConfig`.

`internal/githost/github/github_test.go`:

```go
package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/githost"
)

func newClient(t *testing.T, mux *http.ServeMux) *Client {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := New(config.GitHubConfig{APIURL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestFindPRQueriesByHead(t *testing.T) {
	mux := http.NewServeMux()
	var gotQuery, gotAuth string
	mux.HandleFunc("GET /repos/o/r/pulls", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`[{"number":7,"html_url":"https://github.com/o/r/pull/7","draft":false}]`))
	})
	c := newClient(t, mux)
	pr, err := c.FindPR(context.Background(), "o/r", "hive/HIVE-1")
	if err != nil || pr == nil || pr.Number != 7 || pr.URL != "https://github.com/o/r/pull/7" {
		t.Fatalf("pr=%+v err=%v", pr, err)
	}
	if gotQuery != "head=o%3Ahive%2FHIVE-1&per_page=1&state=open" {
		t.Errorf("query = %q", gotQuery)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth = %q", gotAuth)
	}
}

func TestFindPRNone(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/pulls", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[]`)) })
	pr, err := newClient(t, mux).FindPR(context.Background(), "o/r", "hive/HIVE-1")
	if err != nil || pr != nil {
		t.Fatalf("pr=%v err=%v", pr, err)
	}
}

func TestOpenPRPostsAndMapsErrors(t *testing.T) {
	mux := http.NewServeMux()
	var body map[string]any
	mux.HandleFunc("POST /repos/o/r/pulls", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["head"] == "bad" {
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"message":"No commits between main and bad"}]}`))
			return
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"number":8,"html_url":"https://github.com/o/r/pull/8","draft":true}`))
	})
	c := newClient(t, mux)
	pr, err := c.OpenPR(context.Background(), "o/r", githost.Request{Title: "T", Body: "B", Head: "hive/HIVE-1", Base: "main", Draft: true})
	if err != nil || pr.Number != 8 || !pr.Draft {
		t.Fatalf("pr=%+v err=%v", pr, err)
	}
	if body["title"] != "T" || body["head"] != "hive/HIVE-1" || body["base"] != "main" || body["draft"] != true {
		t.Errorf("body = %v", body)
	}
	_, err = c.OpenPR(context.Background(), "o/r", githost.Request{Head: "bad", Base: "main"})
	if err == nil || !contains(err.Error(), "422") || !contains(err.Error(), "No commits between") {
		t.Errorf("err = %v", err)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0) }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

(Use `strings.Contains` instead of the two helpers — they are shown only to keep the snippet self-contained; import `strings` and delete them.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ ./internal/githost/...`
Expected: FAIL — `cfg.GitHub undefined`, `undefined: New`.

- [ ] **Step 3: Extend config**

In `internal/config/config.go`:
- Add to `Config`: `GitHub GitHubConfig \`yaml:"github"\`` and `StateStore string \`yaml:"state_store"\``.
- Add type:

```go
// GitHubConfig is used only for the pull-request API; git itself uses the
// user's own credentials.
type GitHubConfig struct {
	APIURL string `yaml:"api_url"` // default https://api.github.com
	Token  string `yaml:"-"`       // from HIVE_GITHUB_TOKEN
}
```

- In `applyDefaults`: `def(&c.GitHub.APIURL, "https://api.github.com")` and `def(&c.StateStore, "branch")`.
- In `Load`: `c.GitHub.Token = os.Getenv("HIVE_GITHUB_TOKEN")`.
- In `Validate`: after the Jira token check add

```go
	if c.GitHub.Token == "" {
		problems = append(problems, "HIVE_GITHUB_TOKEN environment variable is required")
	}
	if c.StateStore != "" && c.StateStore != "branch" && c.StateStore != "local" {
		problems = append(problems, fmt.Sprintf("state_store: want branch or local, got %q", c.StateStore))
	}
```

- [ ] **Step 4: Write `internal/githost/github/github.go`**

```go
// Package github implements githost.GitHost against the GitHub REST API.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/githost"
)

// Client talks to one GitHub API host.
type Client struct {
	api   *url.URL
	token string
	http  *http.Client
}

var _ githost.GitHost = (*Client)(nil)

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient replaces the default HTTP client.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// New returns a Client for cfg.
func New(cfg config.GitHubConfig, opts ...Option) (*Client, error) {
	api, err := url.Parse(strings.TrimRight(cfg.APIURL, "/"))
	if err != nil || api.Scheme == "" || api.Host == "" {
		return nil, fmt.Errorf("github: invalid api_url %q", cfg.APIURL)
	}
	c := &Client{api: api, token: cfg.Token, http: &http.Client{Timeout: 30 * time.Second}}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

type prJSON struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	Draft   bool   `json:"draft"`
}

func (p prJSON) toPR() *githost.PR {
	return &githost.PR{URL: p.HTMLURL, Number: p.Number, Draft: p.Draft}
}

// FindPR returns the open PR whose head branch is head, or nil.
func (c *Client) FindPR(ctx context.Context, repo, head string) (*githost.PR, error) {
	owner, _, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("github: repo %q is not owner/name", repo)
	}
	q := url.Values{"state": {"open"}, "head": {owner + ":" + head}, "per_page": {"1"}}
	var prs []prJSON
	if err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/pulls?"+q.Encode(), nil, &prs); err != nil {
		return nil, err
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return prs[0].toPR(), nil
}

// OpenPR creates a pull request.
func (c *Client) OpenPR(ctx context.Context, repo string, req githost.Request) (*githost.PR, error) {
	body := map[string]any{"title": req.Title, "body": req.Body, "head": req.Head, "base": req.Base, "draft": req.Draft}
	var pr prJSON
	if err := c.do(ctx, http.MethodPost, "/repos/"+repo+"/pulls", body, &pr); err != nil {
		return nil, err
	}
	return pr.toPR(), nil
}

type errorJSON struct {
	Message string `json:"message"`
	Errors  []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	ref, err := url.Parse(path)
	if err != nil {
		return fmt.Errorf("github: bad path %q: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.api.ResolveReference(ref).String(), rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e errorJSON
		msg := strings.TrimSpace(string(raw))
		if json.Unmarshal(raw, &e) == nil && e.Message != "" {
			msg = e.Message
			for _, d := range e.Errors {
				msg += "; " + d.Message
			}
		}
		return fmt.Errorf("github: %s %s: %d: %s", method, path, resp.StatusCode, msg)
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}
```

- [ ] **Step 5: Run tests, lint, commit**

Run: `go vet ./... && go test -race ./... && ~/go/bin/golangci-lint run ./...`
Expected: PASS, 0 issues.

```bash
git add internal/config internal/githost cmd
git commit -m "feat(githost): GitHub REST client; github and state_store config

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 3: State branch store

**Files:**
- Create: `internal/state/gitbranch/gitbranch.go`, `internal/state/gitbranch/gitbranch_test.go`

**Interfaces:**
- Produces:

```go
const Branch = "hive/state"
func Open(ctx, base, dir string) (*Store, error)   // base: a clone with origin; dir: worktree path for the state branch
// Store implements state.RunStore. Save/AppendLog/WriteLog commit and push; Load reads the local worktree.
var ErrClaimInvariant = errors.New("gitbranch: another worker modified our run file")
```

Uses `run`/`refExists` — copy the two helpers from `internal/gitops/git/run.go` into `internal/state/gitbranch/run.go` (identical code; a shared internal package would couple state to gitops for two 10-line functions).

- [ ] **Step 1: Write the failing tests**

`internal/state/gitbranch/gitbranch_test.go`:

```go
package gitbranch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/gitops/git/gittest"
	"github.com/thomasmeadows/hivedispatch/internal/state"
)

// worker returns a base clone and state dir for one simulated worker.
func worker(t *testing.T, remote string) (base, dir string) {
	t.Helper()
	root := t.TempDir()
	base = filepath.Join(root, "repo")
	gittest.Git(t, root, "clone", "-q", "--no-checkout", remote, base)
	return base, filepath.Join(root, ".state")
}

func TestOpenCreatesOrphanBranchAndPushes(t *testing.T) {
	remote := gittest.NewRemote(t)
	base, dir := worker(t, remote)
	s, err := Open(context.Background(), base, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := gittest.Git(t, s.Dir, "rev-parse", "--abbrev-ref", "HEAD"); got != Branch+"\n" {
		t.Errorf("branch = %q", got)
	}
	if got := gittest.Git(t, s.Dir, "rev-list", "--count", "HEAD"); got != "1\n" {
		t.Errorf("orphan should have exactly one commit, got %q", got)
	}
	if raw, _ := os.ReadFile(filepath.Join(s.Dir, "README.md")); strings.Contains(string(raw), "seed") {
		t.Error("orphan branch must not carry the default branch's files")
	}
	if !strings.Contains(gittest.Git(t, remote, "branch", "--list", Branch), Branch) {
		t.Error("state branch not pushed")
	}
}

func TestSaveLoadAndSecondWorkerSeesIt(t *testing.T) {
	remote := gittest.NewRemote(t)
	ctx := context.Background()
	baseA, dirA := worker(t, remote)
	a, err := Open(ctx, baseA, dirA)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Save(ctx, &state.Run{Ticket: "HIVE-1", Agent: "a", Attempts: 1, Phase: state.PhaseWorking}); err != nil {
		t.Fatal(err)
	}
	if err := a.AppendLog(ctx, "HIVE-1", state.LogEntry{Agent: "a", Event: "claimed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.WriteLog(ctx, "HIVE-1", "run-1", "output"); err != nil {
		t.Fatal(err)
	}
	baseB, dirB := worker(t, remote)
	b, err := Open(ctx, baseB, dirB)
	if err != nil {
		t.Fatal(err)
	}
	run, err := b.Load(ctx, "HIVE-1")
	if err != nil || run.Agent != "a" || run.Phase != state.PhaseWorking {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	if _, err := os.Stat(filepath.Join(b.Dir, "logs", "HIVE-1", "events.jsonl")); err != nil {
		t.Error("events log not replicated")
	}
	if _, err := os.Stat(filepath.Join(b.Dir, "logs", "HIVE-1", "run-1.log")); err != nil {
		t.Error("raw log not replicated")
	}
	if missing, err := b.Load(ctx, "HIVE-9"); err != nil || missing.Ticket != "HIVE-9" || missing.Attempts != 0 {
		t.Errorf("missing run = %+v, %v", missing, err)
	}
}

func TestSaveRebasesOverOtherWorkersFiles(t *testing.T) {
	remote := gittest.NewRemote(t)
	ctx := context.Background()
	baseA, dirA := worker(t, remote)
	a, _ := Open(ctx, baseA, dirA)
	baseB, dirB := worker(t, remote)
	b, _ := Open(ctx, baseB, dirB)

	if err := a.Save(ctx, &state.Run{Ticket: "HIVE-1", Agent: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(ctx, &state.Run{Ticket: "HIVE-2", Agent: "b"}); err != nil {
		t.Fatal(err) // b is behind a; must rebase and push
	}
	if err := a.Save(ctx, &state.Run{Ticket: "HIVE-1", Agent: "a", Attempts: 1}); err != nil {
		t.Fatal(err) // a is behind b; must rebase and push
	}
	if _, err := os.Stat(filepath.Join(a.Dir, "runs", "HIVE-2.json")); err != nil {
		t.Error("a should have b's file after rebasing")
	}
}

func TestSaveFailsLoudlyWhenOurFileWasModified(t *testing.T) {
	remote := gittest.NewRemote(t)
	ctx := context.Background()
	baseA, dirA := worker(t, remote)
	a, _ := Open(ctx, baseA, dirA)
	baseB, dirB := worker(t, remote)
	b, _ := Open(ctx, baseB, dirB)

	if err := a.Save(ctx, &state.Run{Ticket: "HIVE-1", Agent: "a"}); err != nil {
		t.Fatal(err)
	}
	// b writes a file a already owns upstream: a broken claim, caught on push.
	err := b.Save(ctx, &state.Run{Ticket: "HIVE-1", Agent: "b"})
	if !errors.Is(err, ErrClaimInvariant) {
		t.Fatalf("b err = %v, want ErrClaimInvariant", err)
	}
	// a is unaffected and keeps writing.
	if err := a.Save(ctx, &state.Run{Ticket: "HIVE-1", Agent: "a", Attempts: 2}); err != nil {
		t.Fatalf("a err = %v", err)
	}
}
```

Note: in `TestSaveFailsLoudlyWhenOurFileWasModified`, b's write to `runs/HIVE-1.json` is rejected on its *first* push because the incoming diff from a already contains that file — the check is on files we are pushing versus files that arrived upstream, so a broken claim is caught at the earliest possible moment. The state branch's own `README.md` means the orphan check must compare content, not existence.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/state/gitbranch/`
Expected: FAIL — `undefined: Open`.

- [ ] **Step 3: Write `internal/state/gitbranch/run.go`**

Copy `run` and `refExists` from `internal/gitops/git/run.go` verbatim, with `package gitbranch`.

- [ ] **Step 4: Write `internal/state/gitbranch/gitbranch.go`**

```go
// Package gitbranch stores run state on an orphan branch, hive/state, checked
// out as a worktree of the repo's base clone.
//
// One file per ticket, written only by the worker holding the claim. Every
// write is committed and pushed before the next action starts. A push
// rejection means someone else pushed: rebase if they touched other files,
// fail loudly if they touched ours — that is a broken claim, not a race to
// smooth over.
package gitbranch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/state"
)

// Branch is the orphan branch name.
const Branch = "hive/state"

// ErrClaimInvariant is returned when another worker modified a file this
// worker owns.
var ErrClaimInvariant = errors.New("gitbranch: another worker modified our run file (claim invariant broken)")

const (
	commitName  = "HiveDispatch"
	commitEmail = "hivedispatch@localhost"
)

// Store is a state.RunStore backed by the state branch worktree at Dir.
type Store struct {
	Dir string
	Now func() time.Time

	mu sync.Mutex
}

var _ state.RunStore = (*Store)(nil)

// Open ensures the state branch exists (creating and pushing an orphan if
// not) and is checked out at dir as a worktree of base.
func Open(ctx context.Context, base, dir string) (*Store, error) {
	s := &Store{Dir: dir, Now: time.Now}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		_, _ = run(ctx, dir, "pull", "-q", "--rebase", "origin", Branch) // best effort
		return s, nil
	}
	if _, err := run(ctx, base, "fetch", "-q", "--prune", "origin"); err != nil {
		return nil, err
	}
	if _, err := run(ctx, base, "worktree", "prune"); err != nil {
		return nil, err
	}
	switch {
	case refExists(ctx, base, "refs/heads/"+Branch):
		if _, err := run(ctx, base, "worktree", "add", "-q", dir, Branch); err != nil {
			return nil, err
		}
	case refExists(ctx, base, "refs/remotes/origin/"+Branch):
		if _, err := run(ctx, base, "worktree", "add", "-q", "--track", "-b", Branch, dir, "origin/"+Branch); err != nil {
			return nil, err
		}
	default:
		if _, err := run(ctx, base, "worktree", "add", "-q", "--orphan", "-b", Branch, dir); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("HiveDispatch run state. Managed automatically; do not edit by hand.\n"), 0o644); err != nil {
			return nil, err
		}
		if err := s.commitAndPush(ctx, "hive: init state", nil); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) runPath(key string) string {
	return filepath.Join("runs", key+".json")
}

// Load reads the run from the local worktree.
func (s *Store) Load(_ context.Context, key string) (*state.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(filepath.Join(s.Dir, s.runPath(key)))
	if errors.Is(err, os.ErrNotExist) {
		return &state.Run{Ticket: key}, nil
	}
	if err != nil {
		return nil, err
	}
	var run state.Run
	if err := json.Unmarshal(raw, &run); err != nil {
		return nil, fmt.Errorf("gitbranch: parse %s: %w", key, err)
	}
	return &run, nil
}

// Save writes, commits, and pushes the run file.
func (s *Store) Save(ctx context.Context, run *state.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run.UpdatedAt = s.Now().UTC()
	raw, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	rel := s.runPath(run.Ticket)
	if err := s.write(rel, raw); err != nil {
		return err
	}
	return s.commitAndPush(ctx, fmt.Sprintf("hive: %s %s", run.Ticket, run.Phase), []string{rel})
}

// AppendLog appends an event and pushes.
func (s *Store) AppendLog(ctx context.Context, key string, e state.LogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Time.IsZero() {
		e.Time = s.Now().UTC()
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	rel := filepath.Join("logs", key, "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(s.Dir, rel)), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.Dir, rel), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(raw, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	return s.commitAndPush(ctx, fmt.Sprintf("hive: %s %s", key, e.Event), []string{rel})
}

// WriteLog stores a raw log and pushes.
func (s *Store) WriteLog(ctx context.Context, key, name, content string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rel := filepath.Join("logs", key, name+".log")
	if err := s.write(rel, []byte(content)); err != nil {
		return "", err
	}
	return filepath.Join(s.Dir, rel), s.commitAndPush(ctx, fmt.Sprintf("hive: %s log %s", key, name), []string{rel})
}

func (s *Store) write(rel string, raw []byte) error {
	p := filepath.Join(s.Dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, raw, 0o644)
}

// commitAndPush commits everything staged from the worktree and pushes.
// On rejection it fetches; if the incoming commits touch any of ours it
// returns ErrClaimInvariant, otherwise it rebases and pushes again.
func (s *Store) commitAndPush(ctx context.Context, msg string, ours []string) error {
	if _, err := run(ctx, s.Dir, "add", "-A"); err != nil {
		return err
	}
	if _, err := run(ctx, s.Dir, "diff", "--cached", "--quiet"); err != nil {
		// Non-zero exit means there is something to commit.
		if _, err := run(ctx, s.Dir, "-c", "user.name="+commitName, "-c", "user.email="+commitEmail,
			"commit", "-q", "-m", msg); err != nil {
			return err
		}
	}
	if _, err := run(ctx, s.Dir, "push", "-q", "-u", "origin", Branch); err == nil {
		return nil
	}
	if _, err := run(ctx, s.Dir, "fetch", "-q", "origin", Branch); err != nil {
		return err
	}
	incoming, err := run(ctx, s.Dir, "diff", "--name-only", "HEAD...origin/"+Branch)
	if err != nil {
		return err
	}
	for _, f := range strings.Split(incoming, "\n") {
		for _, o := range ours {
			if filepath.ToSlash(f) == filepath.ToSlash(o) {
				return fmt.Errorf("%w: %s", ErrClaimInvariant, o)
			}
		}
	}
	if _, err := run(ctx, s.Dir, "rebase", "-q", "origin/"+Branch); err != nil {
		_, _ = run(ctx, s.Dir, "rebase", "--abort")
		return fmt.Errorf("gitbranch: rebase onto origin/%s: %w", Branch, err)
	}
	if _, err := run(ctx, s.Dir, "push", "-q", "-u", "origin", Branch); err != nil {
		return fmt.Errorf("gitbranch: push after rebase: %w", err)
	}
	return nil
}
```

`git diff --name-only HEAD...origin/hive/state` lists files changed on the remote side since the merge base — exactly "what did someone else push". `rebase` needs a committer identity: run it with the same `-c user.*` flags (`run(ctx, s.Dir, "-c", "user.name="+commitName, "-c", "user.email="+commitEmail, "rebase", "-q", "origin/"+Branch)`).

- [ ] **Step 5: Run tests, lint, commit**

Run: `go vet ./... && go test -race ./internal/state/... && ~/go/bin/golangci-lint run ./...`
Expected: PASS, 0 issues.

```bash
git add internal/state/gitbranch
git commit -m "feat(state): hive/state orphan-branch store with push-conflict detection

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 4: Store router, placeholder executor, CLI wiring, docs

**Files:**
- Create: `internal/state/router/router.go`, `internal/state/router/router_test.go`
- Modify: `internal/executor/fake/fake.go` (+ `Placeholder bool`), `cmd/hivedispatch/main.go`, `README.md`, `docs/decisions.md`; rename `docs/jira-setup.md` → `docs/setup.md` with GitHub and git sections.

**Interfaces:**
- Produces:

```go
// router
type Store struct { Stores map[string]state.RunStore }   // keyed by Jira project (upper-case)
func (s *Store) Load/Save/AppendLog/WriteLog — route by strings.Cut(key, "-")[0]; error "router: no store for project X"
// fake executor
Placeholder bool  // when true, Run writes <workspace>/HIVEDISPATCH_PLACEHOLDER.md and reports ChangedFiles
```

- [ ] **Step 1: Write the router test**

`internal/state/router/router_test.go`:

```go
package router

import (
	"context"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/state"
	"github.com/thomasmeadows/hivedispatch/internal/state/localdir"
)

func TestRoutesByProject(t *testing.T) {
	a, b := localdir.New(t.TempDir()), localdir.New(t.TempDir())
	r := &Store{Stores: map[string]state.RunStore{"HIVE": a, "OPS": b}}
	ctx := context.Background()
	if err := r.Save(ctx, &state.Run{Ticket: "OPS-3", Agent: "x"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.Load(ctx, "OPS-3"); got.Agent != "x" {
		t.Errorf("not routed to b: %+v", got)
	}
	if got, _ := a.Load(ctx, "OPS-3"); got.Agent != "" {
		t.Errorf("leaked into a: %+v", got)
	}
	if _, err := r.Load(ctx, "NOPE-1"); err == nil {
		t.Error("unknown project must error")
	}
}
```

- [ ] **Step 2: Write `internal/state/router/router.go`**

```go
// Package router fans a single state.RunStore out to one store per Jira
// project, so the dispatcher sees one store while each repo keeps its own
// state branch.
package router

import (
	"context"
	"fmt"
	"strings"

	"github.com/thomasmeadows/hivedispatch/internal/state"
)

// Store routes by the ticket key's project prefix.
type Store struct {
	Stores map[string]state.RunStore
}

var _ state.RunStore = (*Store)(nil)

func (s *Store) pick(key string) (state.RunStore, error) {
	project, _, _ := strings.Cut(key, "-")
	if st, ok := s.Stores[strings.ToUpper(project)]; ok {
		return st, nil
	}
	return nil, fmt.Errorf("router: no store for project %q", project)
}

// Load implements state.RunStore.
func (s *Store) Load(ctx context.Context, key string) (*state.Run, error) {
	st, err := s.pick(key)
	if err != nil {
		return nil, err
	}
	return st.Load(ctx, key)
}

// Save implements state.RunStore.
func (s *Store) Save(ctx context.Context, run *state.Run) error {
	st, err := s.pick(run.Ticket)
	if err != nil {
		return err
	}
	return st.Save(ctx, run)
}

// AppendLog implements state.RunStore.
func (s *Store) AppendLog(ctx context.Context, key string, e state.LogEntry) error {
	st, err := s.pick(key)
	if err != nil {
		return err
	}
	return st.AppendLog(ctx, key, e)
}

// WriteLog implements state.RunStore.
func (s *Store) WriteLog(ctx context.Context, key, name, content string) (string, error) {
	st, err := s.pick(key)
	if err != nil {
		return "", err
	}
	return st.WriteLog(ctx, key, name, content)
}
```

- [ ] **Step 3: Add `Placeholder` to the fake executor**

In `internal/executor/fake/fake.go` add field `Placeholder bool // write a file into the workspace so the git path is exercised` and in `Run`, before `return res, nil`:

```go
	if e.Placeholder && t.Workspace != "" {
		name := "HIVEDISPATCH_PLACEHOLDER.md"
		body := fmt.Sprintf("# %s\n\nPlaceholder written by the fake executor.\n", t.TicketKey)
		if err := os.WriteFile(filepath.Join(t.Workspace, name), []byte(body), 0o644); err != nil {
			return executor.Result{}, err
		}
		res.ChangedFiles = append(res.ChangedFiles, name)
	}
```

(imports `fmt`, `os`, `path/filepath`.) Add a test `TestPlaceholderWritesFile` that sets `Placeholder = true`, runs with `Workspace: t.TempDir()`, and asserts the file exists and `ChangedFiles` has one entry.

- [ ] **Step 4: Wire the CLI**

In `cmd/hivedispatch/main.go` replace the fake imports for gitops/githost/state with:

```go
	gitws "github.com/thomasmeadows/hivedispatch/internal/gitops/git"
	"github.com/thomasmeadows/hivedispatch/internal/githost/github"
	"github.com/thomasmeadows/hivedispatch/internal/state"
	"github.com/thomasmeadows/hivedispatch/internal/state/gitbranch"
	"github.com/thomasmeadows/hivedispatch/internal/state/router"
```

(keep `exfake` and `localdir`), add flag `placeholder := fs.Bool("placeholder", false, "fake executor writes a placeholder file so the branch/PR path is exercised")`, and replace the dispatcher construction with:

```go
	ctx := context.Background()
	ws := gitws.New(filepath.Join(cfg.Workroot, "repos"))
	stores := map[string]state.RunStore{}
	for _, repo := range cfg.Repos {
		base, err := ws.EnsureBase(ctx, repo)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		var st state.RunStore
		if cfg.StateStore == "local" {
			st = localdir.New(filepath.Join(cfg.Workroot, "state", strings.ReplaceAll(repo.Name, "/", "__")))
		} else {
			st, err = gitbranch.Open(ctx, base, filepath.Join(ws.RepoDir(repo), ".state"))
			if err != nil {
				fmt.Fprintln(stderr, "state branch:", err)
				return 1
			}
		}
		stores[strings.ToUpper(repo.JiraProject)] = st
	}
	host, err := github.New(cfg.GitHub)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ex := exfake.New()
	ex.Placeholder = *placeholder
	d := &dispatch.Dispatcher{
		Cfg:        dispatch.ConfigFrom(cfg),
		Tracker:    tr,
		Triager:    passthrough.Triager{},
		Executor:   ex, // Phase 4 replaces this with the Claude Code adapter
		Workspaces: ws,
		Host:       host,
		Store:      &router.Store{Stores: stores},
		Schedule:   sched,
		Log:        logger,
	}
```

Update `usage`: `run   [-config P] [-once] [-placeholder]   poll and dispatch (fake executor until Phase 4)`.

- [ ] **Step 5: Docs**

`git mv docs/jira-setup.md docs/setup.md`; update the two references (`README.md`, `internal/tracker/jira/claim.go` hint string and its test, `cmd/hivedispatch/main.go` check message). Prepend a "## 0. What HiveDispatch needs" section and append:

```markdown
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
```

README: status line → "Phases 0–3 of the MVP are done: config, CLI, Jira tracker, dispatcher, git worktrees, GitHub PRs, and the `hive/state` branch. `hivedispatch run -placeholder` takes a ticket to a real PR with a placeholder commit; the coding agent lands in Phase 4." and change the setup link to `docs/setup.md`.

`docs/decisions.md` append:

```markdown
## 2026-09-19 — Base clone is --no-checkout; one worktree per ticket

Decided: each repo gets one `--no-checkout` base clone and one worktree per ticket at a stable path. Rejected: a fresh clone per ticket (slow, no session resume), a single working clone with branch switching (one ticket at a time, and Claude Code's session store is keyed by path). Why: worktrees share objects, keep the per-ticket path stable for `--resume`, and let several tickets sit on disk at once.

## 2026-09-19 — One state branch per governed repo, routed by Jira project

Decided: `hive/state` lives in each repo HiveDispatch works in; a router picks the store by ticket prefix. Rejected: one central state repo. Why: the spec keeps state with the code it describes, and per-repo state means a repo's history and its run log travel together. The cost is one push per state write; acceptable at MVP scale.

## 2026-09-19 — Safety commits use a HiveDispatch identity

Decided: the dispatcher's own commits (`hive: checkpoint`, `hive: WIP (...)`, state writes) are authored as `HiveDispatch <hivedispatch@localhost>`. Why: reviewers can tell orchestrator housekeeping from agent work in `git log`.
```

- [ ] **Step 6: Run everything, commit, push**

Run: `go vet ./... && go test -race ./... && ~/go/bin/golangci-lint run ./... && go build -o bin/hivedispatch ./cmd/hivedispatch`
Expected: PASS, 0 issues, binary built.

```bash
git add -A
git commit -m "feat(cli): wire git worktrees, GitHub PRs, and state branch; docs/setup.md

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
git push
```

- [ ] **Step 7: Manual end-to-end (needs the user's Jira and a scratch GitHub repo)**

Point `repos[0]` at a scratch repository, export both tokens, put a ticket in Ready, then:

```sh
./bin/hivedispatch run -once -placeholder
```

Expected: branch `hive/<KEY>` on GitHub with a `hive: checkpoint` commit adding `HIVEDISPATCH_PLACEHOLDER.md`; a PR titled `<KEY>: <summary>`; the ticket in In Review with a `[HiveDispatch] Opened https://github.com/.../pull/N` comment; `hive/state` branch with `runs/<KEY>.json` at phase `done`. Move the ticket back to Ready and run again: same branch, `FindPR` returns the same PR, no duplicate. Kill the worker mid-run (`kill -9`) on a second ticket; on restart it reclaims (after `claim_timeout`) and continues on the same branch.

Record surprises in `docs/decisions.md`.

---

## Self-review

**Spec coverage (Phase 3 of the MVP strategy):** worktree/branch/commit/push (Task 1) ✔; open PR via REST behind `GitHost` (Task 2) ✔; state branch, one file per ticket, push-conflict handling that fails loudly on our own file (Task 3) ✔; idempotent branch and PR on restart (`Prepare` resumes remote branch, `ensurePR` finds before opening) ✔; end-to-end with fake executor including crash-restart (Task 4 step 7) ✔; config surface for GitHub token and state store (Task 2) ✔; docs incl. security note (Task 4) ✔.

**Type consistency:** `gitops.Workspace.Base` added in Task 1 and used by `Finalize`; fake updated. `config.GitHubConfig` (Task 2) used by `github.New` and the CLI. `gittest` helpers (Task 1) reused by Task 3 tests. `router.Store{Stores}` (Task 4) constructed in the CLI. `exfake.Executor.Placeholder` set from the `-placeholder` flag.

**Placeholder scan:** the only "placeholder" is the deliberate placeholder file feature.
