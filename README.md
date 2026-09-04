# GO Outbox (Core)

`github.com/assurrussa/outbox` is the core outbox library.

The repository is now a multi-module monorepo:
- core module: `github.com/assurrussa/outbox`
- MySQL backend: `github.com/assurrussa/outbox/backends/mysql`
- SQLite backend: `github.com/assurrussa/outbox/backends/sqlite`
- Postgres backend: `github.com/assurrussa/outbox/backends/pgsql`
- Picodata backend: `github.com/assurrussa/outbox/backends/picodata`

## Install core

```sh
go get github.com/assurrussa/outbox@latest
```

```go
import "github.com/assurrussa/outbox/outbox"
```

## Core usage

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/assurrussa/outbox/outbox"
	outboxlogger "github.com/assurrussa/outbox/outbox/logger"
)

type SendEmailJob struct {
	outbox.DefaultJob
}

func (*SendEmailJob) Name() string { return "send_email" }

func (*SendEmailJob) Handle(_ context.Context, _ string) error { return nil }

func main() {
	// Use signal notification only as a shutdown trigger.
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	svc, err := outbox.New(
		outbox.WithWorkers(1),
		outbox.WithReserveFor(time.Second),
		outbox.WithIdleTime(5*time.Second),
		outbox.WithLogger(outboxlogger.Default()),
		outbox.WithJobsRepo(jobsRepo),
		outbox.WithJobsFailedRepo(jobsFailedRepo),
		outbox.WithTransactor(txManager),
	)
	if err != nil {
		panic(err)
	}

	emailJob := &SendEmailJob{}
	svc.MustRegisterJob(emailJob)

	// Keep a separate run context active during graceful drain.
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()

	group, groupCtx := errgroup.WithContext(runCtx)
	group.Go(func() error {
		return svc.Run(groupCtx)
	})

	if _, err := svc.Put(groupCtx, "send_email", `{"id":"1"}`, time.Now()); err != nil {
		panic(err)
	}

	// Wait for shutdown trigger or worker failure.
	select {
	case <-sigCtx.Done():
		stop()
		// Drain new claims while in-flight jobs keep their lease heartbeats.
		svc.BeginDrain()
		// Cancel the run context only when the bounded drain deadline expires.
		drainTimer := time.AfterFunc(10*time.Second, cancelRun)
		defer drainTimer.Stop()
	case <-groupCtx.Done():
	}

	if err := group.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Printf("service stopped: %v\n", err)
		os.Exit(1)
	}
}
```

For a graceful worker shutdown, first remove the process from readiness, then
call `svc.BeginDrain()` without cancelling the `Run` context. After
`BeginDrain` returns, no new repository claim can start; already reserved
capability jobs keep their fenced lease heartbeat and may ack normally. Cancel
the `Run` context only when the host's bounded drain deadline expires, so an
unfinished handler cannot ack and its lease can be recovered by another
worker.
`svc.Readiness(ctx)` is a non-mutating worker-lifecycle probe: it is successful
only after worker loops start and becomes unavailable before `BeginDrain`
closes claim admission. Pair it with a separate database probe; readiness never
reserves a synthetic outbox job.

`JobsStatRepository` is optional. `WithJobsRepo(...)` detects it automatically
when the same value implements both interfaces. Use `WithJobsStatRepo(...)`
only for a split composition; an explicit value takes priority.

## Version-aware fenced reservation batches

`WithReservationBatchSize(size)` reduces reservation round trips without
changing handler concurrency. The default is `1`; valid values are `1..1000`.
Each worker claims up to `size` jobs that are available immediately and then
executes them sequentially. The service does not wait for a batch to fill, and
`Handle`, retry/reschedule, DLQ, and conditional delete remain per-job.

There is one execution path for every size. `limit=1` is a one-element fenced
batch; larger limits change only prefetch. Every `JobsRepository` must expose
its own `MaxReservationBatchSize()`. The core maximum is exported as
`outbox.MaxReservationBatchSize` (`1000`). `outbox.New(...)` rejects a requested
size above either maximum before workers start. PostgreSQL, MySQL, and SQLite
return `1000`; Picodata returns `1`.

One claim uses one fenced token and commits before any handler starts. A shared
heartbeat extends the current and unstarted jobs. Successful jobs are deleted
immediately and are not rolled back if a later job fails. An ordinary handler
error leaves only that job reserved until its current lease expires and the
worker continues through the batch. Infrastructure or fence errors stop the
batch and best-effort release its unstarted tail. `BeginDrain()` finishes only
the active handler and releases the tail while compensating the attempts added
by that claim. Process crashes and failed releases keep the claimed attempts.

When no matching job is available, repositories return `outbox.ErrNoJobs`.
The legacy `sharederrors.ErrNoJobs` name refers to the same sentinel for
migration compatibility; new consumers should use the public `outbox` name.

Picodata implements the same slice contract for exactly one job. Configuring or
directly requesting a larger batch returns
`ErrReservationBatchSizeUnsupported` (constructor errors are also wrapped by
`ErrOption`).

```go
svc, err := outbox.New(
	outbox.WithReservationBatchSize(32),
	// required repositories and transactor...
)
```

## True handler batches

`RegisterBatchJob` activates one real handler batch independently from
reservation prefetch. The zero config means 100 jobs, 4 MiB of payload, and a
25 ms fill window; use `MaxMessages: 1` as a same-path singleton control. The
PostgreSQL, MySQL, and SQLite use an optional byte-bounded claim to reserve the
longest ordered prefix in one backend operation. Custom repositories that only
implement `BatchJobsRepository` retain the compatible singleton collector
fallback. Every supplemental claim is capped by the remaining `MaxWait` window;
when it expires, the service flushes the jobs already collected while the
parent Run context remains live.

```go
if err := svc.RegisterBatchJob(applicationBatchJob, outbox.BatchConfig{
	MaxMessages: 100,
	MaxBytes:    4 << 20,
	MaxWait:     25 * time.Millisecond,
}); err != nil {
	return err
}
```

The handler receives one ordered `[]BatchJobItem` and returns one keyed
`BatchItemResult` for every input `JobID`. The result order is irrelevant, but
missing, duplicate, or unknown IDs fail the service closed. One backend
transaction applies mixed success, retry, no-attempt defer, and DLQ outcomes.
DLQ rows are created through the configured `JobsFailedRepository`, so split
or custom table composition remains authoritative. Any partial lease mismatch
rolls back both the failed rows and all active-row changes.
A transient top-level error reschedules the whole batch without consuming
attempts; top-level `Permanent` and invalid results stop the service without
ACK, DLQ, or unstarted-claim compensation. Those admitted leases remain for
expiry-based recovery.

Workers rotate across registered batch capabilities and alternate true-batch
and ordinary single-job work when both are present. `BeginDrain` fences every
collector claim and checks admission again before entering `HandleBatch`.
Cancelling `Run` after handler admission never acknowledges a late successful
return; the current lease is left for recovery.

`DeferAt(err, at)` is also supported by ordinary single jobs. It persists an
exact retry time, compensates the claim attempt, and pauses new claims for the
same capability until that time. `RetryAt` consumes an attempt; `Permanent`
takes precedence over both markers. Custom repositories that support
no-attempt defer implement the optional `DeferJobsRepository` capability.

Atomic producer staging is explicit:

```go
results, err := svc.PutVersionedUniqueBatch(ctx, []outbox.UniqueBatchPut{
	{DeduplicationKey: firstKey, Name: "relay", SchemaVersion: 1, Payload: first, AvailableAt: now},
	{DeduplicationKey: secondKey, Name: "relay", SchemaVersion: 1, Payload: second, AvailableAt: now},
})
```

All items commit or roll back together and results preserve input order.
PostgreSQL, MySQL, and SQLite implement multi-item execution, byte-bounded
claims, and unique batch staging with existing schemas. Picodata supports
singleton reservation prefetch and no-attempt defer, but does not expose true
handler batches, bounded claims, or unique batch puts because its current
client cannot provide the required connection-pinned atomic transaction.

### Capacity evidence

In the normalized small-payload PostgreSQL/NATS integration profile, the
complete true Outbox batch path raised the confirmed sustainable frontier from
450 to 2,550 messages per second versus the singleton-Outbox control, a 5.67x
improvement. Both frontiers passed 3/3 fresh-volume confirmations; the next
2,600 msg/s candidate passed only 1/3. See [performance evidence](docs/performance.md)
for exact commits, topology, latency, reconciliation, and test boundaries. This
is repeatable checkout-workspace evidence, not a universal production-capacity
guarantee.

## Version-aware workers

Every worker claim is filtered by the exact registered `(name, schemaVersion)`
set. There is no legacy or unfiltered execution mode.

```go
type PublishV2Job struct {
	outbox.DefaultJob
}

