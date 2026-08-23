# Migration Notes

This project removed the old `infrastructure/*` import paths. Treat this as a
hard break for consumers that still use the previous layout.

## Current Import Shape

Core runtime:

```go
import "github.com/assurrussa/outbox/outbox"
```

Pick exactly one backend module for storage-specific wiring. Examples:

```go
import mysqlstorage "github.com/assurrussa/outbox/backends/mysql/storage"
import sqlitestorage "github.com/assurrussa/outbox/backends/sqlite/storage"
import "github.com/assurrussa/outbox/backends/pgsql/storage/pgsqlinit"
import "github.com/assurrussa/outbox/backends/pgsql/storage/pgsqlclient"
import picostorage "github.com/assurrussa/outbox/backends/picodata/storage"
```

Use the matching backend repositories, migrator, and transaction manager from
the same backend module. Do not mix backend repository packages in one storage
wiring path.

## Migration Checklist

1. Replace all old `infrastructure/*` imports with the current core and backend
   module imports.
2. Keep the host application's direct dependency limited to the backend it
   actually uses.
3. Run embedded backend migrations through that backend's `migrator` package.
4. Build `outbox.Service` with:
   - `WithJobsRepo(...)`
   - `WithJobsFailedRepo(...)`
   - `WithTransactor(...)`
5. Override the default logger with `WithLogger(...)` only when the host needs a
   custom logger.
6. Add `WithJobsStatRepo(...)` only if the application calls
   `Service.GetQueueStats(...)`.
7. Run tests with `GOWORK=off` in backend modules when validating standalone
   consumer compatibility.

## Stable Boundary

`shared/*` packages are kept for reuse inside this repository's modules. They
are not a stable public import surface for external consumers.

For runnable wiring examples, use the README files under `examples/` and
`backends/*/README.md`.

## Additive v0.10 to v0.11 migration

The v0.11 candidate does not replace the v0.10 capability, fan-out, or legacy
repository interfaces. Existing services compile and keep their current retry
behavior until they opt into a new repository capability.

PostgreSQL, MySQL, and SQLite consumers can expose both new capabilities by
passing their standard repository to `WithCapabilityJobsRepo(...)`. The
constructor detects `UniqueJobsRepository` and `ReschedulableJobsRepository`
automatically. A split custom repository may instead configure
`WithUniqueJobsRepo(...)` and `WithReschedulableJobsRepo(...)` explicitly.
Those explicit options still require `WithCapabilityJobsRepo(...)`; v0.11
rejects a versioned producer or fenced rescheduler wired only to the legacy
claim path.

Before enabling `PutVersionedUnique`:

1. publish the root v0.11 tag without rewriting any existing tag;
2. update each backend module to the exact published root version and publish
   its path-qualified v0.11 tag;
3. run the backend's existing migrations and standalone `GOWORK=off` gate;
4. choose a stable deduplication key and make every fingerprint input
   immutable, including `availableAt`;
5. set tombstone retention longer than every producer replay window;
6. deploy workers that understand `Permanent` and persisted `RetryAt` before
   producers depend on those outcomes.

`RetryAt` changes capability-mode scheduling only when a reschedulable
repository is configured. It increments the current attempt, writes the next
availability, clears the reservation and token, and fails with `ErrLeaseLost`
if the worker no longer owns a live lease. If `MaxAttempts()` is already
reached, the job moves to DLQ instead of being rescheduled.

`Permanent` is also additive. Returning it from a capability handler skips
ordinary retry and performs the same fenced DLQ transaction used for exhausted
jobs. Legacy workers recognize permanent failures, but they cannot persist an
explicit future retry time because the legacy repository has no fenced
reschedule primitive.

Picodata v0.11 can expose `ReschedulableJobsRepository`; it still must not be
advertised as a `UniqueJobsRepository`, fan-out runtime, or atomic DLQ runtime.
Use a backend with a proven transaction/idempotency boundary for a
GoMessenger transactional producer.
