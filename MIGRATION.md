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
6. Standard jobs repositories are auto-detected as `JobsStatRepository` and
   `UniqueJobsRepository`. Use the explicit split setters only when those
   interfaces live on another value.
7. Run tests with `GOWORK=off` in backend modules when validating standalone
   consumer compatibility.

## Stable Boundary

`shared/*` packages are kept for reuse inside this repository's modules. They
are not a stable public import surface for external consumers.

For runnable wiring examples, use the README files under `examples/` and
`backends/*/README.md`.

## Migrating from v0.11 to v0.12

The v0.12.0 release intentionally removes the legacy execution contracts;
there are no deprecated aliases. Existing v0.11 tags remain immutable.

Custom repositories must replace the old split interfaces with one required
`JobsRepository` that implements:

- versioned create;
- exact capability-filtered batch claim with one supplied lease token;
- plural lease extension and unstarted-tail release;
- fenced delete and reschedule;
- `MaxReservationBatchSize() int`.

`JobsFailedRepository` now exposes only `CreateFailedJobVersioned`. Remove
`WithCapabilityJobsRepo`, both batch-repository setters,
`WithReschedulableJobsRepo`, and `WithCapabilityJobsFailedRepo` from wiring.
Keep only `WithJobsRepo`, `WithJobsFailedRepo`, and `WithTransactor` as required
dependencies. `WithJobsRepo` auto-detects stats and unique-put extensions;
explicit split setters take priority. Fan-out remains a separate explicit
`WithFanoutJobsRepo` opt-in.

`Put(...)` now always persists schema v1 through the versioned create method.
`limit=1` uses the same fenced batch path as larger reservations. The core
maximum is `MaxReservationBatchSize == 1000`; PostgreSQL, MySQL, and SQLite
advertise that value, while Picodata advertises `1` and rejects larger values
at construction and direct claim boundaries.

Before deploying v0.12.0 workers, drain every v0.11 worker that can perform an
unfiltered claim. Once only v0.12.0 workers remain, an unregistered
`(name, schemaVersion)` stays pending with zero additional attempts and no
automatic DLQ. Operators should alert on exact capability count and oldest
ready age; cleanup or DLQ is an explicit administrative decision.

`RetryAt` always uses fenced rescheduling. `Permanent` and attempt exhaustion
of a supported job use version-preserving fenced DLQ. Picodata exposes the same
single-element repository contract, but its transaction manager remains
best-effort and it still must not be presented as an atomic fan-out or DLQ
runtime.
