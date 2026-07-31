# Releasing outbox

The core module and each backend are separate Go modules. Stable publication
uses two phases so every backend resolves the exact published core tag; a local
`go.work` check is not publication evidence.

## 1. Stable core

For the current stable release, run:

```sh
make release-readiness-core CORE_VERSION=v0.10.0
```

Commit generated files before this gate. Tag and publish the root module only
after the command is clean.

The core gate runs mutating preparation once, then a source-read-only check
with one race+coverage core traversal and one normal traversal per backend.
`make test-race-core` and `make cover-html` remain explicit diagnostics and do
not need to be stacked onto a successful readiness run.

## 2. Stable backends

After the core tag resolves without local replacements, update
every backend `go.mod` to that exact core version and run:

```sh
make release-readiness-backends CORE_VERSION=v0.10.0
```

This gate uses `GOWORK=off`, verifies the actually resolved core version,
checks tidiness, and runs each backend's standalone tests. Tag each module only
after the complete gate passes:

- `backends/pgsql/v0.10.0`;
- `backends/mysql/v0.10.0`;
- `backends/sqlite/v0.10.0`;
- `backends/picodata/v0.10.0`.

PostgreSQL, MySQL, and SQLite provide the complete capability/fencing/fan-out
contract. Picodata provides only versioned create/claim, fenced
heartbeat/ack, and version-preserving failed-row storage primitives. Do not
advertise Picodata as full fan-out parity: it deliberately exposes neither
`FanoutJobsRepository` nor the standard runtime facade while the current
client lacks a connection-pinned transaction boundary.

## 3. Downstream consumers

Only after the required backend tags resolve may downstream consumers update
their BOMs. Their clean-consumer gates must run without local `replace`
directives. Exact tags, checksums, and release notes are required; `latest`,
branches, and local pseudo-versions are not release evidence.
