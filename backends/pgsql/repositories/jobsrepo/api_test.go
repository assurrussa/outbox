//go:build integration

package jobsrepo_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"golang.org/x/sync/errgroup"

	"github.com/assurrussa/outbox/backends/pgsql"
	"github.com/assurrussa/outbox/backends/pgsql/repositories/jobsrepo"
	pgsqltests "github.com/assurrussa/outbox/backends/pgsql/tests"
	coreoutbox "github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/sharederrors"
	"github.com/assurrussa/outbox/shared/tests"
	"github.com/assurrussa/outbox/shared/types"
)

var (
	name            = "job_name"
	payload         = "job_payload"
	availableAt     = time.Now()
	nowTime         = func() time.Time { return time.Now().Add(time.Second) }
	reservationTime = func() time.Time { return time.Now().Add(time.Minute) }
)

type TestRepoSuite struct {
	suite.Suite

	db       pgsql.Client
	dbHelper *pgsqltests.DBHelper
	cleanUp  func(context.Context)

	repo *jobsrepo.Repo
}

func NewTestRepoSuite(t *testing.T, opts ...pgsqltests.OptionDatabase) (context.Context, context.CancelFunc, *TestRepoSuite) {
	t.Helper()

	return tests.NewSuite[*TestRepoSuite](t, func(t *testing.T, ctx context.Context) *TestRepoSuite {
		t.Helper()

		db, dbHelper, cleanUp := pgsqltests.PrepareDB(ctx, t, "TestJobsRepoSuite", opts...)
		repo := jobsrepo.Must(jobsrepo.NewOptions(db))

		return &TestRepoSuite{
			db:       db,
			dbHelper: dbHelper,
			cleanUp:  cleanUp,
			repo:     repo,
		}
	})
}

func Test_Init(t *testing.T) {
	assert.Panics(t, func() {
		jobsrepo.Must(jobsrepo.NewOptions(nil))
	})
}

func Test_FindAndReserveJob_JobFoundAndReserved(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	// Arrange.
	jobExpected := createModel()
	jobID, err := ts.repo.CreateJobVersioned(
		ctx, jobExpected.Name, coreoutbox.DefaultSchemaVersion, jobExpected.Payload, jobExpected.AvailableAt,
	)
	ts.Require().NoError(err)
	ts.NotEmpty(jobID)
	jobExpected.ID = jobID

	// Action.
	job, err := claimDefault(ctx, ts.repo, nowTime(), reservationTime())

	// Assert.
	ts.Require().NoError(err)
	ts.Equal(jobExpected.ID, job.ID)

	ts.Run("job processing increases attempts", func() {
		ts.Equal(1, job.Attempts)
	})
}

func Test_FindAndReserveJob_SkipReservedJob(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	// Arrange.
	const jobs = 3
	expected := make([]types.JobID, jobs)
	for i := 0; i < jobs; i++ {
		jobExpected := createModel()
		jobID, err := ts.repo.CreateJobVersioned(
			ctx, jobExpected.Name, coreoutbox.DefaultSchemaVersion, jobExpected.Payload, jobExpected.AvailableAt,
		)
		ts.Require().NoError(err)
		ts.NotEmpty(jobID)
		expected[i] = jobID
	}

	// Action.
	actual := make([]types.JobID, jobs)
	wg, ctx := errgroup.WithContext(ctx)
	for i := 0; i < jobs; i++ {
		i := i
		wg.Go(func() error {
			job, err := claimDefault(ctx, ts.repo, nowTime(), reservationTime())
			if err != nil {
				return err
			}
			actual[i] = job.ID
			return nil
		})
	}
	err := wg.Wait()
	ts.Require().NoError(err)

	wg, ctx = errgroup.WithContext(context.WithoutCancel(ctx)) // Because wg.Wait() cancel context.
	for i := 0; i < jobs; i++ {
		wg.Go(func() error {
			_, err := claimDefault(ctx, ts.repo, nowTime(), reservationTime())
			if nil == err || errors.Is(err, sharederrors.ErrNoJobs) {
				return nil
			}
			return err
		})
	}
	err = wg.Wait()
	ts.Require().NoError(err)

	// Assert.
	ts.ElementsMatch(expected, actual)
}

