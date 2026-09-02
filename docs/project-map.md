# Project Map

This repository is the local source of truth for the Go outbox library
`github.com/assurrussa/outbox`.

## Purpose

The project provides a core outbox runtime that accepts jobs, reserves available
jobs for workers, executes registered handlers, deletes successful jobs, and
moves supported exhausted/permanent jobs to a failed-jobs store. Unregistered
name/schema pairs remain pending. Storage is pluggable through backend modules.

## Modules

Core module:
- `github.com/assurrussa/outbox`

Backend modules:
- `github.com/assurrussa/outbox/backends/mysql`
- `github.com/assurrussa/outbox/backends/sqlite`
- `github.com/assurrussa/outbox/backends/pgsql`
- `github.com/assurrussa/outbox/backends/picodata`

Workspace modules also include runnable examples:
- `examples/base-app`
- `examples/base-app-mysql`
- `examples/base-app-sqlite`
- `examples/base-app-pgsql`
- `examples/base-app-picodata`

## Directory Responsibilities

- `outbox/`: public core runtime, one version-aware fenced batch repository
  contract, durable fan-out snapshots/deliveries, options, job context helpers,
  logger adapter, models, and small payload helpers.
- `shared/`: support code reused by core and backend modules. It is internal to
  this repository's modules and is not a stable external consumer API.
- `backends/mysql/`: MySQL storage, repositories, transaction manager, and
  migrations.
- `backends/sqlite/`: SQLite storage, repositories, transaction manager, and
  migrations.
- `backends/pgsql/`: Postgres storage, repositories, transaction manager,
  migrations, and pgx-backed client code.
- `backends/picodata/`: Picodata storage, repositories, migrations, deploy-env
  helper, and transaction adapter.
- `examples/`: runnable consumer wiring for the core and each backend.
- `tools/toolsmocks/`: local mock generation tool used by `go generate`.
- `docker/`: local integration test initialization assets.
- `docs/performance.md`: cross-repository integration capacity evidence and
  the boundary between a confirmed checkout-workspace result and production
  acceptance.
- `MIGRATION.md`: short consumer note for the removed `infrastructure/*`
  import paths.

## Public Import Surfaces

Consumers normally import:

```go
import "github.com/assurrussa/outbox/outbox"
```

Then they choose one backend module and import only that backend's storage,
repositories, migrator, and transaction packages.

Old `infrastructure/*` import paths were removed and should not be restored.
Use `MIGRATION.md` when updating consumers from the old layout.

## Dependency Boundary

The core module must not import database drivers. Backend-specific driver
dependencies belong in backend modules. `shared/tests/dependency_isolation_test.go`
checks this boundary with `GOWORK=off`.

Current driver ownership:
- MySQL backend: `github.com/go-sql-driver/mysql`
- SQLite backend: `modernc.org/sqlite`
- Postgres backend: `github.com/jackc/pgx/v5`
- Picodata backend: `github.com/picodata/picodata-go`

Version-aware fenced batch storage is required and backend-owned. PostgreSQL,
MySQL, and SQLite support limits through `1000` plus optional durable fan-out.
Picodata implements the same contract for a one-element batch and does not
claim atomic fan-out support.

## Shared Wiki Context

Shared platform context is in:

```text
${AGENT_CONTEXT_ROOT}/streams/wiki/platforms/outbox.md
```

Use it for cross-project role and durable platform context. Local code, README
files, tests, and this `docs/` directory win for concrete commands, APIs,
config keys, migrations, and release gates.
