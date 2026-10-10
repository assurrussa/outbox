# Implementation Notes

## 2026-09-05: PR 29 PostgreSQL fan-out test completion

- Reproduced the CI four-row failure by delaying the retry claim for 500ms
  while retaining the old 300ms service deadline. The dispatcher remained
  beside three deliveries; decoding it caused the secondary snapshot error.
- The retry now waits for a successful persisted ACK, cancels and joins Run,
  then asserts the exact delivery count and unique IDs. A 10s timeout bounds
  failure only; the injected first ACK loss still must return ErrLeaseLost.
- Kept the delayed claim as a deterministic regression and made the delivery
  count assertion fatal before payload decoding. Runtime and workflow code
  are unchanged; no jobs or matrix entries were added.
- Validation: old deadline fails the regression; `make fmt-check-backends
  test-integration-pgsql` passes against an isolated PostgreSQL under race.
  gopls MCP cannot resolve this temporary workspace, so compilation/vet from
  the package test command provides the code diagnostics.

## 2026-09-05: Lazy lease bounds and historical MySQL upgrade

- Base: `d853a50`, branch `upgrade-logic-lib`. Keep public APIs, dependencies,
  migration 00006, reservation bounds and finalization budgets unchanged.
- Remove per-deletion minimum scans by retaining a conservative lease bound;
  refresh when expiry/admission/finalization needs a stronger bound.
- Document guarded manual recovery of completed MySQL keys from a trusted
  external journal, with tests for the unrecoverable-history boundary.
- Expand the core benchmark matrix to short/long leases and prefetch scaling;
  compare identical harnesses in ten alternating before/after pairs.
- New lease regressions and the exact documented MySQL repair statement pass,
  including stale-byte/fingerprint guards and the lost-history boundary.
- `make prepare` and `make check` pass; core race coverage is 83.0%. gopls MCP
  reports no diagnostics for existing edited files. Its snapshot did not see
  the new test file, so a fresh CLI `gopls check` with local Go caches verified
  the full edited core file set without diagnostics.
- `make test-integration-all` passed all four backend suites under the race
  detector. Test containers were stopped before the performance comparison.
- Ten alternating benchmark pairs passed acceptance: at 5m, prefetch 100/1000
  time per job fell 27.15%/80.44% with unchanged extension counts and no
  significant control slowdown. Full reproducible evidence is retained in
  `docs/performance/lease-bound-20260905.md` and its linked artifacts.
- Synchronized the shared Outbox contract and added an immutable verification
  snapshot. Wiki lint passed with zero errors and 21 existing stale-source/page
  warnings. Documentation links and measured-source checksums were verified.
- PostgreSQL/NATS load campaigns remain in the separate GoMessenger work.

## 2026-09-05: Follow-up code review

- Reproduced two remaining defects: a finalizer could wait past its deadline
  on the heartbeat mutex, and a successful claim racing cancellation discarded
  known unstarted rows without compensating their attempts.
- Finalizers now acquire the lease ledger with their existing deadline and
  cancel the batch heartbeat if that wait expires. Public interfaces are intact.
- All claim paths share the same lease deadline and cancellation cleanup.
  Confirmed live rows with matching tokens are released after leaving the drain
  claim lock. Expired, foreign, and ambiguous failed claims are left fenced.
  A fill timeout cannot hide a cleanup failure.
- Reproduction tests failed before the fixes and pass afterward. Additional
  regressions cover all three claim paths, token/expiry filtering, duplicate
  IDs, drain during cleanup, and normal/error/partial cleanup after MaxWait.
- Verification passed `make prepare`, gopls (no diagnostics), `make check`, and
  one complete `make test-integration-all` with the race detector on all four
  backends. Local contracts and the shared Outbox wiki were synchronized with
  a new immutable evidence snapshot. Wiki lint passed with zero errors and the
  existing 21 stale-source/page warnings.

## 2026-09-05: Delivery review corrections

- Base: `6128bf7`, branch `upgrade-logic-lib`. Preserve public Go interfaces,
  dependencies, published migrations, and the existing batch fill deadline.
- Add exact MySQL identifier storage in migration 00006; Down explicitly fails.
  Application rollback retains the new schema and existing idempotency records.
- Share cancellation-independent SQLite cleanup across manual immediate
  transactions; discard connections whose transaction state is uncertain.
- Track confirmed lease deadlines, bound claims, guard handler admission, and
  cover all outstanding rows for one shared finalization budget.
- Arm the host shutdown timer before BeginDrain. Add deterministic regressions
  and update local contracts plus the shared platform page after validation.
- Lease extensions round their confirmed deadlines upward to milliseconds so
  SQLite storage and the core ledger agree on the protected interval.
- Verification passed `make prepare`, gopls diagnostics (none), and `make check`
  including the existing late-nil timeout and tail-compensation regressions.
- Lost-ACK recovery tests must wait for the persisted dispatcher `ReservedAt`,
  since finalization now extends ownership beyond the initial reservation.
  Adjusted the corresponding MySQL, SQLite, and PostgreSQL fixtures.
- All four full race-enabled integration targets passed. After correcting the
  lost-ACK fixtures, `make test-integration-all` completed MySQL and SQLite;
  the remaining PostgreSQL and Picodata suites passed through
  `make test-integration-pgsql test-integration-picodata`. Implementation sources
  did not change after the successful source gate.
- Synced the shared `platforms/outbox` page and added a sanitized raw validation
  snapshot. Corrected the stale MaxWait description: supplemental claims already
  use a remaining-fill child deadline in the base commit. Shared startup, graph,
  source, link, and PII checks passed through `bash scripts/lint-streams.sh`
  (zero errors; 21 pre-existing stale-page/source warnings).
- Changes remain uncommitted and unreleased. No production migration was run.

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

## 2026-09-04: Review recommendations and patch release v0.13.1 preparations

- Fixed `PruneJobIdempotencyKeys` in MySQL and SQLite backends to reference the
  configured `r.tableName` instead of hardcoded `jobs`.
- Added strict SQL identifier validation `^[A-Za-z_][A-Za-z0-9_]*$` for custom table
  names in MySQL and SQLite `repo.New(...)`.
