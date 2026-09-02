# Implementation Notes

## 2026-09-02: Add byte-bounded claims within the batch fill deadline

- Added the optional `BoundedBatchJobsRepository` capability. PostgreSQL uses
  one ordered `FOR UPDATE SKIP LOCKED` CTE, while MySQL and SQLite select UTF-8
  payload byte lengths before reserving only the longest admissible prefix.
  Existing custom `BatchJobsRepository` implementations remain compatible and
  retain the singleton collector fallback; Picodata is unchanged.
- Each supplemental bounded or singleton claim uses a child context capped by
  the remaining `MaxWait` window. When that deadline expires while the parent
  Run context is still live, the collector flushes the jobs already gathered.
- Count/byte, Unicode, oversized singleton, ordering, capability, fencing, tail
  attempts, and concurrent non-overlap regressions cover the core and all three
  SQL backends without schema changes.

## 2026-09-02: Add execution-path diagnostic benchmarks

- Added one normalized microbenchmark matrix for the legacy single path,
  true-batch singleton control, and a 100-job true batch. It reports time,
  allocations, claim calls, handler calls, and finalization calls per job.
- Hosted CI runs one smoke iteration only. These in-memory measurements diagnose
  mechanism regressions but do not replace the checkout-local PostgreSQL/NATS
  capacity proof maintained by GoMessenger.

## 2026-09-02: Harden true-batch ordering and finalization

- The collector now re-sorts the completed batch by the repository's durable
  `(available_at, created_at, id)` order before calling `HandleBatch`, covering
  an earlier eligible row that appears between singleton fill claims.
- Top-level transient handler failures now emit one batch-level error record
  with capability, batch size, and durable retry time before the rows are
  deferred.
- Finalization keeps the five-second base budget and adds 25 milliseconds for
  every sequential DLQ insert. Before starting the transaction, the lease
  manager extends every batch row past that deadline so the blocked heartbeat
  cannot let a large DLQ batch expire mid-commit.

## 2026-09-02: Enforce the true-batch fill deadline

- Supplemental batch claims use a child context capped by the remaining
  `MaxWait` window. When that deadline expires while the parent Run context is
  still live, the service flushes the jobs already collected instead of
  waiting indefinitely or failing the worker; any ambiguous extra claim stays
  fenced for lease-expiry recovery.
- Regressions block the second singleton and bounded repository claims until
  their contexts end and verify that the first job reaches `HandleBatch` before
  the parent Run context is cancelled.

## 2026-09-01: True handler batches and atomic unique batch staging

- Added `BatchJob`, keyed partial results, zero-value count/byte/wait limits,
  explicit registration, and optional repository capabilities without widening
  the established `JobsRepository` interfaces. Reservation batch size remains
  prefetch for existing single jobs; `MaxMessages=1` exercises the true-batch
  collector and finalizer.
- One homogeneous batch retains durable order and one lease heartbeat. The
  handler is invoked once, all item outcomes are applied in one fenced backend
  transaction, drain releases an unstarted claimed tail with attempt
  compensation, and structural result defects fail the service closed.
- Review hardening puts every fill claim behind the drain claim lock, adds the
  final handler admission check, and treats Run cancellation after admission
  as an abandoned lease even when the handler returns a successful result.
  Per-worker rotation and batch/single alternation prevent capability
  starvation under a continuously ready queue.
- Follow-up hardening validates every returned row against the requested exact
  capability, starts or expands heartbeat ownership before any byte-tail
  release, treats the handler child-context timeout as a top-level transient
  failure, and stops the fill window after the first durable row cannot fit.
- Review fixes keep the service's result key map immutable even if a handler
  reorders its input slice, leave every admitted row leased after structural or
  finalization failure, preserve pauses established by concurrent workers, and
  claim one collector candidate at a time so `MaxBytes` cannot materialize a
  full `MaxMessages` payload set before selection.
- Added `DeferAt` to single and batch paths. It compensates the claim attempt
  and pauses the exact capability until its durable time. Top-level transient
  batch failures use a separate bounded streak and never consume item attempts;
  durable commit state resolves ambiguous outcomes on redelivery.
