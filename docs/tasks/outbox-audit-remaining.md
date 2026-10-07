# Remaining outbox audit implementation

Approved scope: additive APIs and explicit opt-in behavior; preserve existing
APIs, backend capability boundaries, fenced ownership, and delivery guarantees.
Each item is a separate reviewable PR. Merge/release approval remains separate.
Atomic enqueue and fenced acknowledgement do not promise exactly once external
effects. Consumers own effect idempotency and reconciliation of ambiguous commits.

## 1. Public identifiers and transactional quickstart (merged PR #33)

- Expose `outbox.JobID` / `outbox.MessageID` aliases, random constructors and
  parsers without requiring consumers to import unstable `shared/types`.
- Preserve type identity, zero-value validation, text/JSON/SQL encoding and
  compatibility with existing repositories.
- Add a runnable PostgreSQL example using the existing runtime and pgx context
  for a business insert plus `Put` in the same transaction.
- Prove successful commit, callback rollback after enqueue, and enqueue failure;
  use unique owned schemas and no broad fixture cleanup.
- Gate: consumer compilation/regressions and quickstart integration execution.
  Completed on reviewed source `f7a5b7a` after lane handoff: explicit live
  example integration passed without skips on owned PostgreSQL 17.9, the
  runnable command passed, and full `make check` passed. Both invocations left
  zero fixture schemas; the owned container was removed. Neither `make check`
  nor `make test-integration-pgsql` executes the example integration test. No new retry, replay, observer, or backend behavior in this PR.

## 2. Opt-in RetryPolicy independent of lease (merged PR #34)

Implemented a core policy interface/function adapter and bounded exponential
constructor with optional injected jitter. Policy inputs are public job ID,
capability, counted one-based attempt and error. RetryAt takes precedence;
Permanent, DeferAt, exhaustion, success and cancellation bypass policy. True
batch top-level errors retain existing no-attempt defer behavior. Nil policy
preserves both existing single and batch defaults. Negative delay/panic fails
closed. Completion-clock and jitter tests are deterministic; lease ownership,
fencing and attempt accounting remain unchanged. Source/light gates precede
heavy-lane handoff and one full check; no observer/replay implementation.
One full `make check` passed on source `ce9c08a`, including core race/coverage,
standalone backend tests and example builds. Final head `ac64ee2` passed all
nine CI checks after the MySQL test used confirmed ACK synchronization;
merged as `aef520d`.

## 3. Observer events for confirmed outcomes (merged PR #35)

Implemented an optional nonblocking channel observer with no-op nil default,
caller-owned buffering/lifecycle and no runtime telemetry callbacks. Per-job
ACK, retry/defer and atomic DLQ events follow successful persistence; true batch
events follow the whole transaction. Failed or ambiguous commits emit no
success. Lease loss is one group-level signal, not individual ownership proof.
Metadata excludes payloads, tokens and error/reason/context data. Full/closed
sinks drop events without delivery changes. Deterministic tests cover single and
batch storage/commit failures, heartbeat/fence loss, non-atomic DLQ, sink failure
and public consumer wiring. One full `make check` passed on source `f43d990`,
including core race/coverage, standalone backend tests and example builds.
No containers were needed. Parent review included the confirmed collection-count
correction and manual-only CI transition; merged as `b0b2b58`. No replay implementation.

## 4. Safe replay (approved bounded PostgreSQL implementation)

Owner approved retrying the same business operation, preserving its identity
and failed record, without intentionally creating a new effect. Host-owned
authorization is sufficient; no generic authorization framework is required.
Implement a PostgreSQL-only ordinary-job API with a persisted immutable request
ID and explicit host admission of the original exact capability and consumed
business key. Copy payload/queue/capability unchanged into fresh zero-attempt,
unleased work; atomically retain request/source/new-job provenance. Repeated
requests return retained records after ACK. Active sources, conflicting reuse,
unsupported exact handlers and built-in fan-out fail closed. No worker changes,
admin routes, automatic replay, pruning defaults or other-backend fallback.

