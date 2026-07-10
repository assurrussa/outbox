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
