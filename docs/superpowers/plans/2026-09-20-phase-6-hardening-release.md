# Phase 6: Hardening and v0.1.0 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the verified MVP operable day to day — `hivedispatch once <KEY>` to drive one ticket by hand, `hivedispatch status` to answer "what is in flight" from the state branch, log retention so the state branch does not grow without bound, a config reference, and a tagged `v0.1.0` with release binaries.

**Architecture:** The CLI's construction of tracker, workspaces, stores, host, executor and triager moves into one `newWorker` function that `run`, `once` and `status` share. `state.RunStore` gains `List` and `Prune`; the three stores implement them. A release workflow builds static binaries on tags.

**Tech Stack:** Go 1.27 stdlib, GitHub Actions (`softprops/action-gh-release`).

**Spec:** `docs/design-spec.md` — State in git ("Grows without bound. Needs a retention policy"), Observability ("what did the swarm do last night"), Reporting.

## Global Constraints

- No new external Go dependencies.
- `status` and `once` never poll; they do not touch the trigger JQL.
- Retention only ever deletes raw logs and finished runs older than the cutoff; `events.jsonl` (the audit trail) is kept.
- `go vet ./... && go test -race ./... && golangci-lint run` green before every commit; commit messages end with `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.

---

## File Structure

```
cmd/hivedispatch/wire.go            newWorker(ctx, cfg, opts, logger) — shared construction
cmd/hivedispatch/main.go            run/once/status use newWorker; version from ldflags
cmd/hivedispatch/status.go          status subcommand: table or -json
internal/state/state.go             + List, Prune on RunStore
internal/state/localdir/localdir.go + List, Prune (+ tests)
internal/state/gitbranch/gitbranch.go + List, Prune (+ tests)
internal/state/router/router.go     + List (merged), Prune (all)
internal/config/config.go           + RetentionDays (default 30)
.github/workflows/release.yml       tag v* → linux/darwin × amd64/arm64 binaries on a GitHub release
docs/config.md                      every config key, default, and meaning
README.md, docs/decisions.md
```

---

### Task 1: List and Prune on the stores

**Files:**
- Modify: `internal/state/state.go`, `internal/state/localdir/localdir.go` + test, `internal/state/gitbranch/gitbranch.go` + test, `internal/state/router/router.go` + test

**Interfaces:**
- Produces:

```go
// RunStore additions
List(ctx) ([]Run, error)                                  // every run record, any order
Prune(ctx, before time.Time) (removed int, err error)     // delete raw logs older than before, and run records at PhaseDone/UpdatedAt before; keep events.jsonl
```

- [ ] **Step 1: Tests**

Append to `internal/state/localdir/localdir_test.go`:

```go
func TestListAndPrune(t *testing.T) {
	s := New(t.TempDir())
	ctx := context.Background()
	old := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return old }
	if err := s.Save(ctx, &state.Run{Ticket: "HIVE-1", Phase: state.PhaseDone}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteLog(ctx, "HIVE-1", "run-old", "x"); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendLog(ctx, "HIVE-1", state.LogEntry{Event: "done"}); err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return old.Add(48 * time.Hour) }
	if err := s.Save(ctx, &state.Run{Ticket: "HIVE-2", Phase: state.PhaseWorking}); err != nil {
		t.Fatal(err)
	}
	runs, err := s.List(ctx)
	if err != nil || len(runs) != 2 {
		t.Fatalf("list = %v, %v", runs, err)
	}
	n, err := s.Prune(ctx, old.Add(24*time.Hour))
	if err != nil || n != 2 { // one log file + one finished run
		t.Fatalf("pruned %d, %v", n, err)
	}
	runs, _ = s.List(ctx)
	if len(runs) != 1 || runs[0].Ticket != "HIVE-2" {
		t.Errorf("after prune = %+v", runs)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "logs", "HIVE-1", "events.jsonl")); err != nil {
		t.Error("events.jsonl must survive pruning")
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "logs", "HIVE-1", "run-old.log")); err == nil {
		t.Error("old raw log should be gone")
	}
}
```

Append to `internal/state/gitbranch/gitbranch_test.go`:

```go
func TestListAndPruneCommitAndPush(t *testing.T) {
	remote := gittest.NewRemote(t)
	ctx := context.Background()
	base, dir := worker(t, remote)
	s, err := Open(ctx, base, dir)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return old }
	if err := s.Save(ctx, &state.Run{Ticket: "HIVE-1", Phase: state.PhaseDone}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteLog(ctx, "HIVE-1", "run-old", "x"); err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return old.Add(72 * time.Hour) }
	if err := s.Save(ctx, &state.Run{Ticket: "HIVE-2", Phase: state.PhaseWorking}); err != nil {
		t.Fatal(err)
	}
	if runs, err := s.List(ctx); err != nil || len(runs) != 2 {
		t.Fatalf("list = %v, %v", runs, err)
	}
	if n, err := s.Prune(ctx, old.Add(24*time.Hour)); err != nil || n != 2 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	if got := gittest.Git(t, remote, "ls-tree", "-r", "--name-only", Branch); strings.Contains(got, "run-old.log") || strings.Contains(got, "runs/HIVE-1.json") || !strings.Contains(got, "runs/HIVE-2.json") {
		t.Errorf("remote tree after prune:\n%s", got)
	}
}
```

(add `"time"` to the gitbranch test imports.)

Append to `internal/state/router/router_test.go`:

```go
func TestListMergesAndPruneSums(t *testing.T) {
	a, b := localdir.New(t.TempDir()), localdir.New(t.TempDir())
	r := &Store{Stores: map[string]state.RunStore{"HIVE": a, "OPS": b}}
	ctx := context.Background()
	old := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return old }
	b.Now = a.Now
	_ = r.Save(ctx, &state.Run{Ticket: "HIVE-1", Phase: state.PhaseDone})
	_ = r.Save(ctx, &state.Run{Ticket: "OPS-1", Phase: state.PhaseDone})
	if runs, _ := r.List(ctx); len(runs) != 2 {
		t.Errorf("list = %+v", runs)
	}
	if n, err := r.Prune(ctx, old.Add(time.Hour)); err != nil || n != 2 {
		t.Errorf("pruned %d, %v", n, err)
	}
}
```

(add `"time"` to the router test imports.)

- [ ] **Step 2: Interface**

In `internal/state/state.go` add to `RunStore`:

```go
	// List returns every run record.
	List(ctx context.Context) ([]Run, error)
	// Prune deletes raw logs older than before and finished runs
	// (PhaseDone) not updated since before. events.jsonl is kept. It
	// returns how many files were removed.
	Prune(ctx context.Context, before time.Time) (int, error)
