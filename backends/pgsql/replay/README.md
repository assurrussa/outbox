# PostgreSQL ordinary-job replay

`github.com/assurrussa/outbox/backends/pgsql/replay` is an explicit host-operated
API for retrying the same failed business operation. Apply embedded migrations
through `00005_add_job_replay_provenance.sql` first. Construct it with the same
PostgreSQL client/schema used by the runtime; it borrows the client.

```go
r, err := replay.New(runtime.Client())
if err != nil {
    return err
}
result, err := r.Replay(ctx, replay.Request{
    RequestID:   requestID, // persist once before the first attempt
    FailedJobID: failedID,  // jobs_failed.id, not jobs_failed.job_id
}, func(ctx context.Context, source replay.Source) (string, error) {
    if err := hostAuthorizeReplay(ctx, source); err != nil {
        return "", err
    }
    if source.Capability != (outbox.JobCapability{
        Name: "order.created", SchemaVersion: 2,
    }) {
        return "", errUnsupportedHandler
    }
    // The registered handler already consumes this original effect key.
    return originalEffectKeyFromPayload(source.Payload)
})
```

The host functions above belong to the application. Authorization, exact
handler support, payload decoding and external effect deduplication must be
explicitly checked before admission. The callback runs on every request,
including repeats, within a transaction holding the failed row. Keep it bounded
and read-only. The key must be nonblank and must already identify the original
business operation in the handler or its downstream system; inventing a key
does not make replay safe. `OriginalDeduplicationKey` is only a hint from the
original enqueue registry, not evidence of effect deduplication. No generic
authorization framework, admin route, background replay or retention job is
installed.

Use `NewRequestID` once per intentional request and persist it outside this API.
`ParseRequestID` and `ParseJobID` accept UUID text. The public ID aliases are
compatible with existing core/repository IDs without importing `shared/types`.

## Preserved operation and fresh delivery

Replay preserves payload bytes, exact `(name, schema_version)` and queue; it
does not substitute a supported version, upcast payloads or reset the original
enqueue key. The source failed row, reason and exception remain. A fresh queue
job receives a new JobID, zero attempts, no reservation and a zero lease token.
It becomes immediately available to the ordinary fenced workers after commit.
The new JobID is a delivery identity, not a new business-effect identity.

Handlers that previously keyed effects solely by `outbox.JobIDFromContext` must
resolve replay provenance via `ByJobID` and consume the original operation key
before the host admits replay. `ErrReplayNotFound` identifies ordinary jobs.
`Record.BusinessKey` and `Record.Source.OriginalJobID` support that host mapping;
the SDK does not alter worker contexts or handlers. A replay of a replay's own
failed row records that immediate source; preserve the same business key across
the chain. Hosts own authorization for provenance reads as well as writes.

A first admitted request atomically commits the queue job, a separate enqueue
tombstone (`outbox.replay.<request UUID>`) and a provenance snapshot. A repeated
request returns the same record with `Created == false`, even after ACK deletes
the job. It never recreates acknowledged work. Reusing a request for another
failed row, altered source operation or different business key fails closed.
Admission can still reject a repeated request under current host policy.

A new request fails with `ErrSourceActive` while the original job or an earlier
replay of that failed row remains in `jobs`, regardless of its availability or
lease. After completion, a distinct intentionally admitted request may stage
another delivery using the same original business key. Concurrent requests for
one failed row serialize. Host idempotency also covers concurrent operations
represented by different failed rows; this API does not infer those relations.

An error, cancellation or ambiguous commit returns a zero result. Repeat the
**same** request ID to determine whether its transaction committed. Only a
successful `Replay` return confirms commit. Nested caller transactions are
rejected so a result cannot be mistaken for an outer commit. Lookup methods
return provenance, not proof of handler success or external effect completion.

## Boundaries and retention

Only PostgreSQL ordinary jobs are supported. Built-in fan-out dispatcher and
`fanout.*` delivery capabilities are rejected before admission. There is no
core or other-backend fallback. Existing retry policy, leases, worker
concurrency and delivery guarantees remain unchanged. Atomic staging and
fenced acknowledgement do not promise exactly-once external effects.

The provenance table retains source content and the original business key after
ACK. It has no update/delete API, pruning default or retention scheduler. Hosts
must govern access and retention of this content. Referenced failed rows cannot
be deleted; unreplayed failed rows retain their previous behavior. Migration
Down refuses to discard recorded requests. The host must make a separate
retention decision before a destructive downgrade. Ordinary tombstone pruning
does not remove provenance or cause the same recorded request to recreate work.
Do not treat this as a tamper-proof log against privileged direct SQL, table
rewrites or arbitrary job-ID reuse.

## Local validation

With an owned disposable PostgreSQL database:

```sh
cd backends/pgsql
OUTBOX_PG_DSN='postgres://user:password@127.0.0.1:5432/test?sslmode=disable' \
  go test -count=1 -tags integration -race ./replay
```

This gate creates and drops only a unique `outbox_replay_*` schema. It tests
atomic staging, retained evidence, admission/active-source failures, repeated
requests after ACK, request concurrency, persistence rollback, and commit
response faults before and after actual commit. Without `OUTBOX_PG_DSN` the
live test skips; compilation alone is not transactional evidence.
