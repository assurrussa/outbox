# Outbox MySQL Backend

Module path: `github.com/assurrussa/outbox/backends/mysql`

## Install

```sh
go get github.com/assurrussa/outbox/backends/mysql@latest
```

## Usage

```go
import (
	"context"
	"time"

	mysqlmigrator "github.com/assurrussa/outbox/backends/mysql/migrator"
	"github.com/assurrussa/outbox/backends/mysql/repositories/jobsfailedrepo"
	"github.com/assurrussa/outbox/backends/mysql/repositories/jobsrepo"
	mysqlstorage "github.com/assurrussa/outbox/backends/mysql/storage"
	mysqltx "github.com/assurrussa/outbox/backends/mysql/storage/transaction"
	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
)

func build(ctx context.Context, dsn string) (*outbox.Service, error) {
	lg := logger.Default()

	client, err := mysqlstorage.Create(ctx, dsn)
	if err != nil {
		return nil, err
	}

	if err := mysqlmigrator.RunEmbedded(ctx, client.DB(), lg, mysqlmigrator.WithCommand("up")); err != nil {
		return nil, err
	}

	jobs := jobsrepo.Must(client)
	failed := jobsfailedrepo.Must(client)
	trx := mysqltx.New(client.DB())

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
the embedded migrations. The runtime does not migrate automatically. The
capability claim implementation targets MySQL 8.0; the canonical integration
image is `mysql:8.0`.

For the standard facade, set
`runtime.Config.ReservationBatchSize` (`0` keeps the default `1`). Batch claims
first select at most the requested limit per exact capability through
`jobs_capability_claim_index`, merge those bounded candidates in queue order,
then conditionally reserve and reload the winners in a short read-committed
transaction. A worker that loses the candidate race selects again; unsupported
backlog is not scanned through the availability-only index. Migration
`00005_add_batch_claim_index.sql` extends the capability index with the complete
batch ordering. Claims remain functional with the original index from `00003`,
but `00005` avoids a filesort over the supported backlog and should be applied
before enabling workers from this release.

Migration `00006_enforce_exact_identifiers.sql` makes both active and retained
idempotency keys `VARBINARY(512)`. Capability names in active jobs and DLQ use
`utf8mb4_0900_bin`; case, Unicode spelling, and trailing spaces remain distinct.
MySQL 8.0.17 or newer is required for this collation. Existing IDs, fingerprints,
payloads, and retention timestamps remain intact. Active registry keys are
reconciled from their jobs. Completed rows may have lost their original key
spelling under the old batch replay behavior; their historical deduplication
requires external reconciliation. Follow the
[exact identifier upgrade procedure](docs/exact-identifier-upgrade.md), including
the guarded repair SQL. Previously suppressed messages cannot be reconstructed.

Stop writers and workers before applying `00006`, including equivalent changes
to host-managed custom tables. Apply the migration completely before restarting:
MySQL DDL is committed per statement, so an interrupted upgrade must be resumed.
`Down` deliberately fails because the old comparisons can collapse independent
keys. Roll back the application while retaining the exact schema. Custom active
and idempotency tables must use the same exact identifier definitions.

`GetQueueStats` uses one exact grouped scan of the active queue. The host owns
its polling frequency; the backend adds no cache or projection table.

## Migrations

Recommended:

```go
_ = mysqlmigrator.RunEmbedded(ctx, db, log, mysqlmigrator.WithCommand("up"))
```

Filesystem mode:

```go
_ = mysqlmigrator.Run(ctx, db, log,
	mysqlmigrator.WithCommand("up"),
	mysqlmigrator.WithDirectory("/path/to/migrations"),
)
```

## Pagination

Both `jobsrepo.Repo` and `jobsfailedrepo.Repo` expose
`ListPage(ctx, limit, before *mysql.PageCursor)`. Rows are ordered by
`created_at DESC, id DESC`; nil starts the first page. For example:

```go
var before *mysql.PageCursor
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
    before = &mysql.PageCursor{CreatedAt: last.CreatedAt, ID: last.ID}
}
```

Import the backend root package (`github.com/assurrussa/outbox/backends/mysql`)
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

The embedded MySQL schema uses `DATETIME(6)` (microsecond precision). Use
the returned `CreatedAt`, preserving all six fractional digits.

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
