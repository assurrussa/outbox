//go:build integration

package jobsrepo_test

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"golang.org/x/sync/errgroup"

	"github.com/assurrussa/outbox/backends/pgsql"
	"github.com/assurrussa/outbox/backends/pgsql/repositories/jobsrepo"
	pgsqltests "github.com/assurrussa/outbox/backends/pgsql/tests"
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
	return tests.NewSuite[*TestRepoSuite](t, func(t *testing.T, ctx context.Context) *TestRepoSuite {
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
	jobID, err := ts.repo.CreateJob(ctx, jobExpected.Name, jobExpected.Payload, jobExpected.AvailableAt)
	ts.Require().NoError(err)
	ts.NotEmpty(jobID)
	jobExpected.ID = jobID

	// Action.
	job, err := ts.repo.FindAndReserveJob(ctx, nowTime(), reservationTime())

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
		jobID, err := ts.repo.CreateJob(ctx, jobExpected.Name, jobExpected.Payload, jobExpected.AvailableAt)
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
			job, err := ts.repo.FindAndReserveJob(ctx, nowTime(), reservationTime())
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
			_, err := ts.repo.FindAndReserveJob(ctx, nowTime(), reservationTime())
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
		jobID, err := ts.repo.CreateJob(ctx, jobExpected.Name, jobExpected.Payload, jobExpected.AvailableAt)
		ts.Require().NoError(err)
		ts.NotEmpty(jobID)

		// Action.
		job, err := ts.repo.FindAndReserveJob(ctx, nowTime(), reservationTime())

		// Assert.
		ts.Require().ErrorIs(err, sharederrors.ErrNoJobs)
		ts.Empty(job)
	}

	{
		// Arrange.
		time.Sleep(3 * time.Second)

		// Action.
		job, err := ts.repo.FindAndReserveJob(ctx, nowTime(), reservationTime())

		// Assert.
		ts.Require().NoError(err)
		ts.NotEmpty(job)
	}
}

func Test_FindAndReserveJob_JobNotFound(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)
	// Action.
	job, err := ts.repo.FindAndReserveJob(ctx, nowTime(), reservationTime())

	// Assert.
	ts.Require().ErrorIs(err, sharederrors.ErrNoJobs)
	ts.Empty(job.ID)
}

func Test_CreateJob(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	// Action.
	jobExpected := createModel()
	jobID, err := ts.repo.CreateJob(ctx, jobExpected.Name, jobExpected.Payload, jobExpected.AvailableAt)

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

func Test_CreateJob_Multiple(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	// Arrange.
	const jobs = 3

	// Action.
	for i := 0; i < jobs; i++ {
		jobExpected := createModel()
		jobID, err := ts.repo.CreateJob(ctx, jobExpected.Name, jobExpected.Payload, jobExpected.AvailableAt)
		ts.Require().NoError(err)
		ts.NotEmpty(jobID)
	}

	// Assert.
	count, err := ts.repo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Equal(int64(jobs), count)

	// light it's trigger in DB...
	time.Sleep(time.Millisecond * 2000)
	count, err = ts.repo.CountLight(ctx)
	ts.Require().NoError(err)
	ts.Equal(int64(jobs), count)
}

func Test_DeleteJob(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	// Arrange.
	jobExpected := createModel()
	jobID, err := ts.repo.CreateJob(ctx, jobExpected.Name, jobExpected.Payload, jobExpected.AvailableAt)
	ts.Require().NoError(err)
	ts.Require().NotEmpty(jobID)

	// Action.
	count, err := ts.repo.DeleteJob(ctx, jobID)

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
	// Action.
	count, err := ts.repo.DeleteJob(ctx, types.NewJobID())

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

func createModels(t *testing.T, ts *TestRepoSuite, ctx context.Context, size int) []models.Job {
	t.Helper()

	tmCreate := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	list := make([]models.Job, 0, 100)
	for i := 1; i <= size; i++ {
		strIndex := "__" + strconv.Itoa(i)
		rowModel := models.Job{
			Queue:       "queue",
			Name:        "TestName_" + strIndex,
			Payload:     payload,
			Attempts:    i % 3,
			ReservedAt:  sql.NullTime{Valid: true, Time: tmCreate.Add(time.Duration(i) * time.Minute)}, // разные времена создания
			AvailableAt: tmCreate.Add(time.Duration(i) * time.Minute),                                  // разные времена создания
			CreatedAt:   tmCreate.Add(time.Duration(i) * time.Minute),                                  // разные времена создания
		}

		if i%3 == 0 {
			rowModel.ReservedAt = sql.NullTime{}
		}

		id, err := ts.repo.Create(ctx, rowModel)
		ts.Require().NoError(err)
		rowModel.ID = id

		list = append(list, rowModel)
	}

	return list
}