- Refactored true-batch panic handling in `executeBatchHandler` and `executeExecutionBatch`:
  panics are captured into `*HandlerPanicError` along with `debug.Stack()`. Panics do NOT
  convert to `BatchJobOutcomeDefer` or decrement/compensate attempts. Leases are cleared
  from memory via `manager.forgetAll()` for expiry-based recovery, and the error terminates
  `Service.Run` fail-closed.
- Introduced `TransactionCapabilities` interface (`SupportsAtomicDLQ() bool`) in `outbox`.
  Picodata transactor implements `BestEffortRunner` returning `false`. `outbox.New` rejects
  transactors without atomic DLQ unless explicitly permitted via `WithAllowNonAtomicDLQ()`.
- Exported `outbox.DefaultJob` in the core package and aliased `shared/job.DefaultJob` to it
  with deprecation notices.
- Updated `README.md` canonical example to use `errgroup.WithContext(ctx)` instead of ignoring
  `svc.Run` errors with `_ =`, and switched to `outbox.DefaultJob`.
- Fixed placeholder substitution in PostgreSQL query prettier to substitute placeholders in
  descending numerical order, preventing prefix collision with double-digit parameters (e.g. `$10`).
- Removed default `CORE_VERSION ?= v0.12.0` in `Makefile` to prevent accidental version down-pinning.
- Aligned Go version in GitHub Actions workflow with `go.mod` via `go-version-file: go.mod` to the CI workflow.

## 2026-09-04: Second review resolution (P1 & P2 fixes, targeting v0.14.0)

- **Single-Job Handler Timeout & Panic Semantics**:
  - `executeJob` now checks `context.Cause(handlerCtx)`. When a single-job handler exceeds
    `ExecutionTimeout()` and returns `nil`, `executeJob` returns the deadline exceeded cause
    rather than treating it as success.
  - In `processBatchJob`, deadline exceeded error does NOT call `ackBatch` (`DeleteJobWithLease`).
    If attempts reached `MaxAttempts`, it moves to DLQ via `dlqBatch`; otherwise the lease
    is forgotten in memory to be recovered upon lease expiration, and attempts count is preserved.
  - Single-job panic handling adheres to the verified bounded-retry/DLQ contract: panics
    are captured as `"panic in job %q: %v"` errors, retrying up to `MaxAttempts` before DLQ routing,
    without crashing the worker loop (`TestRun_PanicInJobHandler_DoesNotCrashAndMovesToDLQ`).
  - True-batch panic handling uses `*HandlerPanicError` with full stack traces (`debug.Stack()`),
    forgets leases for expiry-based recovery without attempt compensation, and terminates the worker loop fail-closed.
  - Added regression test in `outbox/service_single_timeout_regression_test.go`.

- **Picodata Standalone Build Fix (`GOWORK=off`)**:
  - Removed compile-time assertion `_ coreoutbox.TransactionCapabilities = (*BestEffortRunner)(nil)`
    from `backends/picodata/storage/transaction/manager.go`. The method `SupportsAtomicDLQ() bool { return false }`
    remains, satisfying the core interface via structural typing while allowing standalone builds
    under `GOWORK=off` against core `v0.13.0`.

- **Fail-Closed `TransactionCapabilities`**:
  - Enforced `TransactionCapabilities` in `Options.Validate()`: transactors must implement
    `TransactionCapabilities` or callers must pass `WithAllowNonAtomicDLQ()`.
  - Added `SupportsAtomicDLQ() bool { return true }` to MySQL, SQLite, and PGSQL `transaction.Manager`.
  - Added comprehensive core unit tests in `outbox/service_options_test.go` covering all 5 capability scenarios.

- **Transaction Manager Panic Hardening**:
  - Updated MySQL, SQLite, and PGSQL `RunInTx` / `runTransaction` to safely roll back and return
    an error retaining the recovered value and the full stack trace (`debug.Stack()`), ensuring
    diagnostic information is never lost while maintaining compatibility with callers expecting error returns.

- **Table Identifier Validation & Quoting**:
  - Implemented centralized `ValidateAndQuoteTableName` in `package repositories` for MySQL,
    SQLite, and Picodata, shared by both `jobsrepo` and `jobsfailedrepo` within each backend module.
  - MySQL: quotes identifiers with backticks (`` `table` `` or `` `db`.`table` ``), allowing SQL
    reserved words (e.g. `select`, `order`) and schema-qualified names up to 64 chars per part.
  - SQLite: quotes identifiers with double quotes (`"table"` or `"schema"."table"`), allowing SQL
    reserved words and qualified names up to 1000 chars per part.
  - Picodata: quotes identifiers with double quotes (`"table"`).
  - Added live integration test `TestMySQLCustomTablePruneJobIdempotencyKeys` and dedicated unit tests in
    `repositories/table_test.go` for all three backends.

- **Documentation & CI**:
  - Updated `README.md` and `RELEASING.md` to reference `v0.14.0`.
  - Added detailed migration instructions in `MIGRATION.md` for `v0.13` -> `v0.14`.
  - Added Makefile targets `test-backends-standalone` and `test-examples`.

## 2026-09-04: Third review resolution (P1 multi-table prune, P2 example atomic DLQ, documentation, and CI gates)

- **Configurable Idempotency Keys Registry (P1 Fix)**:
  - Added functional options to MySQL and SQLite `jobsrepo`: `WithJobsTable(string)` and `WithIdempotencyTable(string)`.
  - Parameterized all idempotency table queries in MySQL and SQLite `jobsrepo`:
    `registerUniqueBatchKeys`, `createJobVersionedUnique`, and `PruneJobIdempotencyKeys` now use `r.idempotencyTableName`.
  - Added `WithFailedJobsTable(string)` to `jobsfailedrepo` in MySQL, SQLite, and Picodata.
  - Documented explicit multi-table idempotency rule in `MIGRATION.md` and `docs/contracts.md`: exactly one active jobs
    table owns a given idempotency keys registry. Pruning across multiple active jobs tables sharing the same registry
    is unsupported; each active table must configure a dedicated idempotency table via `WithIdempotencyTable(...)`.
  - Added live integration tests `TestMySQLMultiTableIdempotencyIsolation` and `TestSQLiteMultiTableIdempotencyIsolation`.

