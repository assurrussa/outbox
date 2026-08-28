# Repository Guidelines

## Project Snapshot

This repository is a Go multi-module monorepo for `github.com/assurrussa/outbox`.
It provides the core outbox runtime plus storage backend modules for MySQL,
SQLite, Postgres, and Picodata.

Primary source-of-truth files:
- `README.md` for public consumer setup and release workflow.
- `MIGRATION.md` for the old import-path break and consumer migration notes.
- `go.work` for the local workspace module set.
- `Makefile` for canonical development, test, integration, and release checks.
- `outbox/contract.go`, `outbox/service.go`, `outbox/service_options.go`, and
  `outbox/api.go` for core runtime contracts.
- `backends/*/README.md`, `backends/*/contract.go`, and backend migrations for
  backend-specific contracts.
- `examples/*/README.md` for runnable consumer wiring.
- `docs/` for agent-oriented project notes created from verified local sources.

## Shared Agent Context

Use `$project-context-router` for tasks that need cross-project context from a
local shared wiki.

Do not hard-code machine-local absolute paths in this public repository. If a
local shared wiki is available, expose its root through `AGENT_CONTEXT_ROOT` or
let `$project-context-router` resolve it for the current session.

Local docs and code in this repository remain the source of truth for commands,
public APIs, config keys, supported imports, runtime behavior, and release gates.
Read this repo's `AGENTS.md`, `README.md`, `docs/`, backend READMEs, examples,
code, tests, and configs before shared wiki pages.

When shared context is available, read these paths from the resolved wiki root:
- `streams/wiki/index.md`
- `streams/wiki/glossary.md`
- `streams/wiki/platforms/outbox.md`

If local verified docs/code conflict with the shared wiki, treat the wiki as
stale. When the task includes documentation upkeep, update the matching shared
platform page with concise contract-focused wording; do not copy whole README
files into the wiki.

## Module Layout

- Core module: `github.com/assurrussa/outbox`
- Core import path for consumers: `github.com/assurrussa/outbox/outbox`
- Backend modules:
  - `github.com/assurrussa/outbox/backends/mysql`
  - `github.com/assurrussa/outbox/backends/sqlite`
  - `github.com/assurrussa/outbox/backends/pgsql`
  - `github.com/assurrussa/outbox/backends/picodata`
- Local examples:
  - `examples/base-app` uses in-memory stubs and no backend package.
  - `examples/base-app-mysql`
  - `examples/base-app-sqlite`
  - `examples/base-app-pgsql`
  - `examples/base-app-picodata`

Keep backend-specific driver dependencies out of the core module. The core
module should remain pure-interface from the point of view of DB drivers.

## Core Runtime Contract

`outbox.New(...)` validates required dependencies and options. Required caller
dependencies:
- `WithJobsRepo(...)`
- `WithJobsFailedRepo(...)`
- `WithTransactor(...)`

Optional:
- `WithLogger(...)` overrides the default logger.
- `WithJobsRepo(...)` auto-detects stats and unique-put extensions;
  `WithJobsStatRepo(...)` and `WithUniqueJobsRepo(...)` are split-composition
  overrides.
- Durable independent fan-out is opt-in through `WithFanoutJobsRepo(...)` and
  uses immutable event-time snapshots; the built-in dispatcher creates one
  fenced job per target.

Important behavior:
- Register jobs before `Service.Run(...)`; registering while running returns
  `ErrServiceRunning`.
- `Service.Run(...)` starts worker loops and returns `ErrServiceRunning` when
  called twice concurrently.
- Every worker uses exact `(name, schemaVersion)` fenced batch claim, including
  `limit=1`. Unsupported pairs, including unknown names, remain pending without
  attempts or automatic DLQ.
- Supported permanent or exhausted jobs move to versioned DLQ; ack, retry, and
  DLQ deletion require the current token.
- Fan-out event IDs and delivery IDs are idempotency boundaries. Do not prune
  PostgreSQL, MySQL, or SQLite idempotency tombstones until the host
  replay/audit retention has elapsed.
- Successful jobs are deleted from the jobs repository after `Handle(...)`
  succeeds.
