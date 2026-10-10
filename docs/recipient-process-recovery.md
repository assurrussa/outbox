# Recipient effect and process-crash recovery

The SQLite integration fixture
`TestSQLiteRecipientEffectSurvivesProcessKillBeforeAck` covers the window after
a recipient commits its effect but before the Outbox worker acknowledges its
delivery. It supplements the existing dispatcher-planning lost-ACK tests;
it does not replace them or change production behavior.

## Owned fixture and acceptance

- One immutable event, eight webhook targets, one worker and the default
  single-job reservation path.
- Two separate test-owned SQLite files opened with WAL/FULL: the queue and the
  recipient's idempotency ledger/business counter. No network service or
  production connection is used.
- The child worker runs the real fanout dispatcher and delivery handler. The
  recipient inserts the stable delivery identity and increments its business
  counter atomically in its own database. A repeated identity with changed
  content fails rather than being silently accepted.
- A test-only repository wrapper lets the dispatcher ACK complete, then signals
  the first delivery ACK boundary and blocks before queue deletion. The parent
  waits for that signal and calls `Process.Kill`; the child never gracefully
  closes its databases at this boundary.
- Fresh database handles must observe the committed effect and retained
  leased delivery. A new service starts immediately and recovers naturally
  through the persisted lease, without manually clearing it or changing time.
- All eight delivery identities and targets must be reconciled exactly, the
  interrupted recipient must have two committed handling attempts, every sibling
  one, and the business counter must remain eight. Rolled-back recipient
  attempts are not recorded by this ledger. The interrupted retry timestamp must be
  at or after its persisted lease deadline. Both active queue and DLQ must be
  empty when the worker is joined.

The parent rejects any child race-detector report rather than accepting it as
part of the expected killed-process exit. It owns the subprocess and kills/joins
it on failure. Its watchdog and
the subprocess's test timeout bound failure cleanup. All databases and the
boundary marker are under `t.TempDir()`; no user-supplied DSN is consumed.

## Commands

Focused race gate, from the repository root with the normal workspace enabled:

```sh
cd backends/sqlite
go test -count=1 -tags integration -race \
  -run '^TestSQLiteRecipientEffectSurvivesProcessKillBeforeAck$' ./integration
```

For repeatability use `-count=5` on the same focused command. The child entry
point `TestSQLiteRecipientEffectProcessHelper` is inert in ordinary suite
traversal and is selected only by the owned parent process. Do not set its
internal environment variable manually.

The fixture also belongs to the existing canonical gate:

```sh
make test-integration-sqlite
```

The existing manual `Go` workflow runs that gate. No workflow or automatic
trigger change is required. A compile-only result or helper-only pass does
not establish crash recovery; the parent test must complete its assertions.

## Interpretation

This is a bounded correctness fixture, not a fanout capacity benchmark, an
HTTP recipient implementation, a sustained outage/soak campaign or a database
server restart. It kills one application process, not an operating system or
storage device. WAL/FULL still depends on filesystem/storage synchronization.

The recipient owns its idempotency transaction. Outbox's at-least-once retry
does not by itself guarantee exactly-once arbitrary external effects. The
fixture deliberately observes a duplicate committed handling attempt while
requiring one committed business effect for that identity.

Large-target fanout measurements, skew/resource bounds, real database outages
and sustained faults remain separate work. No optimization is justified by
this test and no throughput numbers are inferred from it.
