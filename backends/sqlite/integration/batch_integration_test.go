//go:build integration

package outbox_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/sqlite/storage/transaction"
	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/sharederrors"
	"github.com/assurrussa/outbox/shared/types"
)

func TestSQLiteBatchClaimFiltersAndFencesLifecycle(t *testing.T) {
	ctx, _, ts := NewTestSQLiteSuite(t)
	defer ts.cleanUp(ctx)

	now := time.Now().UTC().Truncate(time.Millisecond)
	firstID := createSQLiteBatchJob(t, ctx, ts, "publish", 1, now.Add(-3*time.Second))
	unsupportedID := createSQLiteBatchJob(t, ctx, ts, "publish", 2, now.Add(-2*time.Second))
	secondID := createSQLiteBatchJob(t, ctx, ts, "publish", 1, now.Add(-time.Second))
	thirdID := createSQLiteBatchJob(t, ctx, ts, "publish", 1, now)
	capabilities := []outbox.JobCapability{{Name: "publish", SchemaVersion: 1}}

	firstToken := types.NewLeaseToken()
	firstBatch, err := ts.jobsRepo.FindAndReserveJobsForCapabilities(
		ctx, now, now.Add(time.Minute), firstToken, capabilities, 2,
	)
	require.NoError(t, err)
	require.Equal(t, []types.JobID{firstID, secondID}, sqliteBatchJobIDs(firstBatch))

	secondToken := types.NewLeaseToken()
	secondBatch, err := ts.jobsRepo.FindAndReserveJobsForCapabilities(
		ctx, now, now.Add(time.Minute), secondToken, capabilities, 2,
	)
	require.NoError(t, err)
	require.Equal(t, []types.JobID{thirdID}, sqliteBatchJobIDs(secondBatch))
	_, err = ts.jobsRepo.FindAndReserveJobsForCapabilities(
		ctx, now, now.Add(time.Minute), types.NewLeaseToken(), capabilities, 2,
	)
	require.ErrorIs(t, err, sharederrors.ErrNoJobs)

	affected, err := ts.jobsRepo.ExtendJobLeases(
		ctx, sqliteBatchJobIDs(firstBatch), types.NewLeaseToken(), now, now.Add(2*time.Minute),
	)
	require.NoError(t, err)
	require.Zero(t, affected)
	affected, err = ts.jobsRepo.ExtendJobLeases(
		ctx, sqliteBatchJobIDs(firstBatch), firstToken, now, now.Add(2*time.Minute),
	)
	require.NoError(t, err)
	require.Equal(t, int64(2), affected)

	rollbackErr := errors.New("rollback batch release")
	txManager := transaction.New(ts.db.DB())
	err = txManager.RunInTx(ctx, func(txCtx context.Context) error {
		released, releaseErr := ts.jobsRepo.ReleaseUnstartedJobsWithLease(
			txCtx, sqliteBatchJobIDs(firstBatch), firstToken, now,
		)
		require.NoError(t, releaseErr)
		require.Equal(t, int64(2), released)
		return rollbackErr
	})
	require.ErrorIs(t, err, rollbackErr)
	stored, err := ts.jobsRepo.GetByID(ctx, firstID)
	require.NoError(t, err)
	require.Equal(t, 1, stored.Attempts)
	require.Equal(t, firstToken, stored.LeaseToken)

	affected, err = ts.jobsRepo.ReleaseUnstartedJobsWithLease(ctx, sqliteBatchJobIDs(firstBatch), firstToken, now)
	require.NoError(t, err)
	require.Equal(t, int64(2), affected)
	for _, jobID := range []types.JobID{firstID, secondID} {
		stored, getErr := ts.jobsRepo.GetByID(ctx, jobID)
		require.NoError(t, getErr)
		require.Zero(t, stored.Attempts)
		require.False(t, stored.ReservedAt.Valid)
		require.True(t, stored.LeaseToken.IsZero())
	}

	affected, err = ts.jobsRepo.DeleteJobWithLease(ctx, thirdID, types.NewLeaseToken(), now)
	require.NoError(t, err)
	require.Zero(t, affected)
	affected, err = ts.jobsRepo.DeleteJobWithLease(ctx, thirdID, secondToken, now)
	require.NoError(t, err)
	require.Equal(t, int64(1), affected)
	unsupported, err := ts.jobsRepo.GetByID(ctx, unsupportedID)
	require.NoError(t, err)
	require.Zero(t, unsupported.Attempts)
}

func TestSQLiteConcurrentBatchClaimsDoNotOverlap(t *testing.T) {
	ctx, _, ts := NewTestSQLiteSuite(t)
	defer ts.cleanUp(ctx)

	now := time.Now().UTC().Truncate(time.Millisecond)
	for index := 0; index < 20; index++ {
		createSQLiteBatchJob(t, ctx, ts, "publish", 1, now)
	}

	start := make(chan struct{})
	result := make(chan []models.Job, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for worker := 0; worker < 2; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			jobs, err := ts.jobsRepo.FindAndReserveJobsForCapabilities(
				ctx,
				now,
				now.Add(time.Minute),
				types.NewLeaseToken(),
				[]outbox.JobCapability{{Name: "publish", SchemaVersion: 1}},
				10,
			)
			if err != nil {
				errs <- err
				return
			}
			result <- jobs
		}()
	}
	close(start)
	wg.Wait()
	close(result)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	seen := make(map[types.JobID]struct{}, 20)
	for jobs := range result {
		require.Len(t, jobs, 10)
		for _, job := range jobs {
			_, duplicate := seen[job.ID]
			require.False(t, duplicate)
			seen[job.ID] = struct{}{}
		}
	}
	require.Len(t, seen, 20)
}

func createSQLiteBatchJob(
	t *testing.T,
	ctx context.Context,
	ts *TestSQLiteSuite,
	name string,
	schemaVersion outbox.SchemaVersion,
	availableAt time.Time,
) types.JobID {
	t.Helper()
	jobID, err := ts.jobsRepo.CreateJobVersioned(ctx, name, schemaVersion, `{}`, availableAt)
	require.NoError(t, err)
	return jobID
}

func sqliteBatchJobIDs(jobs []models.Job) []types.JobID {
	jobIDs := make([]types.JobID, 0, len(jobs))
	for _, job := range jobs {
		jobIDs = append(jobIDs, job.ID)
	}
	return jobIDs
}
