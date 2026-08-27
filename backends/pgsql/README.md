# Outbox Postgres Backend

Module path: `github.com/assurrussa/outbox/backends/pgsql`

## Install

```sh
go get github.com/assurrussa/outbox/backends/pgsql@latest
```

## Usage

For the standard version-aware fenced worker runtime, use the supported
`backends/pgsql/runtime` facade. It opens and verifies the database client,
constructs the required jobs repository, failed storage, transactor, the
explicit fan-out repository, and `outbox.Service`, and exposes `Run`,
`Readiness`, `BeginDrain`, and `Close`.
It deliberately does not apply migrations:

```go
runtime, err := pgsqlruntime.Open(ctx, pgsqlruntime.Config{DSN: dsn})
if err != nil {
	return err
}
defer runtime.Close()
```

The runtime owns the PostgreSQL pool used by the relay. A `0/0` connection
configuration keeps the historical `min=5/max=10` defaults. To reserve a
bounded pool for relay progress, set both values explicitly:

```go
runtime, err := pgsqlruntime.Open(ctx, pgsqlruntime.Config{
	DSN:                  relayDSN,
	ReservationBatchSize: 32, // zero keeps the core default of 1
	MinConnectionsCount:  1,
	MaxConnectionsCount:  1,
})
```

When producer traffic and relay work need isolation, the host should create a
separate producer client/transactor for the same database and schema. Start the
business transaction through that producer transactor, then call the service
built with relay repositories. PostgreSQL repositories execute through the
`pgx.Tx` carried in `context.Context`, so the business row and Outbox job remain
atomic even though the repositories otherwise own a different pool. Keep the
combined producer and relay maximum inside the host's connection budget.

`Runtime.Close()` closes only its relay pool. The host remains responsible for
closing the producer pool.

```go
import (
	"context"
	"database/sql"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	pgmigrator "github.com/assurrussa/outbox/backends/pgsql/migrator"
	"github.com/assurrussa/outbox/backends/pgsql/repositories/jobsfailedrepo"
	"github.com/assurrussa/outbox/backends/pgsql/repositories/jobsrepo"
	"github.com/assurrussa/outbox/backends/pgsql/storage/pgsqlclient"
	"github.com/assurrussa/outbox/backends/pgsql/storage/pgsqlinit"
	pgtx "github.com/assurrussa/outbox/backends/pgsql/storage/transaction"
	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
)

func build(ctx context.Context, dsn string) (*outbox.Service, error) {
	lg := logger.Default()

	pool, err := pgsqlinit.Create(ctx, dsn, pgsqlclient.WithLogger(lg))
	if err != nil {
		return nil, err
	}

	sqlDB := stdlib.OpenDBFromPool(pool.DB().Pool())
	defer sqlDB.Close()

	if err := pgmigrator.RunEmbedded(ctx, sqlDB, lg, pgmigrator.WithCommand("up")); err != nil {
		return nil, err
	}

	jobs := jobsrepo.Must(jobsrepo.NewOptions(pool))
	failed := jobsfailedrepo.Must(jobsfailedrepo.NewOptions(pool))
	trx := pgtx.New(pool.DB())

	return outbox.New(
		outbox.WithWorkers(1),
		outbox.WithReservationBatchSize(32),
		outbox.WithIdleTime(100*time.Millisecond),
		outbox.WithReserveFor(time.Second),
		outbox.WithJobsRepo(jobs),
		// Opt-in immutable source snapshots and independent fan-out jobs.
		outbox.WithFanoutJobsRepo(jobs),
		outbox.WithJobsFailedRepo(failed),
		outbox.WithTransactor(trx),
		outbox.WithLogger(lg),
	)
}
```

The jobs repository is auto-detected for exact grouped queue stats and unique
puts. Every claim is filtered by `(name, schema_version)`, refreshes leases with
the reservation token, acknowledges only the current lease owner, and
preserves schema version in DLQ. Unsupported exact capabilities remain pending.

A PostgreSQL batch claim is one ordered CTE
`UPDATE ... RETURNING`; the statement commits before handlers run. Batch size
does not increase handler concurrency: each worker processes its own claimed
jobs sequentially.

`GetQueueStats` uses one exact grouped scan of the active queue. The host owns
its polling frequency; the backend adds no cache or projection table.

## Migrations

Recommended:

```go
_ = pgmigrator.RunEmbedded(ctx, db, log, pgmigrator.WithCommand("up"))
```

Migration `00003_add_capability_leases.sql` upgrades existing jobs and failed
jobs to schema v1, adds fenced lease tokens, and creates the capability claim
index. Apply it before starting v0.12 workers. Keep producers on v1
until every pre-v0.12 unfiltered worker has drained.

Migration `00004_add_job_deduplication.sql` adds active-job deduplication and a
durable idempotency registry used by fan-out. The registry deliberately
survives job deletion. Use `jobsrepo.Repo.PruneJobIdempotencyKeys(...)` only
with a cutoff older than the application's complete replay and audit retention
window.

Filesystem mode:

```go
_ = pgmigrator.Run(ctx, db, log,
	pgmigrator.WithCommand("up"),
	pgmigrator.WithDirectory("/path/to/migrations"),
)
```