- **Example Atomic DLQ Contract Realism (P2 Fix)**:
  - Updated `examples/base-app` `stubRepo.SupportsAtomicDLQ()` to return `false`, accurately reflecting its lack of transactional rollback.
  - Added `outbox.WithAllowNonAtomicDLQ()` in `examples/base-app` service initialization.

- **Documentation Alignment (P2 Fix)**:
  - Corrected `MIGRATION.md` to accurately distinguish between single-job panic behavior (bounded retry up to `MaxAttempts` -> DLQ, worker loop continues) and true-batch panic behavior (`*HandlerPanicError` fail-closed worker termination with stack trace).
  - Fixed `README.md` quick-start example error handling: checked error on `svc.Put` and handled `group.Wait()` error.

- **Verification Gates & CI Workflow (P2 Fix)**:
  - Updated `Makefile` `test-examples` to run with `GOWORK=off`.
  - Connected `test-backends-standalone` and `test-examples` directly to `make check`.

- **Contracts Documentation Alignment**:
  - Added `TransactionCapabilities` interface (`SupportsAtomicDLQ() bool`) and explicit fail-closed construction rules to `docs/contracts.md`.
- **Examples Verification Scope**:
  - Maintained `examples/*` compilation check within local `make check` / `test-examples` without adding a dedicated CI workflow step for now.
  - Updated PR #25 description to accurately state that `examples/*` verification is integrated into local `make check`.

- **Graceful Shutdown & Decoupled Run Context in Example (P2 Fix)**:
  - Updated `README.md` canonical consumer example to decouple the signal context (`sigCtx`) from the worker execution context (`runCtx`).
  - Signal notification triggers `svc.BeginDrain()` while in-flight jobs keep their lease heartbeats active.
  - The worker context (`runCtx`) is canceled only after a bounded drain deadline (`time.AfterFunc(10*time.Second, cancelRun)`), ensuring graceful completion under normal conditions and fail-safe termination if a job exceeds the drain budget.
  - `select` waits on both `sigCtx.Done()` and `groupCtx.Done()` so that early startup or fatal worker errors unblock immediately without hanging.

- **Single Backend Traversal in `make check` (P1 Fix)**:
  - Updated `Makefile` `check` target to depend on `test-full-core` instead of `test-full`.
  - Avoids running backend tests twice (once via `test-full -> test-backends` and once via `test-backends-standalone`), preserving the documented single backend traversal contract while retaining standalone `GOWORK=off` verification.


## 2026-10-02: Callback-exit and transaction-cleanup audit

- Fixed all three atomic backend transaction managers committing on explicit
  `runtime.Goexit`; owned commits now require normal callback completion.
- PostgreSQL rollback now uses a bounded uncancelled context. Borrowed
  transaction ownership, panic recovery, and commit/rollback error chains stay
  unchanged. SQL commit failures retain database/sql's already-done behavior.
- Worker callback Goexit is an uncommon explicit abnormal exit, distinct from
  an ordinary returned error. Run now cancels peers, joins heartbeat cleanup,
  and reports ErrWorkerGoexit. Claimed work remains for lease-expiry recovery.
  A private Run-context notifier now cancels peers before deferred heartbeat
  joins, so slow heartbeat cleanup cannot delay sibling cancellation. Normal
  processing paths mark completion before their explicit heartbeat stop;
  returned errors and recovered handler panics retain their existing behavior.
  Deterministic two-worker tests hold cleanup open for both single and true-batch
  handlers, checking peer cancellation, heartbeat joining, and caller isolation.
- Regression tests use synthetic drivers/repositories only; no production
  queues or databases are touched. The disk-full workspace required temporary
  validation in /tmp; final aggregate gates are recorded separately.


## 2026-10-06: Additive listing cursors and Stats compatibility

- Added backend-local `PageCursor` structural aliases and `ListPage` to MySQL,
  SQLite, and Picodata active/DLQ repositories. This avoids making standalone
  backend builds depend on an unpublished core API; core pins stay at v0.16.0.
  The aliases share an API shape, not a cross-database cursor value contract.
- Kept `ListPaged` unchanged and documented its equal-timestamp omission and
  deprecation. No PostgreSQL listing API, new migration, retry framework,
  worker algorithm, replay, or observer was added.
- Bounded new pages to 1000 rows (default 10). Cursor timestamps retain each
  backend's stored precision; the DLQ tie-breaker is the failed-row primary ID.
- Added `Service.QueueStats` as a delegating compatibility method, preserving
  the existing `Stats` interface and `GetQueueStats` implementation.
- Integration regressions exercise 12 equal-time rows across a 10-row page,
  multiple adjacent timestamp groups, deterministic ordering, serialization,
  timezone equivalence, custom tables, defaults, end cursors, and cancellation.
  Each backend's standalone consumer compile test checks both API signatures.
- Local validation uses the available disposable services. Hosted CI remains
  required for backend/version combinations not available in the task runtime;
  SQLite results are not a substitute for MySQL or Picodata integration.

- Canonical `make check` exposed pre-existing standalone example drift: all four
  DB examples still required core v0.15.0 while their backend modules required
  v0.16.0. Aligned only those core requirements; no other dependency, checksum,
  replacement, or toolchain change was needed.
- Picodata pgx scans can return `time.Local` for a UTC instant. Pagination
  regressions compare UTC-normalized instants without rounding fractions,
  preserving the existing scanner behavior rather than asserting Location
  pointer identity.

- The full Codex review found that case-sensitive text ordering could repeat an
  uppercase SQLite ID after `JobID` parsing canonicalized the cursor. Applied
  `LOWER(id)` consistently in WHERE and ORDER BY for SQLite and MySQL (whose
  CHAR(36) ID can also inherit binary collation). Mixed-case distinct UUID
  regressions cover active/DLQ and custom tables; MySQL custom fixtures use
  binary collation. This does not rewrite stored rows or change legacy listing.
- Documented the text-store identity boundary: standard hyphenated UUID text,
  unique by logical UUID value. Added a SQLite fixture showing that two case
  aliases of one UUID at one timestamp collapse to the same public cursor;
  such ambiguous historical data needs normalization/deduplication first.

## 2026-10-07: Public IDs and transactional quickstart

