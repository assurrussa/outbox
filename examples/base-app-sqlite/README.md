# Base App Example

This is a minimal runnable app that shows how to connect:
- core module `github.com/assurrussa/outbox`
- one backend module `github.com/assurrussa/outbox/backends/sqlite`

The example uses SQLite + embedded migrations, so no Docker or external DB is required.
For stability, the example uses:
- single SQLite pooled connection (`max_open_conns=1`)
- one worker (`WithWorkers(1)`)

## Run

```sh
cd examples/base-app-sqlite
go mod tidy
go run .
```

Expected output contains:
- processed jobs (`handled job: ...`)
- queue stats (`total=0 available=0 processing=0`)
- path to local db file (`data/outbox.db`)

## What this example does

1. Creates SQLite storage.
2. Runs backend migrations via `sqlitemigrator.RunEmbedded(...)`.
3. Builds `outbox.Service` with sqlite repositories and tx manager.
4. Registers one job handler (`print_message`).
5. Pushes immediate and delayed jobs.
6. Starts worker loop and prints final stats.

## Switch to another backend

In a real project, replace SQLite imports with one backend module:
- `github.com/assurrussa/outbox/backends/mysql/...`
- `github.com/assurrussa/outbox/backends/pgsql/...`
- `github.com/assurrussa/outbox/backends/picodata/...`

The core service wiring stays the same:
- `WithJobsRepo(...)`
- `WithJobsStatRepo(...)` (optional, only for `GetQueueStats`)
- `WithJobsFailedRepo(...)`
- `WithTransactor(...)`
