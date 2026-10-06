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