```

- [ ] **Step 3: localdir**

```go
// List implements state.RunStore.
func (s *Store) List(_ context.Context) ([]state.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return listRuns(filepath.Join(s.Dir, "runs"))
}

// Prune implements state.RunStore.
func (s *Store) Prune(_ context.Context, before time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed, _, err := pruneTree(s.Dir, before)
	return removed, err
}
```

and, in a new file `internal/state/localdir/tree.go` (package `localdir`, exported so gitbranch can reuse it — gitbranch imports localdir):

```go
package localdir

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/state"
)

// ListRuns reads every runs/<KEY>.json under dir.
func ListRuns(dir string) ([]state.Run, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "runs"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []state.Run
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, "runs", e.Name()))
		if err != nil {
			return nil, err
		}
		var r state.Run
		if json.Unmarshal(raw, &r) == nil {
			out = append(out, r)
		}
	}
	return out, nil
}

// PruneTree removes raw logs older than before and finished runs not
// updated since before. It returns the number of files removed and their
// paths relative to dir.
func PruneTree(dir string, before time.Time) (int, []string, error) {
	var removed []string
	logs := filepath.Join(dir, "logs")
	_ = filepath.WalkDir(logs, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".log") {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.ModTime().Before(before) {
			return nil
		}
		if os.Remove(p) == nil {
			rel, _ := filepath.Rel(dir, p)
			removed = append(removed, rel)
		}
		return nil
	})
	runs, err := ListRuns(dir)
	if err != nil {
		return len(removed), removed, err
	}
	for _, r := range runs {
		if r.Phase == state.PhaseDone && r.UpdatedAt.Before(before) {
			p := filepath.Join(dir, "runs", r.Ticket+".json")
			if os.Remove(p) == nil {
				removed = append(removed, filepath.Join("runs", r.Ticket+".json"))
			}
		}
	}
	return len(removed), removed, nil
}
```

Use `ListRuns`/`PruneTree` from `List`/`Prune` (rename the lowercase calls above accordingly). Raw logs are aged by mtime; `WriteLog` in tests must therefore set the file's mtime to `s.Now()` — add `os.Chtimes(p, now, now)` after `os.WriteFile` in `WriteLog` (both stores).

- [ ] **Step 4: gitbranch**

```go
// List implements state.RunStore.
func (s *Store) List(_ context.Context) ([]state.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return localdir.ListRuns(s.Dir)
}

