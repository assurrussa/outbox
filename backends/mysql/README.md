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
_ = mysqlmigrator.RunEmbedded(ctx, db, log, mysqlmigrator.WithCommand("up"))
```

Filesystem mode:

```go
_ = mysqlmigrator.Run(ctx, db, log,
	mysqlmigrator.WithCommand("up"),
	mysqlmigrator.WithDirectory("/path/to/migrations"),
)
```
