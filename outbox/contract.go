package outbox

import (
	"context"
	"time"

	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/types"
)

//go:generate toolsmocks

type Putter interface {
	Put(ctx context.Context, name, payload string, availableAt time.Time) (types.JobID, error)
}

type QueueStats struct {
	Total      int64
	Available  int64
	Processing int64
}

type Stats interface {
	QueueStats(ctx context.Context) (QueueStats, error)
}

// JobsRepository provides access to the outbox jobs store.
type JobsRepository interface {
	CreateJob(ctx context.Context, name, payload string, availableAt time.Time) (types.JobID, error)
	FindAndReserveJob(ctx context.Context, now time.Time, until time.Time) (models.Job, error)
	DeleteJob(ctx context.Context, jobID types.JobID) (int64, error)
}

// JobsStatRepository provides access to stats the outbox jobs store.
type JobsStatRepository interface {
	CountExact(ctx context.Context) (int64, error)
	CountAvailable(ctx context.Context, now time.Time) (int64, error)
	CountReserved(ctx context.Context, now time.Time) (int64, error)
}

// JobsFailedRepository persists failed jobs for DLQ.
type JobsFailedRepository interface {
	CreateFailedJob(ctx context.Context, jobID types.JobID, name, payload, reason string) (types.JobID, error)
}

// Transactor runs callbacks inside a transaction.
type Transactor interface {
	RunInTx(ctx context.Context, f func(context.Context) error) error
}

type Job interface {
	Name() string

	Handle(ctx context.Context, payload string) error

	// ExecutionTimeout is the time given to the queue handler to execute the task.
	// If the ExecutionTimeout is exceeded, the execution is aborted, the attempt is counted,
	// and the repetition will be performed.
	ExecutionTimeout() time.Duration

	// MaxAttempts is the maximum number of attempts to run the task.
	// An attempt is counted if the task was not completed due to an unknown error.
	// When MaxAttempts() is exceeded, the task moves to the dlq (dead letter queue) table.
	MaxAttempts() int
}
