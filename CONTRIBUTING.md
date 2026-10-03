# Contributing

## Dev setup

- Go 1.27+
- `golangci-lint` v2: `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest`, and make sure `$(go env GOPATH)/bin` is on your `PATH` — the coding agent inherits the worker's environment, so if you cannot run `golangci-lint` in your shell, neither can it

## Build and test locally

```sh
go build ./...                    # compile every package
go build -o hivedispatch ./cmd/hivedispatch && ./hivedispatch -h   # build and run the CLI
go test -race ./...               # run all tests with the race detector
go test -race -run TestName ./internal/pkg   # one test in one package
```

### The web UI

`hivedispatch website` serves a Vue 3 app from `web/`. It has two modes, and in both the browser only talks to the Go server, so everything in front of the UI (the host and origin checks, and the `HIVE_WEBSITE_PASSWORD` gate when it is set) applies to both:

- **Production:** `hivedispatch website` serves the Vite build that is committed to `internal/web/dist` and embedded in the binary. `go build` never needs Node.
- **Development:** `hivedispatch website -dev` starts Vite's dev server itself, on a private loopback port, and proxies every non-API request to it, including the hot-reload websocket. Edit a `.vue` file and the page updates in place. Run it anywhere inside the checkout (it finds `web/`), or pass `-web DIR`. It needs Node 22+ and `npm ci` in `web/` once.

```sh
cd web && npm ci && cd ..
go run ./cmd/hivedispatch website -dev   # hot-reloading UI on http://localhost:7878
cd web && npm run build                  # when done: rewrites internal/web/dist; commit it with the source change
```

CI rebuilds the UI and fails if `internal/web/dist` differs from what is committed.

### The graph workflow (Python)

`executor: langgraph` agents run `hivegraph`, a LangGraph workflow in the Python package `graph/` (Python 3.11+). Its dependencies live only in `graph/pyproject.toml`; the Go module stays stdlib plus yaml.v3. Work on it in a venv:

```sh
python3 -m venv graph/.venv && graph/.venv/bin/pip install -e './graph[dev]'
graph/.venv/bin/pytest graph          # tests use fake chat models and a fake agent-run
```

Unit tests are hermetic: every external system has a fake under `internal/` (and in `graph/tests/`), so no credentials or network are needed to run the suite.

## Before every commit

```sh
go vet ./... && go test -race ./... && golangci-lint run
```

When `graph/` changed, also:

```sh
graph/.venv/bin/ruff check graph && graph/.venv/bin/ruff format --check graph && graph/.venv/bin/pytest graph
```

## Package guide maintenance

Keep [docs/packages.md](docs/packages.md) and [docs/testing.md](docs/testing.md)
current in the same commit as the code they describe. The package guide covers
runtime Go/Python packages and the frontend; the testing guide covers fakes,
test-helper packages, suites and fixtures.

- When adding a package, add its linked table-of-contents entry, purpose,
  non-test source-file table and operation flow to the appropriate guide.
- When adding, renaming, moving or removing a source file, update its owning
  package's file table and links. Put test support in the testing guide; summarize
  individual tests by suite. Describe generated output as a tree with its source
  and build process rather than listing changing asset hashes.
- Update flow descriptions when behavior or package boundaries change, even if
  the file list does not. Update affected callers' descriptions as well.
- Before committing, compare both guides with the package/source inventory,
  check table-of-contents anchors and file links, and run the normal checks above.
  Reviewers check guide coverage alongside code; mention relevant guide updates
  in the PR description.

See the [detailed update protocol](docs/packages.md#update-protocol) for inventory
commands and scope. These are contributor requirements, not an automated CI gate.

## Rules

- Every external system (tracker, git host, executor, model) sits behind an interface in `internal/` with a fake. Unit tests never touch the network.
- Design decisions go in `docs/decisions.md` with the alternative rejected. If you change a decision, add a new entry that supersedes the old one; do not edit history.
- Errors are never discarded: `errcheck` is on, including for `io.WriteString` and `Close`. If an error truly does not matter, write `_ = f()` so the choice is visible.
- Tests first. A task is not done until `go test -race ./...` passes.
- Conventional commit messages: `feat:`, `fix:`, `test:`, `docs:`, `chore:`.
