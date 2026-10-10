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

## Recorded baseline: 2026-10-10

[Manual run 89](https://github.com/assurrussa/outbox/actions/runs/38029640414)
passed on clean tracked source `2ad4da37468f97a11edcd7e9004c6dfc87d51439`.
The runner used Go 1.26.8, Ubuntu 24.04.5, an AMD EPYC 9V45 virtual CPU
(4 logical CPUs exposed; `GOMAXPROCS=1`), and 16,766,410,752 bytes of host memory.
`GOMEMLIMIT=256MiB` remained the soft runtime budget. This is one hosted machine
session, not independent hardware replication.

Each cell summarizes five samples of five measured operations. Time range is
min–max across those sample averages, not an individual-operation distribution.
Allocation columns are medians; full allocation ranges are in the manifest.

| Targets | Phase | Median ms/event | Sample range ms/event | Median B/event | Median allocations/event |
|---:|---|---:|---:|---:|---:|
| 10 | enqueue | 0.013022 | 0.011768–0.024421 | 7,144 | 36 |
| 10 | dispatch | 0.068722 | 0.058392–0.075255 | 46,417 | 265 |
| 1,000 | enqueue | 0.291155 | 0.286274–0.351171 | 301,024 | 2,018 |
| 1,000 | dispatch | 5.373204 | 5.223944–5.450621 | 4,183,528 | 24,039 |
| 10,000 | enqueue | 3.900252 | 3.300382–4.100137 | 2,976,420 | 20,054 |
| 10,000 | dispatch | 58.612782 | 57.568998–59.339580 | 43,042,256 | 240,138 |

At 10,000 targets the dispatcher passed exactly 10,000 delivery payloads totaling
12,870,000 encoded bytes to the sink per event. Its approximately 43 MB of
allocated bytes per event is cumulative allocation churn, not retained heap or
RSS. The large enqueue sample's B/event varied from 2,714,659 to 2,976,420;
reporting only the median must not erase that variability. No peak-memory
measurement or hard memory-limit claim is made.

This establishes a reference for the current core implementation. It does not
identify an application SLO breach or a production bottleneck, and no optimization
was made. Storage commit cost and real recipient work remain unmeasured here.

### Validation and raw evidence

- PASS: exact fanout correctness selection under race; six one-iteration race
  smoke rows; `go vet ./outbox`; new-code golangci-lint 2.14.0 with zero issues;
  all 30 non-race samples; independent final source review.
- PASS: local source formatting via gofmt, gofumpt 0.11.0 and gci 0.14.0
  (local formatting toolchain Go 1.27.2); measured toolchain was Go 1.26.8.
- SKIPPED: full core and backend job groups, exactly as the true selector
  requires. Retain unchanged correctness evidence from master `267ace7` and
  PR42/run88. This run is not a new full `make check` or backend-capacity pass.
- NOT_RUN: database-backed fanout load, recipient load, soak, fault-at-load,
  multi-machine replication and peak RSS. No release or deployment.

Four artifact text files preserved byte-for-byte (including trailing newlines),
plus a derived summary manifest:

- [Non-race samples](fanout-planning-20261010/benchmark.txt)
- [Source and environment](fanout-planning-20261010/environment.txt)
- [Focused correctness](fanout-planning-20261010/correctness.txt)
- [Race smoke, excluded from measurements](fanout-planning-20261010/race-smoke.txt)
- [Machine-readable provenance and summary](fanout-planning-20261010/manifest.json)

GitHub artifact `11662066097` had ZIP SHA-256
`e2087cb02b67d28d7e06977248672bbfbe2ac0294853a95953205b42b9cf2b0b`,
verified after download. The repository copies keep the evidence beyond the
artifact's 30-day retention. Subsequent evidence-only commits retain this
source's successful checks; benchmark/production/workflow bytes are unchanged.
