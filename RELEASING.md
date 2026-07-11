# Releasing outbox

The core module and each backend are separate Go modules. Capability-aware
claims and durable fan-out require a two-phase prerelease; a local `go.work`
check is not publication evidence.

## 1. Core prerelease

Choose one exact prerelease, currently expected to be
`v0.10.0-alpha.0`, and run:

```sh
make release-readiness-core CORE_VERSION=v0.10.0-alpha.0
```

Commit generated files before this gate. Tag and publish the root module only
after the command is clean.

## 2. PostgreSQL backend prerelease

After the core tag resolves without local replacements, update
`backends/pgsql/go.mod` to that exact core version and run:

```sh
make release-readiness-pgsql CORE_VERSION=v0.10.0-alpha.0
```

This gate uses `GOWORK=off`, verifies the actually resolved core version, checks
tidiness, and runs the standalone PostgreSQL backend tests. Tag the backend
with its module-path-prefixed tag only after this passes.

MySQL, SQLite, and Picodata remain on their legacy API until they implement the
additive capability repositories. They must not be advertised as supporting
capability-aware claims or durable fan-out merely because `go.work` compiles.

## 3. Downstream consumers

Only after both tags resolve may `gocms` and `platformctl` update their BOMs.
Their clean-consumer gates must run without local `replace` directives. Exact
tags, checksums, and release notes are required; `latest`, branches, and local
pseudo-versions are not release evidence.
