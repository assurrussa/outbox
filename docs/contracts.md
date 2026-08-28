# Runtime And Backend Contracts

## Core Construction

`outbox.New(...)` requires:

- `WithJobsRepo(...)`;
- `WithJobsFailedRepo(...)`;
- `WithTransactor(...)`.

Defaults are one worker, `1s` idle time, `5m` reservation duration, reservation
batch size `1`, and the named default logger. Validation bounds are:

- workers: any positive integer;
- idle time: `100ms..10s`;
- reservation duration: `1s..10m`;
- reservation batch size: `1..MaxReservationBatchSize` (`1000`) and no larger
  than the configured repository maximum.

`WithJobsRepo(...)` automatically detects `JobsStatRepository` and
`UniqueJobsRepository`. `WithJobsStatRepo(...)` and
`WithUniqueJobsRepo(...)` support split compositions and take priority over
auto-detection. Fan-out is never auto-detected; it remains an explicit
`WithFanoutJobsRepo(...)` opt-in.

## Registration And Version Identity

Jobs implement:

```go
type Job interface {
	Name() string
	Handle(ctx context.Context, payload string) error
	ExecutionTimeout() time.Duration
	MaxAttempts() int
}
```

A job may also implement `VersionedJob`. Otherwise it registers as schema v1.
Registration identity is the exact `(name, schemaVersion)` pair, so several
versions of one job name may coexist. Versions must be positive. Registration
while the service is running returns `ErrServiceRunning`.

`Put(...)` is shorthand for `PutVersioned(..., DefaultSchemaVersion, ...)` and
always calls `JobsRepository.CreateJobVersioned`. `PutVersioned(...)` preserves
the supplied positive version.

## One Fenced Batch Execution Path

Every worker uses `FindAndReserveJobsForCapabilities`, including when the limit
is one. `WithReservationBatchSize(size)` changes prefetch only. A worker handles
its claimed slice sequentially, so maximum handler concurrency remains the
configured worker count.

The repository receives the complete registered capability set and one new
non-zero lease token. It must atomically claim at most `limit` ready rows in
`available_at, created_at, id` order, increment their attempts, and return the
same token on every row. An empty queue must return `outbox.ErrNoJobs`;
`sharederrors.ErrNoJobs` remains the same sentinel for migration compatibility.
Returning an empty slice with a `nil` error is a contract violation surfaced as
`ErrEmptyReservationBatch`, so a broken custom adapter cannot silently spin.
The service also rejects too many rows or a token mismatch.

One owned heartbeat extends every unfinished row every `reserveFor / 3`. The
heartbeat goroutine is stopped and joined before tail release or final return.
A database error or affected-row mismatch is a lost fence: the active handler
is cancelled, no later handler starts, and the worker returns `ErrLeaseLost`.

Per-job outcomes are fenced:

- success calls `DeleteJobWithLease`;
- `RetryAt(err, at)` calls `RescheduleJobWithLease`, clears the lease, and does
  not sleep in the worker;
- `Permanent(err)` creates a versioned failed row and deletes the active row in
  one transaction;
- reaching `MaxAttempts()` takes precedence over `RetryAt` and uses the same
  versioned DLQ transaction;
- an ordinary retriable error leaves that job reserved until its current lease
  expires and the worker continues with the next claimed row.

`BeginDrain`, run cancellation, or an infrastructure/fence error releases only
the unstarted tail. `ReleaseUnstartedJobsWithLease` clears its reservation and
token and compensates the attempt added by that claim. The active row is never
acknowledged merely because the run context ended. A crash or failed release
leaves the claimed attempts intact.

Delivery remains at-least-once. Fencing prevents a stale worker from deleting a
row owned by a newer worker; it cannot make an external side effect exactly
once.

## Unsupported Capability Policy

Any unregistered `(name, schemaVersion)` pair, including an unknown name, must
remain pending:

- it is not claimed;
- attempts are not incremented;
- it is not sent to automatic DLQ;
- it becomes eligible when a matching worker is registered;
- it remains included in backlog statistics.

This is required for safe rolling deployment. One worker cannot infer that no
other worker supports a version. Cleanup or DLQ of unsupported work is an
explicit administrative operation; there is no callback, flag, or cluster
capability registry in the core contract.

## Queue Snapshot

`Service.GetQueueStats(...)` is optional and returns one exact snapshot:

```go
type QueueStats struct {
	ObservedAt   time.Time
	Total        int64
	Available    int64
	Processing   int64
	ByCapability []CapabilityQueueStats
}

type CapabilityQueueStats struct {
	Name              string
	SchemaVersion     SchemaVersion
	Total             int64
	Available         int64
	Processing        int64
	OldestAvailableAt time.Time
}
```

