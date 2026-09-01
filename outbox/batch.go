package outbox

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/types"
)

const (
	defaultBatchMaxMessages = 100
	defaultBatchMaxBytes    = 4 << 20
	defaultBatchMaxWait     = 25 * time.Millisecond
)

var (
	// ErrInvalidBatchResult means a BatchJob returned a result that does not
	// cover every input JobID exactly once.
	ErrInvalidBatchResult = errors.New("outbox invalid batch result")
	// ErrBatchRepositoryNotConfigured means the configured jobs repository
	// does not implement the complete batch execution contract.
	ErrBatchRepositoryNotConfigured = errors.New("outbox batch repository is not configured")
)

// BatchConfig controls one real handler batch. A zero value means 100 jobs,
// 4 MiB of payload bytes, and a 25 millisecond fill window.
type BatchConfig struct {
	MaxMessages int
	MaxBytes    int
	MaxWait     time.Duration
}

type normalizedBatchConfig struct {
	maxMessages int
	maxBytes    int
	maxWait     time.Duration
}

// Normalize applies zero-value defaults and validates supported bounds.
func (c BatchConfig) Normalize() (BatchConfig, error) {
	normalized, err := c.normalize()
	if err != nil {
		return BatchConfig{}, err
	}
	return BatchConfig{
		MaxMessages: normalized.maxMessages,
		MaxBytes:    normalized.maxBytes,
		MaxWait:     normalized.maxWait,
	}, nil
}

func (c BatchConfig) normalize() (normalizedBatchConfig, error) {
	if c.MaxMessages < 0 || c.MaxBytes < 0 || c.MaxWait < 0 {
		return normalizedBatchConfig{}, errors.New("outbox batch limits must not be negative")
	}

	result := normalizedBatchConfig{
		maxMessages: c.MaxMessages,
		maxBytes:    c.MaxBytes,
		maxWait:     c.MaxWait,
	}
	if result.maxMessages == 0 {
		result.maxMessages = defaultBatchMaxMessages
	}
	if result.maxBytes == 0 {
		result.maxBytes = defaultBatchMaxBytes
	}
	if result.maxWait == 0 {
		result.maxWait = defaultBatchMaxWait
	}
	if result.maxMessages > MaxReservationBatchSize {
		return normalizedBatchConfig{}, fmt.Errorf(
			"outbox batch maximum messages %d exceeds %d",
			result.maxMessages,
			MaxReservationBatchSize,
		)
	}
	if result.maxBytes > math.MaxInt32 {
		return normalizedBatchConfig{}, fmt.Errorf("outbox batch maximum bytes %d overflows supported bound", result.maxBytes)
	}

	return result, nil
}

// BatchJobItem is one claimed job passed to a BatchJob. Items retain durable
// queue order and expose the attempt number written by the claim transaction.
type BatchJobItem struct {
	JobID   types.JobID
	Payload string
	Attempt int
}

// BatchItemResult classifies one input job by its stable JobID. A nil Err
// acknowledges the item; error dispositions control retry, defer, or DLQ.
type BatchItemResult struct {
	JobID types.JobID
	Err   error
}

// BatchResult contains one result for every input JobID. Result order is not
// significant.
type BatchResult struct {
	Items []BatchItemResult
}

// BatchJob handles one real batch invocation. A non-nil top-level error is
// valid only when the returned BatchResult is empty.
type BatchJob interface {
	Name() string
	HandleBatch(ctx context.Context, items []BatchJobItem) (BatchResult, error)
	ExecutionTimeout() time.Duration
	MaxAttempts() int
}

// VersionedBatchJob opts a batch handler into an explicit payload schema
// version. Other batch handlers use DefaultSchemaVersion.
type VersionedBatchJob interface {
	BatchJob
	SchemaVersion() SchemaVersion
}

// BatchJobOutcomeKind is the durable action applied by a batch-capable
// repository.
type BatchJobOutcomeKind uint8

const (
	BatchJobOutcomeSuccess BatchJobOutcomeKind = iota + 1
	BatchJobOutcomeRetry
	BatchJobOutcomeDefer
	BatchJobOutcomeDLQ
)

// BatchJobOutcome is a repository-facing fenced outcome. The service persists
// DLQ records through its configured JobsFailedRepository in the same outer
// transaction before the batch repository removes the active rows.
type BatchJobOutcome struct {
	JobID       types.JobID
	Kind        BatchJobOutcomeKind
	AvailableAt time.Time
	Reason      string
}

// BatchJobsRepository is the optional complete execution capability required
// by RegisterBatchJob. ApplyBatchJobOutcomes must apply all items atomically.
// A successful call returns len(outcomes); a lease mismatch must roll back the
// entire operation and return ErrLeaseLost.
type BatchJobsRepository interface {
	FindAndReserveJobsForCapability(
		ctx context.Context,
		now time.Time,
		until time.Time,
		leaseToken LeaseToken,
		capability JobCapability,
		limit int,
	) ([]models.Job, error)
	ApplyBatchJobOutcomes(
		ctx context.Context,
		leaseToken LeaseToken,
		now time.Time,
		outcomes []BatchJobOutcome,
	) (int64, error)
	MaxExecutionBatchSize() int
}

// DeferJobsRepository is the optional fenced capability used by DeferAt to
// postpone one claimed job without consuming its attempt.
type DeferJobsRepository interface {
	DeferJobWithLease(
		ctx context.Context,
		jobID types.JobID,
		leaseToken LeaseToken,
		now time.Time,
		availableAt time.Time,
	) (int64, error)
}

// UniqueBatchPut describes one immutable versioned job staging request.
type UniqueBatchPut struct {
	DeduplicationKey string
	Name             string
	SchemaVersion    SchemaVersion
	Payload          string
	AvailableAt      time.Time
}

// UniqueBatchVersionedPutter atomically stages an ordered set of unique jobs.
type UniqueBatchVersionedPutter interface {
	PutVersionedUniqueBatch(ctx context.Context, items []UniqueBatchPut) ([]UniquePutResult, error)
}

// UniqueBatchJobsRepository is the optional atomic batch staging capability.
type UniqueBatchJobsRepository interface {
	CreateJobVersionedUniqueBatch(
		ctx context.Context,
		items []UniqueBatchPut,
	) ([]UniquePutResult, error)
}

func validateBatchResult(input []BatchJobItem, result BatchResult) ([]error, error) {
	if len(result.Items) != len(input) {
		return nil, fmt.Errorf(
			"%w: got %d items for %d inputs",
			ErrInvalidBatchResult,
			len(result.Items),
			len(input),
		)
	}

	positions := make(map[types.JobID]int, len(input))
	for index, item := range input {
		if item.JobID.IsZero() {
			return nil, fmt.Errorf("%w: input %d has an empty JobID", ErrInvalidBatchResult, index)
		}
		if _, duplicate := positions[item.JobID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate input JobID %s", ErrInvalidBatchResult, item.JobID)
		}
		positions[item.JobID] = index
	}

	errs := make([]error, len(input))
	seen := make(map[types.JobID]struct{}, len(result.Items))
	for _, item := range result.Items {
		position, ok := positions[item.JobID]
		if !ok {
			return nil, fmt.Errorf("%w: unknown JobID %s", ErrInvalidBatchResult, item.JobID)
		}
		if _, duplicate := seen[item.JobID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate JobID %s", ErrInvalidBatchResult, item.JobID)
		}
		seen[item.JobID] = struct{}{}
		errs[position] = item.Err
	}

	return errs, nil
}
