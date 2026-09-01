package outbox

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/assurrussa/outbox/shared/sharederrors"
	"github.com/assurrussa/outbox/shared/types"
)

func (s *Service) Put(ctx context.Context, name, payload string, availableAt time.Time) (types.JobID, error) {
	return s.PutVersioned(ctx, name, DefaultSchemaVersion, payload, availableAt)
}

func (s *Service) PutVersioned(
	ctx context.Context,
	name string,
	schemaVersion SchemaVersion,
	payload string,
	availableAt time.Time,
) (types.JobID, error) {
	capability := JobCapability{Name: name, SchemaVersion: schemaVersion}
	if err := capability.Validate(); err != nil {
		return types.JobIDNil, err
	}

	jobID, err := s.jobsRepo.CreateJobVersioned(
		ctx,
		name,
		schemaVersion,
		payload,
		availableAt,
	)
	if err != nil {
		return types.JobIDNil, fmt.Errorf("create versioned job: %w", err)
	}

	return jobID, nil
}

// PutVersionedUnique stores one versioned job under an immutable
// deduplication key and reports whether this call created it. A replay with
// different content returns ErrIdempotencyConflict from the repository.
func (s *Service) PutVersionedUnique(
	ctx context.Context,
	deduplicationKey string,
	name string,
	schemaVersion SchemaVersion,
	payload string,
	availableAt time.Time,
) (UniquePutResult, error) {
	if s.uniqueJobsRepo == nil {
		return UniquePutResult{}, ErrUniqueRepositoryNotConfigured
	}
	if deduplicationKey == "" {
		return UniquePutResult{}, errors.New("outbox deduplication key is empty")
	}

	capability := JobCapability{Name: name, SchemaVersion: schemaVersion}
	if err := capability.Validate(); err != nil {
		return UniquePutResult{}, err
	}

	result, err := s.uniqueJobsRepo.CreateJobVersionedUniqueResult(
		ctx,
		deduplicationKey,
		name,
		schemaVersion,
		payload,
		availableAt,
	)
	if err != nil {
		return UniquePutResult{}, fmt.Errorf("create unique versioned job: %w", err)
	}

	return result, nil
}

// PutVersionedUniqueBatch validates and atomically stages all items. Results
// preserve input order; no item is staged when validation or repository work
// fails.
func (s *Service) PutVersionedUniqueBatch(
	ctx context.Context,
	items []UniqueBatchPut,
) ([]UniquePutResult, error) {
	if s.uniqueBatchJobsRepo == nil {
		return nil, ErrUniqueRepositoryNotConfigured
	}
	if len(items) == 0 {
		return nil, errors.New("outbox unique batch is empty")
	}
	if len(items) > MaxReservationBatchSize {
		return nil, fmt.Errorf(
			"outbox unique batch contains %d items; maximum is %d",
			len(items),
			MaxReservationBatchSize,
		)
	}

	seen := make(map[string]struct{}, len(items))
	prepared := make([]UniqueBatchPut, len(items))
	copy(prepared, items)
	for index := range prepared {
		item := &prepared[index]
		if item.DeduplicationKey == "" {
			return nil, fmt.Errorf("outbox unique batch item %d has an empty deduplication key", index)
		}
		if _, duplicate := seen[item.DeduplicationKey]; duplicate {
			return nil, fmt.Errorf("outbox unique batch item %d duplicates deduplication key %q", index, item.DeduplicationKey)
		}
		seen[item.DeduplicationKey] = struct{}{}
		if err := (JobCapability{Name: item.Name, SchemaVersion: item.SchemaVersion}).Validate(); err != nil {
			return nil, fmt.Errorf("outbox unique batch item %d: %w", index, err)
		}
		item.AvailableAt = item.AvailableAt.UTC()
	}

	results, err := s.uniqueBatchJobsRepo.CreateJobVersionedUniqueBatch(ctx, prepared)
	if err != nil {
		return nil, fmt.Errorf("create unique versioned job batch: %w", err)
	}
	if len(results) != len(prepared) {
		return nil, fmt.Errorf("unique batch repository returned %d results for %d items", len(results), len(prepared))
	}
	for index, result := range results {
		if result.JobID.IsZero() {
			return nil, fmt.Errorf("unique batch repository returned an empty JobID at index %d", index)
		}
	}

	return results, nil
}

// GetQueueStats returns queue totals when JobsStatRepository is configured.
// If stats repo is not set, returns sharederrors.ErrJobStatNotInit.
func (s *Service) GetQueueStats(ctx context.Context) (QueueStats, error) {
	if s.jobsStatRepo == nil {
		return QueueStats{}, sharederrors.ErrJobStatNotInit
	}

	observedAt := time.Now().UTC()
	stats, err := s.jobsStatRepo.GetQueueStats(ctx, observedAt)
	if err != nil {
		return QueueStats{}, fmt.Errorf("get queue stats: %w", err)
	}
	stats.ObservedAt = observedAt
	for index := range stats.ByCapability {
		if !stats.ByCapability[index].OldestAvailableAt.IsZero() {
			stats.ByCapability[index].OldestAvailableAt = stats.ByCapability[index].OldestAvailableAt.UTC()
		}
	}
	sort.Slice(stats.ByCapability, func(i, j int) bool {
		if stats.ByCapability[i].Name == stats.ByCapability[j].Name {
			return stats.ByCapability[i].SchemaVersion < stats.ByCapability[j].SchemaVersion
		}
		return stats.ByCapability[i].Name < stats.ByCapability[j].Name
	})

	return stats, nil
}
