# PostgreSQL connection-outage recovery

`TestPostgresConnectionOutageBeforeAckRecoversBacklog` is a bounded integration
regression for the existing PostgreSQL backend and worker lifecycle. It uses
the normal test configuration and the unique database created by the existing
`NewTestRepoSuite` helper. The test has no skip path.

## Scenario and assertions

1. Persist eight ordinary jobs with distinct identities and payloads.
2. Let one real handler finish. Immediately before its actual fenced ACK,
   deny new connections to that test-owned database and terminate only that
   database's existing PostgreSQL sessions through a separate control connection.
3. Require the actual repository ACK to fail and the existing fail-fast `Run`
   to return that failure before the watchdog expires. Confirm that the pool
   cannot ping the unavailable database and that no successful ACK was recorded.
4. Restore database connection availability. Reuse the same pgx pool,
   repositories and service. Inspect all eight persisted jobs before recovery,
   including the interrupted job's lease and first persisted attempt.
5. Restart `Run`, as a host supervisor would. Do not reset or edit any lease.
   Require eight unique successful ACKs, exact identities and payloads, one
   repeated handling attempt for the interrupted job after its persisted lease
   expires, one handling attempt for every sibling, an empty queue and empty DLQ.

The 30-second context is a failure bound, not a soak duration or performance
measurement. One worker and the default one-job reservation make the injected
boundary deterministic. Handler bookkeeping is read only after `Run` joins.
The wrapper injects the real PostgreSQL availability change; it does not
substitute a fabricated ACK error.

## Isolation and cleanup

Use only the documented disposable integration server. As with the existing
suite, the configured account must create and own its unique test database.
This scenario additionally exercises `ALTER DATABASE ... ALLOW_CONNECTIONS`
and termination of that same account's sessions in that database. The fixture
checks ownership and rejects the control database as its fault target.

The shared PostgreSQL server process, control database, other test databases
and their sessions stay available. A fresh, independently bounded control
connection restores availability on every exit path after setup, including a
failed assertion or expired main test context, before normal fixture cleanup.
No credentials are added, printed or copied into the test.

## Commands

With the existing integration environment running, the canonical gate includes
this regression:

```sh
make test-integration-pgsql
```

For a focused rerun after loading the repository's documented test configuration:

```sh
cd backends/pgsql
go test -count=1 -tags integration -race -run '^TestPostgresConnectionOutageBeforeAckRecoversBacklog$' ./integration
```

The existing manual-only `Go` workflow runs the canonical PostgreSQL gate.
No workflow, automatic trigger, dependency, production API or schema change is
needed. Tie runtime results to the exact tested commit; adding the fixture does
not by itself mean that the runtime gate passed.

## What this establishes

This is database connection-outage/reconnection and backlog recovery coverage.
`Run` still reports storage failure; the test explicitly performs the host-owned
restart rather than claiming transparent background worker restart.

It does not stop or restart the PostgreSQL server process, interrupt its storage,
simulate network partitions, prove power-loss durability, measure capacity or
establish sustained soak behavior. The ordinary handler records invocation
identity in memory; this test does not claim exactly-once external effects.
The separate [recipient process-kill fixture](recipient-process-recovery.md)
covers a durable recipient ledger and process loss before ACK.
