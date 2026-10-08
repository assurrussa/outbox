# PostgreSQL true-batch consumer

This runnable example registers a `BatchJob` with `RegisterBatchJob` and
`BatchConfig`, stages six ready jobs with `PutVersionedUniqueBatch`, then starts
one worker. The handler receives multiple items in one call and returns exactly
one successful `BatchItemResult` per input `JobID`. Results are keyed by ID, not
by their position in the slice.

## Run

Use a disposable development PostgreSQL database. The database role must be able
to create schemas. From the repository root, using the existing local compose
service and its development credentials:

```sh
docker compose --profile pgsql up -d
export OUTBOX_PG_DSN='postgres://tests-service:tests-service@127.0.0.1:54335/tests-db-pgsql?sslmode=disable'
cd examples/base-app-pgsql
GOWORK=off go run ./batch
```

The existing module replacements use the checked-out core and PostgreSQL backend;
no dependency updates are needed. `OUTBOX_PG_DSN` must be a PostgreSQL URL.

The command creates a uniquely named schema, runs embedded migrations there, and
removes only that schema on exit. It never truncates existing tables. Other
schemas and their rows are left alone.

Output includes `HandleBatch received 6 items` (or another multi-item size) and a
final `durably completed 6 jobs` summary with `active=0 failed=0`. The summary is
printed only after the database confirms committed job removal, every staged ID
has been observed by the handler, at least one call contained multiple items,
and the worker has stopped and cleanup has succeeded. The command has a
30-second work deadline, a 5-second worker shutdown wait, and a separate bounded
cleanup context. Cancellation or incomplete work exits nonzero.

## Tests

From `examples/base-app-pgsql`:

```sh
GOWORK=off go test -race ./batch
OUTBOX_PG_DSN="$OUTBOX_PG_DSN" GOWORK=off go test -race -count=1 -tags integration ./batch
```

The integration test requires the database above; it skips if the DSN is unset.
It runs the same example path, verifies multi-item invocation, all staged IDs,
zero active/failed jobs, bounded completion, removal of the owned schema, and
survival of a separately owned sentinel table and row.

## Scope

`BatchConfig` controls real handler batches; it is distinct from reservation
prefetch. Batch staging and batch handling are also separate operations. All
jobs are staged before workers start so the example has a ready multi-item batch.
The handler only records receipt in memory; a real consumer must make its side
effects idempotent because delivery may repeat after a crash. No external broker,
exactly-once side effect guarantee, or production retention policy is implied.