Provenance reads support host audit and legacy handler identity resolution;
fresh queue IDs never become new business-effect keys. Test duplicate/concurrent
requests, unsupported inputs, admission, source activity, persistence rollback,
commit-response ambiguity before/after commit, retained evidence and guarded
migration Down. Source preparation precedes a coordinated owned PostgreSQL
gate and one necessary local readiness aggregate; draft review follows checks.

## 5. SQLite durability configuration (merged PR #36)

Deterministic regressions reproduced missing connection-local settings on pool
growth, discarded replacements and zero-idle pools. Configure each physical
connection before pooling, preserve defaults and add explicit validated
NORMAL/FULL synchronization through storage/runtime. Backend-owned PRAGMAs
take precedence over DSN values; other parameters remain intact. Check effective
WAL/memory journal mode without rejecting expected memory behavior. Cover
reopen/rollback, mode validation, DSN precedence and configuration-failure
cleanup. Document filesystem/power-loss assumptions; no power-loss measurement
or durability guarantee is inferred from clean reopen tests. No schema changes.

Replay owner semantics are approved in item 4. Picodata ping-failure cleanup and
capability GoDoc are complete in PR #37. SQLite final gates and parent review
are complete.

Final local `make check test-integration-sqlite` passed on source `8a2a7e1`,
including core race/coverage, standalone backend tests, example builds and
SQLite integration race tests using owned temporary files. The heavy lane was
released explicitly. No power-loss test,
hosted CI dispatch, tag or deployment was performed.

PR review correction requires no main database file for both effective MEMORY
and OFF. On corrected source `5537c4d`, affected storage/runtime tests and lint
plus SQLite integration race tests passed; the owned file/unix-dotfile MEMORY
reproduction ran without skipping. Genuine memory compatibility remains intact.
Reuse the earlier unrelated full gate. Parent independently approved the
correction; merged as `ab4d5eb23f4a7ae7b1f95f57c0bb840f6f889692`.

## 6. Picodata capability clarity (merged PR #37)

Implemented cleanup of the newly owned pool when construction ping fails and
clarified legacy transaction names in GoDoc/consumer docs. Synthetic
tests cover ownership transfer/failure, callback context/error behavior and
existing fail-closed construction limits without network fixtures. No client
protocol, repository SQL, public signature or capability expansion.

One local `make check` passed on source `9744020`, including core race/coverage,
standalone backend tests and all example builds. Parent approved the source and
documentation-only merge resolution; merged as
`f4ec804674a2cc7108f87ee0d5434a06ec0a308e`. CPU lane returned explicitly; no
Picodata container integration was needed for the unchanged network protocol
and SQL.

Best-effort transaction limits and capability-storage support are explicit in
consumer docs/construction diagnostics. Preserve fail-closed atomic DLQ and
unsupported fan-out/runtime boundaries. Test negative capability contracts;
claim full transactional parity only when a connection-pinned atomic boundary
is implemented and verified.

## 7. Large fan-out and fault/soak measurements

This is optional characterization, separate from the documented
[release-readiness gates](../../RELEASING.md), and awaits a measurement request.

After correctness gates, measure large fan-out, worker/target skew, restart,
lease loss, database outage and ambiguous acknowledgement on owned fixtures.
Record configuration, hardware, versions, dataset, durations, distributions,
resource cost and raw results. Assert no lost persisted work, fenced mutations,
bounded resource use and documented duplicate effects. Coordinate the heavy
lane first; no registry pushes, production access or broad Docker cleanup.

## Execution and review

Run source and focused light checks during shared-lane contention. Schedule
live integrations, race/full gates and fault/soak work only after lane
coordination. Report exact commands and skips; compile-only tests do not prove
transactional behavior. Open a draft PR for parent review after permitted
checks, then complete pending gates before requesting merge. Further
implementation is limited to approved item 4. Release and measurement work
require their own request, scope and lane coordination.