func Test_FindAndReserveJob_SkipDelayedJob(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)
	{
		// Arrange.
		jobExpected := createModel()
		jobExpected.AvailableAt = time.Now().Add(2 * time.Second)
		jobID, err := ts.repo.CreateJobVersioned(
			ctx, jobExpected.Name, coreoutbox.DefaultSchemaVersion, jobExpected.Payload, jobExpected.AvailableAt,
		)
		ts.Require().NoError(err)
		ts.NotEmpty(jobID)

		// Action.
		job, err := claimDefault(ctx, ts.repo, nowTime(), reservationTime())

		// Assert.
		ts.Require().ErrorIs(err, sharederrors.ErrNoJobs)
		ts.Empty(job)
	}

	{
		// Arrange.
		time.Sleep(3 * time.Second)

		// Action.
		job, err := claimDefault(ctx, ts.repo, nowTime(), reservationTime())

		// Assert.
		ts.Require().NoError(err)
		ts.NotEmpty(job)
	}
}

func Test_FindAndReserveJob_JobNotFound(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)
	// Action.
	job, err := claimDefault(ctx, ts.repo, nowTime(), reservationTime())

	// Assert.
	ts.Require().ErrorIs(err, sharederrors.ErrNoJobs)
	ts.Empty(job.ID)
}

func Test_CapabilityClaimFiltersAndFences(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	now := time.Now().UTC()
	unsupportedID, err := ts.repo.CreateJobVersioned(ctx, "publish", 2, `{"version":2}`, now)
	ts.Require().NoError(err)
	supportedID, err := ts.repo.CreateJobVersioned(ctx, "publish", 1, `{"version":1}`, now)
	ts.Require().NoError(err)

	leaseToken := types.NewLeaseToken()
	reservedUntil := now.Add(time.Minute)
	job, err := claimOne(
		ctx,
		ts.repo,
		now.Add(time.Second),
		reservedUntil,
		leaseToken,
		[]coreoutbox.JobCapability{{Name: "publish", SchemaVersion: 1}},
	)
	ts.Require().NoError(err)
	ts.Equal(supportedID, job.ID)
	ts.Equal(coreoutbox.SchemaVersion(1), job.SchemaVersion)
	ts.Equal(leaseToken, job.LeaseToken)

	unsupported, err := ts.repo.GetByID(ctx, unsupportedID)
	ts.Require().NoError(err)
	ts.Zero(unsupported.Attempts)
	ts.True(unsupported.LeaseToken.IsZero())

	affected, err := ts.repo.ExtendJobLeases(
		ctx,
		[]types.JobID{job.ID},
		types.NewLeaseToken(),
		now.Add(2*time.Second),
		reservedUntil.Add(time.Minute),
	)
	ts.Require().NoError(err)
	ts.Zero(affected)

	affected, err = ts.repo.ExtendJobLeases(
		ctx,
		[]types.JobID{job.ID},
		leaseToken,
		now.Add(2*time.Second),
		reservedUntil.Add(time.Minute),
	)
	ts.Require().NoError(err)
	ts.Equal(int64(1), affected)

	affected, err = ts.repo.DeleteJobWithLease(
		ctx,
		job.ID,
		types.NewLeaseToken(),
		now.Add(3*time.Second),
	)
	ts.Require().NoError(err)
	ts.Zero(affected)

	affected, err = ts.repo.DeleteJobWithLease(
		ctx,
		job.ID,
		leaseToken,
		now.Add(3*time.Second),
	)
	ts.Require().NoError(err)
	ts.Equal(int64(1), affected)
}

func Test_CapabilityClaimWithEmptyCapabilitiesLeavesJobsPending(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	now := time.Now().UTC()
	jobID, err := ts.repo.CreateJobVersioned(ctx, "publish", 2, `{}`, now)
	ts.Require().NoError(err)

	_, err = ts.repo.FindAndReserveJobsForCapabilities(
		ctx,
		now.Add(time.Second),
		now.Add(time.Minute),
		types.NewLeaseToken(),
		nil,
		1,
	)
	ts.Require().ErrorIs(err, sharederrors.ErrNoJobs)

	job, err := ts.repo.GetByID(ctx, jobID)
	ts.Require().NoError(err)
	ts.Zero(job.Attempts)
}

