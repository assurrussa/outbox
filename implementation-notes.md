# Implementation Notes

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
  during verification was moved to `/private/tmp/outbox-gomodcache-doc-init`.

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