func (*PublishV2Job) Name() string { return "cms.entry.publish" }

func (*PublishV2Job) SchemaVersion() outbox.SchemaVersion { return 2 }

func (*PublishV2Job) Handle(_ context.Context, _ string) error { return nil }

svc, err := outbox.New(
	outbox.WithJobsRepo(jobsRepo),
	outbox.WithJobsFailedRepo(jobsFailedRepo),
	outbox.WithTransactor(txManager),
)
if err != nil {
	panic(err)
}

svc.MustRegisterJob(&PublishV2Job{})
_, _ = svc.PutVersioned(
	ctx,
	"cms.entry.publish",
	2,
	`{"entryId":"1"}`,
	time.Now(),
)
```

Jobs without `SchemaVersion()` register as schema v1. `Put(...)` is shorthand
for `PutVersioned(..., 1, ...)`; `PutVersioned(...)` requires an explicit
positive version. Unsupported schemas and unknown names remain pending: they
are not claimed, their attempts stay unchanged, and they never enter automatic
DLQ. This is the safe rolling-deployment policy because one worker cannot know
whether another worker supports the row. Cleanup or DLQ of unsupported work is
an explicit administrative operation outside the worker contract.

Supported jobs use fenced outcomes. Active leases are refreshed every
`reserveFor / 3`; successful ack, `RetryAt`, and version-preserving DLQ deletion
all require the same live token. `Permanent` and attempt exhaustion move a
supported job to DLQ.

PostgreSQL, MySQL, and SQLite implement the complete capability and durable
fan-out contracts. Picodata implements versioned/CAS capability storage only;
it deliberately omits fan-out and standard runtime composition until its client
can provide a real atomic transaction boundary.

## Queue observability

`Service.GetQueueStats(...)` returns one exact snapshot with UTC `ObservedAt`,
aggregate `Total`, `Available`, and `Processing`, plus sorted `ByCapability`
groups. Each group includes the exact name, schema version, the same counts, and
`OldestAvailableAt`; a zero timestamp means that group has no ready job. For a
group with ready work, its oldest age is
`ObservedAt.Sub(OldestAvailableAt)`.

The standard backends calculate this with one aggregate query that groups the
entire active backlog. It is intentionally not cached and has no projection
table, top-N truncation, or new index. Treat it as an active-queue scan and let
the host choose a suitable polling/scrape frequency. Unsupported capabilities
remain included in total backlog and are visible by name and schema version.

## Unique puts and persisted retry dispositions

`PutVersionedUnique` is an additive producer contract for one immutable
deduplication key. PostgreSQL, MySQL, and SQLite repositories implement it and
are detected automatically from `WithJobsRepo(...)`:

```go
result, err := svc.PutVersionedUnique(
	ctx,
	messageID,
	"integration.message.publish",
	1,
	canonicalEnvelope,
	messageTime,
)
if err != nil {
	return err
}
if !result.Created {
	// Identical content was already staged or completed.
}
```

The deduplication key covers name, schema version, payload, and availability.
An identical replay returns the original job ID with `Created == false` even
after the active job was acknowledged; different content returns
`ErrIdempotencyConflict`. The host owns bounded tombstone retention and must not
prune a key while the message can still be replayed.

Handlers can classify a failure without changing the `Job` interface:

```go
func (j *PublishJob) Handle(ctx context.Context, payload string) error {
	if err := j.publisher.Publish(ctx, payload); err != nil {
		if errors.Is(err, ErrInvalidEnvelope) {
			return outbox.Permanent(err)
		}
		return outbox.RetryAt(err, time.Now().Add(30*time.Second))
	}
	return nil
}
```

`Permanent` moves the owned job directly to DLQ. `RetryAt` atomically persists
the next availability and releases the current lease; it never sleeps in a
worker. Both terminal operations remain fenced by job ID, lease token, and an
unexpired lease. Attempt limits still take precedence over a requested retry.
`RescheduleJobWithLease` is part of the required `JobsRepository`; a custom
backend cannot silently fall back to an unfenced retry path. Standard
repositories auto-detect `UniqueJobsRepository`; split compositions can still
use `WithUniqueJobsRepo(...)` explicitly.

Picodata implements fenced rescheduling but not unique puts, because its
current transaction boundary cannot honestly provide the complete immutable
idempotency contract. `PutVersionedUnique` therefore fails closed there unless
the host supplies a separate `UniqueJobsRepository`.

## Durable fan-out (opt-in)

Configure `WithFanoutJobsRepo(jobsRepo)` when one
integration event must produce independently retried deliveries. `PutFanout`
stores the event plus the exact eligible target snapshots under the event ID.
The built-in dispatcher is registered during construction and creates one job
per target in a transaction.

```go
event := outbox.FanoutEvent{
	ID:            types.NewMessageID(),
	Topic:         "cms.entry.published",
	SchemaVersion: 1,
	Payload:       json.RawMessage(`{"entryId":"1"}`),
	OccurredAt:    time.Now(),
}

