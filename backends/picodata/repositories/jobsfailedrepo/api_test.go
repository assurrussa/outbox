//go:build integration

package jobsfailedrepo_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"

	"github.com/assurrussa/outbox/backends/picodata"
	"github.com/assurrussa/outbox/backends/picodata/repositories/jobsfailedrepo"
	"github.com/assurrussa/outbox/backends/picodata/repositories/jobsrepo"
	picodatatests "github.com/assurrussa/outbox/backends/picodata/tests"
	"github.com/assurrussa/outbox/outbox/models"
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

	db       picodata.Client
	dbHelper *picodatatests.DBHelper
	cleanUp  func(context.Context)

	repo *jobsfailedrepo.Repo
}

func NewTestRepoSuite(t *testing.T, opts ...picodatatests.OptionDatabase) (context.Context, context.CancelFunc, *TestRepoSuite) {
	return tests.NewSuite[*TestRepoSuite](t, func(t *testing.T, ctx context.Context) *TestRepoSuite {
		db, dbHelper, cleanUp := picodatatests.PrepareDB(ctx, t, "TestJobsFailedRepoSuite", opts...)
		repo := jobsfailedrepo.Must(db, dbHelper.FnGetReplaceName("outbox_jobs_failed"))

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
		jobsrepo.Must(nil)
	})
}

func Test_CreateFailedJob(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	jobID := types.NewJobID()
	failedJobID, err := ts.repo.CreateFailedJob(ctx, jobID, name, payload, reason)

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
		jobID := types.NewJobID()
		_, err := ts.repo.CreateFailedJob(ctx, jobID, name, payload, reason)
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

func Test_Create_StoresFailedAtAndCreatedAt(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	failedAtExpected := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	createdAtExpected := time.Date(2026, 1, 2, 4, 5, 6, 0, time.UTC)

	id, err := ts.repo.Create(ctx, models.JobFailed{
		JobID:      types.NewJobID(),
		Queue:      "queue",
		Name:       "job",
		Payload:    "payload",
		Reason:     "reason",
		FailedAt:   failedAtExpected,
		CreatedAt:  createdAtExpected,
		Connection: "conn",
		Exception:  "exc",
	})
	ts.Require().NoError(err)

	got, err := ts.repo.GetByID(ctx, id)
	ts.Require().NoError(err)
	ts.Equal(failedAtExpected.UnixMilli(), got.FailedAt.UnixMilli())
	ts.Equal(createdAtExpected.UnixMilli(), got.CreatedAt.UnixMilli())
}

func Test_DeleteJob(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	// Arrange.
	jobExpected := createModel()
	jobID, err := ts.repo.CreateFailedJob(ctx, jobExpected.JobID, jobExpected.Name, jobExpected.Payload, jobExpected.Reason)
	ts.Require().NoError(err)
	ts.Require().NotEmpty(jobID)

	// Action.
	count, err := ts.repo.Delete(ctx, jobID)

	// Assert.
	ts.Require().NoError(err)
	ts.Equal(int64(1), count)

	// Checking if failed job was deleted.
	job, err := ts.repo.GetByID(ctx, jobID)
	ts.Require().Error(err)
	ts.Empty(job)
}

func Test_DeleteJob_NoJobs(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)
	// Action.
	count, err := ts.repo.Delete(ctx, types.NewJobID())

	// Assert.
	ts.Require().NoError(err)
	ts.Equal(int64(0), count)
}

func Test_ListPaged(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	all, err := ts.repo.All(ctx)
	ts.Require().NoError(err)
	for _, job := range all {
		_, _ = ts.repo.Delete(ctx, job.ID)
	}

	now := time.Now()
	jobs := []models.JobFailed{
		{JobID: types.NewJobID(), Queue: "q", Name: "newest", Payload: "p1", Reason: "r1", FailedAt: now, CreatedAt: now},
		{JobID: types.NewJobID(), Queue: "q", Name: "middle", Payload: "p2", Reason: "r2", FailedAt: now.Add(-time.Minute), CreatedAt: now.Add(-time.Minute)},
		{JobID: types.NewJobID(), Queue: "q", Name: "oldest", Payload: "p3", Reason: "r3", FailedAt: now.Add(-2 * time.Minute), CreatedAt: now.Add(-2 * time.Minute)},
	}

	for i := range jobs {
		id, err := ts.repo.Create(ctx, jobs[i])
		ts.Require().NoError(err)
		jobs[i].ID = id
	}

	page1, err := ts.repo.ListPaged(ctx, 2, time.Now())
	ts.Require().NoError(err)
	ts.Require().Len(page1, 2)
	assert.Equal(t, "newest", page1[0].Name)
	assert.Equal(t, "middle", page1[1].Name)

	cursor := page1[len(page1)-1].CreatedAt.Add(-time.Nanosecond)
	page2, err := ts.repo.ListPaged(ctx, 2, cursor)
	ts.Require().NoError(err)
	ts.Require().Len(page2, 1)
	assert.Equal(t, "oldest", page2[0].Name)
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
