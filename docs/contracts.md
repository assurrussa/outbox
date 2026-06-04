# Runtime And Backend Contracts

## Core Service

`outbox.New(...)` constructs a `*outbox.Service` from options and validates the
required dependencies before returning.

Required caller dependencies:
- `WithJobsRepo(...)`
- `WithJobsFailedRepo(...)`
- `WithTransactor(...)`

Defaulted options:
- workers: `1`
- idle time: `1s`
- reserve duration: `5m`
- logger: named default logger

Validation bounds:
- workers: `1..32`
- idle time: `100ms..10s`
- reserve duration: `1s..10m`

Optional stats:
- `WithLogger(...)` can override the default logger.
- `WithJobsStatRepo(...)` is required only for `Service.GetQueueStats(...)`.
- If stats repo is missing, `GetQueueStats(...)` returns
  `sharederrors.ErrJobStatNotInit`.

## Job Registration And Execution

Jobs implement:

```go
type Job interface {
	Name() string
	Handle(ctx context.Context, payload string) error
	ExecutionTimeout() time.Duration
	MaxAttempts() int
}
```

Register jobs before starting the service. `RegisterJob(...)` rejects nil jobs,
duplicate names, and registration after the service is running. `MustRegisterJob`
panics on registration failure.

`Service.Run(ctx)` starts worker loops. A concurrent second run returns
`ErrServiceRunning`.

Execution behavior:
- The repository reserves a job until `now + reserveFor`.
- The handler runs with a timeout from `ExecutionTimeout()`.
- `JobIDFromContext(ctx)` returns the current job ID inside the handler.
- A successful handler causes the job to be deleted.
- A panic in `Handle(...)` is converted into an error.
- Unknown jobs and jobs whose attempts reach `MaxAttempts()` move to DLQ.

DLQ behavior uses `Transactor.RunInTx(...)` to create the failed-job row and
delete the original job.

## Repository Interfaces

Core storage dependencies are interfaces:

```go
type JobsRepository interface {
	CreateJob(ctx context.Context, name, payload string, availableAt time.Time) (types.JobID, error)
	FindAndReserveJob(ctx context.Context, now time.Time, until time.Time) (models.Job, error)
	DeleteJob(ctx context.Context, jobID types.JobID) (int64, error)
}
```

```go
type JobsFailedRepository interface {
	CreateFailedJob(ctx context.Context, jobID types.JobID, name, payload, reason string) (types.JobID, error)
}
```

```go
type Transactor interface {
	RunInTx(ctx context.Context, f func(context.Context) error) error
}
```

`JobsStatRepository` is separate so normal producer/worker flows do not depend
on exact queue-count support.

## Backend Pattern

Every backend module follows the same consumer shape:

1. Create storage client.
2. Run embedded migrations.
3. Build `jobsrepo`, `jobsfailedrepo`, and transaction manager.
4. Pass those into `outbox.New(...)`.
5. Register jobs and run the service.

Backend modules expose embedded migrations and filesystem migration mode:

```go
RunEmbedded(ctx, ..., log, WithCommand("up"))
Run(ctx, ..., log, WithCommand("up"), WithDirectory("/path/to/migrations"))
```

Use embedded migrations for normal applications. Filesystem migrations are for
explicit migration-directory control.

## Backend Notes

MySQL:
- Client contract exposes `DB() *sql.DB` and `Close() error`.
- Compose maps local MySQL to `127.0.0.1:33306` by default.

SQLite:
- Client contract exposes `DB() *sql.DB` and `Close() error`.
- SQLite example uses one worker and a single pooled connection for stability.

Postgres:
- Client contract exposes `DB() storage.DBEngine` and `Close() error`.
- Compose maps local Postgres to `127.0.0.1:54335` by default.
- The backend uses pgx under the storage/client layer.

Picodata:
- Client contract exposes `Pool() *picodata-go.Pool` and `Close() error`.
- Deployment config is env-only.
- `PICODATA_CONFIG_FILE` and `cluster-storage*.yml` render flows are not
  supported.
- Client DSN host `0.0.0.0` is rejected.
- `localhost` is normalized to `127.0.0.1` by the deploy-env helper.
- Do not set both `PICODATA_LISTEN` and `PICODATA_IPROTO_LISTEN`.
- Do not set both `PICODATA_PG_ADVERTISE` and `PICODATA_IPROTO_ADVERTISE`.
- The current transaction manager is best-effort because the Picodata client API
  does not provide connection-pinned SQL transactions.

## Release Boundary

Before claiming a backend module is safe for standalone consumers, verify it
without workspace replacement:

```sh
make release-verify-backends
```

The dependency isolation test also checks package dependencies with
`GOWORK=off`, so it is a useful guard for accidental cross-backend dependency
leaks.
