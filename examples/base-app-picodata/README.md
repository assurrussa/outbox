# Base App Picodata Example

This is a minimal runnable app that shows how to connect:
- core module `github.com/assurrussa/outbox`
- one backend module `github.com/assurrussa/outbox/backends/picodata`

The example uses Picodata + embedded migrations and resolves connection settings via `deployenv.LoadAppConnFromEnv`.

## Quick Start (with local compose)

From repo root:

```sh
export TEST_OUTBOXLIB_PICODATA_ADMIN_PASSWORD=passWord!123
docker compose --profile picodata up -d
cd examples/base-app-picodata
go mod tidy
go run .
```

Stop services:

```sh
docker compose down --remove-orphans
```

## Configuration

`deployenv.LoadAppConnFromEnv` resolves connection values in this order:
1. `OUTBOX_PICODATA_DSN`
2. `TEST_OUTBOXLIB_PICODATA_DSN`
3. Built from separate env vars (with defaults):
   - `OUTBOX_PICODATA_HOST` (default `127.0.0.1`)
   - `OUTBOX_PICODATA_PORT` (default `5049`)
   - `OUTBOX_PICODATA_USER` (default `admin`)
   - `OUTBOX_PICODATA_PASSWORD` (default `passWord!123`)
   - `OUTBOX_PICODATA_SSLMODE` (default `disable`)

Rules:
- `localhost` is normalized to `127.0.0.1`.
- Host `0.0.0.0` is rejected for client DSN.

## What this example does

1. Creates Picodata storage client.
2. Runs backend migrations via `picomigrator.RunEmbedded(...)`.
3. Cleans demo tables (`outbox_jobs`, `outbox_jobs_failed`) for deterministic reruns.
4. Builds `outbox.Service` with picodata repositories and tx manager.
5. Registers one job handler (`print_message`).
6. Pushes immediate and delayed jobs.
7. Starts worker loop and prints final stats.

## Notes

- `WithJobsStatRepo(...)` is optional and needed only for `GetQueueStats(...)`.
- This example uses `WithWorkers(1)` for predictable demo logs.
- Migrations in example use custom version table `picodata_db_version_examples`.

## Switch to another backend

- `github.com/assurrussa/outbox/backends/mysql/...`
- `github.com/assurrussa/outbox/backends/pgsql/...`
- `github.com/assurrussa/outbox/backends/sqlite/...`
