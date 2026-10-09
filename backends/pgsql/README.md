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

## Staging in an existing database/sql transaction

For a host that already owns a PostgreSQL `*sql.Tx`, use
`jobsrepo.NewSQLTxPutter(tx)`. It implements `outbox.UniqueVersionedPutter`
without creating a client, opening a connection, starting another transaction,
or taking commit/rollback ownership. The existing pgx runtime and
`storage.WithTx` behavior are unchanged.

```go
func stageOutbound(ctx context.Context, tx *sql.Tx, eventID string, availableAt time.Time) error {
	producer, err := jobsrepo.NewSQLTxPutter(tx)
	if err != nil {
		return err
	}
	_, err = producer.PutVersionedUnique(
		ctx, eventID, "message.accepted", 1, `{"message_id":"message-17"}`,
		availableAt,
	)
	return err
}
```

Call this from the host's business/Inbox transaction with its existing context
and transaction. Propagate staging errors to that transaction owner; it owns
commit, rollback and savepoints. A returned job ID or `Created=true` is only
a staged result until the owner confirms commit. A rollback removes both the
new job and its idempotency key. Resolve ambiguous commits by retrying the same
identity and unchanged content.

The transaction must come from `database/sql` using a PostgreSQL driver, with
the intended database and trusted `search_path` already configured. Apply the
embedded migrations (at least through `00004_add_job_deduplication.sql`) in that
same schema. The adapter uses the transaction's existing schema resolution;
it never issues `SET`, selects a different schema or falls back to a pool.
It cannot detect that a valid Outbox schema belongs to the wrong business
database, so the caller owns this binding. Keep the binding fixed while staging
and configure the relay for the same database/schema.

Nil transactions fail construction. Ended transactions, driver/SQL errors and
context cancellation are returned from the put operation with their original
errors preserved for `errors.Is`/`errors.As`. The adapter is a producer only,
not a relay transactor. Do not wrap it in the pgx transaction manager.

Single-event SQL and fingerprinting are shared with the existing pgx repository:
the immutable identity covers exact name, schema version, payload and UTC
availability instant. Identical replays return the original ID with
`Created=false`, including after ACK; changed content returns
`outbox.ErrIdempotencyConflict`. Retain the idempotency registry for the host's
full replay/audit window, and reuse the same availability instant on retries.

## Ordinary-job replay

The opt-in [`replay` package](replay/README.md) stages an explicitly admitted
failed operation with unchanged payload/capability/queue, a fresh queue identity
and atomic retained provenance. Hosts own authorization and the original
business-effect idempotency key. Repeated request IDs return the same record
after ACK; active sources and built-in fan-out fail closed. No other-backend
fallback, automatic replay or retention default is provided.

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

Migration `00005_add_job_replay_provenance.sql` adds the replay request journal.
Recorded requests survive queue deletion and restrict deletion of referenced
failed evidence. Down refuses to drop a nonempty journal. Apply it before using
the replay package; ordinary unreplayed jobs retain their existing behavior.

Filesystem mode:

```go
_ = pgmigrator.Run(ctx, db, log,
	pgmigrator.WithCommand("up"),
	pgmigrator.WithDirectory("/path/to/migrations"),
)
```
