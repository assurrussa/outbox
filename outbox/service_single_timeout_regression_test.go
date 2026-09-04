package outbox_test

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/types"
)

type timeoutTestJob struct {
	outbox.DefaultJob
	name        string
	timeout     time.Duration
	maxAttempts int
	executed    atomic.Int32
}

func (j *timeoutTestJob) Name() string {
	return j.name
}

func (j *timeoutTestJob) SchemaVersion() outbox.SchemaVersion {
	return 1
}

func (j *timeoutTestJob) ExecutionTimeout() time.Duration {
	return j.timeout
}

func (j *timeoutTestJob) MaxAttempts() int {
	return j.maxAttempts
}

func (j *timeoutTestJob) Handle(ctx context.Context, _ string) error {
	j.executed.Add(1)
	<-ctx.Done()
	// Buggy handler returns nil despite timeout/cancellation
	return nil
}

type singleTimeoutTrackingRepo struct {
	*capabilityRepo
	deleteCalls atomic.Int32
}

func (r *singleTimeoutTrackingRepo) DeleteJobWithLease(
	ctx context.Context,
	jobID types.JobID,
	leaseToken outbox.LeaseToken,
	now time.Time,
) (int64, error) {
	r.deleteCalls.Add(1)
	return r.capabilityRepo.DeleteJobWithLease(ctx, jobID, leaseToken, now)
}

func TestSingleJobTimeoutDoesNotAck(t *testing.T) {
	t.Run("attempts under max: timeout does not ACK, delete is not called, attempts not compensated", func(t *testing.T) {
		baseRepo := newCapabilityRepo()
		jobID := types.NewJobID()
		baseRepo.jobs = append(baseRepo.jobs, models.Job{
			ID:            jobID,
			Queue:         testQueue,
			Name:          "timeout.job",
			SchemaVersion: 1,
			Attempts:      0,
			ReservedAt:    sql.NullTime{Valid: false},
			AvailableAt:   time.Now().UTC().Add(-time.Second),
			CreatedAt:     time.Now().UTC(),
		})

		repo := &singleTimeoutTrackingRepo{capabilityRepo: baseRepo}

		jobHandler := &timeoutTestJob{
			name:        "timeout.job",
			timeout:     25 * time.Millisecond,
			maxAttempts: 3,
		}

		svc, err := outbox.New(
			outbox.WithWorkers(1),
			outbox.WithIdleTime(100*time.Millisecond),
			outbox.WithReserveFor(2*time.Second),
			outbox.WithLogger(logger.Discard()),
			outbox.WithJobsRepo(repo),
			outbox.WithJobsFailedRepo(repo),
			outbox.WithTransactor(repo),
		)
		require.NoError(t, err)
		svc.MustRegisterJob(jobHandler)

		runCtx, cancelRun := context.WithCancel(t.Context())
		runErrCh := make(chan error, 1)
		go func() {
			runErrCh <- svc.Run(runCtx)
		}()

		// Wait for job to execute and hit timeout
		require.Eventually(t, func() bool {
			return jobHandler.executed.Load() >= 1
		}, time.Second, 10*time.Millisecond)

		// Allow processing loop to complete processBatchJob
		time.Sleep(100 * time.Millisecond)

		svc.BeginDrain()
		cancelRun()
		runErr := <-runErrCh
		if runErr != nil && !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, outbox.ErrServiceDraining) {
			require.NoError(t, runErr)
		}

		// DeleteJobWithLease MUST NOT be called (job not ACKed)
		require.Equal(t, int32(0), repo.deleteCalls.Load(), "DeleteJobWithLease must NOT be called on timeout")

		// Job must still exist in repository (leased for expiry-based recovery)
		jobs := repo.Jobs()
		require.Len(t, jobs, 1)
		require.Equal(t, jobID, jobs[0].ID)
		// Attempts must be 1 (incremented during claim, NOT decremented/compensated)
		require.Equal(t, 1, jobs[0].Attempts, "Attempts must not be compensated on timeout")
		// No DLQ entry
		require.Empty(t, repo.Failed(), "Job must not be sent to DLQ before max attempts")
	})

	t.Run("attempts reach max: timeout moves job to DLQ, not ACKed as success", func(t *testing.T) {
		baseRepo := newCapabilityRepo()
		jobID := types.NewJobID()
		baseRepo.jobs = append(baseRepo.jobs, models.Job{
			ID:            jobID,
			Queue:         testQueue,
			Name:          "timeout.job.dlq",
			SchemaVersion: 1,
			Attempts:      2, // Claim will make it 3 == maxAttempts
			ReservedAt:    sql.NullTime{Valid: false},
			AvailableAt:   time.Now().UTC().Add(-time.Second),
			CreatedAt:     time.Now().UTC(),
		})

		repo := &singleTimeoutTrackingRepo{capabilityRepo: baseRepo}

		jobHandler := &timeoutTestJob{
			name:        "timeout.job.dlq",
			timeout:     25 * time.Millisecond,
			maxAttempts: 3,
		}

		svc, err := outbox.New(
			outbox.WithWorkers(1),
			outbox.WithIdleTime(100*time.Millisecond),
			outbox.WithReserveFor(2*time.Second),
			outbox.WithLogger(logger.Discard()),
			outbox.WithJobsRepo(repo),
			outbox.WithJobsFailedRepo(repo),
			outbox.WithTransactor(repo),
		)
		require.NoError(t, err)
		svc.MustRegisterJob(jobHandler)

		runCtx, cancelRun := context.WithCancel(t.Context())
		runErrCh := make(chan error, 1)
		go func() {
			runErrCh <- svc.Run(runCtx)
		}()

		// Wait for job to execute, hit timeout, and move to DLQ
		require.Eventually(t, func() bool {
			return len(repo.Failed()) == 1
		}, 2*time.Second, 20*time.Millisecond)

		svc.BeginDrain()
		cancelRun()
		<-runErrCh

		failed := repo.Failed()
		require.Len(t, failed, 1)
		require.Equal(t, jobID, failed[0].JobID)
		require.Contains(t, failed[0].Reason, "max attempts exceeded")
	})
}
