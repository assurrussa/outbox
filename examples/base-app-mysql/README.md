# Base App MySQL Example

This is a minimal runnable app that shows how to connect:
- core module `github.com/assurrussa/outbox`
- one backend module `github.com/assurrussa/outbox/backends/mysql`

The example uses MySQL + embedded migrations.

## Quick Start (with local compose)

From repo root:

```sh
docker compose --profile mysql up -d
cd examples/base-app-mysql
go mod tidy
go run .
```

Stop services:

```sh
docker compose down --remove-orphans
```

## Configuration

The app resolves DSN in this order:
1. `OUTBOX_MYSQL_DSN`
2. Built from separate env vars (with defaults):
   - `OUTBOX_MYSQL_HOST` (default `127.0.0.1`)
   - `OUTBOX_MYSQL_PORT` (default `33306`)
   - `OUTBOX_MYSQL_USER` (default `root`)
   - `OUTBOX_MYSQL_PASSWORD` (default `tests-service`)
   - `OUTBOX_MYSQL_DB` (default `tests-db-mysql`)

## What this example does

1. Creates MySQL storage client.
2. Runs backend migrations via `mysqlmigrator.RunEmbedded(...)`.
3. Cleans demo tables (`jobs`, `jobs_failed`) for deterministic reruns.
4. Builds `outbox.Service` with mysql repositories and tx manager.
5. Registers one job handler (`print_message`).
6. Pushes immediate and delayed jobs.
7. Starts worker loop and prints final stats.

## Notes

- `WithJobsStatRepo(...)` is optional and needed only for `GetQueueStats(...)`.
- This example uses `WithWorkers(1)` for predictable demo logs.

## Switch to another backend

- `github.com/assurrussa/outbox/backends/pgsql/...`
- `github.com/assurrussa/outbox/backends/sqlite/...`
- `github.com/assurrussa/outbox/backends/picodata/...`