// Prune implements state.RunStore and pushes the deletions.
func (s *Store) Prune(ctx context.Context, before time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, removed, err := localdir.PruneTree(s.Dir, before)
	if err != nil || n == 0 {
		return n, err
	}
	return n, s.commitAndPush(ctx, fmt.Sprintf("hive: prune %d file(s) before %s", n, before.UTC().Format("2006-01-02")), removed)
}
```

Note `commitAndPush` does `git add -A`, which stages deletions. In `gitbranch.WriteLog` add the `os.Chtimes` call as above (git does not preserve mtimes, so on a fresh clone every log looks new; that errs on keeping, which is the safe side — say so in a comment).

- [ ] **Step 5: router**

```go
// List implements state.RunStore by concatenating every store.
func (s *Store) List(ctx context.Context) ([]state.Run, error) {
	var out []state.Run
	for _, st := range s.Stores {
		runs, err := st.List(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, runs...)
	}
	return out, nil
}

// Prune implements state.RunStore across every store.
func (s *Store) Prune(ctx context.Context, before time.Time) (int, error) {
	total := 0
	for _, st := range s.Stores {
		n, err := st.Prune(ctx, before)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
```

- [ ] **Step 6: Run, lint, commit**

```bash
go vet ./... && go test -race ./internal/state/... && ~/go/bin/golangci-lint run ./...
git add internal/state
git commit -m "feat(state): List and Prune on every store; raw logs aged by mtime, events kept

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 2: Shared worker construction

**Files:**
- Create: `cmd/hivedispatch/wire.go`
- Modify: `cmd/hivedispatch/main.go` (`runRun` uses it), `internal/config/config.go` (+ `RetentionDays int` default 30, validation ≥ 0), `internal/config/config_test.go`

**Interfaces:**
- Produces:

```go
type wireOptions struct { executor, triage string; placeholder bool; preflight bool }
type worker struct {
    cfg *config.Config; tracker *jira.Client; ws *gitws.Workspaces
    store state.RunStore; host githost.GitHost; d *dispatch.Dispatcher
}
func newWorker(ctx context.Context, cfg *config.Config, opts wireOptions, logger *slog.Logger, stdout, stderr io.Writer) (*worker, error)
```

`newWorker` does exactly what `runRun` does today between `config.Load` and the dispatcher literal, including `ResolveFields`, the preflight (when `opts.preflight`), base clones, stores, token discovery, executor and triager selection. `runRun` shrinks to flags → `config.Load` → status line → `newWorker` → retention prune (`w.store.Prune(ctx, now-RetentionDays)`, logged) → signals → `Once`/`Run`.

- [ ] **Step 1: Config**

Add `RetentionDays int \`yaml:"retention_days"\`` (default 30; `0` disables; negative rejected) and a test `TestLoadRetentionDefault`.

- [ ] **Step 2: Extract**

Move the code; keep behaviour identical. Existing `cmd` tests must still pass unchanged.

- [ ] **Step 3: Prune at startup**

In `runRun`, after `newWorker`:

```go
	if cfg.RetentionDays > 0 {
		cutoff := time.Now().AddDate(0, 0, -cfg.RetentionDays)
		if n, err := w.store.Prune(ctx, cutoff); err != nil {
			logger.Warn("retention prune failed", "err", err)
		} else if n > 0 {
			logger.Info("retention", "removed", n, "before", cutoff.Format("2006-01-02"))
		}
	}
```

- [ ] **Step 4: Run, lint, commit**

```bash
go vet ./... && go test -race ./... && ~/go/bin/golangci-lint run ./... && go build -o bin/hivedispatch ./cmd/hivedispatch
git add -A
git commit -m "refactor(cli): newWorker shared construction; retention prune at startup

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 3: `once <KEY>` and `status`

**Files:**
- Create: `cmd/hivedispatch/status.go`
- Modify: `cmd/hivedispatch/main.go`, `cmd/hivedispatch/main_test.go`

**Behaviour:**
- `hivedispatch once KEY [-config P] [-executor …] [-triage …] [-placeholder] [-skip-preflight]` — fetches the ticket with `tracker.Get`, calls `d.Handle` directly (no schedule, no JQL), prints the outcome, exit 0 on any handled outcome, 1 on error. A key whose project is not configured is rejected before contacting Jira.
- `hivedispatch status [-config P] [-json]` — opens the stores (no Jira preflight, no token discovery, no executor) and prints one line per run sorted by `UpdatedAt` desc:

```
TICKET    PHASE      STATUS       ATTEMPTS  AGENT     UPDATED           PR
SCRUM-7   done       completed    2         worker-1  2026-09-20 03:41  https://github.com/…/pull/2
SCRUM-5   done       completed    1         worker-1  2026-09-20 02:12  https://github.com/…/pull/1
```

`-json` emits the `[]state.Run` array. With no runs: `no runs recorded`.

- [ ] **Step 1: Tests**

```go
func TestOnceRejectsUnknownProjectBeforeNetwork(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	p := writeValidConfig(t) // helper: the YAML from TestCheckValidConfig
	var out, errb bytes.Buffer
	if code := run([]string{"once", "NOPE-1", "-config", p}, &out, &errb); code != 1 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "NOPE") || !strings.Contains(errb.String(), "jira_project") {
		t.Errorf("stderr = %q", errb.String())
	}
}

func TestStatusWithLocalStore(t *testing.T) {
	t.Setenv("HIVE_JIRA_TOKEN", "secret")
	home := t.TempDir()
	t.Setenv("HOME", home)
	p := writeValidConfigWith(t, "state_store: local\nworkroot: "+home+"/work\n")
	st := localdir.New(filepath.Join(home, "work", "state", "o__r"))
	if err := st.Save(context.Background(), &state.Run{Ticket: "X-1", Phase: state.PhaseDone, LastStatus: "completed", Attempts: 1, Agent: "w", PRURL: "https://x/pull/1"}); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"status", "-config", p}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, want := range []string{"X-1", "done", "completed", "https://x/pull/1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout missing %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if code := run([]string{"status", "-config", p, "-json"}, &out, &errb); code != 0 || !strings.HasPrefix(strings.TrimSpace(out.String()), "[") {
		t.Errorf("json: exit %d out=%q", code, out.String())
	}
}
```

`writeValidConfig`/`writeValidConfigWith` are small helpers in `main_test.go` writing the YAML from `TestCheckValidConfig` (with `jira_project: X`, `name: o/r`) plus any extra lines. `status` with `state_store: local` must not need git or Jira, so `newWorker` gets a `storesOnly` path: `openStores(cfg)` is split out and `status` calls only that (for `branch` stores it still needs `EnsureBase` — acceptable; the test uses `local`).

- [ ] **Step 2: Implement** `runOnce` in `main.go` and `runStatus` in `status.go` (use `text/tabwriter` for the table; `encoding/json` for `-json`). Update `usage`.

- [ ] **Step 3: Run, lint, commit**

```bash
go vet ./... && go test -race ./... && ~/go/bin/golangci-lint run ./... && go build -o bin/hivedispatch ./cmd/hivedispatch && ./bin/hivedispatch status
git add -A
git commit -m "feat(cli): once <KEY> drives one ticket by hand; status lists runs from the state branch

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 4: Docs, release workflow, v0.1.0

**Files:**
- Create: `docs/config.md`, `.github/workflows/release.yml`
- Modify: `README.md`, `docs/decisions.md`, `cmd/hivedispatch/main.go` (version already via ldflags)

- [ ] **Step 1: `docs/config.md`** — every worker config key with type, default, and one-line meaning, generated by hand from `config.Config`; plus the `.hivedispatch.yaml` repo policy keys; plus the environment variables (`HIVE_JIRA_TOKEN`, `HIVE_GITHUB_TOKEN`).

- [ ] **Step 2: `.github/workflows/release.yml`**

```yaml
name: release
on:
  push:
    tags: ["v*"]
permissions:
  contents: write
jobs:
  build:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        include:
          - { goos: linux,  goarch: amd64 }
          - { goos: linux,  goarch: arm64 }
          - { goos: darwin, goarch: amd64 }
          - { goos: darwin, goarch: arm64 }
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: |
          CGO_ENABLED=0 GOOS=${{ matrix.goos }} GOARCH=${{ matrix.goarch }} \
            go build -trimpath -ldflags "-s -w -X main.version=${GITHUB_REF_NAME}" \
            -o hivedispatch-${{ matrix.goos }}-${{ matrix.goarch }} ./cmd/hivedispatch
      - uses: actions/upload-artifact@v4
        with:
          name: hivedispatch-${{ matrix.goos }}-${{ matrix.goarch }}
          path: hivedispatch-${{ matrix.goos }}-${{ matrix.goarch }}
  release:
    needs: build
    runs-on: ubuntu-latest
    steps:
      - uses: actions/download-artifact@v4
        with:
          merge-multiple: true
      - uses: softprops/action-gh-release@v2
        with:
          files: hivedispatch-*
          generate_release_notes: true
```

- [ ] **Step 3: README** — status line → "**v0.1.0.** …"; add `once`, `status` to the quick start; link `docs/config.md`; add an "Install from a release" line.

- [ ] **Step 4: decisions** — retention by mtime (safe side on fresh clones), `status` reads local state (no Jira), `once` bypasses schedule and JQL by design.

- [ ] **Step 5: Commit, tag, push**

```bash
go vet ./... && go test -race ./... && ~/go/bin/golangci-lint run ./...
git add -A && git commit -m "docs: config reference; release workflow; README for v0.1.0

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
git tag -a v0.1.0 -m "HiveDispatch v0.1.0 — single-worker MVP: Jira → triage → Claude Code → PR"
git push && git push origin v0.1.0
```

Then watch https://github.com/thomasmeadows/HiveDispatch/actions for the release job and confirm four binaries on the release page.

---

## Self-review

**Spec coverage:** retention policy ✔ (Task 1–2); "what's in flight" from the state branch ✔ (`status`, Task 3); a way to drive a single ticket ✔ (`once`); release ✔ (Task 4). Not in scope: reporting rollups, review capability, multi-worker — post-MVP.

**Type consistency:** `RunStore.List/Prune` (Task 1) used by `status` and the startup prune (Tasks 2–3); `localdir.ListRuns/PruneTree` shared by gitbranch; `newWorker`/`openStores` (Task 2) used by `run`, `once`, `status` (Task 3).

**Placeholder scan:** none.
