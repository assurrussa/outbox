# Outbox Backend Capability And Fan-Out Parity

Status: implemented, fully reviewed, and verified; backend release tags pending

## Goal

Bring the additive outbox v0.10 capability, fenced lease, conditional ack,
durable fan-out, and standard runtime contracts from PostgreSQL to the other
backend modules without weakening backend-specific guarantees.

Target modules:

- `backends/mysql`;
- `backends/sqlite`;
- `backends/picodata`.

The baseline is root module `v0.10.0-alpha.0` and PostgreSQL backend
`backends/pgsql/v0.10.0-alpha.0`.

## Required Contract

For a backend to claim full parity it must prove:

1. persisted positive `schema_version` for active and failed jobs;
2. capability-filtered claim that leaves unsupported jobs untouched;
3. a fresh non-zero lease token on every claim;
4. heartbeat and ack conditioned on the current unexpired token;
5. schema-preserving DLQ plus leased delete in one real transaction;
6. immutable idempotency tombstones for source and delivery keys;
7. complete fan-out planning in one real transaction;
8. crash-before-commit leaves no partial delivery set;
9. crash-after-commit-before-ack cannot duplicate deliveries;
10. runtime readiness, idempotent `BeginDrain`, and no claim after drain.

## Backend Matrix

### MySQL

Full parity is required. `database/sql` provides connection-bound
transactions, while `SELECT ... FOR UPDATE SKIP LOCKED` provides claim
serialization. Idempotency registry and active-job insert must share the same
transaction.

### SQLite

Full parity is required. Claims use a serialized write transaction and a
compare-and-set update. Fan-out and DLQ use the existing real SQLite
transaction manager. Production guidance keeps worker concurrency bounded
because SQLite remains a single-writer database.

### Picodata

`picodata-go v1.0.0` exposes pool-level `Exec`, `Query`, `QueryRow`, and
`SendBatch`, but no connection-pinned `Begin`, `Commit`, or `Rollback` API.
Therefore:

- versioned create, capability-filtered claims, heartbeat, and conditional
  leased delete may be implemented and tested as single-statement/CAS storage
  primitives;
- the backend must not expose a standard capability/fan-out runtime while
  schema-preserving DLQ plus delete and complete delivery planning cannot be
  committed atomically;
- best-effort callback execution is not accepted as full fan-out parity.

Closing Picodata full parity requires either a connection-pinned transaction
API from the client/backend or a backend-owned atomic finalization/batch-plan
primitive added to the core contract and implemented by Picodata.

## Verification Matrix

Fast gates:

```sh
make test-core
make test-backends
make test-race-core
```

Backend integration gates:

```sh
make test-integration-mysql
make test-integration-sqlite
make test-integration-pgsql
make test-integration-picodata
```

Standalone module gate after an exact core tag exists:

```sh
make release-verify-backends
```

Full parity integration cases for MySQL and SQLite mirror PostgreSQL:

- unsupported schema remains pending with attempts unchanged;
- lease heartbeat rejects stale tokens;
- stale ack cannot delete a reclaimed job;
- failed delivery preserves schema version;
- partial fan-out planning rolls back completely;
- retry after lost source ack produces one complete unique delivery set;
- drain stops new claims while the active handler keeps its heartbeat.

Picodata integration cases prove only the storage primitives currently
available and explicitly assert that standard fan-out runtime construction is
not offered. The canonical Picodata target uses `go test -p 1` because
distributed DDL is serialized by the database and separate Go package test
binaries otherwise race while creating and dropping test tables.
The suite also inserts post-migration rows using the legacy column set and
proves capability-aware readers treat them as schema v1 with no active lease.
DDL that Picodata permits to wait (`DROP TABLE` and `ALTER TABLE`) uses
`WAIT APPLIED GLOBALLY`; the full Picodata integration target passed twice
consecutively after this gate was added.

## Release Order

1. Implement and verify MySQL and SQLite against the local v0.10 core.
2. Implement and verify safe Picodata capability primitives and the fail-closed
   runtime boundary.
