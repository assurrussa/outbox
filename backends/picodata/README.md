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

The jobs repository is auto-detected for exact grouped queue stats.

`Transactor` in Picodata backend is currently best-effort (no connection-pinned SQL transaction in current client API).

The repository exposes the required version-aware fenced batch contract for
exactly one job, plus version-preserving failed-job storage. Picodata Go
client v1.0.0 has no connection-pinned transaction, so failed-row plus leased
delete and complete fan-out planning cannot be committed atomically. The
backend intentionally does not implement `FanoutJobsRepository` or expose a
standard runtime facade.

Picodata returns `1` from `MaxReservationBatchSize`. Keep
`WithReservationBatchSize` unset or equal to `1`; a larger value is rejected by
`outbox.New` with `ErrReservationBatchSizeUnsupported` wrapped by `ErrOption`.
Direct claim and plural lease/release calls also reject any size other than
one. `GetQueueStats` is one exact grouped scan of the active queue.

Migration `00003_add_capability_leases.sql` is additive. Picodata 25.2 supports
only `ADD COLUMN` in `ALTER TABLE`, so a one-step down records the migration as
rolled back but retains the added columns. A full reset drops the owning tables.
Rows inserted before the capability columns existed are read as schema v1 with
a nil lease until a version-aware worker claims them.
Table drops and additive alters wait for cluster-wide application so a
subsequent migration cannot race DDL that is still being applied.

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