- Isolated branch from approved master `ec405124`; source checkout left untouched.
- Public ID aliases and random/parse constructors preserve underlying identity
  and encoding. No broad `shared/*` API promotion or backend behavior change.
- Added a PostgreSQL runtime quickstart under the existing example module; each
  invocation owns a unique schema and uses embedded migrations. Commit and
  callback rollback share pgx transaction context with `Put`. Integration tests
  additionally exercise enqueue persistence failure.
- Business row/job invariants: successful callback commits both; callback error
  after enqueue or Put error persists neither; failed calls return a zero ID.
  No workers are started, so queue inspection cannot race with acknowledgement.
- Remaining work is sequenced in `docs/tasks/outbox-audit-remaining.md`.
- DataGrid owns the heavy Mac lane: live database/race/full checks deferred;
  source and focused light checks only. No containers started or cleaned up.

Validation for this bounded PR:

- `go test ./outbox -count=1` passed after mock regeneration.
- Focused `golangci-lint run --timeout=3m ./outbox/...`: zero issues.
- Compile-only `go test` for `outbox/...` and all four backend module trees
  with `-run '^$'` passed; no backend integration was executed.
- `GOWORK=off go test ./outbox -run TestPublicID -count=1` passed.
- PostgreSQL quickstart tagged integration compilation and standalone example
  module compilation passed; `go vet -tags integration ./transactional` passed.
- `TestBusinessWriteAndPutAtomicity` explicitly skipped without `OUTBOX_PG_DSN`.
- Touched-file gofumpt/gci checks and `git diff --check` passed.
- Live quickstart integration and `make check` remain pending lane coordination.

## 2026-10-07: Quickstart enqueue-failure review correction

The original integration scenario incorrectly assumed `Put` rejects an empty
job name. Existing validation rejects nonpositive schema versions, not empty
names. Replaced the scenario with a test-owned PostgreSQL CHECK constraint
rejecting its payload and require SQLSTATE `23514` through error wrapping.
The business insert succeeds first; the enqueue SQL fails and the test asserts
that neither new row survives rollback and no usable job ID is returned.
No public validation or runtime behavior changed. Live PostgreSQL verification
remains deferred while DataGrid owns the heavy lane.

The transaction docs now distinguish an outermost commit from nested callback-
only reuse, and require propagating nested errors. The demo helper is called
without an attached transaction. The example's live integration requires its
explicit documented command; it is not part of `make check` or
`make test-integration-pgsql`. Compile-only validation does not satisfy that gate.

Correction validation: tagged quickstart compile-only check and tagged `go vet`
passed using shared caches; `git diff --check` passed. Live database execution
was not attempted while the heavy lane remains occupied.

## 2026-10-07: Final bounded PR validation after lane handoff

All requested gates passed on reviewed source `f7a5b7a`:

- Explicit `go test -tags integration ./examples/base-app-pgsql/transactional
  -count=1 -v` passed with no skips on an owned PostgreSQL 17.9 fixture. Commit,
  callback rollback, and CHECK-constraint enqueue failure (SQLSTATE `23514`)
  all passed.
- `go run ./examples/base-app-pgsql/transactional` passed and printed the
  confirmed business-row/job commit and rollback result.
- Fixture inspection after both invocations found zero `outbox_quickstart_*`
  schemas. The owned container and its disposable volumes were removed; no
  owned container/network or background Go process remained.
- Full `make check` passed with shared-cache overrides: formatting, vet, zero
  lint issues, core race/coverage, standalone backend tests, and standalone
  example builds. This does not claim the broader backend integration matrix.
- Docker could not bind-mount this workspace through Compose. Only the failed
  owned Compose fixture was removed; validation used a uniquely named owned
  container without a host mount on local port 55483. No broad Docker cleanup.

The heavy lane is released for the next sequential project window. Source is
unchanged from reviewed head; this follow-up records evidence only. Draft PR
remains for parent review/merge, with no tags, deployment, or feature expansion.

## 2026-10-07: Opt-in retry policy design

New bounded branch starts from merged master `5195675`. The policy computes
only a delay from public immutable attempt metadata (job ID, capability,
claimed one-based attempt and error). Scheduling accepts an explicit `now`
internally for deterministic tests; lease clocks/extension remain untouched.

Transition matrix:
- nil policy + ordinary single failure: existing lease-expiry recovery;
- configured policy + ordinary retryable single/item failure: fenced persisted
  reschedule from completion time, retaining the counted attempt;
- RetryAt: explicit timestamp wins over policy, clamped to completion time;
- success, Permanent, DeferAt, exhausted attempts or cancellation: existing
  paths; no policy invocation;
- true batch top-level failure: existing no-attempt capability defer/streak,
  excluded from counted-attempt policy;
- invalid negative delay or policy panic: fail closed, no retry mutation;
- stale fence during reschedule: ErrLeaseLost; no attempt/fence bypass.

Provide a small policy interface/function adapter and bounded exponential
constructor with optional caller-supplied jitter. Jitter and custom policy
callbacks must be concurrency-safe and promptly return; the runtime contains
panics. Deterministic tests inject completion time and jitter without changing
worker/lease clocks. No observer, replay, schema or backend change. Source/light
checks only while GoUploads owns the heavy lane; request handoff before full
check or containers. No repeated previously passed gates.

Retry-policy source/light validation:
- New deterministic policy and in-memory single/batch regressions passed.
- Affected core package traversal `go test ./outbox -count=1` passed.
- Focused source lint `golangci-lint run --timeout=3m ./outbox/...` passed with
  zero issues after correcting new test formatting.
- Self-review confirms only retry-time selection changes; default lease
  recovery, batch no-attempt deferral, disposition/attempt precedence and
  current fenced finalization remain in place. No backend/schema/facade edits.
- `git diff --check` passed. One full `make check` is pending explicit lane
  release; no full/race/container checks were started during GoUploads ownership.

Retry-policy final gate: one authorized `make check` passed on unchanged source
`ce9c08a`, using existing shared caches. Formatting, vet, zero lint issues, core
race/coverage, all four standalone backend tests and standalone example builds
passed. Checkout remained clean and no containers were used. Later lane
reassignment does not require repeating this passed gate; final source evidence
is reused exactly. This evidence-only update is the sole subsequent repo change.
Draft PR is for one final parent review; no merge, tags, deploy or package
publication is authorized in this task.