- PostgreSQL uses set-based `unnest` finalization. MySQL and SQLite use bounded
  constant-count mixed-outcome statements inside one transaction. DLQ inserts
  go through the configured failed-job repository in the same outer
  transaction, and every backend rolls back on a partial fence match. Picodata
  retains singleton reservation prefetch and `DeferAt`, but does not advertise
  true handler batches because its client cannot provide that atomic boundary.
  Existing migrations are sufficient.
- Added atomic ordered `PutVersionedUniqueBatch` and backend conflict/replay
  tests, mixed batch outcomes, concurrent claims, service-level invocation
  tests, drain/heartbeat/result validation, and race-enabled live integration
  coverage for PostgreSQL, MySQL, and SQLite; Picodata keeps its existing
  singleton reservation coverage.
- This checkout work is intentionally unpublished. No commit, push, tag,
  release, or downstream version pin was performed.

## 2026-08-28: Unified version-aware fenced execution in v0.12.0

- The v0.12.0 release removes the legacy single/unfiltered path and all split
  capability, batch, lease, reschedule, and failed-repository aliases. One
  required `JobsRepository` owns versioned create, exact capability batch
  claim, plural heartbeat/tail release, fenced ack/reschedule, and its maximum
  batch size.
- `limit=1` and larger reservations execute the same service code. One worker
  remains sequential; reservation size changes prefetch, not handler
  concurrency. PostgreSQL, MySQL, and SQLite advertise `1000`; Picodata wraps
  its CAS claim in a one-element slice and advertises `1`.
- Unsupported `(name, schemaVersion)` pairs, including unknown names, are never
  claimed. They remain pending with attempts unchanged and no automatic DLQ.
  Supported permanent/exhausted jobs use versioned fenced DLQ; `RetryAt` always
  uses fenced rescheduling.
- Queue observability is one exact aggregate snapshot grouped by name/schema
  version. It includes UTC observation time and oldest ready time, scans the
  active backlog, and intentionally adds no cache, projection table, top-N, or
  index.
- Picodata 25.2 cannot aggregate `DATETIME` through a conditional `MIN` and
  returns `COUNT`/`SUM` as a numeric type that pgx cannot safely decode to
  `int64`. Its single snapshot query therefore casts counts to `INT` and the
  conditional ready timestamp to RFC 3339 text before the repository restores
  UTC `time.Time`; the live grouped-stats integration test covers this path.
- Existing migrations and schema-v1 defaults are retained. Published v0.11
  tags are not changed; the v0.12.0 core and backend modules follow the
  repository's ordered immutable-tag release workflow.
- Review of the MySQL claim plan found that the availability-only forced index
  examined unsupported rows before applying exact capability filters. The
  corrected path performs one bounded candidate lookup per unique capability
  through the index introduced by `00003`, then conditionally reserves and
  reloads only the winning IDs. A lost concurrent race repeats selection.
- Follow-up review found a check-to-call race between batch cancellation and
  the next handler. Heartbeat failure publication and handler admission now
  share the lease-manager mutex: a failed heartbeat closes admission before
  releasing the mutex, while an admitted job is treated as the active job and
  receives the resulting cancellation.
- Migration `00005` now extends the existing capability index with the full
  `(available_at, created_at, id)` ordering instead of introducing a runtime
  dependency on a separate availability-first index. Pre-`00005` schemas remain
  functionally compatible; the migration removes their supported-backlog
  filesort.
- Before this review correction, verification passed `make prepare` followed by
  `make check-all`, with core race/coverage and live MySQL, SQLite, PostgreSQL,
  and Picodata suites.
  GoMessenger also passed its full workspace-aligned `make check`, including
  the SQLite Outbox-to-JetStream E2E, without changing module dependency pins.
- The correction passed the query-builder regression, live concurrent and
  multi-capability claims, a live default claim after restoring the original
  `00003` index, the full race-enabled `make test-integration-mysql`, and
  `make check`. The first full integration invocation was blocked before its
  database connection by the local network sandbox; the identical canonical
  target passed with loopback access.
