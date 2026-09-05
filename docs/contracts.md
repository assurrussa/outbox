# Runtime And Backend Contracts

## Core Construction

`outbox.New(...)` requires:

- `WithJobsRepo(...)`;
- `WithJobsFailedRepo(...)`;
- `WithTransactor(...)`.

The configured `Transactor` must implement `TransactionCapabilities` (declaring
`SupportsAtomicDLQ() bool`) and report `true` unless `WithAllowNonAtomicDLQ()`
is explicitly passed. Transactors without capabilities or reporting `false`
fail closed during construction (`ErrTransactionCapabilitiesRequired` or
`ErrNonAtomicDLQUnsupported`).

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
The service also rejects too many rows or a token mismatch. The true handler
batch collector additionally rejects duplicate job IDs and any row outside the
requested exact capability before handler admission. Built-in SQL repositories
also implement the optional `BoundedBatchJobsRepository` capability and return
the longest ordered prefix bounded by both message count and UTF-8 payload
bytes. Custom repositories without it retain the compatible singleton claim
loop.

Every claimed row must return its actual, valid `ReservedAt` deadline, still
in the future. Each repository claim receives a child context capped by its
requested lease deadline; an earlier parent or fill deadline wins. Initial
claim timeout is an infrastructure failure. Supplemental fill timeout keeps
the existing early-flush behavior. Repositories must honor cancellation during
connection acquisition and SQL execution. A returned expired or missing lease
is `ErrLeaseLost` and cannot enter a handler. If a successful claim returns after
cancellation, its confirmed live rows are released with attempt compensation;
cleanup runs outside the drain claim lock with an independent bounded context.
Unknown or expired ownership is not released. A supplemental fill timeout cannot
hide a failed cleanup.

The lease manager tracks confirmed deadlines per unfinished row. Before each
`Handle` and `HandleBatch`, it checks cancellation, drain, and live ownership;
less than `reserveFor / 3` headroom triggers synchronous fenced renewal. Renewal
must finish before the prior earliest lease deadline, match every requested
row, and leave live leases after the call. No expired lease is resurrected.

The cached earliest deadline may conservatively precede the actual minimum
after a row is removed. Removal needs no remaining-row scan; a stronger bound
is recomputed when admission or finalization needs it. Only confirmed live rows
can advance that bound, including after a fenced extension.

One owned heartbeat checks unfinished rows every `reserveFor / 3` and extends
only those needing more time. Already longer leases are never shortened. The
heartbeat goroutine is stopped and joined before releasing manager-owned
unstarted rows or returning. Built-in bounded claims return only the admissible
ordered prefix. The singleton fallback may reserve one candidate beyond the
remaining byte limit; that candidate is released while the heartbeat continues
for selected rows. A database error or affected-row mismatch is a lost fence:
the active handler is cancelled, no later handler starts, and the worker
returns `ErrLeaseLost`.

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

Before any per-job ACK, retry, defer, or DLQ finalization, all outstanding leases
cover one shared five-second deadline plus a one-second margin. Renewal and
terminal SQL share that deadline; nested operations do not reset it. Waiting for
an in-flight heartbeat to unlock the ledger is also bounded by that deadline;
expiry cancels the batch heartbeat before cleanup. True batch
finalization retains its additional per-DLQ-insert budget and uses the same
deadline protection. This lets a bounded finalization hold the manager lock
without exposing the waiting tail to another owner.

Per-job protective renewal targets that required deadline plus `reserveFor`,
so sequential finalizations can reuse the headroom. Short reservations remain
supported but may require extra writes; longer reservations trade fewer renewals
for longer recovery after an owner disappears. The host must account for the
persisted lease deadline, which can exceed the initial reservation.

`BeginDrain`, run cancellation, or an infrastructure/fence error releases only
the unstarted tail. `ReleaseUnstartedJobsWithLease` clears its reservation and
token and compensates the attempt added by that claim. The active row is never
acknowledged merely because the run context ended. A crash or failed release
leaves the claimed attempts intact.

`BeginDrain()` waits for already-started repository claims. The host must arm
its cancellation timer before calling it, so the same external deadline bounds
both claim-admission shutdown and handler drain. The method signature is
unchanged; repository cancellation remains cooperative.

Delivery remains at-least-once. Fencing prevents a stale worker from deleting a
row owned by a newer worker; it cannot make an external side effect exactly
once.

## True Handler Batch Contract

`RegisterBatchJob` is a separate opt-in path. It does not change `Job`,
`RegisterJob`, or `WithReservationBatchSize`; the latter remains sequential
prefetch. A single `(name, schemaVersion)` cannot be registered as both a
single and batch capability.

