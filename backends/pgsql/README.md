# Outbox Postgres Backend

Module path: `github.com/assurrussa/outbox/backends/pgsql`

## Install

```sh
go get github.com/assurrussa/outbox/backends/pgsql@latest
```

## Usage

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
		outbox.WithIdleTime(100*time.Millisecond),
		outbox.WithReserveFor(time.Second),
		outbox.WithJobsRepo(jobs),
		// Opt-in version-aware claims and fenced leases.
		outbox.WithCapabilityJobsRepo(jobs),
		// Optional: only if you call svc.GetQueueStats(...)
		outbox.WithJobsStatRepo(jobs),
		outbox.WithJobsFailedRepo(failed),
		outbox.WithCapabilityJobsFailedRepo(failed),
		outbox.WithTransactor(trx),
		outbox.WithLogger(lg),
	)
}
```

`JobsStatRepository` is optional.  
Keep `WithJobsStatRepo(...)` only when queue stats are needed.

The capability options are also optional, but must be configured together.
When enabled, this backend filters claims by `(name, schema_version)`, refreshes
leases with the reservation token, conditionally acknowledges only the current
lease owner, and preserves schema version in DLQ.

## Migrations

Recommended:

```go
_ = pgmigrator.RunEmbedded(ctx, db, log, pgmigrator.WithCommand("up"))
```

Migration `00003_add_capability_leases.sql` upgrades existing jobs and failed
jobs to schema v1, adds fenced lease tokens, and creates the capability claim
index. Apply it before enabling the capability options. Keep producers on v1
until no legacy worker remains.

Filesystem mode:

```go
_ = pgmigrator.Run(ctx, db, log,
	pgmigrator.WithCommand("up"),
	pgmigrator.WithDirectory("/path/to/migrations"),
)
```