- The earlier additive batch notes below describe the branch baseline that this
  unified contract supersedes.

## 2026-08-28: Fenced reservation batches

- The public option is `WithReservationBatchSize(1..1000)` with default `1`;
  the default keeps the existing single-job repository path and custom-store
  compatibility unchanged.
- Batch reservation is additive for capability and legacy execution. One
  worker claims up to the configured maximum with a shared token, executes
  handlers sequentially, heartbeats explicit outstanding job IDs, and keeps
  ACK, retry, and DLQ finalization individual.
- Handler-level outcomes do not stop later jobs. A lease, heartbeat, database,
  or finalization error stops the batch and returns from `Service.Run` after a
  best-effort fenced release of unstarted jobs.
- A graceful release clears unstarted leases and compensates the claim-time
  attempt increment. A crashed process retains those claim attempts. Batch
  transactions never span handler or external broker work.
- PostgreSQL, MySQL, and SQLite are the implementation scope. Picodata retains
  the source-compatible `batch=1` path because its current client cannot offer
  the same atomic batch boundary.
- PostgreSQL uses one ordered CTE `UPDATE ... RETURNING`. SQLite uses a short
  `BEGIN IMMEDIATE` transaction and reapplies its five-second busy timeout on
  every acquired batch connection; the first concurrent race run exposed that
  the pool-level setup had configured only the initial connection.
- The first MySQL concurrent claim test showed the second worker observing no
  jobs even though ten of twenty rows were unclaimed. Exact `EXPLAIN FORMAT=JSON`
  on the full-row claim selected `access_type: ALL` plus filesort. Migration
  `00005_add_batch_claim_index.sql` adds `(available_at, created_at, id)`, and
  the batch query explicitly selects it; the resulting plan is range access
  without filesort and the concurrent plus full MySQL race integration passed.
- Targeted race-enabled integration is green for PostgreSQL, MySQL, and SQLite,
  covering disjoint concurrent claims, capability filtering, partial/empty
  batches, heartbeat/release fences, and transaction rollback. Picodata has an
  explicit integration assertion that batch values above one are rejected.
- After generation and formatting, the maximal `make check-all` gate passed:
  core vet/lint/race/coverage, every backend unit module, and race-enabled
  MySQL, SQLite, PostgreSQL, and Picodata integration with final container
  cleanup. No tag, publication, push, or deployment was performed.

## 2026-08-23: stable v0.11.0 backend release preparation

- The additive core contract was merged and published first as immutable root
  tag `v0.11.0`.
- One release branch pins MySQL, SQLite, PostgreSQL, and Picodata to that exact
  core version and refreshes all four checksum files together.
- `release-ready-backends` now performs the complete mutating pin-and-tidy step;
  `release-readiness-backends` remains the non-mutating standalone pre-tag gate.
- Backend tags remain a post-merge step and must resolve to the verified release
  commit; existing v0.10 tags are not moved.

## 2026-08-23: generic messenger integration foundation

- Started an additive `v0.11` candidate for the new `gomessenger` durable
  transport adapter. Existing `PutVersioned`, fan-out, claim, lease, and DLQ
  contracts remain source-compatible.
- A new unique-put capability reports whether a deduplication key created a
  job or replayed an existing tombstone. Existing unique repository methods
  remain as compatibility wrappers.
- Handler disposition errors distinguish permanent failures from scheduled
  retries. Capability workers keep lease fencing for immediate DLQ and
  reschedule operations; retry timing is persisted instead of sleeping in a
  worker.
- Publication, tags, and downstream module version pins remain separate
  release steps after the full repository and clean-consumer gates pass.

## 2026-07-14: stable v0.10.0 promotion

- Promoted the already verified capability/fan-out contract from
  `v0.10.0-alpha.0` to stable `v0.10.0` without moving or rewriting the
  prerelease tags.
- The root module is released first. Backend `go.mod` files move to the exact
  published stable core only after that root tag resolves, then each backend
  receives its own path-qualified `v0.10.0` tag.
- Picodata keeps its documented limited capability surface; stable versioning
  does not claim full fan-out/runtime parity with PostgreSQL, MySQL, or SQLite.

