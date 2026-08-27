# Base App (In-Memory)

This example shows the minimum wiring of `outbox` **without any database/backend package**.

It is useful to understand the core contracts:
- `JobsRepository`
- `JobsStatRepository` (optional, only for `GetQueueStats`)
- `JobsFailedRepository`
- `Transactor`

All of them are implemented by one in-memory `stubRepo` in [main.go](./main.go).

## Run

```sh
cd examples/base-app
go run .
```

Expected logs:
- worker start/finish
- two handled jobs (`hello from outbox #1`, delayed `#2`)
- queue stats before and after processing

## What this example demonstrates

1. Build `outbox.Service` from pure interfaces.
2. Register a job handler (`print_message`).
3. Put immediate and delayed jobs.
4. Run worker loop.
5. Collect queue stats.

## How fenced batch reservation works here

`stubRepo.FindAndReserveJobsForCapabilities(...)` follows the same contract for
every limit, including the default `1`:
1. Lock repository state (`mutex`).
2. Select only rows whose exact `(name, schemaVersion)` was registered and that
   are:
   - available: `available_at <= now`
   - not reserved: `reserved_at IS NULL OR reserved_at <= now`
3. Pick deterministic order:
   - earlier `available_at`
   - then earlier `created_at`
4. Atomically reserve up to `limit` rows with one supplied lease token:
   - `attempts++`
   - `reserved_at = until`
5. Heartbeat outstanding IDs and use fenced ack/reschedule/DLQ; unstarted tail
   release compensates the claim-time attempt.
6. Return `ErrNoJobs` when no candidate exists. Unsupported pairs remain
   pending with attempts unchanged.

## Important limitations

- No persistence (everything is in-memory).
- No cross-process safety.
- No real SQL transaction behavior.
- Intended for understanding API wiring, not production use.

## Want a real backend?

Use [examples/base-app-sqlite](../base-app-sqlite/README.md) or one of backend modules:
- `github.com/assurrussa/outbox/backends/mysql`
- `github.com/assurrussa/outbox/backends/sqlite`
- `github.com/assurrussa/outbox/backends/pgsql`
- `github.com/assurrussa/outbox/backends/picodata`
