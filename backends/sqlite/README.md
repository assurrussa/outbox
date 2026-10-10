# Outbox SQLite Backend

Module path: `github.com/assurrussa/outbox/backends/sqlite`

## Install

```sh
go get github.com/assurrussa/outbox/backends/sqlite@latest
```

## Usage

```go
import (
	"context"
	"time"

	sqlitemigrator "github.com/assurrussa/outbox/backends/sqlite/migrator"
	"github.com/assurrussa/outbox/backends/sqlite/repositories/jobsfailedrepo"
	"github.com/assurrussa/outbox/backends/sqlite/repositories/jobsrepo"
	sqlitestorage "github.com/assurrussa/outbox/backends/sqlite/storage"
	sqlitetx "github.com/assurrussa/outbox/backends/sqlite/storage/transaction"
	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
)

func build(ctx context.Context, dsn string) (*outbox.Service, error) {
	lg := logger.Default()

	client, err := sqlitestorage.Create(ctx, dsn)
	if err != nil {
		return nil, err
	}

	if err := sqlitemigrator.RunEmbedded(ctx, client.DB(), lg, sqlitemigrator.WithCommand("up")); err != nil {
		return nil, err
	}

	jobs := jobsrepo.Must(client)
	failed := jobsfailedrepo.Must(client)
	trx := sqlitetx.New(client.DB())

	return outbox.New(
		outbox.WithWorkers(1),
		outbox.WithReservationBatchSize(32),
		outbox.WithIdleTime(100*time.Millisecond),
		outbox.WithReserveFor(time.Second),
		outbox.WithJobsRepo(jobs),
		outbox.WithFanoutJobsRepo(jobs),
		outbox.WithJobsFailedRepo(failed),
		outbox.WithTransactor(trx),
		outbox.WithLogger(lg),
	)
}
```

The jobs repository is auto-detected for exact grouped queue stats and unique
puts. Fan-out remains the explicit opt-in shown above.

The backend implements the complete version-aware fenced batch,
schema-preserving DLQ, and durable fan-out contracts. Unsupported exact
capabilities remain pending. For standard worker composition, prefer
`runtime.Open(ctx, runtime.Config{DSN: dsn})` after the migrate role has applied
the embedded migrations. The runtime does not migrate automatically and pins
the pool to one connection because SQLite is a single-writer database.

For the standard facade, set
`runtime.Config.ReservationBatchSize` (`0` keeps the default `1`). A batch uses
a short `BEGIN IMMEDIATE` write transaction and commits before handlers run.
Concurrent direct repository users serialize at SQLite's writer boundary; the
repository applies the busy timeout to every batch connection rather than only
the first pooled connection.

Manual immediate transactions used by claims and single unique puts always
clean up before returning their connection. Rollback has a separate five-second
context that survives work cancellation. An uncertain BEGIN or failed rollback
discards the physical connection; cleanup errors retain the original error.
An enclosing caller-owned transaction remains under the caller's control.

`GetQueueStats` uses one exact grouped scan of the active queue. The host owns
its polling frequency; the backend adds no cache or projection table.

## Durability And Connection Settings

Every physical connection, including pool growth and discarded/expired
replacements, receives the backend settings before it enters the pool:
requested WAL journal mode, `busy_timeout=5000`, `foreign_keys=ON`, and
`synchronous=NORMAL`. Storage pool defaults remain ten open/idle connections;
the standard runtime remains one open/idle connection. Zero idle connections
are supported for file databases and still receive initialization on each open.

Choose synchronization explicitly through either construction surface:

```go
client, err := sqlitestorage.Create(ctx, dsn,
    sqlitestorage.WithSynchronousMode(sqlitestorage.SynchronousFull))

rt, err := sqliteruntime.Open(ctx, sqliteruntime.Config{
    DSN: dsn, SynchronousMode: sqlitestorage.SynchronousFull,
})
```

Import `backends/sqlite/runtime` as `sqliteruntime` for the second example.
Empty mode preserves NORMAL; only the exported NORMAL/FULL values are accepted.
Backend settings take precedence over DSN `_pragma` values for `journal_mode`,
`synchronous`, `busy_timeout` and `foreign_keys`, matching the existing startup
precedence consistently across connections. Other DSN parameters remain intact.
The driver parses/applies the DSN first; invalid DSN options still return errors.
No process-global connection hook is installed.