## 2026-07-12 Full Branch Code Review

- Reviewed the entire branch diff from `origin/master`, including the already
  published core and PostgreSQL prerelease commits. Those commits remain
  ancestors of the branch; rewriting or squashing them would break the release
  ancestry contract.
- Found no unresolved correctness or concurrency defect in capability claims,
  lease fencing/heartbeat, conditional ack, atomic fan-out planning, drain, or
  the PostgreSQL/MySQL/SQLite transaction implementations. Picodata remains
  explicitly fail-closed at the unsupported atomic fan-out boundary.
- Corrected stale release/docs claims that still described PostgreSQL as the
  only capable backend and made MySQL 8.0 an explicit runtime contract.
- Added missing Picodata coverage to pull-request CI. CI now uses the canonical
  backend integration targets, retaining Picodata's required `-p 1` package
  serialization, and waits for the selected database before testing.
- Reworked `check-all` to run `devdown` after success, partial startup failure,
  or test failure. Cleanup failure becomes the result only when all prior steps
  succeeded.
- A fresh full integration run exposed a Picodata `RaftLogCompacted` teardown
  failure even with package serialization and global DDL wait. Added bounded
  exponential retry only for syntactically idempotent create/drop statements;
  non-idempotent or ambiguous DDL remains fail-closed. The failed `check-all`
  run also proved the new cleanup path removed all service containers.
- After the fix, the Picodata integration target passed three consecutive
  fresh race-enabled runs, followed by a green full `check-all`. The final
  standalone backend release gate also resolved core `v0.10.0-alpha.0` with
  `GOWORK=off` for every module and passed tidy/unit checks.
- GitHub's two-core Linux runner exposed a separate Picodata claim deadlock:
  ten workers exhausted the small pgx pool with open SELECT rows, then every
  worker waited for another connection to perform its CAS update. macOS did
  not reproduce it because the default pool scaled with a larger CPU count.
  Claims now buffer at most ten candidates and close rows before updates. The
  integration harness pins `pool_max_conns=2` so this failure mode remains
  covered on every machine.
- The review fixup will be folded only into the final unpushed backend-parity
  commit before branch publication.

## 2026-07-12 Backend Capability And Fan-Out Parity

Request: create a separate task and bring the v0.10 capability/fencing/fan-out
solution to MySQL, SQLite, and Picodata.

Decisions made:

- Started from the published v0.10 core/PostgreSQL line rather than `master`,
  because `master` still represents the v0.9.8 legacy backend surface.
- Created a dedicated task contract in
  `docs/tasks/outbox-backend-parity.md` and a separate implementation branch.
- Require the same observable lease, DLQ, idempotency, fan-out, and drain
  semantics for MySQL and SQLite; dialect-specific SQL is not allowed to
  weaken the public contract.
- Keep Picodata fail-closed for standard fan-out runtime. Context7 Picodata
  connector docs and the installed `picodata-go v1.0.0` source expose
  pool-level queries but no connection-pinned transaction API. The existing
  best-effort transactor cannot honestly prove atomic DLQ or complete fan-out
  planning.
- Picodata may still gain versioned/capability/fenced storage primitives in
  this task. Full runtime parity remains blocked on an atomic backend primitive
  or client transaction support and will be reported explicitly.

Verification baseline:

- Source worktree started clean at the exact published PostgreSQL backend tag.
- Local repo code/docs remain authoritative; the shared wiki only describes
  the older high-level multi-backend shape and will be refreshed after verified
  implementation.

## 2026-07-10 CMS Outbox Foundation

Request: implement the accepted CMS platform plan, starting with the clean
shared outbox prerequisite.

Decisions made:
- Kept the existing `JobsRepository`, `Put`, and legacy worker behavior intact.
- Added a separate opt-in capability repository so current consumers do not
  change unknown-job/DLQ behavior merely by upgrading the module.
- Capability identity is `(name, schemaVersion)`; handlers without an explicit
  version remain schema v1.