- `JobIDFromContext(ctx)` exposes the current job ID while a handler executes.
- Option bounds are part of the public behavior: workers must be positive, idle time
  `100ms..10s`, reserve time `1s..10m`.

## Backend Contract

Each backend owns storage initialization, repositories, transaction manager, and
embedded migrations. Prefer backend README examples when wiring a real consumer.

Postgres, MySQL, and SQLite implement the complete required fenced batch
contract with maximum `1000`, optional fan-out, and standard runtime facades.
The MySQL runtime targets MySQL 8.0, matching the integration image. Picodata
implements the same repository contract with maximum `1`; it deliberately has
no fan-out repository or standard runtime because its current client cannot
provide the required atomic transaction boundary.

Migration convention:
- Use `RunEmbedded(..., WithCommand("up"))` for normal consumers.
- Filesystem migration mode exists for explicit migration-directory use cases.

Picodata-specific note:
- Picodata deployment is env-only.
- `PICODATA_CONFIG_FILE` and `cluster-storage*.yml` flows are not supported.
- Do not set both `PICODATA_LISTEN` and `PICODATA_IPROTO_LISTEN`.
- Do not set both `PICODATA_PG_ADVERTISE` and `PICODATA_IPROTO_ADVERTISE`.
- Do not use `0.0.0.0` as a client DSN host.
- Current Picodata transactor is best-effort because the client API does not
  provide connection-pinned SQL transactions.

## Commands

Use workspace-aware commands from the repository root:

```sh
make test-core
make test-backends
make test
make check
```

`make prepare` owns generation, formatting, and safe lint fixes. `make check`
is source-read-only and runs one race+coverage traversal for the core plus one
normal traversal per backend. Keep `make test-race-core` and `make cover-html`
for explicit stress/report diagnostics; do not stack them onto a successful
check on an unchanged tree.

Integration services:

```sh
make devup
make test-integration-all
make devdown
```

The Makefile provides safe local defaults. Copy `.env.example` only when local
overrides are needed.

Focused integration checks:

```sh
make test-integration-mysql
make test-integration-sqlite
make test-integration-pgsql
make test-integration-picodata
```

Release-oriented backend checks:

```sh
make release-readiness-backends CORE_VERSION=v0.12.0
```

Formatting and generated mocks:

```sh
make generate
make fmt
make lint
```

The Makefile exports ignored repository-local Go and linter caches. For ad hoc
commands outside Make, use the same local build-cache principle:

```sh
mkdir -p tmp/gocache
GOCACHE=$PWD/tmp/gocache go test ./...
```

If the module download cache is also restricted, keep `GOMODCACHE` outside the
repository tree. Do not put `GOMODCACHE` under `tmp/`, because `go test ./...`
will traverse downloaded dependency packages.

## Environment

`.env` is ignored and may contain local secrets. Do not read or copy real local
`.env` values into docs. Use `.env.example`, `compose.yml`, and
`shared/tests/config.go` for documented test defaults.

The compose profiles are `mysql`, `pgsql`, and `picodata`. SQLite integration
tests do not require Docker.

## Documentation Rules

Use `docs/project-map.md` for module ownership and source navigation,
`docs/development.md` for commands and local environment notes, and
`docs/contracts.md` for stable runtime/backend contracts.

When changing public APIs, config keys, module boundaries, backend migrations,
or release gates, update the relevant local docs in the same change. Keep docs
contract-focused; avoid copying full README examples or generated output.

Keep `implementation-notes.md` current during implementation work when decisions,
tradeoffs, sandbox constraints, or inferred project rules are not obvious from
the original request.

## Editing Discipline

- Preserve the multi-module boundary in `go.work` and backend `go.mod` files.
- Do not reintroduce old `infrastructure/*` import paths.
- Treat `shared/*` as internal/unstable for external consumers.
- Prefer small, focused docs updates over broad rewrites.
- Do not edit `.env`, generated coverage files, `tmp/`, or local IDE files.
- Before broad release claims, run the relevant Make targets or document exactly
  which checks were not run.
