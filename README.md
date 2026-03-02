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

## License

MIT. See [LICENSE](./LICENSE).