- Lease ownership uses a generated token, heartbeat extension, and conditional
  acknowledgement. Constructors still perform validation/wiring only; no
  goroutine starts before `Service.Run`.
- The first vertical slice targets core plus PostgreSQL. Other backends remain
  legacy-compatible and will implement the additive contract in later slices.
- Durable fan-out reuses the existing fenced queue: an immutable source job
  stores the event-time target snapshot and an internal v1 handler creates the
  complete delivery set in one transaction.
- Source and delivery keys have durable fingerprint tombstones outside the
  active jobs table. This prevents replay after ack from recreating a completed
  delivery. Bounded pruning is explicit and host retention-controlled.
- Target delivery capability names are derived from consumer kind plus event
  topic. Each delivery retains the original topic/schema, stable delivery ID,
  and opaque target config/secret revision snapshot.

Verification baseline before edits:
- Core `go test ./...` passed with isolated `GOCACHE` and `GOMODCACHE`.
- PostgreSQL module `go test ./...` passed with the same isolated caches.
- The default global module cache is sandbox-read-only, so implementation
  checks use temporary caches outside the repository.
- Added core unit/race coverage for unsupported schemas, heartbeat extension,
  lost fences, conditional ack, and schema-preserving DLQ.
- Added PostgreSQL migration/repository integration coverage for capability
  filtering, lease extension/deletion, empty capability sets, and versioned
  failed jobs.
- PostgreSQL integration tests passed against the repository Compose service
  with the race detector enabled.
- Fan-out integration coverage kills planning after the first insert and loses
  the source-job fence after the delivery transaction commits. Retry proves a
  complete unique delivery set with no partial or duplicate rows.
- PostgreSQL idempotency coverage proves an active key cannot be pruned, a
  completed tombstone prevents recreation after job deletion, conflicting
  content is rejected, and bounded retention pruning deliberately reopens the
  key only after the caller-provided cutoff.
- Final slice gates passed: core tests, every backend unit module, `go vet`,
  core race tests with five repetitions, core lint with zero issues, and the
  full PostgreSQL integration suite with the race detector.
- The fan-out follow-up also passes the PostgreSQL integration-tag linter with
  zero issues and the full integration suite under the race detector.
- Lease tokens are database-only model state and are excluded from JSON to
  avoid leaking the fencing credential through logs or transport DTOs.

## 2026-06-04 Project Documentation Initialization

Request: initialize project documentation, analyze the repository, fill
`AGENTS.md`, and create focused `docs/` material where useful.

Decisions made:
- Kept project docs in English because the existing README, backend READMEs, and
  examples are English.
- Treated local code and README files as the source of truth, then checked the
  shared outbox wiki page for cross-project context.
- Did not read the real `.env`; used `.env.example`, `compose.yml`, and
  `shared/tests/config.go` for safe documented defaults.
- Created docs that are agent/developer overlays instead of duplicating backend
  README examples.
- Added `MIGRATION.md` because `README.md` already linked to it, but the file
  did not exist in the checkout.
- Recorded the sandbox-specific Go cache issue because `go list ./...` tried to
  write the default user cache outside the writable workspace.
- Confirmed `GOMODCACHE` should not be placed under repository `tmp/`; `go test
  ./...` traverses that downloaded module tree. The temporary cache created
  during verification was moved under `${TMPDIR:-/tmp}` outside the repository.

Tradeoffs:
- `AGENTS.md` now includes command and contract details that overlap lightly
  with `docs/`; this is intentional so agents can work safely after reading only
  the required instruction file.
- `docs/contracts.md` documents current behavior from code and backend READMEs;
  it does not try to replace API reference docs.
- Shared wiki did not need an update during this pass because its current
  outbox summary matched the verified local project shape.

## 2026-06-04 Lint Follow-Up

Request: fix `goconst` issues reported after the documentation commit.

Decisions made:
- Added local test constants for repeated backend driver module paths in
  `shared/tests/dependency_isolation_test.go`.
- Added a local `validSize` constant for repeated validator test size literals.
- Left existing dirty generated/source files outside the follow-up commit
  because they were already modified before this fix and are unrelated to the
  reported `goconst` failures.

