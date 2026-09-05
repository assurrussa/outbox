package outbox

import (
	"context"
	"fmt"
	"time"

	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/types"
)

//go:generate toolsmocks

type Putter interface {
	Put(ctx context.Context, name, payload string, availableAt time.Time) (types.JobID, error)
}

type VersionedPutter interface {
	PutVersioned(
		ctx context.Context,
		name string,
		schemaVersion SchemaVersion,
		payload string,
		availableAt time.Time,
	) (types.JobID, error)
}

// UniquePutResult describes whether a unique put created a new job or
// resolved an existing idempotency tombstone with identical content.
type UniquePutResult struct {
	JobID   types.JobID
	Created bool
}

// UniqueVersionedPutter stores a versioned job under an immutable
// deduplication key.
type UniqueVersionedPutter interface {
	PutVersionedUnique(
		ctx context.Context,
		deduplicationKey string,
		name string,
		schemaVersion SchemaVersion,
		payload string,
		availableAt time.Time,
	) (UniquePutResult, error)
}

type FanoutPutter interface {
	PutFanout(
		ctx context.Context,
		event FanoutEvent,
		targets []FanoutTarget,
		availableAt time.Time,
	) (types.JobID, error)
}

type QueueStats struct {
	ObservedAt   time.Time
	Total        int64
	Available    int64
	Processing   int64
	ByCapability []CapabilityQueueStats
}

// CapabilityQueueStats describes one exact active-queue group. OldestAvailableAt
// is zero when the group has no currently available jobs.
type CapabilityQueueStats struct {
	Name              string
	SchemaVersion     SchemaVersion
	Total             int64
	Available         int64
	Processing        int64
	OldestAvailableAt time.Time
}

type Stats interface {
	QueueStats(ctx context.Context) (QueueStats, error)
}

// JobsRepository owns the version-aware, fenced lifecycle of active jobs.
// Unsupported capabilities must remain unclaimed. A successful claim returns
// between one and limit jobs carrying the supplied non-zero lease token. When
// no jobs are available, return ErrNoJobs instead of an empty successful
// result; the latter is rejected as ErrEmptyReservationBatch.
// Each claimed row must carry its actual, valid ReservedAt lease deadline.
// Expired claims are rejected before handler admission. Operations must honor
// their context, including the claim deadline and cancellation while acquiring
// a connection. ExtendJobLeases must update only live leases with matching
// tokens and report the number of affected rows.
type JobsRepository interface {
	CreateJobVersioned(
		ctx context.Context,
		name string,
		schemaVersion SchemaVersion,
		payload string,
		availableAt time.Time,
	) (types.JobID, error)
	FindAndReserveJobsForCapabilities(
		ctx context.Context,
		now time.Time,
		until time.Time,
		leaseToken LeaseToken,
		capabilities []JobCapability,
		limit int,
	) ([]models.Job, error)
	ExtendJobLeases(
		ctx context.Context,
		jobIDs []types.JobID,
		leaseToken LeaseToken,
		now time.Time,
		until time.Time,
	) (int64, error)
	ReleaseUnstartedJobsWithLease(
		ctx context.Context,
		jobIDs []types.JobID,
		leaseToken LeaseToken,
		now time.Time,
	) (int64, error)
	DeleteJobWithLease(
		ctx context.Context,
		jobID types.JobID,
		leaseToken LeaseToken,
		now time.Time,
	) (int64, error)
	RescheduleJobWithLease(
		ctx context.Context,
		jobID types.JobID,
		leaseToken LeaseToken,
		now time.Time,
		availableAt time.Time,
	) (int64, error)
	MaxReservationBatchSize() int
}

// FanoutJobsRepository creates jobs under an immutable idempotency key.
// Reusing a key with different job content must return ErrIdempotencyConflict.
type FanoutJobsRepository interface {
	CreateJobVersionedUnique(
		ctx context.Context,
		deduplicationKey string,
		name string,
		schemaVersion SchemaVersion,
		payload string,
		availableAt time.Time,
	) (types.JobID, error)
}

// UniqueJobsRepository extends unique job creation with a created/replayed
// result while keeping FanoutJobsRepository source-compatible.
type UniqueJobsRepository interface {
	CreateJobVersionedUniqueResult(
		ctx context.Context,
		deduplicationKey string,
		name string,
		schemaVersion SchemaVersion,
		payload string,
		availableAt time.Time,
	) (UniquePutResult, error)
}

// FanoutMaintenanceRepository prunes completed idempotency tombstones only
// after the host's replay and audit retention window has elapsed.
type FanoutMaintenanceRepository interface {
	PruneJobIdempotencyKeys(ctx context.Context, before time.Time, limit int) (int64, error)
}

// JobsStatRepository provides access to outbox queue stats.
//
// Optional:
// this repository is required only when calling Service.GetQueueStats.
// The worker flow (Put/Run processing) works without it.
type JobsStatRepository interface {
	GetQueueStats(ctx context.Context, observedAt time.Time) (QueueStats, error)
}

// JobsFailedRepository persists versioned failed jobs for DLQ.
type JobsFailedRepository interface {
	CreateFailedJobVersioned(
		ctx context.Context,
		jobID types.JobID,
		name string,
		schemaVersion SchemaVersion,
		payload string,
		reason string,
	) (types.JobID, error)
}

// Transactor runs callbacks inside a transaction.
type Transactor interface {
	RunInTx(ctx context.Context, f func(context.Context) error) error
}

// TransactionCapabilities declares storage transactor capabilities.
type TransactionCapabilities interface {
	SupportsAtomicDLQ() bool
}

// HandlerPanicError captures panic details recovered during batch execution.
type HandlerPanicError struct {
	JobName string
	Value   any
	Stack   []byte
}

func (e *HandlerPanicError) Error() string {
	return fmt.Sprintf("panic in batch job %q: %v", e.JobName, e.Value)
}

const (
	defaultExecutionTimeout = 30 * time.Second
	defaultMaxAttempts      = 30
)

// DefaultJob provides safe defaults for optional Job methods.
type DefaultJob struct{}

func (j DefaultJob) ExecutionTimeout() time.Duration {
	return defaultExecutionTimeout
}

func (j DefaultJob) MaxAttempts() int {
	return defaultMaxAttempts
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
