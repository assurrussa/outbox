//go:build integration

package outbox_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/pgsql/storage/transaction"
	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/sharederrors"
	"github.com/assurrussa/outbox/shared/types"
)

func TestPostgreSQLBatchClaimFiltersAndFencesLifecycle(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	now := time.Now().UTC().Truncate(time.Microsecond)
	firstID := createPostgreSQLBatchJob(t, ctx, ts, "publish", 1, now.Add(-3*time.Second))
	unsupportedID := createPostgreSQLBatchJob(t, ctx, ts, "publish", 2, now.Add(-2*time.Second))
	secondID := createPostgreSQLBatchJob(t, ctx, ts, "publish", 1, now.Add(-time.Second))
	thirdID := createPostgreSQLBatchJob(t, ctx, ts, "publish", 1, now)
	capabilities := []outbox.JobCapability{{Name: "publish", SchemaVersion: 1}}

	firstToken := types.NewLeaseToken()
	firstBatch, err := ts.jobsRepo.FindAndReserveJobsForCapabilities(
		ctx, now, now.Add(time.Minute), firstToken, capabilities, 2,
	)
	require.NoError(t, err)
	require.Equal(t, []types.JobID{firstID, secondID}, batchJobIDs(firstBatch))

	secondToken := types.NewLeaseToken()
	secondBatch, err := ts.jobsRepo.FindAndReserveJobsForCapabilities(
		ctx, now, now.Add(time.Minute), secondToken, capabilities, 2,
	)
	require.NoError(t, err)
	require.Equal(t, []types.JobID{thirdID}, batchJobIDs(secondBatch))
	_, err = ts.jobsRepo.FindAndReserveJobsForCapabilities(
		ctx, now, now.Add(time.Minute), types.NewLeaseToken(), capabilities, 2,
	)
	require.ErrorIs(t, err, sharederrors.ErrNoJobs)

	affected, err := ts.jobsRepo.ExtendJobLeases(
		ctx, batchJobIDs(firstBatch), types.NewLeaseToken(), now, now.Add(2*time.Minute),
	)
	require.NoError(t, err)
	require.Zero(t, affected)
	affected, err = ts.jobsRepo.ExtendJobLeases(
		ctx, batchJobIDs(firstBatch), firstToken, now, now.Add(2*time.Minute),
	)
	require.NoError(t, err)
	require.Equal(t, int64(2), affected)

	rollbackErr := errors.New("rollback batch release")
	txManager := transaction.New(ts.db.DB())
	err = txManager.RunInTx(ctx, func(txCtx context.Context) error {
		released, releaseErr := ts.jobsRepo.ReleaseUnstartedJobsWithLease(
			txCtx, batchJobIDs(firstBatch), firstToken, now,
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

	affected, err = ts.jobsRepo.ReleaseUnstartedJobsWithLease(ctx, batchJobIDs(firstBatch), firstToken, now)
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

func TestPostgreSQLConcurrentBatchClaimsDoNotOverlap(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	now := time.Now().UTC().Truncate(time.Microsecond)
	for index := 0; index < 20; index++ {
		createPostgreSQLBatchJob(t, ctx, ts, "publish", 1, now)
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

func TestPostgreSQLUniqueBatchPutIsOrderedIdempotentAndAtomic(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	now := time.Now().UTC().Truncate(time.Microsecond)
	items := []outbox.UniqueBatchPut{
		{DeduplicationKey: "pgsql-batch-key-1", Name: "publish", SchemaVersion: 1, Payload: `{"id":1}`, AvailableAt: now},
		{DeduplicationKey: "pgsql-batch-key-2", Name: "publish", SchemaVersion: 1, Payload: `{"id":2}`, AvailableAt: now},
	}
	created, err := ts.jobsRepo.CreateJobVersionedUniqueBatch(ctx, items)
	require.NoError(t, err)
	require.Len(t, created, 2)
	require.True(t, created[0].Created)
	require.True(t, created[1].Created)

	replayed, err := ts.jobsRepo.CreateJobVersionedUniqueBatch(ctx, items)
	require.NoError(t, err)
	require.Equal(t, created[0].JobID, replayed[0].JobID)
	require.Equal(t, created[1].JobID, replayed[1].JobID)
	require.False(t, replayed[0].Created)
	require.False(t, replayed[1].Created)

	before, err := activeJobsCount(ctx, ts.jobsRepo)
	require.NoError(t, err)
	_, err = ts.jobsRepo.CreateJobVersionedUniqueBatch(ctx, []outbox.UniqueBatchPut{
		{DeduplicationKey: "pgsql-created-before-conflict", Name: "publish", SchemaVersion: 1, Payload: `{}`, AvailableAt: now},
		{DeduplicationKey: "pgsql-batch-key-1", Name: "publish", SchemaVersion: 1, Payload: `{"changed":true}`, AvailableAt: now},
	})
	require.ErrorIs(t, err, outbox.ErrIdempotencyConflict)
	after, err := activeJobsCount(ctx, ts.jobsRepo)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestPostgreSQLBatchOutcomesAreAtomicAndAttemptAware(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	now := time.Now().UTC().Truncate(time.Microsecond)
	created := make([]types.JobID, 4)
	for index := range created {
		created[index] = createPostgreSQLBatchJob(t, ctx, ts, "publish", 1, now)
	}
	token := types.NewLeaseToken()
	claimed, err := ts.jobsRepo.FindAndReserveJobsForCapability(
		ctx, now, now.Add(time.Minute), token,
		outbox.JobCapability{Name: "publish", SchemaVersion: 1}, len(created),
	)
	require.NoError(t, err)
	require.ElementsMatch(t, created, batchJobIDs(claimed))
	ids := batchJobIDs(claimed)

	retryAt := now.Add(time.Minute)
	deferAt := now.Add(2 * time.Minute)
	affected, err := ts.jobsRepo.ApplyBatchJobOutcomes(ctx, token, now, []outbox.BatchJobOutcome{
		{JobID: ids[0], Kind: outbox.BatchJobOutcomeSuccess},
		{JobID: ids[1], Kind: outbox.BatchJobOutcomeRetry, AvailableAt: retryAt},
		{JobID: ids[2], Kind: outbox.BatchJobOutcomeDefer, AvailableAt: deferAt},
		{JobID: ids[3], Kind: outbox.BatchJobOutcomeDLQ, Reason: "permanent"},
	})
	require.NoError(t, err)
	require.Equal(t, int64(4), affected)

	_, err = ts.jobsRepo.GetByID(ctx, ids[0])
	require.ErrorIs(t, err, sharederrors.ErrNoJobs)
	retryJob, err := ts.jobsRepo.GetByID(ctx, ids[1])
	require.NoError(t, err)
	require.Equal(t, 1, retryJob.Attempts)
	require.Equal(t, retryAt.UnixMicro(), retryJob.AvailableAt.UnixMicro())
	deferredJob, err := ts.jobsRepo.GetByID(ctx, ids[2])
	require.NoError(t, err)
	require.Zero(t, deferredJob.Attempts)
	require.Equal(t, deferAt.UnixMicro(), deferredJob.AvailableAt.UnixMicro())
	_, err = ts.jobsRepo.GetByID(ctx, ids[3])
	require.ErrorIs(t, err, sharederrors.ErrNoJobs)
}

func createPostgreSQLBatchJob(
	t *testing.T,
	ctx context.Context,
	ts *TestSuite,
	name string,
	schemaVersion outbox.SchemaVersion,
	availableAt time.Time,
) types.JobID {
	t.Helper()
	jobID, err := ts.jobsRepo.CreateJobVersioned(ctx, name, schemaVersion, `{}`, availableAt)
	require.NoError(t, err)
	return jobID
}

func batchJobIDs(jobs []models.Job) []types.JobID {
	jobIDs := make([]types.JobID, 0, len(jobs))
	for _, job := range jobs {
		jobIDs = append(jobIDs, job.ID)
	}
	return jobIDs
}