## 2026-07-11: Two-Phase Prerelease Gate

- Confirmed the complete workspace check after capability/fan-out changes:
  generation, formatting, vet, zero lint issues, all backend unit modules,
  core race tests repeated five times, and coverage.
- Kept core and PostgreSQL backend publication as two explicit phases because
  their Go modules have separate tags. The backend gate runs with `GOWORK=off`
  and refuses a core version different from the requested exact tag.
- Documented that MySQL, SQLite, and Picodata still expose only the legacy API;
  workspace compilation does not claim capability or fan-out support for them.
- Added the repository-local cache path to `.gitignore` and committed the
  deterministic mock import grouping produced by the canonical generate/format
  pipeline.

## 2026-07-11: Graceful Worker Drain

- Added an explicit, idempotent `BeginDrain` boundary separate from Run context
  cancellation. It closes claim admission while active handlers keep their
  existing contexts and fenced lease heartbeats.
- Serialized the drain transition against repository claim start. Once
  `BeginDrain` returns, no new legacy or capability claim can begin; a claim
  already reserved before the boundary is treated as active work.
- Added capability-mode tests proving the active job can finish and ack, the
  next queued job remains unclaimed, heartbeat continues during drain, and
  draining before Run leaves the queue untouched. A bounded-context expiry
  cancels the handler without ack so the fenced job remains for lease recovery.
- Added a non-mutating structural readiness probe on `Service`. It becomes
  available only after worker loops launch, closes before claim drain, and does
  not reserve a synthetic job; hosts compose it with their database probe.
# 2026-07-11: PostgreSQL Runtime Facade

- Added `backends/pgsql/runtime` as the supported standard composition for the
  PostgreSQL client, legacy/capability/fan-out repositories, failed jobs,
  transactor and core service.
- The runtime implements the worker lifecycle directly and combines database
  plus service readiness. It does not apply migrations; the host keeps a
  separate one-shot migrate role before starting web or worker replicas.

## 2026-07-11: Attempt Metadata Context

- Replaced the job-ID-only handler context value with immutable public
  `JobMetadata` containing the persisted job ID and current claimed attempt.
- Kept `JobIDFromContext` source-compatible and added a fail-closed
  `JobMetadataFromContext` accessor for delivery handlers that must persist
  exact retry history.
- Metadata is attached only after a repository claim; missing IDs, zero
  attempts, and contexts outside a running outbox handler are rejected.

## 2026-07-11: Atomic Capability Registration

- Added `RegisterJobs`/`MustRegisterJobs` to validate a complete handler batch
  before mutating the service capability map.
- Existing-capability conflicts, duplicates inside the batch, invalid schema
  versions, nil jobs and missing capability repositories leave every new job
  unregistered. Single-job registration remains source-compatible through the
  same atomic path.

## 2026-07-11: PostgreSQL DSN Runtime Parameters

- Preserved parsed PostgreSQL startup parameters when `pgsqlinit.Create`
  adapts a DSN into the shared client options. Previously only `sslmode`
  survived reconstruction, silently dropping `search_path`,
  `application_name`, and similar runtime settings.
- Added a defensive copy option and a pool-config regression test for both
  `search_path` and `application_name`.

## 2026-07-11: Core Prerelease Publication

- Ran the complete core pre-tag gate on clean commit `b8e0f43`: generation,
  formatting, vet, zero lint issues, all core/backend unit modules, core race
  tests repeated five times, and coverage passed.
- Published root-module tag `v0.10.0-alpha.0` only after that gate. A separate
  `GOWORK=off` consumer resolution with an isolated module cache resolved the
  tag to the exact commit, so the evidence does not rely on the local
  workspace.
- Updated only `backends/pgsql` to the exact published core prerelease. The
  PostgreSQL backend remains a separately tagged module and must pass its own
  standalone pre-tag gate before its module-path-prefixed tag is created.
- The standalone PostgreSQL gate resolved the published core version with
  `GOWORK=off`, reported no `go mod tidy -diff`, and passed all backend unit
  packages. The full PostgreSQL integration suite also passed under the race
  detector against an isolated test database.

