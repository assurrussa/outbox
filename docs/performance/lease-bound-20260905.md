# Lazy lease bound: core benchmark evidence

Removing the per-deletion minimum scan reduced sequential prefetch time per
job by 27.15% at batch 100 and 80.44% at batch 1000 with a five-minute lease.
Both changes are statistically significant (`p < 0.001`). Single and true-batch
controls show no statistically significant slowdown. All acceptance conditions
passed: improvement in prefetch 100/1000 at both long leases and no significant
time regression over 5% in any scenario.

## Method and source identity

- Baseline: `d853a50944e43b48c8051a893b0975dbb9aebb53`.
- Candidate: the verified working changes on that base; source checksums below
  identify the measured tree without claiming a commit or published release.
- Identical current `BenchmarkExecutionPaths` harness applied to both snapshots;
  one operation processes 1000 jobs. Before and after each contain 280 samples:
  28 scenarios repeated in ten alternating AB/BA pairs.
- Apple M5 Pro, Darwin/arm64, Go 1.27.0; `GOMAXPROCS=2`, `GOGC=100`,
  `GOMEMLIMIT=off`, empty `GOFLAGS` and `GOEXPERIMENT`, `-benchtime=300ms`,
  `-cpu=2`, without race instrumentation. Test containers were stopped first.
- `benchstat` from `golang.org/x/perf` revision
  `v0.0.0-20220411212318-84e58bfe0a7e`, with its default significance and outlier
  handling. Raw samples are retained, including outliers. Some printed comparisons
  therefore contain fewer than ten retained samples. `~` means no significant
  difference; it is not proof of exact equality. No multiple-comparison adjustment
  was applied, and the small isolated single-path improvement is not a claim.

Run the comparison from the candidate source checkout:

```sh
python3 scripts/compare-lease-benchmarks.py --base d853a50 --output /tmp/outbox-lease-comparison
```

The runner saves complete source snapshots and individual pair outputs in the
new output directory. The tracked diff covers tracked changes; the complete
source checksum manifest also covers newly added files. Report and notes edits
after the run do not change the measured Go, SQL or module files.

Candidate `outbox/service_batch.go` SHA-256:
`c5c5b159fae6d8866ffa6d9bd6c65a67f46a30a9747bab2b00fa2970209053bc`.
The common benchmark harness SHA-256 is
`b13336945238a8617be7b5ee77ccb1a15032a21c962bfa1ce0f4ccaf822d5547`.

## Time per job

Values below are the rounded benchstat results, in microseconds per job.

| Path | Lease | Before | After | Change |
|---|---:|---:|---:|---:|
| Single | 10s | 2.83 | 2.82 | ~ |
| Prefetch 16 | 10s | 1.37 | 1.19 | -13.08% |
| Prefetch 100 | 10s | 1.44 | 1.05 | -27.31% |
| Prefetch 1000 | 10s | 4.94 | 0.97 | -80.42% |
| True batch 1 | 10s | 3.15 | 3.13 | ~ |
| True batch 100 | 10s | 0.867 | 0.864 | ~ |
| True batch 1000 | 10s | 0.958 | 0.953 | ~ |
| Single | 5m | 2.84 | 2.81 | -1.09% |
| Prefetch 16 | 5m | 1.37 | 1.19 | -13.19% |
| Prefetch 100 | 5m | 1.44 | 1.05 | -27.15% |
| Prefetch 1000 | 5m | 4.93 | 0.96 | -80.44% |
| True batch 1 | 5m | 3.15 | 3.13 | ~ |
| True batch 100 | 5m | 0.867 | 0.866 | ~ |
| True batch 1000 | 5m | 0.958 | 0.954 | ~ |

At short leases (1s and 5s), prefetch 100 improves by 26.28–26.73% and
prefetch 1000 by 80.33–80.36%. The repeated tail scan is removed at every lease
length. Full results for all 28 scenarios are in the comparison artifact.

## Allocations and renewals

The ledger optimization adds no per-row allocation structure. For prefetch 100
at long leases, both variants use about 1050 bytes and 13.3 allocations per job;
prefetch 1000 uses about 1080 bytes and 13.0 allocations per job. No allocation
regression over 5% was observed in the matrix.

Extension calls and extended rows are identical before and after. At 1s/5s:

| Path | Extension calls/job | Extended rows/job |
|---|---:|---:|
| Single / true batch 1 | 1 | 1 |
| Prefetch 16 | 0.063 | 1 |
| Prefetch 100 / true batch 100 | 0.01 | 1 |
| Prefetch 1000 / true batch 1000 | 0.001 | 1 |

Prefetch 16 has 63 claims for 1000 jobs, including the final partial claim.
At 10s/5m, every scenario records zero extensions in both variants. The old
benchstat formatter rounds small custom metrics to two decimals; consult raw
samples for exact values such as 0.001, which it prints as 0.00.

These are no-op repository and handler measurements. Short leases still require
protective writes, and real handler/database latency can cause additional
renewals. This result proves a core scheduling improvement while preserving the
renewal mechanism; it does not establish PostgreSQL/NATS throughput or compare
the full hardening series against the older `6128bf7` implementation.

## Validation and artifacts

`make prepare`, fresh CLI gopls diagnostics, `make check` (core race coverage
83.0%) and the complete race-enabled `make test-integration-all` passed. Existing
late-nil timeout and unstarted-tail compensation checks remain included. The
documented MySQL repair SQL passed integration scenarios for lost history,
successful recovery, stale input, active rows, no-op updates and key conflicts.

- [Environment and source identity](lease-bound-20260905/manifest.json)
- [Complete source checksums](lease-bound-20260905/source-checksums.json)
- [Tracked source diff at measurement time](lease-bound-20260905/tracked-changes.patch)
- [Before samples](lease-bound-20260905/before.txt) and [after samples](lease-bound-20260905/after.txt)
- [Full benchstat comparison](lease-bound-20260905/comparison.txt)
- [Acceptance check](lease-bound-20260905/acceptance.json)
