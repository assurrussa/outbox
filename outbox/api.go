package outbox

import (
	"context"
	"fmt"
	"time"

	"github.com/assurrussa/outbox/shared/sharederrors"
	"github.com/assurrussa/outbox/shared/types"
)

func (s *Service) Put(ctx context.Context, name, payload string, availableAt time.Time) (types.JobID, error) {
	jobID, err := s.jobsRepo.CreateJob(ctx, name, payload, availableAt)
	if err != nil {
		return types.JobIDNil, fmt.Errorf("create job: %w", err)
	}

	return jobID, nil
}

func (s *Service) GetQueueStats(ctx context.Context) (QueueStats, error) {
	if s.jobsStatRepo == nil {
		return QueueStats{}, sharederrors.ErrJobStatNotInit
	}

	total, err := s.jobsStatRepo.CountExact(ctx)
	if err != nil {
		return QueueStats{}, fmt.Errorf("count exact: %w", err)
	}

	available, err := s.jobsStatRepo.CountAvailable(ctx, time.Now())
	if err != nil {
		return QueueStats{}, fmt.Errorf("count available: %w", err)
	}

	reserved, err := s.jobsStatRepo.CountReserved(ctx, time.Now())
	if err != nil {
		return QueueStats{}, fmt.Errorf("count reserved: %w", err)
	}

	return QueueStats{
		Total:      total,
		Available:  available,
		Processing: reserved,
	}, nil
}