## 2026-07-12: MySQL, SQLite, And Picodata Capability Follow-Up

- Implemented complete capability, fenced lease, schema-preserving DLQ,
  immutable idempotency, and durable fan-out storage contracts for MySQL and
  SQLite. Both backends now provide the same standard runtime composition as
  PostgreSQL.
- Kept SQLite on one pooled connection in its runtime facade. This makes the
  single-writer constraint explicit and prevents application-level worker
  concurrency from being mistaken for parallel SQLite writers.
- Implemented only versioned create/failed rows, capability-filtered CAS claim,
  heartbeat, and conditional leased delete for Picodata. The installed
  `picodata-go v1.0.0` pool exposes no connection-pinned transaction API, so
  Picodata deliberately does not implement `FanoutJobsRepository` and has no
  standard runtime facade.
- Picodata 25.2 accepts additive `ALTER TABLE ... ADD COLUMN` but does not
  support dropping columns. Migration 00003 therefore uses nullable additive
  columns plus explicit backfill. Its one-step down is a harmless data no-op;
  full reset removes the columns when migrations 00002/00001 drop the tables.
- Because Picodata cannot attach defaults to added columns, repository reads
  treat null `schema_version` as v1 and a null lease as the nil token. This
  preserves expand-first compatibility when an old process inserts a row after
  migration 00003 while new and old workers overlap.
- Serialized Picodata integration packages with `go test -p 1`. A process-local
  migration mutex cannot protect separate package test binaries, and concurrent
  distributed DDL produced `RaftLogCompacted` failures. Picodata table drops
  and additive alters also use `WAIT APPLIED GLOBALLY`, which prevents the next
  test/migration from racing a DDL operation that returned before cluster-wide
  application.
- Made `make devup` runnable without a local `.env`: Compose and Make now carry
  local-only integration defaults and wait for all three database services.
  Added `-count=1` to integration targets so database evidence cannot be
  satisfied from the Go test cache.
- Added a non-mutating `release-readiness-backends` gate that verifies the exact
  published core version, tidy state, and `GOWORK=off` unit tests for every
  backend module.

## Development gate efficiency (2026-07-31)

- Preserved the workspace, backend, integration, and release target names.
- Split generation/format/lint fixes into `make prepare`; `make check` now
  verifies source and runs core race+coverage once plus each backend once.
- Kept five-run core race stress and HTML coverage as explicit diagnostics.
- Added ignored repository-local Go and linter caches so sandboxed runs do not
  fail or serialize work around user-level cache permissions.
- 2026-08-27: added explicit PostgreSQL relay-pool sizing to the standard
  runtime. `0/0` retains the existing `5/10` defaults; partial, non-positive,
  and `min > max` configurations fail before opening a connection. The same
  ordering check now lives in the low-level `pgsqlclient.PoolOptions` contract.
- Documented the host-owned split-pool pattern: relay repositories may execute
  staging inside a producer-owned `pgx.Tx` carried in context, while
  `Runtime.Close` owns only the relay pool. This preserves atomic staging and
  allows a fixed producer/relay connection budget without changing core or
  other backend interfaces.
- Profiled the capability reservation statement on disposable PostgreSQL 17.9
  after `ANALYZE`, with `EXPLAIN (ANALYZE, BUFFERS, WAL, FORMAT JSON)` and three
  rollback-safe repetitions at 0, 1,000, and 10,000 eligible jobs. With
  realistic distinct `available_at`/`created_at` timestamps, the current schema
  used `jobs_available_at_index` plus an incremental sort. Its median execution
  time was 0.093/0.209/0.191 ms and the warm 1k/10k plans both used 30 shared
  buffer hits, so backlog size did not cause material sort or scan growth.
- A temporary `(name, schema_version, available_at, created_at, id) INCLUDE
  (reserved_at)` candidate was selected by the planner and removed the sort. At
  10k it reduced median execution time from 0.191 to 0.139 ms and warm shared
  hits from 30 to 19, with the same six WAL records. Because the required
  size-dependent regression was absent, no forward migration replaces the
  existing capability-claim index in this iteration.
