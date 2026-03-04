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
	"os"
	"time"

	"github.com/assurrussa/outbox/backends/picodata/deployenv"
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

Connection config helper:

```go
cfg, err := deployenv.LoadAppConnFromEnv(os.Getenv)
if err != nil {
	return nil, err
}

dsn := cfg.ConnectionURL()
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

## Deployment Contract (env-only)

Picodata runtime in this repository is configured via `PICODATA_*` env vars only.
`PICODATA_CONFIG_FILE` and `cluster-storage*.yml` render flow are not supported anymore.

Required invariants:
- Do not set both `PICODATA_LISTEN` and `PICODATA_IPROTO_LISTEN`.
- Do not set both `PICODATA_PG_ADVERTISE` and `PICODATA_IPROTO_ADVERTISE`.
- Do not use `0.0.0.0` as client host in DSN or `OUTBOX_PICODATA_HOST`.
- For Dokploy deployment use alias/hostname `picodata_storage_1` for app-to-db DSN resolution.

### Advanced Picodata Tuning (env-only)

- `memtx` settings can be configured directly via environment variables:
  - `PICODATA_MEMTX_MEMORY`
  - `PICODATA_MEMTX_SYSTEM_MEMORY`
  - `PICODATA_MEMTX_MAX_TUPLE_SIZE`
- Tier-level settings like `can_vote` are not exposed as dedicated top-level env vars.
  - Use `PICODATA_CONFIG_PARAMETERS`, for example:
    - `PICODATA_CONFIG_PARAMETERS=cluster.tier.default.can_vote=false`
