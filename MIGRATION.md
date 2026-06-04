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
