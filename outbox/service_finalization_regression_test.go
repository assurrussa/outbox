package outbox_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/shared/types"
)

type delayedFinalizationRepo struct {
	*capabilityRepo
	headID    types.JobID
	tailID    types.JobID
	delayOnce sync.Once
	tailLive  atomic.Bool
	tailDone  chan struct{}
}

func (r *delayedFinalizationRepo) delayHead(jobID types.JobID) {
	if jobID != r.headID {
		return
	}
	r.delayOnce.Do(func() {
		time.Sleep(1200 * time.Millisecond)
		for _, job := range r.Jobs() {
			if job.ID == r.tailID {
				r.tailLive.Store(job.ReservedAt.Valid && job.ReservedAt.Time.After(time.Now()))
			}
		}
	})
}

func (r *delayedFinalizationRepo) DeleteJobWithLease(
	ctx context.Context, jobID types.JobID, token outbox.LeaseToken, now time.Time,
) (int64, error) {
	r.delayHead(jobID)
	affected, err := r.capabilityRepo.DeleteJobWithLease(ctx, jobID, token, now)
	if jobID == r.tailID && affected == 1 && err == nil {
		close(r.tailDone)
	}
	return affected, err
}

func (r *delayedFinalizationRepo) RescheduleJobWithLease(
	ctx context.Context, jobID types.JobID, token outbox.LeaseToken, now, availableAt time.Time,
) (int64, error) {
	r.delayHead(jobID)
	return r.capabilityRepo.RescheduleJobWithLease(ctx, jobID, token, now, availableAt)
}

func TestSlowFinalizationDoesNotLoseReservationTail(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"successful acknowledgement", "retry", "permanent DLQ"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			base := newCapabilityRepo()
			repo := &delayedFinalizationRepo{capabilityRepo: base, tailDone: make(chan struct{})}
			now := time.Now().UTC().Add(-time.Second)
			var err error
			repo.headID, err = base.CreateJobVersioned(t.Context(), testValueBatch, 1, "head", now)
			require.NoError(t, err)
			repo.tailID, err = base.CreateJobVersioned(t.Context(), testValueBatch, 1, "tail", now.Add(time.Millisecond))
			require.NoError(t, err)
			service, err := outbox.New(
				outbox.WithJobsRepo(repo), outbox.WithJobsFailedRepo(base), outbox.WithTransactor(base),
				outbox.WithReserveFor(time.Second), outbox.WithReservationBatchSize(2),
				outbox.WithIdleTime(100*time.Millisecond), outbox.WithLogger(logger.Discard()),
			)
			require.NoError(t, err)
			service.MustRegisterJob(capabilityJob{name: testValueBatch, version: 1, handle: func(_ context.Context, payload string) error {
				if payload == "head" {
					switch outcome {
					case "retry":
						return outbox.RetryAt(errors.New("retry"), time.Now().Add(time.Hour))
					case "permanent DLQ":
						return outbox.Permanent(errors.New("permanent"))
					}
				}
				return nil
			}})
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- service.Run(ctx) }()
			select {
			case err := <-done:
				t.Fatalf("service stopped before finalizing tail: %v", err)
			case <-ctx.Done():
				t.Fatal("timed out waiting for tail finalization")
			case <-repo.tailDone:
			}
			service.BeginDrain()
			require.NoError(t, <-done)
			require.True(t, repo.tailLive.Load(), "tail lease expired while the head was being finalized")
		})
	}
}
