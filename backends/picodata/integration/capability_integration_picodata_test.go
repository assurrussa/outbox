//go:build integration

package outbox_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/picodata/storage/transaction"
	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/sharederrors"
	"github.com/assurrussa/outbox/shared/types"
)

func TestPicodataRejectsReservationBatchAboveOne(t *testing.T) {
	ctx, _, ts := NewTestPicodataSuite(t)
	defer ts.cleanUp(ctx)

	service, err := outbox.New(
		outbox.WithReservationBatchSize(2),
		outbox.WithJobsRepo(ts.jobsRepo),
		outbox.WithJobsFailedRepo(ts.jobsFailedRepo),
		outbox.WithTransactor(transaction.New(ts.db.Pool())),
		outbox.WithLogger(logger.Discard()),
	)
	require.Nil(t, service)
	require.ErrorIs(t, err, outbox.ErrOption)
	require.ErrorIs(t, err, outbox.ErrReservationBatchSizeUnsupported)

	_, err = ts.jobsRepo.FindAndReserveJobsForCapabilities(
		ctx,
		time.Now().UTC(),
		time.Now().UTC().Add(time.Second),
		types.NewLeaseToken(),
		[]outbox.JobCapability{{Name: "versioned", SchemaVersion: 1}},
		2,
	)
	require.ErrorIs(t, err, outbox.ErrReservationBatchSizeUnsupported)
}

func TestPicodataCapabilityClaimLeavesUnsupportedSchemaPending(t *testing.T) {
	ctx, _, ts := NewTestPicodataSuite(t)
	defer ts.cleanUp(ctx)

	jobID, err := ts.jobsRepo.CreateJobVersioned(ctx, "versioned", 2, `{}`, time.Now().UTC())
	require.NoError(t, err)
	_, err = ts.jobsRepo.FindAndReserveJobsForCapabilities(
		ctx,
		time.Now().UTC(),
		time.Now().UTC().Add(time.Second),
		types.NewLeaseToken(),
		[]outbox.JobCapability{{Name: "versioned", SchemaVersion: 1}},
		1,
	)
	require.ErrorIs(t, err, sharederrors.ErrNoJobs)
	job, err := ts.jobsRepo.GetByID(ctx, jobID)
	require.NoError(t, err)
	require.Zero(t, job.Attempts)
	require.Equal(t, outbox.SchemaVersion(2), job.SchemaVersion)
}

func TestPicodataCapabilityLeaseRejectsStaleOwner(t *testing.T) {
	ctx, _, ts := NewTestPicodataSuite(t)
	defer ts.cleanUp(ctx)

	_, err := ts.jobsRepo.CreateJobVersioned(ctx, "versioned", 2, `{}`, time.Now().UTC())
	require.NoError(t, err)
	now := time.Now().UTC()
	token := types.NewLeaseToken()
	job, err := claimPicodataOne(
		ctx,
		ts.jobsRepo,
		now,
		now.Add(time.Second),
		token,
		[]outbox.JobCapability{{Name: "versioned", SchemaVersion: 2}},
	)
	require.NoError(t, err)
	require.Equal(t, token, job.LeaseToken)

	affected, err := ts.jobsRepo.ExtendJobLeases(
		ctx, []types.JobID{job.ID}, types.NewLeaseToken(), now, now.Add(2*time.Second),
	)
	require.NoError(t, err)
	require.Zero(t, affected)
	affected, err = ts.jobsRepo.ExtendJobLeases(
		ctx, []types.JobID{job.ID}, token, now, now.Add(2*time.Second),
	)
	require.NoError(t, err)
	require.Equal(t, int64(1), affected)
	affected, err = ts.jobsRepo.DeleteJobWithLease(ctx, job.ID, types.NewLeaseToken(), now)
	require.NoError(t, err)
	require.Zero(t, affected)
	affected, err = ts.jobsRepo.DeleteJobWithLease(ctx, job.ID, token, now)
	require.NoError(t, err)
	require.Equal(t, int64(1), affected)
}

