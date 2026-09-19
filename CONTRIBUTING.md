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