## 2026-10-07: PR 34 MySQL fan-out completion regression

CI head `80363f2` failed the unchanged partial-planning retry test: three
committed deliveries remained beside the dispatcher at attempt 2 when its
300ms run deadline cancelled before acknowledgement. The retry policy is nil
and the successful dispatcher path is unchanged from baseline `5195675`.

A controlled 500ms delay after durable fan-out commit reproduced the exact
four-versus-three failure once on baseline `5195675` and once on candidate
`80363f2`, both under race on owned MySQL 8.0.46. The fix is test-only: wait for
a positive persisted dispatcher ACK, cancel and join Run, then keep the exact
delivery-count and unique-delivery assertions. The 10s deadline bounds failure
only. Retain the delayed commit response as a deterministic regression proving
completion is not inferred from elapsed wall time. Runtime/CI workflow and
rollback assertions remain unchanged.

The fixed targeted test passed three consecutive race runs without skips.
Fixture inspection found zero remaining TestMySQLSuite databases; the owned
container/volumes and diagnostic baseline worktree were removed. Final CI on
the new exact head is required before merge. Previously passed core/runtime
source gates on `ce9c08a` are reused; no unrelated full local rerun.

## 2026-10-07: Persisted-outcome observer design

Separate branch from merged master `aef520d`. Use an optional best-effort channel
sink, not runtime-executed telemetry callbacks: nonblocking sends, no owned
observer goroutines/queues, no-op nil default. Callers own bounded buffering,
consumption, telemetry errors/panics and channel shutdown after Run joins.
Closed sinks are contained and full sinks drop events; concurrent caller close
is outside the ownership contract. Values contain only public identifiers,
capability, counted attempt, UTC timestamps and outcome kind, never payload,
lease token, handler error/reason text or caller context values.

Transition matrix:
- ACK/retry/defer events only after successful fenced repository mutation;
- DLQ committed only after successful atomic transactor return, never inside
  its callback; no committed-DLQ event for explicitly non-atomic DLQ mode;
- true batch outcomes only after all mutations and outer transaction succeed;
- failed/ambiguous persistence or failed commit: no success event;
- ErrLeaseLost: one group-level event at reservation operation exit, without
  asserting individual row ownership loss; no per-job false ACK/retry/DLQ;
- ordinary legacy retry leaving the lease to expire: no persisted-retry event;
- channel full/closed/nil: no delivery/lease/error behavior change.

No external callback runs in a worker or lease critical section. The observer
is telemetry, not a durable audit log or replay mechanism; no exactly-once
notification promise. Configure through core construction; backend facade/API
and schemas remain unchanged. Add deterministic single/batch persistence and
commit-failure regressions, then the required one full check and final review.

Atomic-DLQ capability is cached during option validation, preserving the
existing construction decision and avoiding external capability callbacks from
telemetry emission. New deterministic observer/option regressions passed.
Nil/full/unbuffered/closed sinks preserve confirmed ACK behavior; no observer
generates errors that enter the delivery path. Privacy and ambient-transaction
assumptions are explicit in the contract. Final required gate remains one full
`make check`, without containers, before draft PR/final review.

Validation: focused observer/option tests and lint passed. The single required
`make check` passed on exact source `f43d9903b3923f2d903e0a06ec4c43d0a207d133`:
read-only formatting, core/backend vet, core lint (zero issues), core race with
coverage, standalone backend tests, and all five example builds. No local live
backend integration or container fixture was needed for this core-only change.
The subsequent evidence update changes documentation only; reuse this source
gate rather than repeat it. Heavy lane released before draft PR/final review.

Final review found a collection-loss telemetry count bug: a supplemental claim
could be added before a later fill error, but the observer retained the initial
count. Capture the returned collection length before the fill-error branch and
include selected rows already added to the lease manager before byte-tail
release. This only corrects known group claim metadata; collection, admission,
fencing and cleanup behavior remain unchanged. Deterministic regressions for
supplemental claim loss and byte-tail release loss both reproduced `1` instead
of `2` on the prior source, with no handler admission or persisted success.
Source/focused validation precedes the coordinated final full gate; GoUploads
currently owns the heavy lane. Prior-head CI does not validate this correction.
Focused `go test ./outbox -run '^TestObserver' -count=1` passed after the fix;
focused core lint reported zero issues, formatting and diff checks passed.

After GoUploads released the heavy lane, one final local `make check` passed on
exact corrected source `01a879d5e1994ccf4aec00860fcd7303bbd11b99`: formatting,
core/backend vet, zero-issue lint, core race/coverage, standalone backend tests
and all five example builds. No fixture or container was started. This subsequent
evidence update changes documentation only and reuses that passed source gate.
The correction remains unpushed: automatic CI publication requires separate
parent coordination under the user's GitHub Actions minutes constraint. No
workflow settings or dispatches were changed. Heavy lane released again.

## 2026-10-07: Explicit manual-only CI transition

User explicitly authorized manual CI, with automatic launches requiring
separate agreement. Change only the `Go` workflow's trigger block from push/PR
to `workflow_dispatch`; retain jobs, commands, matrices, runner settings and
the absence of manual inputs. Inventory contains one workflow and no
workflow_run, schedule, workflow_call or alternate automatic path. Each manual
launch retains the same nine jobs and therefore still consumes runner minutes.
No workflow dispatch/rerun or cancellation is authorized or performed here.

Validate YAML and exact unchanged job content locally. Reuse the final passed
`make check` on observer source `01a879d`; this transition changes no Go source
or check commands. Earlier green nine-job CI at `e2b5c6c` predates the collection
count correction and must not be described as final-head CI. Default-branch
automatic configuration remains until reviewed merge, so publication/transition
may still produce an old-config run. Manual launch availability depends on the
configuration reaching the default branch; no branch protection is bypassed.

## 2026-10-07: SQLite per-connection durability configuration

Separate branch from merged master `b0b2b58`. Before repair, deterministic real
SQLite tests reproduced foreign_keys=0, busy_timeout=0 and synchronous=2 on
second, discarded replacement and zero-idle connections instead of the backend
settings 1/5000/1. No timing assumptions or shared fixtures were needed.

