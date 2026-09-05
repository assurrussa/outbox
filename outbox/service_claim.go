package outbox

import (
	"context"
	"errors"
	"time"

	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/types"
)

var errCancelledClaimCleanup = errors.New("outbox could not release cancelled claim")

func (s *Service) claimJobsWithLease(
	ctx context.Context,
	token LeaseToken,
	claim func(context.Context, time.Time, time.Time) ([]models.Job, error),
) ([]models.Job, error) {
	var jobs []models.Job
	var claimErr, claimCause error
	func() {
		s.claimMu.RLock()
		defer s.claimMu.RUnlock()
		if s.IsDraining() {
			claimErr = ErrServiceDraining
			return
		}
		now := time.Now().UTC()
		until := now.Add(s.reserveFor)
		claimCtx, cancel := context.WithDeadline(ctx, until)
		defer cancel()
		jobs, claimErr = claim(claimCtx, now, until)
		claimCause = context.Cause(claimCtx)
	}()
	if claimErr != nil {
		return nil, claimErr
	}
	if claimCause != nil {
		// A successful return confirms these rows even when cancellation won
		// the admission race. Release outside the drain claim lock, with a
		// cleanup context independent of the cancelled claim.
		if err := s.releaseCancelledClaim(ctx, token, jobs); err != nil {
			return nil, errors.Join(claimCause, errCancelledClaimCleanup, err)
		}
		return nil, claimCause
	}
	return jobs, nil
}

func (s *Service) releaseCancelledClaim(ctx context.Context, token LeaseToken, jobs []models.Job) error {
	confirmed := make([]models.Job, 0, len(jobs))
	seen := make(map[types.JobID]struct{}, len(jobs))
	now := time.Now().UTC()
	for _, job := range jobs {
		if validateClaimedJobLease(job, token, now) != nil {
			continue
		}
		if _, duplicate := seen[job.ID]; duplicate {
			continue
		}
		seen[job.ID] = struct{}{}
		confirmed = append(confirmed, job)
	}
	return s.releaseClaimedBatchTail(ctx, token, confirmed)
}