_, err = svc.PutFanout(ctx, event, []outbox.FanoutTarget{
	{
		Kind:     "nitro",
		ID:       "site-1",
		Snapshot: json.RawMessage(`{"namespace":"public"}`),
	},
	{
		Kind:     "webhook",
		ID:       "subscription-7",
		Snapshot: json.RawMessage(`{"configRevision":4,"secretRevision":2}`),
	},
}, time.Now())
```

Delivery handlers register
`FanoutDeliveryJobName(targetKind, eventTopic)` for the event schema they
understand and decode payloads with `DecodeFanoutDelivery`. Every delivery has
a deterministic ID suitable for webhook idempotency. Unsupported delivery
capabilities stay pending with zero attempts.

Fan-out retries are idempotent even after a delivery job was acknowledged and
deleted: PostgreSQL, MySQL, and SQLite retain a compact key/fingerprint
tombstone separately from active jobs. Prune tombstones in bounded batches
only after the host's event replay, audit, and webhook retry retention windows
have elapsed.

## Backend modules

Pick only the backend module you need for a project.

- [MySQL backend](./backends/mysql/README.md)
- [SQLite backend](./backends/sqlite/README.md)
- [Postgres backend](./backends/pgsql/README.md)
- [Picodata backend](./backends/picodata/README.md)

## Example app

Runnable examples:
- [examples/base-app](examples/base-app/README.md) (core only, in-memory stubs)
- [examples/base-app-mysql](examples/base-app-mysql/README.md)
- [examples/base-app-picodata](examples/base-app-picodata/README.md)
- [examples/base-app-pgsql](examples/base-app-pgsql/README.md)
- [examples/base-app-sqlite](examples/base-app-sqlite/README.md)

## Migration from old import paths

Old `infrastructure/*` paths were removed (hard break).

See [MIGRATION.md](./MIGRATION.md).

## Notes on `shared/*`

`shared/*` is kept in core for internal library/backend reuse, but should be treated as unstable internal API by external consumers.

## Development

Use workspace-aware commands:

```sh
make test-core
make test-backends
make test-integration-all
```

For integration services:

```sh
make devup
make devdown
```

Release prep for backend modules:

```sh
# pin all backend modules to a published core tag and refresh their sums
make release-ready-backends CORE_VERSION=v0.14.0

# non-mutating exact-version pre-tag gate
make release-readiness-backends CORE_VERSION=v0.14.0
```

The commands above name the currently published stable core. Root releases are
immutable and must exist before backend preparation. One backend release change
updates all four modules; publish their path-qualified tags only after that
change passes the standalone gate and merges.

## License

MIT. See [LICENSE](./LICENSE).
