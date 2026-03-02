# Outbox Picodata Backend

Module path: `github.com/assurrussa/outbox/backends/picodata`

## Install

```sh
go get github.com/assurrussa/outbox/backends/picodata@latest
```

## Usage

```go
import (
	"context"
	"time"

	picomigrator "github.com/assurrussa/outbox/backends/picodata/migrator"
	"github.com/assurrussa/outbox/backends/picodata/repositories/jobsfailedrepo"
	"github.com/assurrussa/outbox/backends/picodata/repositories/jobsrepo"
	picostorage "github.com/assurrussa/outbox/backends/picodata/storage"
	picotx "github.com/assurrussa/outbox/backends/picodata/storage/transaction"
	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
)

func build(ctx context.Context, dsn string) (*outbox.Service, error) {
	lg := logger.Default()

	client, err := picostorage.Create(ctx, dsn)
	if err != nil {
		return nil, err
	}

	if err := picomigrator.RunEmbedded(ctx, client, lg, picomigrator.WithCommand("up")); err != nil {
		return nil, err
	}

	jobs := jobsrepo.Must(client)
	failed := jobsfailedrepo.Must(client)
	trx := picotx.New(client.Pool())

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

`Transactor` in Picodata backend is currently best-effort (no connection-pinned SQL transaction in current client API).

## Migrations

Recommended:

```go
_ = picomigrator.RunEmbedded(ctx, client, log, picomigrator.WithCommand("up"))
```

Filesystem mode:

```go
_ = picomigrator.Run(ctx, client, log,
	picomigrator.WithCommand("up"),
	picomigrator.WithDirectory("/path/to/migrations"),
)
```
