# GO Outbox

A simple outbox for Go projects.

## Install

Use go get.
```sh
$ go get github.com/assurrussa/outbox@v0.2.0
```

Then import the package into your own code:
```
import "github.com/assurrussa/outbox/outbox"
```

## Usage
```go
package main

import (
	"context"
	"time"

	"github.com/assurrussa/outbox/outbox"
	outboxlogger "github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/shared/job"
)

type SendEmailJob struct {
	sharedjob.DefaultJob
}

func (*SendEmailJob) Name() string {
	return "send_email"
}

func (*SendEmailJob) Handle(ctx context.Context, payload string) error {
	_ = ctx
	_ = payload
	return nil
}

func main() {
	ctx := context.Background()

	svc, err := outbox.New(
		outbox.WithWorkers(1),
		outbox.WithReserveFor(time.Second),
		outbox.WithIdleTime(5*time.Minute),
		outbox.WithLogger(outboxlogger.WrapNamed(log, "any_custom_name_outbox_logger")),
		outbox.WithJobsRepo(jobsRepo),
		outbox.WithJobsStatRepo(jobsRepo),
		outbox.WithJobsFailedRepo(jobsFailedRepo),
		outbox.WithTransactor(txManager),
	)
	if err != nil {
		panic(err)
	}

	emailJob := &SendEmailJob{}
	svc.MustRegisterJob(emailJob)
	go func() {
		_ = svc.Run(ctx)
	}()

	_, _ = svc.Put(ctx, "send_email", `{"id":"1"}`, time.Now())
}
```

## Storage drivers

- Postgres storage: `github.com/assurrussa/outbox/infrastructure/pgsql/storage`
- Picodata storage: `github.com/assurrussa/outbox/infrastructure/picodata/storage`

## Migrations

Use embedded migrations for tools like goose:
```go
package main

import (
	"context"

	"github.com/assurrussa/outbox/infrastructure/pgsql/migrator"
	"github.com/assurrussa/outbox/infrastructure/picodata/migrator"
)

func main() {
	ctx := context.Background()

	migrator.Run(
		ctx,
		db,
		lg,
		migrator.WithCommand(command),
		migrator.WithDirectory(dir),
		migrator.WithArgs(...),
        ...,
    )
}
```

## License

This project is released under the MIT licence. See [LICENSE](https://github.com/assurrussa/outbox/blob/master/LICENSE) for more details.
