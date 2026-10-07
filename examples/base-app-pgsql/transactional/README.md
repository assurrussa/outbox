# Transactional PostgreSQL quickstart

Run from the repository root against a **disposable development database**:

```sh
export OUTBOX_PG_DSN='postgres://tests-service:tests-service@127.0.0.1:54335/tests-db-pgsql?sslmode=disable'
go run ./examples/base-app-pgsql/transactional
```

The database must already exist and the user must have permission to create and
remove schemas. Use a PostgreSQL URL. Each invocation creates a uniquely named
`outbox_quickstart_*` schema, runs the backend's embedded migrations there, and
removes only that schema on exit. It does not start Docker or truncate existing
tables. An interrupted process may leave its own schema for manual removal.

The command checks two outcomes before printing success:

- the business `orders` insert and `Service.Put` commit together;
- an error returned after `Put` rolls back both the order and the queued job.

The important boundary in [main.go](main.go) is:

```go
err := runtime.Transactor().RunInTx(ctx, func(txCtx context.Context) error {
    tx := storage.GetTx(txCtx)
    if _, err := tx.Exec(txCtx, "INSERT INTO orders (id) VALUES ($1)", orderID); err != nil {
        return err
    }
    var err error
    jobID, err = runtime.Service().Put(txCtx, "order.created", orderID, time.Now().UTC())
    return err
})
```

Use the same runtime/database and pass `txCtx` to every participating operation.
The repository uses the pgx transaction carried by that context. `Put` outside
this boundary enqueues independently; an allocated ID inside the callback is
not evidence of a committed job. The helper returns a usable ID only when
`RunInTx` succeeds. A commit error must be reconciled according to the host's
business idempotency contract; a connection failure can leave commit outcome
uncertain.

Workers are deliberately not started, so the command can inspect persisted
rows. Register an `order.created` handler before starting workers in an actual
consumer. Handlers can run again after a crash or lost acknowledgement and
must make external effects idempotent. Atomic enqueue does not promise exactly
once delivery or atomically commit external effects.

Integration regression (same fixture ownership, plus `Put` validation failure):

```sh
go test -tags integration ./examples/base-app-pgsql/transactional -count=1
```

Without `OUTBOX_PG_DSN`, the integration test skips. For a compile-only check:

```sh
go test -tags integration ./examples/base-app-pgsql/transactional -run '^$'
```