func TestPicodataCapabilityRescheduleIsFencedAndReleasesLease(t *testing.T) {
	ctx, _, ts := NewTestPicodataSuite(t)
	defer ts.cleanUp(ctx)

	now := time.Now().UTC()
	jobID, err := ts.jobsRepo.CreateJobVersioned(ctx, "versioned", 2, `{}`, now)
	require.NoError(t, err)
	token := types.NewLeaseToken()
	job, err := claimPicodataOne(
		ctx, ts.jobsRepo, now, now.Add(time.Minute), token,
		[]outbox.JobCapability{{Name: "versioned", SchemaVersion: 2}},
	)
	require.NoError(t, err)
	retryAt := now.Add(time.Hour)

	affected, err := ts.jobsRepo.RescheduleJobWithLease(ctx, job.ID, types.NewLeaseToken(), now, retryAt)
	require.NoError(t, err)
	require.Zero(t, affected)
	affected, err = ts.jobsRepo.RescheduleJobWithLease(ctx, job.ID, token, now, retryAt)
	require.NoError(t, err)
	require.Equal(t, int64(1), affected)

	stored, err := ts.jobsRepo.GetByID(ctx, jobID)
	require.NoError(t, err)
	require.WithinDuration(t, retryAt, stored.AvailableAt, time.Microsecond)
	require.False(t, stored.ReservedAt.Valid)
	require.True(t, stored.LeaseToken.IsZero())
}

func TestPicodataFailedJobPreservesSchemaVersion(t *testing.T) {
	ctx, _, ts := NewTestPicodataSuite(t)
	defer ts.cleanUp(ctx)

	jobID := types.NewJobID()
	failedID, err := ts.jobsFailedRepo.CreateFailedJobVersioned(
		ctx, jobID, "versioned", 3, `{}`, "test failure",
	)
	require.NoError(t, err)
	failed, err := ts.jobsFailedRepo.GetByID(ctx, failedID)
	require.NoError(t, err)
	require.Equal(t, outbox.SchemaVersion(3), failed.SchemaVersion)
}

func TestPicodataCapabilityReadsRowsWrittenByLegacyWorkerAfterMigration(t *testing.T) {
	ctx, _, ts := NewTestPicodataSuite(t)
	defer ts.cleanUp(ctx)

	now := time.Now().UTC()
	jobID := types.NewJobID()
	jobsTable := ts.dbHelper.FnGetReplaceName("outbox_jobs")
	insertJob := fmt.Sprintf(`INSERT INTO %s (
		id, queue, name, payload, attempts, reserved_at, available_at, created_at
	) VALUES ($1, $2, $3, $4, 0, NULL, $5, $5);`, jobsTable)
	_, err := ts.db.Pool().Exec(ctx, insertJob, jobID, "default", "legacy", `{}`, now)
	require.NoError(t, err)

	legacyJob, err := ts.jobsRepo.GetByID(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, outbox.DefaultSchemaVersion, legacyJob.SchemaVersion)
	require.True(t, legacyJob.LeaseToken.IsZero())
	claimed, err := claimPicodataOne(
		ctx,
		ts.jobsRepo,
		now,
		now.Add(time.Second),
		types.NewLeaseToken(),
		[]outbox.JobCapability{{Name: "legacy", SchemaVersion: outbox.DefaultSchemaVersion}},
	)
	require.NoError(t, err)
	require.Equal(t, jobID, claimed.ID)

	failedID := types.NewJobID()
	failedTable := ts.dbHelper.FnGetReplaceName("outbox_jobs_failed")
	insertFailed := fmt.Sprintf(`INSERT INTO %s (
		id, job_id, queue, name, payload, reason, failed_at, created_at, connection, exception
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $7, $8, $9);`, failedTable)
	_, err = ts.db.Pool().Exec(
		ctx,
		insertFailed,
		failedID,
		jobID,
		"default",
		"legacy",
		`{}`,
		"legacy failure",
		now,
		"",
		"",
	)
	require.NoError(t, err)
	failed, err := ts.jobsFailedRepo.GetByID(ctx, failedID)
	require.NoError(t, err)
	require.Equal(t, outbox.DefaultSchemaVersion, failed.SchemaVersion)
}

func claimPicodataOne(
	ctx context.Context,
	repo outbox.JobsRepository,
	now time.Time,
	until time.Time,
	leaseToken outbox.LeaseToken,
	capabilities []outbox.JobCapability,
) (models.Job, error) {
	jobs, err := repo.FindAndReserveJobsForCapabilities(
		ctx, now, until, leaseToken, capabilities, 1,
	)
	if err != nil {
		return models.Job{}, err
	}
	if len(jobs) != 1 {
		return models.Job{}, errors.New("claim returned an unexpected batch size")
	}
	return jobs[0], nil
}