Use a database/sql Connector to configure each physical connection after the
registered driver opens it. Retain that existing driver (including caller
registered functions/collations); install no global hooks and do not rewrite
the DSN. Backend-owned settings follow the driver-applied DSN configuration and
therefore consistently take precedence for journal_mode, foreign_keys,
busy_timeout and synchronous, matching prior startup precedence. Other DSN
parameters survive; invalid driver DSN configuration still fails before the
backend settings. Preserve WAL/NORMAL/five-second wait/foreign-keys-on defaults
and existing storage/runtime pool limits. Typed NORMAL/FULL selection is opt-in.

Read effective journal mode rather than treating successful PRAGMA execution
as proof of WAL. Accept legitimate memory journals; file/WAL refusal fails with
the observed mode. Failed connection initialization closes/discards its physical
connection, retaining configuration and cleanup errors. Initial construction
validates configuration even with ping disabled. Reopen/rollback tests do not
simulate power failure; documentation states WAL/NORMAL and filesystem limits.

Focused storage/runtime behavior tests passed. Touched-package lint is being
completed; recursive lint also found existing unrelated transaction-test issues,
which this bounded PR does not change. GoUploads confirmed heavy lane free and
queued its validation after our one final local gate. No hosted CI dispatch,
schema changes, Picodata implementation or replay mutations in this PR.

Final source review also preserved an explicitly OFF memory journal: WAL cannot
replace it, so allow it only after SQLite confirms no main database file; do
not accept OFF file storage. An intermediate raw-row double close failed the
memory regression, then was corrected by separating journal reading from
validation, with exactly-one-close assertions. Final memory/connector focused
tests passed and touched storage/runtime lint reports zero issues. The one
required local aggregate is `make check test-integration-sqlite`, adding the
existing SQLite race integration target to validate fenced claims and
transaction cleanup under the connection change; no containers are required.

Final local aggregate `make check test-integration-sqlite` passed on exact source
`8a2a7e1a772ce3a311d32c874842de8e233055bf`: formatting, core/backend vet,
zero-issue core lint, core race/coverage, standalone backend tests, five example
builds and SQLite integration race tests. SQLite integration has no service
availability skip path and ran against test-owned temporary files. The heavy
lane was explicitly released to GoUploads after completion; no background Go
process, container or fixture remains from this gate. This evidence update is
documentation only and reuses the passed source gate. Publication is a draft
for parent review; no hosted CI dispatch/rerun, tag or deployment was performed.

## 2026-10-07: SQLite review correction for file-backed MEMORY journals

Independent PR review identified that effective journal mode MEMORY can also
belong to a file when its VFS cannot enter WAL. Check the main database filename
for both MEMORY and OFF; accept either only when it is empty. WAL still succeeds
directly. Add an unconditional fake-backed MEMORY/file rejection and a real
pinned-driver unix-dotfile VFS probe using an owned temporary file. The real
probe skips only when the requested VFS does not exist on the platform, so do
not claim that reproduction unless it executes. Genuine memory compatibility
and exactly-once raw result close assertions remain required.

Source was prepared while GoUploads owned the heavy lane; correction checks
were deferred. Coordinate only affected storage/runtime checks, lint and SQLite
integration race tests after its explicit release; reuse prior unrelated gates.
Picodata source remains saved on its independent branch and unpublished. Hold
SQLite publication/merge until correction evidence and final review complete.

GoUploads explicitly lent the idle CPU lane with its make scheduler paused and
owned fixtures already cleaned. On correction source `5537c4d2339de098a480e5d8856c809e1aefdf71`,
storage/runtime tests passed, touched-package lint reported zero issues and the
existing SQLite integration race target passed. The real unix-dotfile VFS test
ran without skipping on the pinned driver: a file-backed database retained
MEMORY after a WAL request and the corrected backend rejected it. Genuine
memory/MEMORY and memory/OFF cases passed. No unrelated full gate was repeated.
This subsequent evidence edit changes documentation only. Picodata's one local
gate follows sequentially, then the CPU lane returns explicitly to GoUploads.

## 2026-10-07: Picodata construction cleanup and capability documentation

Independent branch from merged master `b0b2b58`; no dependency on the unmerged
SQLite durability PR. The constructor owns a newly created pool until optional
ping succeeds (or remains disabled). Ping failure/cancellation must close that
pool once and preserve the wrapped original error. Success/disabled ping retain
the pool for caller-owned client cleanup. Use a private Ping/Close seam with an
owned fake rather than replacing a global factory or changing the public client.

Legacy transaction names retain signatures and behavior. GoDoc and consumer
docs describe callback-only best-effort execution, no installed transaction,
no rollback on callback error and no atomic business-write/enqueue boundary.
WithTx selects an externally owned query executor only. Negative construction,
single-row maximum and absent batch/fan-out capabilities remain fail-closed.
Add synthetic tests for these boundaries, plus cleanup in the existing successful
constructor test. No network protocol, SQL, dependency, migration or capability
change. GoUploads owns the current heavy lane; source preparation only, with one
necessary local gate deferred until an explicitly coordinated release.

GoUploads explicitly lent its idle CPU slot with the owned make scheduler
paused, anonymous downloads continuing and PG/MinIO fixtures already cleaned.
SQLite's affected correction gates ran first on its own branch. Picodata's one
`make check` then passed on source `9744020be4be2230195a7f39208c939a5108b5be`:
formatting, core/backend vet, zero-issue core lint, core race/coverage, standalone
backend tests (including new Picodata ownership/consumer capability tests) and
five example builds. No protocol/SQL behavior changed, so no Picodata container
integration was added. The CPU lane was explicitly returned after completion;
no background Go process or fixture remains from this gate. This evidence
update changes documentation only. Draft publication precedes parent review;
no hosted CI dispatch/rerun, capability expansion, tag or deployment.

## 2026-10-07: Bounded PostgreSQL ordinary-job replay

Owner approved retrying the same business operation while retaining its identity
and failed evidence, not intentionally creating a new business effect. Branch
starts from merged master `08e7266`. Implement only a PostgreSQL `replay` package;
no generic backend fallback, core worker changes, routes or automation. Keep
public backend-local ID aliases compatible with the supported core aliases
without requiring an unpublished core tag for standalone backend compilation.

