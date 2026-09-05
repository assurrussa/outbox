# Performance evidence

## Core lease scheduling

The [2026-09-05 lease-bound comparison](performance/lease-bound-20260905.md)
measures the lazy minimum-bound change against `d853a50` using identical
in-memory benchmark harnesses. At five-minute leases, prefetch 100/1000 time per
job falls by 27.15%/80.44%, with unchanged extension counts and no significant
slowdown in single or true-batch controls. This is core evidence, separate from
the database-backed capacity series below.

## Confirmed true-batch capacity improvement

The normalized GoMessenger PostgreSQL/NATS capacity series demonstrates that
the complete true Outbox batch path materially increases sustainable throughput
for the recorded small-payload profile. With the downstream consumer held in
batch mode for both variants, changing Outbox ingress and relay from singleton
execution to true batches raised the confirmed frontier from 450 to 2,550
messages per second: a 5.67x improvement.

Both frontiers passed three independent fresh-volume confirmations. The three
2,550 msg/s candidate runs accepted and reconciled 918,200 business effects
with zero dropped k6 iterations, HTTP failures, redeliveries, duplicate Inbox
effects, invalid measurements, missing links, or DLQ messages. Business p95
stayed between 135.938 and 137.530 ms, and drain completed in 1.018–1.664
seconds. The measured Outbox handler, broker publish, and fenced finalization
batches averaged 99.935–100 messages per call.

| Path | Outbox ingress / relay | Confirmed frontier | Confirmations | Business p95 | Accepted effects | Reconciliation |
|---|---|---:|---:|---:|---:|---:|
| `consumer-batch` control | single / single | 450 msg/s | 3/3 | 117.986–470.747 ms | 162,001 | 3/3 |
| `full-batch` candidate | batch / batch | 2,550 msg/s | 3/3 | 135.938–137.530 ms | 918,200 | 3/3 |

The next candidate rate, 2,600 msg/s, passed only 1/3 confirmations. One run
exceeded the two-second business p95 limit and dropped two scheduled
iterations; another detected two producer-pool connection replacements.
Therefore the evidence supports 2,550 msg/s as the maximum repeatably
demonstrated rate for this profile, not 2,600 msg/s.

## Scope and provenance

The capacity harness is maintained in GoMessenger because it exercises the
complete integration path: batched business transaction and Outbox staging ->
PostgreSQL -> batch relay -> NATS JetStream -> batch Inbox consumer ->
PostgreSQL projection. The full reviewable snapshot is
[GoMessenger's true Outbox batch frontier evidence](https://github.com/assurrussa/gomessenger/blob/master/docs/performance/outbox-true-batch-small-frontier-20260903.md).

The claim-bearing runs used:

- capacity report specification `2.1`;
- clean GoMessenger commit
  `dc33c969ee39ea387e98f091debe23492fc8cce2`;
- clean Outbox commit
  `e579abdc25804f1ae1439e8fcfa3fc42d727fb59`;
- small payloads and the fixed `o2-c2` topology on stock PostgreSQL 18.6;
- two Outbox workers, six producer and two relay connections, two batch
  consumers, and maximum batch size 100;
- a shared two-CPU SUT cpuset, fixed container memory, 60-second warm-up,
  120-second measured load, 60-second drain limit, and fresh PostgreSQL/NATS
  volumes for every run.

This is a complete repeated capacity result for that exact profile, not an
in-memory microbenchmark or a one-off screen. It directly supports the claim
that true batch staging, relay handling, broker publication, and fenced batch
finalization improved practical pipeline capacity under the tested conditions.

## Test boundary

The operator-observed campaign lasted approximately eleven hours, but that time
covered source gates, image builds, multiple frontier candidates and
controls, clean Docker turnover, an invalid telemetry-sampling run, a harness
correction, and a partial restart. It must not be described as eleven hours of
continuous traffic at 2,550 msg/s.

The campaign did not run a continuous multi-hour soak at the confirmed
frontier, inject restart/outage/network faults under frontier load, validate a
multi-node production topology, or complete the mixed-payload and full
eight-frontier verdict. These are different test classes. Their absence bounds
the result to repeatable checkout-workspace capacity evidence; it does not
invalidate the measured 5.67x improvement.

The real-service pilot remains responsible for production traffic, operational
recovery, soak, and deployment-specific acceptance. The public delivery
contract remains at-least-once; exact reconciliation in these runs does not
prove exactly-once external effects.