func Test_RescheduleJobWithLeaseIsFencedAndReleasesLease(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	now := time.Now().UTC().Truncate(time.Microsecond)
	jobID, err := ts.repo.CreateJobVersioned(ctx, "publish", 2, `{}`, now)
	ts.Require().NoError(err)
	token := types.NewLeaseToken()
	job, err := claimOne(
		ctx, ts.repo, now, now.Add(time.Minute), token,
		[]coreoutbox.JobCapability{{Name: "publish", SchemaVersion: 2}},
	)
	ts.Require().NoError(err)
	retryAt := now.Add(time.Hour)

	affected, err := ts.repo.RescheduleJobWithLease(ctx, job.ID, types.NewLeaseToken(), now, retryAt)
	ts.Require().NoError(err)
	ts.Zero(affected)
	affected, err = ts.repo.RescheduleJobWithLease(ctx, job.ID, token, now, retryAt)
	ts.Require().NoError(err)
	ts.Equal(int64(1), affected)

	stored, err := ts.repo.GetByID(ctx, jobID)
	ts.Require().NoError(err)
	ts.True(retryAt.Equal(stored.AvailableAt), "stored retry instant: %s", stored.AvailableAt)
	ts.False(stored.ReservedAt.Valid)
	ts.True(stored.LeaseToken.IsZero())
}

func Test_CreateJobVersionedUniqueResultDistinguishesCreateFromReplay(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	availableAt := time.Now().UTC().Truncate(time.Microsecond)
	first, err := ts.repo.CreateJobVersionedUniqueResult(
		ctx, "message-1", "publish", 1, `{}`, availableAt,
	)
	ts.Require().NoError(err)
	ts.True(first.Created)
	replayed, err := ts.repo.CreateJobVersionedUniqueResult(
		ctx, "message-1", "publish", 1, `{}`, availableAt,
	)
	ts.Require().NoError(err)
	ts.False(replayed.Created)
	ts.Equal(first.JobID, replayed.JobID)
}

func Test_CreateJobVersioned(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	// Action.
	jobExpected := createModel()
	jobID, err := ts.repo.CreateJobVersioned(
		ctx, jobExpected.Name, coreoutbox.DefaultSchemaVersion, jobExpected.Payload, jobExpected.AvailableAt,
	)

	// Assert.
	ts.Require().NoError(err)
	ts.NotEmpty(jobID)

	// Checking if job was created.
	job, err := ts.repo.GetByID(ctx, jobID)
	ts.Require().NoError(err)
	ts.NotNil(job)
	ts.Equal(jobID, job.ID)
	ts.Equal(name, job.Name)
	ts.Equal(payload, job.Payload)
	ts.Equal(
		availableAt.Format("2006-01-02 15-01-05"),
		job.AvailableAt.Format("2006-01-02 15-01-05"),
	)
}

func Test_CreateJobVersionedUniqueRetainsIdempotencyAfterAck(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	availableAt := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	firstID, err := ts.repo.CreateJobVersionedUnique(
		ctx,
		"delivery:event-1:webhook-1",
		"fanout.webhook.cms.entry.published",
		2,
		`{"deliveryId":"delivery-1"}`,
		availableAt,
	)
	ts.Require().NoError(err)
	secondID, err := ts.repo.CreateJobVersionedUnique(
		ctx,
		"delivery:event-1:webhook-1",
		"fanout.webhook.cms.entry.published",
		2,
		`{"deliveryId":"delivery-1"}`,
		availableAt,
	)
	ts.Require().NoError(err)
	ts.Equal(firstID, secondID)
	pruned, err := ts.repo.PruneJobIdempotencyKeys(ctx, time.Now().UTC().Add(time.Minute), 10)
	ts.Require().NoError(err)
	ts.Zero(pruned)

	leaseToken := types.NewLeaseToken()
	job, err := claimOne(
		ctx,
		ts.repo,
		time.Now().UTC(),
		time.Now().UTC().Add(time.Minute),
		leaseToken,
		[]coreoutbox.JobCapability{{
			Name:          "fanout.webhook.cms.entry.published",
			SchemaVersion: 2,
		}},
	)
	ts.Require().NoError(err)
	_, err = ts.repo.DeleteJobWithLease(ctx, job.ID, leaseToken, time.Now().UTC())
	ts.Require().NoError(err)

	replayedID, err := ts.repo.CreateJobVersionedUnique(
		ctx,
		"delivery:event-1:webhook-1",
		"fanout.webhook.cms.entry.published",
		2,
		`{"deliveryId":"delivery-1"}`,
		availableAt,
	)
	ts.Require().NoError(err)
	ts.Equal(firstID, replayedID)

	_, err = ts.repo.FindAndReserveJobsForCapabilities(
		ctx,
		time.Now().UTC(),
		time.Now().UTC().Add(time.Minute),
		types.NewLeaseToken(),
		[]coreoutbox.JobCapability{{
			Name:          "fanout.webhook.cms.entry.published",
			SchemaVersion: 2,
		}},
		1,
	)
	ts.Require().ErrorIs(err, sharederrors.ErrNoJobs)

	_, err = ts.repo.CreateJobVersionedUnique(
		ctx,
		"delivery:event-1:webhook-1",
		"fanout.webhook.cms.entry.published",
		2,
		`{"deliveryId":"different"}`,
		availableAt,
	)
	ts.Require().ErrorIs(err, coreoutbox.ErrIdempotencyConflict)

	pruned, err = ts.repo.PruneJobIdempotencyKeys(ctx, time.Now().UTC().Add(time.Minute), 10)
	ts.Require().NoError(err)
	ts.Equal(int64(1), pruned)

	newID, err := ts.repo.CreateJobVersionedUnique(
		ctx,
		"delivery:event-1:webhook-1",
		"fanout.webhook.cms.entry.published",
		2,
		`{"deliveryId":"different"}`,
		availableAt,
	)
	ts.Require().NoError(err)
	ts.NotEqual(firstID, newID)
}

