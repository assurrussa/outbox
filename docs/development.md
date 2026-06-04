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

This target runs generation, formatting, vet, lint, core/backend tests,
race tests for core, and HTML coverage generation.

## Integration Services

Create local env from the template when needed:

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
SQLite tests do not need Docker.

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

Picodata local compose requires:

```sh
TEST_OUTBOXLIB_PICODATA_ADMIN_PASSWORD=passWord!123
```

The template `.env.example` already contains the local test value.

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

Pin backend modules to a published core version:

```sh
make release-ready-backends CORE_VERSION=v0.9.0
```

Verify backend modules as standalone modules:

```sh
make release-verify-backends
```

`release-verify-backends` intentionally uses `GOWORK=off` inside backend
modules. Use this before claiming backend modules are consumable outside the
local workspace.

## Sandbox Note

In restricted Codex environments, the default Go build cache may point outside
the writable workspace and fail with `operation not permitted`. Use a repo-local
build cache for ad hoc checks:

```sh
mkdir -p tmp/gocache
GOCACHE=$PWD/tmp/gocache go test ./...
```

If `go list` or `go test` also needs a writable module cache, keep
`GOMODCACHE` outside the repository tree, for example under `/private/tmp`.
Do not set `GOMODCACHE=$PWD/tmp/gomodcache`: `go test ./...` will traverse that
downloaded module tree and may start testing third-party packages.

Do not commit `tmp/`; it is ignored.
