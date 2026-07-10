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

## Capability-Aware Execution

Capability mode is enabled only when callers provide both:

- `WithCapabilityJobsRepo(...)`;
- `WithCapabilityJobsFailedRepo(...)`.

The legacy repositories remain required, which keeps `Put(...)`, queue stats,
and existing consumers source-compatible. A handler may implement
`VersionedJob`; otherwise it is registered as schema v1. Registration identity
is `(name, schemaVersion)`, so one worker can intentionally support more than
one schema for the same job name.

In capability mode:

- `Put(...)` persists schema v1;
- `PutVersioned(...)` validates and persists an explicit positive version;
- the repository receives the complete registered capability set and must not
  reserve anything else;
- an unsupported job remains pending without consuming attempts or entering
  DLQ;
- every reservation receives a new non-zero lease token;
- the service extends the lease every `reserveFor / 3` while the handler runs;
- heartbeat failure or a lost fence cancels the handler and returns
  `ErrLeaseLost`;
- ack and DLQ deletion affect exactly one row only when token and lease are
  still current;
- versioned DLQ rows retain the source schema version.

Delivery remains at-least-once. A handler may finish its external side effect
and lose the fence before ack, so payloads need a stable delivery/idempotency
identifier. Fencing prevents a stale worker from deleting a job owned by a
newer worker; it cannot provide exactly-once semantics in a remote system.

Capability rollout is expand-first. Legacy workers do not understand schema
filters, so producers must stay on v1 until all legacy workers have drained.
Capability-aware workers can register v1 and v2 together during the transition.

## Durable Fan-Out

Fan-out is a second opt-in layer enabled by `WithFanoutJobsRepo(...)`. It also
requires capability mode. `PutFanout(...)` writes one source job containing:

- a stable event ID, topic, schema version, payload, and occurrence time;
- the complete eligible target set fixed at event commit;
- an opaque immutable snapshot per target, such as config and secret revisions;
- the requested delivery availability time.

Call `PutFanout` inside the same host transaction that commits the source
domain event when atomic domain-state/event publication is required. Target
ordering is canonicalized. Reusing an event ID with different event data,
target eligibility, snapshots, or availability returns
`ErrIdempotencyConflict`.

Snapshots and event payloads are byte-immutable idempotency inputs. Store only
secret identifiers/revisions in a target snapshot, never plaintext signing
secrets. The consumer resolves the referenced encrypted secret version and the
host retains that version until every related delivery reaches terminal state.

Target kinds use `[A-Za-z0-9_-]`; topics use `[A-Za-z0-9._-]`. The derived
delivery capability name must fit the PostgreSQL `varchar(255)` job-name
contract.

The internal `outbox.fanout.dispatch` v1 handler materializes all target jobs
inside one transaction. Its source job is acknowledged only after that commit.
A crash before commit leaves no partial set; a crash after commit but before
source ack may replay the dispatcher, but stable delivery keys prevent duplicate
jobs. Each materialized job has:

- capability name `FanoutDeliveryJobName(target.Kind, event.Topic)`;
- the event schema version;
- a deterministic delivery ID;
- the original event and target snapshot in its payload;
- its own attempts, lease/fence, retry, ack, and DLQ lifecycle.

Idempotency keys live in a separate PostgreSQL registry, so deleting a completed
job does not reopen its delivery key. `PruneJobIdempotencyKeys` removes only
tombstones without an active job and works in bounded batches. The caller owns
the retention cutoff and must keep tombstones at least as long as any event can
be replayed or audited.

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

Capability storage is deliberately separate from `JobsRepository` so adding
it does not break existing custom repository implementations. The opt-in
interfaces cover versioned create/claim, lease heartbeat, conditional delete,
and version-preserving DLQ writes. PostgreSQL is the first backend implementing
the new contracts.

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
- Embedded migration `00003_add_capability_leases.sql` adds positive
  `schema_version`, lease tokens, a capability claim index, and DLQ schema
  versions without rewriting existing v1 rows.
- `jobsrepo.Repo` and `jobsfailedrepo.Repo` implement the opt-in capability
contracts.

`FanoutJobsRepository` is another additive interface for immutable unique job
creation. `FanoutMaintenanceRepository` is optional operational maintenance;
normal producer and worker execution does not depend on pruning.

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
