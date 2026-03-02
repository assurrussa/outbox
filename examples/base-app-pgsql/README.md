# Base App PGSQL Example

This is a minimal runnable app that shows how to connect:
- core module `github.com/assurrussa/outbox`
- one backend module `github.com/assurrussa/outbox/backends/pgsql`

The example uses PostgreSQL + embedded migrations.

## Quick Start (with local compose)

From repo root:

```sh
docker compose --profile pgsql up -d
cd examples/base-app-pgsql
go mod tidy
go run .
```

Stop services:

```sh
docker compose down --remove-orphans
```

## Configuration

The app resolves DSN in this order:
1. `OUTBOX_PG_DSN`
2. Built from separate env vars (with defaults):
   - `OUTBOX_PG_HOST` (default `127.0.0.1`)
   - `OUTBOX_PG_PORT` (default `54325`)
   - `OUTBOX_PG_USER` (default `tests-service`)
   - `OUTBOX_PG_PASSWORD` (default `tests-service`)
   - `OUTBOX_PG_DB` (default `tests-db-pgsql`)
   - `OUTBOX_PG_SSLMODE` (default `disable`)

## What this example does

1. Creates pgsql storage client.
2. Runs backend migrations via `pgsqlmigrator.RunEmbedded(...)`.
3. Cleans demo tables (`jobs`, `jobs_failed`) for deterministic reruns.
4. Builds `outbox.Service` with pgsql repositories and tx manager.
5. Registers one job handler (`print_message`).
6. Pushes immediate and delayed jobs.
7. Starts worker loop and prints final stats.

## Notes

- `WithJobsStatRepo(...)` is optional and needed only for `GetQueueStats(...)`.
- This example uses `WithWorkers(1)` for predictable demo logs.

## Switch to another backend

- `github.com/assurrussa/outbox/backends/mysql/...`
- `github.com/assurrussa/outbox/backends/sqlite/...`
- `github.com/assurrussa/outbox/backends/picodata/...`
