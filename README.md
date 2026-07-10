# GO Outbox (Core)

`github.com/assurrussa/outbox` is the core outbox library.

The repository is now a multi-module monorepo:
- core module: `github.com/assurrussa/outbox`
- MySQL backend: `github.com/assurrussa/outbox/backends/mysql`
- SQLite backend: `github.com/assurrussa/outbox/backends/sqlite`
- Postgres backend: `github.com/assurrussa/outbox/backends/pgsql`
- Picodata backend: `github.com/assurrussa/outbox/backends/picodata`

## Install core

```sh
go get github.com/assurrussa/outbox@latest
```

```go
import "github.com/assurrussa/outbox/outbox"
```

## Core usage

```go
package main

import (
	"context"
	"time"

	"github.com/assurrussa/outbox/outbox"
	outboxlogger "github.com/assurrussa/outbox/outbox/logger"
	sharedjob "github.com/assurrussa/outbox/shared/job"
)

type SendEmailJob struct {
	sharedjob.DefaultJob
}

func (*SendEmailJob) Name() string { return "send_email" }

func (*SendEmailJob) Handle(_ context.Context, _ string) error { return nil }

func main() {
	ctx := context.Background()

	svc, err := outbox.New(
		outbox.WithWorkers(1),
		outbox.WithReserveFor(time.Second),
		outbox.WithIdleTime(5*time.Minute),
		outbox.WithLogger(outboxlogger.Default()),
		outbox.WithJobsRepo(jobsRepo),
		// Optional: only needed for svc.GetQueueStats(...)
		outbox.WithJobsStatRepo(jobsRepo),
		outbox.WithJobsFailedRepo(jobsFailedRepo),
		outbox.WithTransactor(txManager),
	)
	if err != nil {
		panic(err)
	}

	emailJob := &SendEmailJob{}
	svc.MustRegisterJob(emailJob)

	go func() { _ = svc.Run(ctx) }()
	_, _ = svc.Put(ctx, "send_email", `{"id":"1"}`, time.Now())
}
```

`JobsStatRepository` is optional.  
Set `WithJobsStatRepo(...)` only if you need `svc.GetQueueStats(...)`.

## Capability-aware workers (opt-in)

Use capability mode when workers must claim only payload schemas they can
decode. It is additive: legacy `Put`, repositories, and unknown-job/DLQ
behavior stay unchanged until both capability repositories are configured.

```go
type PublishV2Job struct {
	sharedjob.DefaultJob
}

func (*PublishV2Job) Name() string { return "cms.entry.publish" }

func (*PublishV2Job) SchemaVersion() outbox.SchemaVersion { return 2 }

func (*PublishV2Job) Handle(_ context.Context, _ string) error { return nil }

svc, err := outbox.New(
	outbox.WithJobsRepo(jobsRepo),
	outbox.WithCapabilityJobsRepo(jobsRepo),
	outbox.WithJobsFailedRepo(jobsFailedRepo),
	outbox.WithCapabilityJobsFailedRepo(jobsFailedRepo),
	outbox.WithTransactor(txManager),
)
if err != nil {
	panic(err)
}

svc.MustRegisterJob(&PublishV2Job{})
_, _ = svc.PutVersioned(
	ctx,
	"cms.entry.publish",
	2,
	`{"entryId":"1"}`,
	time.Now(),
)
```

Jobs without `SchemaVersion()` use schema v1. In capability mode, ordinary
`Put(...)` also persists schema v1. Claims are filtered by the registered
`(name, schemaVersion)` set, so unsupported jobs remain pending and do not move
to DLQ. Active handlers refresh their lease every `reserveFor / 3`; successful
ack and DLQ deletion require the same live lease token.

Rollout rule: do not enqueue schemas newer than v1 while legacy workers are
still running. Deploy capability-aware workers that understand both versions,
remove legacy workers, and only then enable the new producer schema.

The PostgreSQL backend currently implements the capability repository
contracts. Other backends remain available through the legacy API until they
gain their own additive implementations.

## Durable fan-out (opt-in)

Configure `WithFanoutJobsRepo(jobsRepo)` together with capability mode when one
integration event must produce independently retried deliveries. `PutFanout`
stores the event plus the exact eligible target snapshots under the event ID.
The built-in dispatcher is registered during construction and creates one job
per target in a transaction.

```go
event := outbox.FanoutEvent{
	ID:            types.NewMessageID(),
	Topic:         "cms.entry.published",
	SchemaVersion: 1,
	Payload:       json.RawMessage(`{"entryId":"1"}`),
	OccurredAt:    time.Now(),
}

_, err = svc.PutFanout(ctx, event, []outbox.FanoutTarget{
	{
		Kind:     "nitro",
		ID:       "site-1",
		Snapshot: json.RawMessage(`{"namespace":"public"}`),
	},
	{
		Kind:     "webhook",
		ID:       "subscription-7",
		Snapshot: json.RawMessage(`{"configRevision":4,"secretRevision":2}`),
	},
}, time.Now())
```

Delivery handlers register
`FanoutDeliveryJobName(targetKind, eventTopic)` for the event schema they
understand and decode payloads with `DecodeFanoutDelivery`. Every delivery has
a deterministic ID suitable for webhook idempotency. Unsupported delivery
capabilities stay pending with zero attempts.

Fan-out retries are idempotent even after a delivery job was acknowledged and
deleted: PostgreSQL retains a compact key/fingerprint tombstone separately
from active jobs. Prune tombstones in bounded batches only after the host's
event replay, audit, and webhook retry retention windows have elapsed.

## Backend modules

Pick only the backend module you need for a project.

- [MySQL backend](./backends/mysql/README.md)
- [SQLite backend](./backends/sqlite/README.md)
- [Postgres backend](./backends/pgsql/README.md)
- [Picodata backend](./backends/picodata/README.md)

## Example app

Runnable examples:
- [examples/base-app](examples/base-app/README.md) (core only, in-memory stubs)
- [examples/base-app-mysql](examples/base-app-mysql/README.md)
- [examples/base-app-picodata](examples/base-app-picodata/README.md)
- [examples/base-app-pgsql](examples/base-app-pgsql/README.md)
- [examples/base-app-sqlite](examples/base-app-sqlite/README.md)

## Migration from old import paths

Old `infrastructure/*` paths were removed (hard break).

See [MIGRATION.md](./MIGRATION.md).

## Notes on `shared/*`

`shared/*` is kept in core for internal library/backend reuse, but should be treated as unstable internal API by external consumers.

## Development

Use workspace-aware commands:

```sh
make test-core
make test-backends
make test-integration-all
```

For integration services:

```sh
make devup
make devdown
```

Release prep for backend modules:

```sh
# pin backend modules to a published core tag
make release-ready-backends CORE_VERSION=v0.9.0

# verify each backend as standalone module (without go.work)
make release-verify-backends
```

## License

MIT. See [LICENSE](./LICENSE).
