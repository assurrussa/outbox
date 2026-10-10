# Synthetic fanout planning baseline

## Scope

This is a bounded core microbenchmark of the existing `PutFanout` and dispatcher
implementation, with a counting sink and callback-only transactor. It does not
measure database inserts/commits, unique-key lookup, worker claims, ACK, queue
drain, recipient effects, network I/O, skew, latency distributions, or durable
whole-system capacity. The existing execution-path benchmark measures a different
1,000-job scheduler workload and is retained unchanged.

The production dispatcher validates/copies/sorts the complete target snapshot
and serially serializes one delivery per target inside one transaction. No
production recipient-cardinality data is available in this repository, so the
following labels are synthetic measurement points, not observed traffic:

- Small: 10 recipients per event
- Medium: 1,000 recipients per event
- Large: 10,000 recipients per event

All points use one fixed event identity/time, a 1,024-byte valid JSON event
payload, one webhook kind, fixed-width reverse-ordered target IDs and the same
14-byte target configuration. Reverse ordering exercises normalization/sorting,
but is not a worst-case ordering; the sorter may recognize descending runs.
No storage idempotency behavior is simulated: repeating the event in the
counting sink repeats preparation rather than taking a persisted replay path.

## Work and metrics

`BenchmarkFanoutPlanning` separately measures:

- `enqueue`: `Service.PutFanout` normalization, sorting, snapshot serialization
  and one sink call per event.
- `dispatch`: decoding that snapshot, normalization, stable delivery identity,
  delivery serialization and one sink call per target, in one callback-only
  transaction. It reuses the existing test transactor rather than adding a
  second transaction fixture.

Input construction and an exact identity/content/order preflight are outside
the timers. The preflight decodes every emitted delivery and checks its target,
event, schema, availability and deduplication key. Timed iterations count sink
calls and encoded payload bytes; the sink retains only the latest payload,
never an accumulated delivery set. Its fixed returned job ID is synthetic and
does not measure ID allocation.

Standard `ns/op`, `B/op`, and `allocs/op` describe one event. Custom
amortized `ns/target`, `targets/op`, `repo-calls/op`, and `encoded-B/op` make
cardinality and work explicit. Encoded bytes are bytes passed to the sink, not
memory or network bytes. Allocation totals include transient work, not peak
resident memory. Five fixed-iteration samples support a median/range summary,
not p95/p99 latency, confidence in a capacity frontier, or a regression verdict.

## Bounded commands

From the repository root on Linux with GNU `timeout` available:

```sh
GOMEMLIMIT=256MiB GOMAXPROCS=1 go test -race -count=1 \
  -run '^Test(PutFanout|Fanout)' ./outbox
GOMEMLIMIT=256MiB GOMAXPROCS=1 timeout --kill-after=10s 180s go test -race -run '^$' \
  -bench '^BenchmarkFanoutPlanning$' -benchtime=1x -count=1 \
  -cpu=1 ./outbox
GOMEMLIMIT=256MiB GOMAXPROCS=1 timeout --kill-after=10s 120s go test -run '^$' \
  -bench '^BenchmarkFanoutPlanning$' -benchmem -benchtime=5x \
  -count=5 -cpu=1 ./outbox
```

The matrix has six rows and 25 measured iterations per row. Go also performs
benchmark calibration/preflight calls. Retained input is bounded at 10,000
targets plus serialized snapshots; deliveries are discarded after counting.
`GOMEMLIMIT` is a soft Go runtime limit, not a hard process-memory guarantee.
GNU `timeout` bounds the measured command at two minutes (plus a ten-second
kill grace); the hosted benchmark step has a three-minute cap. Go's test timeout
does not bound benchmarks. The race smoke has its own three-minute command
budget. The 15-minute job budget also includes compilation and focused checks.

The existing Go workflow remains `workflow_dispatch` only. Set
`fanout_baseline_only=true` to run the bounded lane and skip unchanged backend
and full-core suites. Its default false preserves the existing full workflow.
The lane records exact HEAD, tracked dirty state/patch, source hashes, Go/OS/CPU
and memory settings; runs focused race tests, one-iteration race smoke, vet and
new-code lint; and attempts to upload raw evidence even if a preceding step
fails. A hard job timeout or cancellation can prevent that upload.
Race-smoke timings are diagnostic only and must not enter the baseline table.

## Interpretation

Results must name the exact source and run/environment. Report preparation
growth and allocation cost without attributing database or recipient behavior
to this sink. This baseline is characterization only: no optimization or new
fanout API is justified solely by these synthetic samples. A storage-backed
workload with owned fixtures, application acceptance criteria and explicit
resource budgets is a separate next measurement if needed.
