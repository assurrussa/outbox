# Implementation Notes

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