func Test_CreateJobVersioned_Multiple(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	// Arrange.
	const jobs = 3

	// Action.
	for i := 0; i < jobs; i++ {
		jobExpected := createModel()
		jobID, err := ts.repo.CreateJobVersioned(
			ctx, jobExpected.Name, coreoutbox.DefaultSchemaVersion, jobExpected.Payload, jobExpected.AvailableAt,
		)
		ts.Require().NoError(err)
		ts.NotEmpty(jobID)
	}

	// Assert.
	stats, err := ts.repo.GetQueueStats(ctx, time.Now().UTC())
	ts.Require().NoError(err)
	ts.Equal(int64(jobs), stats.Total)
}

func Test_DeleteJobWithLease(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	// Arrange.
	jobExpected := createModel()
	jobID, err := ts.repo.CreateJobVersioned(
		ctx, jobExpected.Name, coreoutbox.DefaultSchemaVersion, jobExpected.Payload, jobExpected.AvailableAt,
	)
	ts.Require().NoError(err)
	ts.Require().NotEmpty(jobID)

	// Action.
	leaseToken := types.NewLeaseToken()
	job, err := claimOne(
		ctx,
		ts.repo,
		nowTime(),
		reservationTime(),
		leaseToken,
		[]coreoutbox.JobCapability{{Name: jobExpected.Name, SchemaVersion: coreoutbox.DefaultSchemaVersion}},
	)
	ts.Require().NoError(err)
	count, err := ts.repo.DeleteJobWithLease(ctx, job.ID, leaseToken, time.Now().UTC())

	// Assert.
	ts.Require().NoError(err)
	ts.Equal(int64(1), count)

	// Checking if failed job was deleted.
	job, err = ts.repo.GetByID(ctx, jobID)
	ts.Require().ErrorIs(err, sharederrors.ErrNoJobs)
	ts.Empty(job)
}

func Test_DeleteJobWithLease_NoJobs(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)
	// Action.
	count, err := ts.repo.DeleteJobWithLease(
		ctx, types.NewJobID(), types.NewLeaseToken(), time.Now().UTC(),
	)

	// Assert.
	ts.Require().NoError(err)
	ts.Equal(int64(0), count)
}

func createModel() models.Job {
	return models.Job{
		Queue:       "queue",
		Name:        name,
		Payload:     payload,
		Attempts:    0,
		AvailableAt: availableAt,
		CreatedAt:   availableAt,
	}
}

func claimDefault(
	ctx context.Context,
	repo *jobsrepo.Repo,
	now time.Time,
	until time.Time,
) (models.Job, error) {
	return claimOne(
		ctx,
		repo,
		now,
		until,
		types.NewLeaseToken(),
		[]coreoutbox.JobCapability{{Name: name, SchemaVersion: coreoutbox.DefaultSchemaVersion}},
	)
}

func claimOne(
	ctx context.Context,
	repo *jobsrepo.Repo,
	now time.Time,
	until time.Time,
	leaseToken coreoutbox.LeaseToken,
	capabilities []coreoutbox.JobCapability,
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