Replay takes an immutable request UUID, failed-row UUID and an explicit small
host admission function. Admission confirms the exact original capability is
supported, authorizes this operation under host policy, and returns the stable
business-effect key already consumed by the handler. The SDK cannot infer that
key from opaque payloads. This is a callback contract, not an authorization
framework. Reject nil admission, built-in fan-out, invalid source and nested
caller-owned transactions; own one existing PostgreSQL transaction manager.

Lock the failed row to serialize requests for that source. Repeat the same
request by returning retained provenance after admission, including after ACK;
conflicting source/content/business-key reuse fails. A new request rejects the
original source job or an earlier replay of that failed row still in active
jobs. Copy payload, capability and queue unchanged into a new zero-attempt,
unleased job under a distinct replay request key using existing unique enqueue.
Commit new work, its idempotency tombstone and provenance in one transaction.
On failed/ambiguous commit return no confirmed result; callers repeat the same
request, never invent a new effect identity to resolve uncertainty.

Add one additive provenance migration. Keep failed evidence referenced and
retain request provenance after job deletion; no pruning API/default. Guard
migration down when recorded requests exist instead of discarding evidence.
Expose provenance reads by request and new queue-job ID for host audit and
legacy handler identity resolution. Direct SQL privileges remain host-owned;
the API supplies no tamper-proof audit claim or exactly-once external guarantee.

Transition cases: invalid/nil admission or unsupported fan-out => no writes;
missing/active source => no staging; first admitted request => atomic fresh
work/provenance; same request => prior result without recreation; conflicting
request => no writes; queue/provenance/commit failure => rollback or unresolved
commit with zero confirmed result; cancellation => existing cleanup contract.
Test live commit ambiguity and request concurrency on unique owned PostgreSQL
schemas. AuthHub minpassword owns the current lane, then GoUploads' short
cutoff correction; all Outbox work remains source-only until explicit release.

After explicit GoUploads release, source `bb6a89e` passed touched-package
integration-enabled lint (zero issues), owned PostgreSQL 18.6 replay race tests
without skips, and one `make check` (core race/coverage, standalone backends,
example builds). The first lint finding required the consumer example's empty
Output marker; it was corrected before these passing gates. Narrow cleanup
removed the recorded-ID/owner-label container and found zero replay schemas.
The heavy lane was explicitly returned to AuthHub for its affected correction.

Final source review strengthens the same failed-row invariant for distinct
request IDs: they must preserve its first admitted operation/business key too,
even after ACK. Existing row locking serializes the baseline check. Add changed
key/operation assertions to the PG contract matrix. This correction is
source-only until AuthHub explicitly releases; then repeat only affected replay
lint, standalone consumer/unit checks and owned PG race validation. Preserve
the earlier unrelated full readiness evidence; do not repeat its aggregate.

AuthHub explicitly returned the short corrective window. On final code/test
source `ee0e8b0670b52cb24cd69b47edff60cbea5021dd`, integration-enabled replay lint
reported zero issues, `GOWORK=off go test -count=1 ./replay` passed against the
unchanged published core dependency, and the owned PostgreSQL 18.6 replay race
contract passed without skips. Changed capability/key under a distinct request
after ACK failed closed; the Down test confirmed the actual retention guard
message. The corrective container was removed by recorded ID plus owner label,
with zero remaining replay schemas; the lane was explicitly released. Final
evidence/consumer-navigation edits are Markdown only. No full aggregate repeat,
hosted CI launch, tag, deployment, production operation or retention mutation.

## 2026-10-07: Replay downgrade concurrency review correction

Independent PR39 review found that EXISTS before the destructive lock could
see an empty journal while the first replay remained uncommitted. DROP would
then wait, resume after that commit, and destroy its new provenance. Move
ACCESS EXCLUSIVE acquisition before EXISTS in migration 00005 Down, with lock,
check and DROP in the same migration transaction.

Verified pinned goose v3.26.0 source: internal/sqlparser/parser.go defaults
useTx=true (only NO TRANSACTION opts out); migration.go passes that flag to
runSQLMigration; migration_sql.go uses one db.BeginTx for SQL statements and
store.DeleteVersion, rolls back errors and commits both together. This migration
has no NO TRANSACTION annotation.

Add a deterministic real-PG interleaving: pause actual Replay at its pre-commit
boundary after provenance insertion, confirm zero rows visible outside it,
start actual goose Down on a distinct named connection, observe its ungranted
AccessExclusiveLock blocked by that replay PID, then permit commit. Guarded Down
must retain provenance, source, queued work and version 5. A test-owned copy
with only the new lock removed must reproduce the original successful Down and
lost committed provenance. A separate owned version-deletion trigger fails after
DROP, proving DDL rolls back with version bookkeeping; empty Down then succeeds.
Coordination uses channels and actual pg_locks/pg_blocking_pids, not elapsed
sleeps. Cancellation cleanup joins all test goroutines before closing pools.
Source-only while AuthHub RegistryUI has its reservation; only affected static
and owned PG migration/replay gates are authorized, with prior full evidence
retained. No production downgrade or broad check rerun.

After AuthHub's explicit short handoff, exact correction source
`6f1bcae7dd598a6577f1c87ed0da97a50c90a6f7` passed integration-tag vet for
replay/migrator, new-code lint with zero issues, and the affected PostgreSQL
18.6 migration/replay race gate without skips. The unguarded control reproduced
lost committed provenance; the fixed interleaving retained its record and
version 5. Version-update failure restored dropped DDL; empty Down reached
version 4. Cleanup removed the recorded-ID/owner-labelled container with zero
replay schemas left. Lane explicitly returned to RegistryUI; no Go/fixture
remains. Prior full gate evidence is preserved without rerun. Subsequent changes
are evidence Markdown and the existing draft update only.


## 2026-10-09: Caller-owned PostgreSQL database/sql unique staging

