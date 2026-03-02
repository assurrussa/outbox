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
		outbox.WithIdleTime(100*time.Millisecond),
		outbox.WithReserveFor(time.Second),
		outbox.WithJobsRepo(jobs),
		// Optional: only if you call svc.GetQueueStats(...)
		outbox.WithJobsStatRepo(jobs),
		outbox.WithJobsFailedRepo(failed),
		outbox.WithTransactor(trx),
		outbox.WithLogger(lg),
	)
}
```

`JobsStatRepository` is optional.  
Keep `WithJobsStatRepo(...)` only when queue stats are needed.

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