`ObservedAt` and non-zero `OldestAvailableAt` values are UTC. Groups are sorted
by name and schema version. A zero oldest timestamp means that the group has no
ready row. When it is non-zero, the oldest ready age is
`ObservedAt.Sub(OldestAvailableAt)`. `Total` includes ready, scheduled,
processing, and unsupported active rows; `Available` means ready and not
currently leased; `Processing` means a lease is live at `ObservedAt`.

The backend interface is one aggregate call:

```go
type JobsStatRepository interface {
	GetQueueStats(ctx context.Context, observedAt time.Time) (QueueStats, error)
}
```

Standard backends execute one exact `GROUP BY name, schema_version` query over
the active queue. There is no cache, projection table, top-N truncation, or new
index. Treat it as an active-backlog scan and let the host control call
frequency. Without a stats repository, the service returns
`sharederrors.ErrJobStatNotInit`.

## Repository Interfaces

```go
type JobsRepository interface {
	CreateJobVersioned(
		ctx context.Context,
		name string,
		schemaVersion SchemaVersion,
		payload string,
		availableAt time.Time,
	) (types.JobID, error)
	FindAndReserveJobsForCapabilities(
		ctx context.Context,
		now, until time.Time,
		leaseToken LeaseToken,
		capabilities []JobCapability,
		limit int,
	) ([]models.Job, error)
	ExtendJobLeases(
		ctx context.Context,
		jobIDs []types.JobID,
		leaseToken LeaseToken,
		now, until time.Time,
	) (int64, error)
	ReleaseUnstartedJobsWithLease(
		ctx context.Context,
		jobIDs []types.JobID,
		leaseToken LeaseToken,
		now time.Time,
	) (int64, error)
	DeleteJobWithLease(
		ctx context.Context,
		jobID types.JobID,
		leaseToken LeaseToken,
		now time.Time,
	) (int64, error)
	RescheduleJobWithLease(
		ctx context.Context,
		jobID types.JobID,
		leaseToken LeaseToken,
		now, availableAt time.Time,
	) (int64, error)
	MaxReservationBatchSize() int
}

type JobsFailedRepository interface {
	CreateFailedJobVersioned(
		ctx context.Context,
		jobID types.JobID,
		name string,
		schemaVersion SchemaVersion,
		payload, reason string,
	) (types.JobID, error)
}

type Transactor interface {
	RunInTx(ctx context.Context, f func(context.Context) error) error
}
```

There are no legacy single-claim, unfiltered batch, unconditional delete, or
unversioned failed-job interfaces and no deprecated aliases.

`UniqueJobsRepository` remains an optional immutable-idempotency extension.
Identical active or tombstone replay returns the original ID with
`Created == false`; a mismatch returns `ErrIdempotencyConflict`.

## Durable Fan-Out

`WithFanoutJobsRepo(...)` enables the built-in `outbox.fanout.dispatch` v1
handler. `PutFanout(...)` stores an immutable source event plus the exact target
snapshots selected at event time. The dispatcher creates one independently
retried versioned job per target inside a transaction. Stable event and
delivery IDs are idempotency boundaries.

PostgreSQL, MySQL, and SQLite keep completed idempotency tombstones separately
from active jobs. Hosts may prune them only after their replay, audit, and
delivery retry windows. Picodata does not implement fan-out or unique puts.

## Backend Boundaries

PostgreSQL, MySQL, and SQLite implement the full required repository contract
and return `1000` from `MaxReservationBatchSize()`. Their standard runtimes
accept `ReservationBatchSize`; zero preserves the core default of one.

MySQL capability claims target MySQL 8.0. They select a bounded set per exact
capability, merge those candidates in queue order, and conditionally reserve
the winning IDs in a short read-committed transaction. SQLite uses a short
`BEGIN IMMEDIATE` and the standard runtime owns one pooled connection because
SQLite is single-writer. PostgreSQL uses one ordered CTE `UPDATE ... RETURNING`
statement.

Picodata implements the same public slice contract but only for a single row
and returns `1` from `MaxReservationBatchSize()`. Plural lease and release
methods reject any slice whose length is not one. Its pool API still lacks a
connection-pinned SQL transaction, so its transactor is best-effort and the
backend does not expose the standard atomic runtime or fan-out contract.

Existing migrations and schema-v1 defaults are preserved. Rows created before
capability columns existed are interpreted as schema v1 with an empty lease.
