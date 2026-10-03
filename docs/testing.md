# Testing guide

## Table of contents

- [Testing approach and checks](#testing-approach-and-checks)
- [Test suites](#test-suites)
- [Fixtures and subprocess helpers](#fixtures-and-subprocess-helpers)
- [internal/command-line-interfaces/claudecli/clitest](#package-internal-command-line-interfaces-claudecli-clitest)
- [internal/command-line-interfaces/codexcli/codextest](#package-internal-command-line-interfaces-codexcli-codextest)
- [internal/executor/fake](#package-internal-executor-fake)
- [internal/githost/fake](#package-internal-githost-fake)
- [internal/gitops/fake](#package-internal-gitops-fake)
- [internal/gitops/git/gittest](#package-internal-gitops-git-gittest)
- [internal/supervisor/model/fake](#package-internal-supervisor-model-fake)
- [internal/trace/fake](#package-internal-trace-fake)
- [internal/tracker/fake](#package-internal-tracker-fake)
- [internal/triage/fake](#package-internal-triage-fake)
- [Update protocol](#update-protocol)

## Testing approach and checks

This companion to the [runtime package guide](packages.md) covers fake implementations, reusable test-helper packages, test suites and fixtures. Tests live next to Go implementations as `*_test.go`; Python tests live in `graph/tests/`. Go test files are summarized by suite rather than listed individually. Fake packages are importable Go code, even when only tests use them; `internal/executor/fake` also supports CLI dry runs.

External boundaries use fakes or local test servers/processes: tests do not require live service credentials. Git integration tests use temporary local repositories. Python uses fake chat models and a fake `agent-run`. See [CONTRIBUTING.md](../CONTRIBUTING.md#before-every-commit) for the authoritative verification requirements:

```sh
go vet ./...
go test -race ./...
golangci-lint run ./...
```

For Python changes, also run `graph/.venv/bin/ruff check graph`, `graph/.venv/bin/ruff format --check graph`, and `graph/.venv/bin/pytest graph`. Frontend changes require rebuilding `internal/web/dist`; CI checks that the committed build matches the source.

## Test suites

Each link opens the source directory containing the tests. File names and test functions describe the individual cases.

| Suite | What it verifies |
| --- | --- |
| [cmd/hivedispatch](../cmd/hivedispatch/) | Command behavior, agent-run JSON bridge and tracing setup. |
| [config](../internal/config/), [discover](../internal/discover/), [repoconfig](../internal/repoconfig/) | Configuration/defaults, agent validation, repository discovery and worktree policy. |
| [dispatch](../internal/dispatch/) | Ticket lifecycle, role routing, reporting, retries, concurrent claims/slots/agents and tracing. |
| [executor adapters](../internal/executor/) | Invocation options, policy, result mapping, resume behavior and supported roles. |
| [CLI runners](../internal/command-line-interfaces/) | Stream/session parsing, process cancellation, budgets, output bounds and fake-binary invocations. |
| [githost](../internal/githost/) | PR hosting, credential discovery and fake/no-credential behavior. |
| [gitops](../internal/gitops/) | Local Git worktrees, finalization and shared-repository locking. |
| [state](../internal/state/) | Directory/branch persistence, routing, pruning and push-conflict handling. |
| [tracker](../internal/tracker/) | Naming, native ticket conversion, claims/heartbeats, transitions, routing and tracker administration. |
| [triage](../internal/triage/) | Read-only Claude decisions and structured verdict validation. |
| [prompt](../internal/prompt/), [schedule](../internal/schedule/), [statusline](../internal/statusline/) | Prompt context, run windows and terminal rendering. |
| [supervisor](../internal/supervisor/) | Agent/tool/session loops, configuration, prompts, memory, terminal interrupts and provider protocols. |
| [trace](../internal/trace/) | Span hierarchy, usage/data handling and LangSmith export. |
| [web](../internal/web/) | HTTP/API guards, configuration operations, executor setup and Vite integration. |
| [yamlfile](../internal/yamlfile/) | Comment-preserving edits, staged validation, backups and diffs. |
| [graph/tests](../graph/tests/) | Workflow routing, checks/review loops, checkpoints/resume, shutdown, events, CLI bridge and built-in coder tools/agent. |

**Typical flow:** arrange a temporary workspace and scripted boundary → invoke the real package under test → assert state/output/calls → let test cleanup remove temporary resources. Concurrent Go tests run under the race detector. Python workflow tests inject fake models and subprocess results rather than calling providers.

## Fixtures and subprocess helpers

| Location | Responsibility and flow |
| --- | --- |
| [claudecli/clitest](../internal/command-line-interfaces/claudecli/clitest/) | fakeclaude.sh plus JSONL success/question/error streams; helper setup selects a mode, runner invokes the script, tests inspect captured arguments/input. |
| [codexcli/codextest](../internal/command-line-interfaces/codexcli/codextest/) | fakecodex.sh and native JSONL fixtures; exercises the Codex runner through a real local process. |
| [deepcodecli/deepcodetest](../internal/command-line-interfaces/deepcodecli/deepcodetest/) | fakedeepcode.sh, session_success.jsonl and index_success.json simulate stdout plus on-disk DeepCode sessions. This is a fixture directory, not a Go package. |
| [graphcli/graphtest](../internal/command-line-interfaces/graphcli/graphtest/) | fakegraph.sh simulates hivegraph events and process behavior; not a Go package. |
| [triage/claudecode/testdata](../internal/triage/claudecode/testdata/) | Dispatch, needs-info and invalid structured-decision streams replayed through the Claude helper. |
| [tracker/jira/testdata](../internal/tracker/jira/testdata/) | Issue and search-page JSON responses for normalization and pagination tests. |
| [graph/tests/conftest.py](../graph/tests/conftest.py) | Shared Python test fixtures. |
| [graph/tests/fake_agent_run.py](../graph/tests/fake_agent_run.py) | Fake agent-run subprocess for Python bridge/workflow tests. |

## Fake and helper packages

<a id="package-internal-command-line-interfaces-claudecli-clitest"></a>

## internal/command-line-interfaces/claudecli/clitest

Reusable test-support package for a fake Claude executable; not a production runner.

**Files (excluding tests)**

| File | Responsibility |
| --- | --- |
| [clitest.go](../internal/command-line-interfaces/claudecli/clitest/clitest.go) | Configures the fake executable and fixture environment for callers. Shell scripts and JSON fixtures are intentionally excluded from this guide. |

**Flow:** A test selects a fake mode or fixture → Setup exposes the executable and captured argv/stdin locations → the production runner invokes it → the test inspects the captured invocation.

[Back to contents](#table-of-contents)


<a id="package-internal-command-line-interfaces-codexcli-codextest"></a>

## internal/command-line-interfaces/codexcli/codextest

Reusable test-support package for a fake Codex executable; not a production runner.

**Files (excluding tests)**

| File | Responsibility |
| --- | --- |
| [codextest.go](../internal/command-line-interfaces/codexcli/codextest/codextest.go) | Configures the fake executable and fixture environment for callers. Shell scripts and JSON fixtures are intentionally excluded from this guide. |

**Flow:** A test selects a fake mode or fixture → Setup exposes the executable and captured argv/stdin locations → the production runner invokes it → the test inspects the captured invocation.

[Back to contents](#table-of-contents)


<a id="package-internal-executor-fake"></a>

## internal/executor/fake

Provides a scripted executor for tests and dry runs.

**Files (excluding tests)**

| File | Responsibility |
| --- | --- |
| [fake.go](../internal/executor/fake/fake.go) | Scripted planning/advice/run behavior, optional workspace changes and active/peak call accounting. |

**Flow:** Configure results/advice or placeholder edits → invoke the normal executor methods → record calls and concurrency → return scripted results without a model or external CLI.

[Back to contents](#table-of-contents)


<a id="package-internal-githost-fake"></a>

## internal/githost/fake

Provides an in-memory pull-request host for tests.

**Files (excluding tests)**

| File | Responsibility |
| --- | --- |
| [fake.go](../internal/githost/fake/fake.go) | Thread-safe fake PR storage, configurable head SHAs and recorded operations. |

**Flow:** Seed PR heads as needed → find/open/review through GitHost → inspect recorded requests and review bodies.

[Back to contents](#table-of-contents)


<a id="package-internal-gitops-fake"></a>

## internal/gitops/fake

Provides directory-backed workspaces without invoking Git.

**Files (excluding tests)**

| File | Responsibility |
| --- | --- |
| [fake.go](../internal/gitops/fake/fake.go) | Fake Prepare, Finalize and Diff operations and finalized-ticket records. |

**Flow:** Prepare a ticket directory → executor can write files → return the configured diff and record finalization for assertions.

[Back to contents](#table-of-contents)


<a id="package-internal-gitops-git-gittest"></a>

## internal/gitops/git/gittest

Reusable test-support package that builds disposable local Git repositories.

**Files (excluding tests)**

| File | Responsibility |
| --- | --- |
| [gittest.go](../internal/gitops/git/gittest/gittest.go) | Test helpers for Git environment, commands, temporary remotes and clones. |

**Flow:** Set an isolated Git identity/environment → create a local bare remote and clone → run Git commands against those directories for integration assertions without a network.

[Back to contents](#table-of-contents)


<a id="package-internal-supervisor-model-fake"></a>

## internal/supervisor/model/fake

Supplies scripted model responses without a provider call.

**Files (excluding tests)**

| File | Responsibility |
| --- | --- |
| [fake.go](../internal/supervisor/model/fake/fake.go) | Scripted Model plus text and tool-call response constructors. |

**Flow:** Queue text/tool responses → record each Chat request → return the next response or a one-shot configured failure; exhausted scripts end the turn.

[Back to contents](#table-of-contents)


<a id="package-internal-trace-fake"></a>

## internal/trace/fake

Captures trace exports in memory for assertions.

**Files (excluding tests)**

| File | Responsibility |
| --- | --- |
| [fake.go](../internal/trace/fake/fake.go) | In-memory trace exporter and recorded-run access. |

**Flow:** Receive span starts/ends through Exporter → retain records → let tests inspect the resulting trace tree without HTTP.

[Back to contents](#table-of-contents)


<a id="package-internal-tracker-fake"></a>

## internal/tracker/fake

Provides an in-memory tracker for deterministic lifecycle and race tests.

**Files (excluding tests)**

| File | Responsibility |
| --- | --- |
| [fake.go](../internal/tracker/fake/fake.go) | Concurrent fake tracker with ticket storage and claim/lifecycle operations. |

**Flow:** Seed tickets → poll/read through Tracker → apply claims, comments and state changes in memory → inspect recorded state without a network.

[Back to contents](#table-of-contents)


<a id="package-internal-triage-fake"></a>

## internal/triage/fake

Provides scripted triage decisions for tests.

**Files (excluding tests)**

| File | Responsibility |
| --- | --- |
| [fake.go](../internal/triage/fake/fake.go) | Fake triager, default decision and call recording. |

**Flow:** Configure decisions keyed by ticket → record Decide inputs → return the script or the default dispatch decision.

[Back to contents](#table-of-contents)

## Update protocol

Follow [CONTRIBUTING.md — Package guide maintenance](../CONTRIBUTING.md#package-guide-maintenance) and the [package guide update protocol](packages.md#update-protocol).

- Add new fake/helper packages here with a TOC link, purpose, source-file table and operation flow.
- Update a package's file table when its helper source is added, renamed, moved or removed.
- Add/update suite and fixture rows when new test areas, support files or fixture directories appear. Individual test cases need not become separate inventory rows; keep each suite's coverage description accurate.
- Keep runtime packages in packages.md; update both documents when responsibilities move between runtime and testing code.
- Check anchors/source links and reconcile the two guides against the repository in the same commit as the code change.