3. Run the complete workspace and integration matrix.
4. Publish the next root core tag only if a core contract change is required.
5. Pin each backend module to the exact published core tag.
6. Run `GOWORK=off` standalone checks.
7. Publish independent backend module tags.
8. Update consuming BOMs only after clean consumers resolve those tags.

## Verification Evidence (2026-07-12)

Passed from the isolated `tasks/outbox-backend-parity` worktree:

- `make fmt`;
- `make vet`;
- `make lint` with zero issues;
- `make test-core`;
- `make test-backends`;
- `make test-race-core` (`-race -count=5`);
- `make test-integration-all` against MySQL 8.0, PostgreSQL 17, SQLite,
  and Picodata 25.2.2 with fresh `-count=1` executions;
- `make release-readiness-backends CORE_VERSION=v0.10.0-alpha.0` with
  `GOWORK=off`, exact core-version resolution, tidy diff checks, and standalone
  unit tests for all four backend modules;
- `git diff --check`.

MySQL and SQLite additionally prove immutable tombstones survive active-job
ack, reject conflicting replay, and reopen a key only after explicit bounded
pruning. Picodata proves only its advertised safe capability primitives and a
negative `FanoutJobsRepository` boundary.

No backend-parity tags were created. The implementation branch is published
for review before independently versioned MySQL, SQLite, and Picodata backend
module tags are considered. Picodata's tag must be described as
capability-storage support, not full fan-out parity.

## Full Branch Review (2026-07-12)

The review covered the complete `origin/master...tasks/outbox-backend-parity`
range, including core lifecycle/concurrency, fan-out/idempotency, all backend
repositories and migrations, runtime facades, integration tests, CI, release
gates, and public documentation.

Review fixes:

- aligned `AGENTS.md`, the root README, and `RELEASING.md` with the implemented
  MySQL/SQLite full-parity and Picodata capability-storage-only boundaries;
- documented the MySQL 8.0 claim contract used by the implementation and
  integration image;
- added Picodata unit and serialized integration coverage to pull-request CI;
- changed `check-all` so `devdown` runs even when startup, a check, or an
  integration test fails, preserving the original failure unless cleanup is
  the only failure;
- split service readiness into focused Make targets so CI can wait for only
  the selected backend profile.
- added a bounded retry for Picodata's transient `RaftLogCompacted` response on
  provably idempotent `CREATE TABLE IF NOT EXISTS` and
  `DROP TABLE IF EXISTS` migration statements. Ambiguous DDL such as
  `ALTER TABLE ... ADD COLUMN` still fails closed instead of risking a
  duplicate or partially applied schema change.
- fixed a Linux CI pool-starvation deadlock in Picodata claims. The repository
  previously kept candidate `Rows` open while acquiring another connection for
  CAS update; ten workers could occupy every small pool and wait forever. It
  now buffers at most ten candidates, closes the query rows, and only then
  attempts CAS updates. Picodata integration tests force a two-connection pool
  so this invariant is independent of runner CPU count.

No unresolved correctness or concurrency findings remain after these changes.
Published core and PostgreSQL tag commits stay in branch history; only the
final, previously unpushed backend-parity commit may be amended during review.

Final review evidence:

- `make check-all` passed after the retry fix, including generation, format,
  vet, zero lint issues, core/backend unit tests, core race tests repeated five
  times, coverage, all four fresh integration suites under the race detector,
  and successful Compose cleanup;
- the Picodata integration target passed three additional consecutive fresh
  runs under `-race -p 1` before the final full matrix;
- `make release-readiness-backends CORE_VERSION=v0.10.0-alpha.0` passed for all
  four standalone modules with `GOWORK=off`, exact version resolution, tidy
  checks, and unit tests;
- `git diff --check` passed and public docs contain no machine-local paths.

The first GitHub Picodata integration job then exposed the small-pool claim
deadlock described above while all other eight jobs passed. Its stack trace was
used as review evidence; final GitHub CI must pass after the fix before this
task is considered merge-ready.