Initialization checks the effective journal mode returned by SQLite. File
databases must enter WAL; another effective mode returns an error. In-memory
databases legitimately remain `memory`; this and an explicitly OFF memory
journal are accepted only when SQLite confirms no main database file. MEMORY
can also be a file journal mode, so its name alone is insufficient. WAL requests
cannot override memory database journal modes. Neither is
durable WAL storage. Plain `:memory:` belongs to one physical connection, so multiple
connections or replacement can see separate/empty databases. Use a deliberate
shared-memory URI and connection lifetime when memory sharing is needed.

WAL/NORMAL preserves transaction consistency, but a committed transaction can
roll back following power loss or an operating-system crash. WAL/FULL adds a
commit synchronization operation; its durability depends on the filesystem,
VFS and storage honoring synchronization. WAL requires local storage with its
shared-memory requirements, not a network filesystem. See SQLite's
[synchronization contract](https://www.sqlite.org/pragma.html#pragma_synchronous)
and [WAL requirements](https://www.sqlite.org/wal.html). A clean reopen/rollback
test proves those code paths, not survival of a power-loss event. The backend
does not change filesystem policy or promise exactly once external effects.

## Migrations

Recommended:

```go
_ = sqlitemigrator.RunEmbedded(ctx, db, log, sqlitemigrator.WithCommand("up"))
```

Filesystem mode:

```go
_ = sqlitemigrator.Run(ctx, db, log,
	sqlitemigrator.WithCommand("up"),
	sqlitemigrator.WithDirectory("/path/to/migrations"),
)
```

## Pagination

Both `jobsrepo.Repo` and `jobsfailedrepo.Repo` expose
`ListPage(ctx, limit, before *sqlite.PageCursor)`. Rows are ordered by
`created_at DESC, id DESC`; nil starts the first page. For example:

```go
var before *sqlite.PageCursor
for {
    rows, err := jobs.ListPage(ctx, 100, before)
    if err != nil {
        return err
    }
    if len(rows) == 0 {
        break
    }
    // Consume rows before requesting the next page.
    last := rows[len(rows)-1]
    before = &sqlite.PageCursor{CreatedAt: last.CreatedAt, ID: last.ID}
}
```

Import the backend root package (`github.com/assurrussa/outbox/backends/sqlite`)
for `PageCursor`. The same loop works for the failed-jobs repository; its cursor
uses the failed row's `ID`, never the source `JobID`. Non-positive limits use
`DefaultPageSize` (10); values above `MaxPageSize` (1000) return an error. Custom table options apply to every page.

Use the exact returned pair, retaining timestamp precision and the ID. A cursor
belongs to the selected backend and table. Do not reuse it across databases or
between active and failed queues. The API queries live data rather than holding
a snapshot across pages; concurrent deletes can remove rows, and inserts newer
than the cursor are seen only by restarting from nil.

`ListPaged(ctx, limit, before time.Time)` is retained unchanged but deprecated.
Its strict time-only boundary can skip rows when a page ends inside a group
with identical creation times. Migrate complete listings to `ListPage`.

SQLite stores these timestamps at millisecond precision. Use the returned
`CreatedAt` rather than reconstructing it from a higher-precision source.

For MySQL and SQLite, IDs are compared in lowercase in both the cursor predicate
and ordering, so uppercase/lowercase spellings of distinct UUIDs traverse
consistently even under a case-sensitive text collation. Stored IDs must use
standard hyphenated UUID text and be unique by logical UUID value. Case-only
aliases of the same UUID at one timestamp produce identical public cursors;
complete traversal of those ambiguous physical rows is not supported. Normalize
nonstandard UUID text and resolve duplicate logical identities before listing
historical/custom data. Repository-generated IDs already satisfy this contract.

The new listing orders by `LOWER(id)`. A custom index on raw text IDs may not
satisfy that normalized ordering; inspect the query plan for large listings.

## Recipient process-crash recovery fixture

The [recipient process-crash fixture](../../docs/recipient-process-recovery.md)
kills an owned worker after a separate recipient effect commits and before
queue ACK, then reopens both SQLite files and reconciles backlog recovery.
It runs in `make test-integration-sqlite`. This bounded correctness test does
not establish power-loss durability, large-fanout capacity or exactly-once
arbitrary external effects.
