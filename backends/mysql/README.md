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