Add the bounded `jobsrepo.NewSQLTxPutter(*sql.Tx)` producer requested for an
existing Inbox/business transaction. Concrete transaction binding prevents
implicit pgx-context/pool fallback. Construction only rejects nil; it does not
issue SQL. The adapter never opens, begins, commits, rolls back or changes
search_path. Hosts own valid PostgreSQL transaction/database/schema selection,
savepoints, commit outcome and relay binding. A valid schema in the wrong
database cannot be detected by this adapter.

Extract the existing single-event validation, CTE, fingerprint and result/error
handling into one private helper used by both producers; batch fingerprinting
uses the same unchanged function. No schema, dependency, interface, worker or
pgx transaction-manager change. Existing pgx calls retain their operation name
and SQL arguments; SQL no-row conflicts map to the existing identity sentinel.

Tests cover nil/zero producers, input rejection before SQL, driver/scan/context
errors, exact arguments, one caller connection/transaction and no finalization.
Owned-schema PostgreSQL tests cover business/job/key visibility before commit,
commit and rollback, sql.ErrTxDone, pgx/SQL idempotency parity, all fingerprint
conflict fields, post-ACK replay, wrong search_path without fallback, statement
failure rollback and caller savepoint recovery. The SQL test pool has one
connection; separate pgx observers verify visibility.

Source-only preparation through repository connectors. No checks have run for
this candidate yet; the coordinated cloud validation lane must run formatting,
standalone backend tests, affected lint/vet and the real PostgreSQL race cases
with OUTBOX_PG_DSN set to its owned disposable fixture. Missing-DSN skips do not
constitute PostgreSQL evidence. No CI trigger, release, tag or deployment change.


### Validation correction: preserve existing capability-name semantics

The first canonical `make check` on source `00d7cd02` under real Go 1.27.2
failed in `TestSQLTxPutterValidatesBeforeQuery/empty_capability`: the new test
expected an error that the published contract does not require. Verified
`outbox/capability.go` at both core v0.16.0 and the current source: Validate
rejects only nonpositive SchemaVersion. The existing jobs schema allows empty
names and the pgx unique producer already delegates to that same validation.

Remove the incorrect rejection case and add positive unit plus PostgreSQL
cross-producer empty-name identity regressions. Production code, SQL and core
validation stay unchanged. This correction has only a stdin formatting check;
tests must run in the coordinated cloud lane. The canonical aggregate remains
failed until validated again, and real PostgreSQL gates have not yet run.


### Scoped lint correction after successful test continuation

On source `c74e6439`, PostgreSQL and Picodata standalone tests, all five
example builds and integration-tag PostgreSQL vet passed. Backend lint found
ten issues in the new SQLTx test/example files plus sixty unrelated baseline
findings. Correct only the new files; keep production code and shared SQL
unchanged, and leave unrelated findings for their own work.

Use the external test package and a fixed exact fingerprint compatibility
vector instead of reaching into the private helper. Retain the live PostgreSQL
cross-producer and all fingerprint-field parity checks. Deduplicate test
literals, use equivalent test-driver struct conversions, and order imports
as standard/default/backend-local module. The executable example retains its
normal existing-transaction staging closure and demonstrates deterministic
nil rejection without database I/O; the README's success-path guidance stays.

Source verification used stdin gofumpt, gci with the backend module prefix
equivalent to localmodule, and gofmt. No tests or lint were run in this source
correction. Resume affected package/example tests, tagged vet and scoped lint
in the validation environment. Owned PostgreSQL race tests remain unrun.

## 2026-10-10: Recipient effect and process-kill recovery fixture

Add one SQLite integration test for the delivery side-effect/ACK boundary,
complementing the existing dispatcher lost-ACK tests. The parent owns two
WAL/FULL temporary files, an eight-target event and a bounded child worker.
A test-only repository wrapper allows dispatcher ACK, then pauses delivery ACK
after the separate recipient ledger/business counter has committed. Kill the
worker process, reopen both files, and restart the real service without editing
leases. Assert the persisted lease is live at restart, the retried handler
starts after that deadline, exact targets/payloads, one duplicate committed recipient attempt,
one business effect per delivery, queue drain and empty DLQ. Recipient identity
and business mutation share one recipient transaction; Outbox still provides
at-least-once delivery, not arbitrary exactly-once external effects.

No production code, API, dependency, migration or workflow change. No fanout
capacity or database-server-outage claim. The own working environment supports
format/source review but cannot create Go build temporaries. Publish only as a
draft after independent review; validate through the existing manual Go workflow
before merge. The fixture guide records commands and interpretation; runtime
validation must be reported separately for the exact tested commit.

## 2026-10-10: Bounded synthetic fanout planning baseline

Existing fanout correctness fixtures use small target sets; the sole existing
benchmark characterizes execution scheduling, not fanout cardinality. Add one
core benchmark with fixed synthetic 10/1,000/10,000 target inputs and a 1 KiB
event. Separate enqueue preparation from dispatcher materialization; reuse the
existing callback test transactor and discard emitted deliveries after counting.
Exact identity/content/order preflight is excluded from timed samples. Do not
infer storage, worker, recipient, capacity, tail-latency or peak-memory results.

The manual Go workflow gains an opt-in baseline-only selector, default false,
with fixed iteration/time budgets, focused race/vet/new-code lint, and raw
source/environment evidence. No automatic triggers, production code, dependency,
API, database fixture or optimization changes. The own working environment can
format/review source but cannot create Go build temporaries; runtime evidence
must come from the explicitly selected manual hosted lane for the exact head.
Reuse prior unchanged correctness constituents rather than repeating heavy
backend/full-suite gates for this benchmark-only source addition.

Manual focused run 89 passed on source `2ad4da37468f97a11edcd7e9004c6dfc87d51439`
with Go 1.26.8: selected fanout race tests, all six race smoke rows, vet,
zero-issue new-code lint and 30 bounded non-race samples. Existing full-core
and backend job groups were correctly skipped. The exact artifact was retrieved
and SHA-256 verified; preserve its four raw text files and summary manifest.
At 10,000 targets, median enqueue preparation was 3.900252 ms/event and dispatcher
materialization was 58.612782 ms/event, with median allocation churn 2,976,420
and 43,042,256 bytes/event respectively. These are synthetic sink costs, not
storage/recipient capacity or peak memory. Subsequent changes are evidence-only;
retain the exact source gate and prior unchanged correctness constituents.