The zero-value `BatchConfig` resolves to 100 jobs, 4 MiB of payload bytes, and
25 ms after the first claim. A batch flushes at the first count, byte, or wait
limit. Every supplemental claim uses a child context capped by the remaining
`MaxWait` window. If that deadline expires while the parent Run context remains
live, the collector flushes the jobs already gathered. Accepted payload stays
within `MaxBytes`, except that an individually oversized first job remains a
singleton. `MaxMessages=1` uses the same collector, handler, and atomic outcome
path. Negative limits, arithmetic overflow, or a maximum above the backend
capability fail registration.

`BatchJob.HandleBatch` receives ready jobs in durable queue order. Its
`BatchResult` may be in any order but must contain exactly one result for every
input `JobID`. Missing, repeated, or unknown keys, or a non-empty result paired
with a top-level error, return `ErrInvalidBatchResult` and stop the service
without finalizing the batch. Because every row was admitted to the handler,
its leases remain for expiry-based recovery rather than being released as
unstarted work.

Disposition precedence is `Permanent`, `DeferAt`, `RetryAt`, then ordinary
error:

- item success deletes the fenced active row;
- item ordinary or `RetryAt` failure consumes the claimed attempt and persists
  a bounded or explicit retry time, then reaches DLQ at `MaxAttempts`;
- item `DeferAt` compensates the claimed attempt and persists the exact time;
- item `Permanent` or attempt exhaustion creates the failed row and deletes
  the active row atomically;
- a transient top-level error, panic, timeout, retry, or defer compensates all
  claimed attempts and reschedules the batch with a separate bounded
  capability retry streak;
- top-level `Permanent` and structural result defects stop the service with no
  ACK or DLQ.

A defer pauses new claims for that exact capability until its durable time, so
a broker outage cannot create a tight claim storm. A valid result resets the
top-level streak without clearing a later pause established by another worker.
The service creates failed rows through its configured `JobsFailedRepository`
and applies active-row outcomes inside the same configured `Transactor`. The
batch repository must roll back every active-row change when any requested
lease is missing. A commit failure leaves the durable rows replayable, and a
commit whose result was ambiguous is resolved from stored state on the next
claim.

Every collector claim shares the `BeginDrain` claim boundary, and handler
admission is checked after the fill window. Cancellation after admission leaves
the claimed rows leased for recovery even if `HandleBatch` returns success.
Workers rotate their starting batch capability and alternate batch and single
work so a continuously ready capability cannot starve the others.

`BatchJobsRepository`, `BoundedBatchJobsRepository`, `DeferJobsRepository`,
`UniqueBatchJobsRepository`, and `UniqueBatchVersionedPutter` are optional
capabilities; existing repository interfaces are not widened. Batch
registration and batch staging fail closed when the configured backend does
not implement their required contracts. PostgreSQL, MySQL, and SQLite support
multi-item batch execution, bounded claims, and atomic unique staging.
Picodata implements no-attempt defer but does not provide true handler batches,
bounded claims, or unique batch staging because its client lacks a
connection-pinned transaction. Existing tables and migrations are reused.

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

type TransactionCapabilities interface {
	SupportsAtomicDLQ() bool
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
delivery retry windows. Exactly one active jobs table may own a given idempotency
keys registry table; pruning across multiple active tables that share an idempotency
table is unsupported. Custom active tables must configure dedicated idempotency
tables via `jobsrepo.WithIdempotencyTable(...)`. Picodata does not implement fan-out or unique puts.

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

MySQL migration `00006` requires MySQL 8.0.17+ and stores deduplication keys as
`VARBINARY(512)` in both active rows and the retained registry. Active and failed
job names use `utf8mb4_0900_bin` for exact Unicode and trailing-space comparison.
Apply it with writers and workers stopped, including equivalent host-owned
custom schemas. Down is prohibited; application rollback retains this schema.
Active key spellings are repaired from the job. Completed tombstones may have
lost their original spelling under the old case-insensitive batch replay;
retaining the fingerprint and ID does not restore it. The
[MySQL upgrade procedure](../backends/mysql/docs/exact-identifier-upgrade.md)
defines the inventory, external journal comparison and guarded manual repair.

SQLite owns cleanup of its manual immediate transactions for both claim paths
and single unique puts. Rollback uses a separate five-second uncancelled context;
an ambiguous BEGIN or unsuccessful cleanup discards the connection. Original
and cleanup errors are preserved, while caller-owned transactions remain under
caller control.

Picodata implements the same public slice contract but only for a single row
and returns `1` from `MaxReservationBatchSize()`. Plural lease and release
methods reject any slice whose length is not one. Its pool API still lacks a
connection-pinned SQL transaction, so its transactor is best-effort and the
backend does not expose the standard atomic runtime or fan-out contract.

Existing migrations and schema-v1 defaults are preserved. Rows created before
capability columns existed are interpreted as schema v1 with an empty lease.
