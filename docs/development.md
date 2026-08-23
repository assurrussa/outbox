# Development

## Prerequisites

- Go workspace support with the repository's `go.work`.
- Docker for MySQL, Postgres, and Picodata integration services.
- Make targets are the canonical command surface.
- Optional formatting/lint tools used by `make fmt` and `make lint`:
  `gofumpt`, `gci`, and `golangci-lint`.

The root `go.mod` currently declares `go 1.26`, and every local command should
be treated as workspace-aware unless a release check explicitly sets
`GOWORK=off`.

## Fast Checks

From the repository root:

```sh
make test-core
make test-backends
make test
```

`make test-core` runs `go test ./...` in the core workspace context.
`make test-backends` iterates each backend module and runs `go test ./...`.

## Full Local Check

```sh
make check
```

This source-read-only target verifies formatting, vet, and lint, then runs one
race+coverage traversal for core and one normal traversal for each backend.
Run `make prepare` for generation, formatting, and lint fixes. Repeated core
race stress and HTML coverage are explicit `make test-race-core` and
`make cover-html` diagnostics.

## Integration Services

The Makefile exports safe local integration defaults. Copy the template only
when you need to override them:

```sh
cp .env.example .env
```

Start services:

```sh
make devup
```

Stop services:

```sh
make devdown
```

`make devup` uses Docker Compose profiles for MySQL, Postgres, and Picodata.
It waits for all three services before returning. SQLite tests do not need
Docker.

## Integration Tests

All integration tests:

```sh
make test-integration-all
```

Focused integration tests:

```sh
make test-integration-mysql
make test-integration-sqlite
make test-integration-pgsql
make test-integration-picodata
```

The integration config is defined in `shared/tests/config.go` and uses
`TEST_OUTBOXLIB_*` env variables. Defaults are mirrored by `.env.example` and
`compose.yml`.

Use the Make target for Picodata rather than a raw parallel `go test ./...`:
the target sets `-p 1` so separate package test binaries cannot race on
distributed DDL.

The default Picodata test password and DSN are local-only values mirrored in
`.env.example`; do not reuse them outside the integration stack.

## Examples

Core-only in-memory example:

```sh
cd examples/base-app
go run .
```

SQLite example:

```sh
cd examples/base-app-sqlite
go run .
```

MySQL/Postgres/Picodata examples require matching compose services first. See
the backend-specific example README files for exact env and DSN behavior.

## Release-Oriented Checks

Pin all backend modules to a published core version and refresh their sums:

```sh
make release-ready-backends CORE_VERSION=v0.11.0
```

This is the only mutating backend release-preparation command. It updates all
four backend `go.mod` and `go.sum` pairs in one release change.

Verify backend modules as standalone modules:

```sh
make release-verify-backends
```

`release-verify-backends` intentionally uses `GOWORK=off` inside backend
modules. Use this before claiming backend modules are consumable outside the
local workspace.

For a non-mutating pre-tag gate that also proves every backend resolves the
exact core tag:

```sh
make release-readiness-backends CORE_VERSION=v0.11.0
```

## Sandbox Note

In restricted Codex environments, the default Go build cache may point outside
the writable workspace and fail with `operation not permitted`. Use a repo-local
build cache for ad hoc checks:

```sh
mkdir -p tmp/gocache
GOCACHE=$PWD/tmp/gocache go test ./...
```

If `go list` or `go test` also needs a writable module cache, keep
`GOMODCACHE` outside the repository tree, for example under
`${TMPDIR:-/tmp}/outbox-gomodcache`.
Do not set `GOMODCACHE=$PWD/tmp/gomodcache`: `go test ./...` will traverse that
downloaded module tree and may start testing third-party packages.

Do not commit `tmp/`; it is ignored.
