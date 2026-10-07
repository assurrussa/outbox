# Remaining outbox audit implementation

Approved scope: additive APIs and explicit opt-in behavior; preserve existing
APIs, backend capability boundaries, fenced ownership, and delivery guarantees.
Each item is a separate reviewable PR. Merge/release approval remains separate.
Atomic enqueue and fenced acknowledgement do not promise exactly once external
effects. Consumers own effect idempotency and reconciliation of ambiguous commits.

## 1. Public identifiers and transactional quickstart (this PR)

- Expose `outbox.JobID` / `outbox.MessageID` aliases, random constructors and
  parsers without requiring consumers to import unstable `shared/types`.
- Preserve type identity, zero-value validation, text/JSON/SQL encoding and
  compatibility with existing repositories.
- Add a runnable PostgreSQL example using the existing runtime and pgx context
  for a business insert plus `Put` in the same transaction.
- Prove successful commit, callback rollback after enqueue, and enqueue failure;
  use unique owned schemas and no broad fixture cleanup.
- Gate: consumer compilation/regressions and quickstart integration execution.
  Live integration remains pending while the shared heavy Mac lane is owned by
  DataGrid. Run the explicit example integration command in its README; neither
  `make check` nor `make test-integration-pgsql` executes that test. No new retry, replay, observer, or backend behavior in this PR.

## 2. Opt-in RetryPolicy independent of lease (next ready item)

Specify a backwards-compatible optional policy for retry scheduling; keep lease
reservation/extension exclusively about ownership. Define attempt numbering,
policy inputs, defaults, delay bounds, jitter/clock injection and cancellation
before implementation. Keep permanent disposition, exhausted attempts, fencing,
unknown capabilities and existing default scheduling behavior intact. Tests must
prove legacy behavior and opt-in backoff without coupling delay to reserve time.

## 3. Observer events for confirmed outcomes

Define events after successful persisted claim/finalization or transaction
commit, with separate failed/ambiguous persistence diagnostics. Do not label a
handler return as acknowledged delivery. Specify ordering, bounded callback
cost, panic isolation, metadata/redaction and shutdown ownership. Test failed
ack/retry/DLQ and stale tokens; observers must not alter worker correctness.

## 4. Safe replay

Define replay authorization, immutable provenance, deduplication identity,
version selection, tombstone retention and operator visibility. Replay should
create auditable work without bypassing current fencing or deleting evidence.
Test duplicate requests, conflicting content, unsupported versions and crash
boundaries. Clarify replay versus retry and host effect idempotency.

## 5. SQLite durability configuration

Document and expose deliberate durability/concurrency choices, connection-local
PRAGMA application and validation; preserve defaults unless separately approved.
Test reopened databases, rollback/crash behavior, busy handling and multi-
connection configuration. State filesystem/WAL/synchronous assumptions and
avoid presenting a throughput setting as a durability guarantee.

## 6. Picodata capability clarity

Make best-effort transaction limits and capability-storage support explicit in
consumer docs/construction diagnostics. Preserve fail-closed atomic DLQ and
unsupported fan-out/runtime boundaries. Test negative capability contracts;
claim full transactional parity only when a connection-pinned atomic boundary
is implemented and verified.

## 7. Large fan-out and fault/soak measurements

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
checks, then complete pending gates before requesting merge. Continue the next
item after branch coordination so dependent changes do not conflict.
