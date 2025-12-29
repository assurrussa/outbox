//go:build integration

package jobsfailedrepo_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"

	"github.com/assurrussa/outbox/infrastructure/pgsql"
	"github.com/assurrussa/outbox/infrastructure/pgsql/repositories/jobsfailedrepo"
	"github.com/assurrussa/outbox/infrastructure/pgsql/repositories/jobsrepo"
	pgsqltests "github.com/assurrussa/outbox/infrastructure/pgsql/tests"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/sharederrors"
	"github.com/assurrussa/outbox/shared/tests"
	"github.com/assurrussa/outbox/shared/types"
)

var (
	name     = "job_name"
	payload  = "job_payload"
	reason   = "any reason"
	failedAt = time.Now()
)

type TestRepoSuite struct {
	suite.Suite

	db       pgsql.Client
	dbHelper *pgsqltests.DBHelper
	cleanUp  func(context.Context)

	repo *jobsfailedrepo.Repo
}

func NewTestRepoSuite(t *testing.T, opts ...pgsqltests.OptionDatabase) (context.Context, context.CancelFunc, *TestRepoSuite) {
	return tests.NewSuite[*TestRepoSuite](t, func(t *testing.T, ctx context.Context) *TestRepoSuite {
		db, dbHelper, cleanUp := pgsqltests.PrepareDB(ctx, t, "TestJobsFailedRepoSuite", opts...)
		repo := jobsfailedrepo.Must(jobsfailedrepo.NewOptions(db))

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

func Test_CreateFailedJob(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	failedJobID, err := ts.repo.CreateFailedJob(ctx, types.NewJobID(), name, payload, reason)

	// Assert.
	ts.Require().NoError(err)

	// Checking if failed job was created.
	fJob, err := ts.repo.GetByID(ctx, failedJobID)
	ts.Require().NoError(err)
	ts.Require().NotNil(fJob)
	ts.NotEmpty(fJob.ID)
	ts.Equal(name, fJob.Name)
	ts.Equal(payload, fJob.Payload)
	ts.Equal(reason, fJob.Reason)
}

func Test_CreateFailedJob_Multiple(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	// Arrange.
	const fJobs = 3

	// Action.
	for i := 0; i < fJobs; i++ {
		_, err := ts.repo.CreateFailedJob(ctx, types.NewJobID(), name, payload, reason)
		ts.Require().NoError(err)
	}

	// Assert.
	count, err := ts.repo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Equal(int64(fJobs), count)

	// light it's trigger in DB...
	time.Sleep(time.Millisecond * 2000)
	count, err = ts.repo.CountLight(ctx)
	ts.Require().NoError(err)
	ts.Equal(int64(fJobs), count)
}

func Test_DeleteJob(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	// Arrange.
	jobExpected := createModel()
	jobID, err := ts.repo.CreateFailedJob(ctx, types.NewJobID(), jobExpected.Name, jobExpected.Payload, jobExpected.Reason)
	ts.Require().NoError(err)
	ts.Require().NotEmpty(jobID)

	// Action.
	count, err := ts.repo.Delete(ctx, jobID)

	// Assert.
	ts.Require().NoError(err)
	ts.Equal(int64(1), count)

	// Checking if failed job was deleted.
	job, err := ts.repo.GetByID(ctx, jobID)
	ts.Require().ErrorIs(err, sharederrors.ErrNoJobs)
	ts.Empty(job)
}

func Test_DeleteJob_NoJobs(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	createModels(t, ts, ctx, 50)

	// Action.
	count, err := ts.repo.Delete(ctx, types.NewJobID())

	// Assert.
	ts.Require().NoError(err)
	ts.Equal(int64(0), count)
}

func createModel() models.JobFailed {
	return models.JobFailed{
		Connection: "",
		Queue:      "queue",
		Name:       name,
		Payload:    payload,
		Reason:     reason,
		FailedAt:   failedAt,
	}
}

func createModels(t *testing.T, ts *TestRepoSuite, ctx context.Context, size int) []models.JobFailed {
	t.Helper()

	tmCreate := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	list := make([]models.JobFailed, 0, 100)
	for i := 1; i <= size; i++ {
		strIndex := "__" + strconv.Itoa(i)
		rowModel := models.JobFailed{
			ID:         types.NewJobID(),
			JobID:      types.NewJobID(),
			Connection: "",
			Queue:      "queue",
			Name:       "TestName_" + strIndex,
			Payload:    payload,
			Reason:     reason,
			FailedAt:   tmCreate.Add(time.Duration(i) * time.Minute), // разные времена создания
		}

		if i%3 == 0 {
			rowModel.FailedAt = tmCreate
		}

		id, err := ts.repo.Create(ctx, rowModel)
		ts.Require().NoError(err)
		rowModel.ID = id

		list = append(list, rowModel)
	}

	return list
}
