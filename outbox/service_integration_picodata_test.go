//go:build integration

package outbox_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/assurrussa/outbox/infrastructure/picodata"
	"github.com/assurrussa/outbox/infrastructure/picodata/repositories/jobsfailedrepo"
	"github.com/assurrussa/outbox/infrastructure/picodata/repositories/jobsrepo"
	"github.com/assurrussa/outbox/infrastructure/picodata/storage/transaction"
	picodatatests "github.com/assurrussa/outbox/infrastructure/picodata/tests"
	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/shared/tests"
)

type TestPicodataSuite struct {
	suite.Suite

	db       picodata.Client
	dbHelper *picodatatests.DBHelper
	cleanUp  func(context.Context)

	ctrl *gomock.Controller

	outboxSvc *outbox.Service

	jobsRepo       *jobsrepo.Repo
	jobsFailedRepo *jobsfailedrepo.Repo
}

func NewTestPicodataSuite(t *testing.T, opts ...picodatatests.OptionDatabase) (context.Context, context.CancelFunc, *TestPicodataSuite) {
	return tests.NewSuite[*TestPicodataSuite](t, func(t *testing.T, ctx context.Context) *TestPicodataSuite {
		db, dbHelper, cleanUp := picodatatests.PrepareDB(ctx, t, "TestJobsSuite", opts...)
		trx := transaction.New(db.Pool())
		jobsRepo := jobsrepo.Must(db, dbHelper.FnGetReplaceName("outbox_jobs"))
		jobsFailedRepo := jobsfailedrepo.Must(db, dbHelper.FnGetReplaceName("outbox_jobs_failed"))
		log := logger.Discard()

		outboxSvc, err := outbox.New(
			outbox.WithWorkers(workers),
			outbox.WithIdleTime(idleTime),
			outbox.WithReserveFor(reserveFor),
			outbox.WithJobsRepo(jobsRepo),
			outbox.WithJobsStatRepo(jobsRepo),
			outbox.WithJobsFailedRepo(jobsFailedRepo),
			outbox.WithTransactor(trx),
			outbox.WithLogger(log),
		)
		require.NoError(t, err)

		return &TestPicodataSuite{
			db:             db,
			dbHelper:       dbHelper,
			cleanUp:        cleanUp,
			outboxSvc:      outboxSvc,
			jobsRepo:       jobsRepo,
			jobsFailedRepo: jobsFailedRepo,
		}
	})
}

func TestPicodataMustRegisterJob(t *testing.T) {
	ctx, _, ts := NewTestPicodataSuite(t)
	defer ts.cleanUp(ctx)

	job := newJobMock("duplicated_job", nop, time.Second, 1)

	ts.NotPanics(func() {
		ts.outboxSvc.MustRegisterJob(job)
	})

	ts.Panics(func() {
		ts.outboxSvc.MustRegisterJob(job)
	})
}

func TestPicodataPutJob(t *testing.T) {
	ctx, _, ts := NewTestPicodataSuite(t)
	defer ts.cleanUp(ctx)
	// Arrange.
	const jobName = "TestPutJob"
	const jobPayload = "{}"
	availableAt := time.Now()

	// Action.
	jobID, err := ts.outboxSvc.Put(ctx, jobName, jobPayload, availableAt)
	ts.Require().NoError(err)

	// Assert.
	j, err := ts.jobsRepo.GetByID(ctx, jobID)
	ts.Require().NoError(err)
	ts.Equal(jobID, j.ID)
	ts.Equal(jobName, j.Name)
	ts.Equal(jobPayload, j.Payload)
	ts.Equal(0, j.Attempts)
	ts.Equal(availableAt.Unix(), j.AvailableAt.Unix())
	ts.Empty(j.ReservedAt)
	ts.NotEmpty(j.CreatedAt)
}

func TestPicodataAllJobsProcessed(t *testing.T) {
	ctx, _, ts := NewTestPicodataSuite(t)
	defer ts.cleanUp(ctx)
	// Arrange.
	const jobName = "TestAllJobsProcessed"

	job := newJobMock(jobName, nop, time.Second, 1)
	ts.outboxSvc.MustRegisterJob(job)

	const jobsCount = 30
	for i := 0; i < jobsCount; i++ {
		_, err := ts.outboxSvc.Put(ctx, jobName, `{messageId:"4242"}`, time.Now())
		ts.Require().NoError(err)
	}

	// Action.
	runPicodataOutboxFor(ctx, ts, time.Second)

	// Assert.
	ts.Equal(jobsCount, job.ExecutedTimes())

	count, err := ts.jobsRepo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Require().Equal(int64(0), count)
	count, err = ts.jobsFailedRepo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Require().Equal(int64(0), count)
}

func TestPicodataDLQ_UnknownJob(t *testing.T) {
	ctx, _, ts := NewTestPicodataSuite(t)
	defer ts.cleanUp(ctx)
	// Arrange.
	const jobName = "unknown-job"
	const jobPayload = "{}"
	_, err := ts.outboxSvc.Put(ctx, jobName, jobPayload, time.Now())
	ts.Require().NoError(err)

	// Action.
	runPicodataOutboxFor(ctx, ts, time.Second)

	// Assert.
	count, err := ts.jobsRepo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Require().Equal(int64(0), count)
	count, err = ts.jobsFailedRepo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Require().Equal(int64(1), count)

	data, err := ts.jobsFailedRepo.All(ctx)
	ts.Require().NoError(err)
	ts.Len(data, 1)
	j := data[0]
	ts.NotEmpty(j.ID)
	ts.Equal(jobName, j.Name)
	ts.Equal(jobPayload, j.Payload)
	ts.NotEmpty(j.Reason)
	ts.NotEmpty(j.CreatedAt)
}

func TestPicodataDLQ_AfterMaxAttemptsExceeding(t *testing.T) {
	ctx, _, ts := NewTestPicodataSuite(t)
	defer ts.cleanUp(ctx)
	// Arrange.
	const jobName = "TestDLQ_AfterMaxAttemptsExceeding"
	const jobPayload = "{}"
	const maxAttempts = 3
	availableAt := time.Now()

	var executedTimes int
	job := newJobMock(jobName, func(ctx context.Context, _ string) error {
		executedTimes++
		if executedTimes == maxAttempts-1 {
			select {
			case <-ctx.Done():
				return ctx.Err() // Check job failing after ExecutionTimeout() exceeding.
			case <-time.After(50 * time.Millisecond):
			}
		}
		return errors.New("unknown")
	}, time.Millisecond, maxAttempts)
	ts.outboxSvc.MustRegisterJob(job)

	_, err := ts.outboxSvc.Put(ctx, jobName, jobPayload, availableAt)
	ts.Require().NoError(err)

	// Action.
	runPicodataOutboxFor(ctx, ts, maxAttempts*time.Second)

	// Assert.
	count, err := ts.jobsRepo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Require().Equal(int64(0), count)
	count, err = ts.jobsFailedRepo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Require().Equal(int64(1), count)

	data, err := ts.jobsFailedRepo.All(ctx)
	ts.Require().NoError(err)
	ts.Len(data, 1)
	j := data[0]
	ts.NotEmpty(j.ID)
	ts.Equal(jobName, j.Name)
	ts.Equal(jobPayload, j.Payload)
	ts.NotEmpty(j.Reason)
	ts.NotEmpty(j.CreatedAt)

	ts.Equal(maxAttempts, job.ExecutedTimes())
}

func TestPicodataIfNoJobsThenWorkersSleepForIdleTime(t *testing.T) {
	ctx, _, ts := NewTestPicodataSuite(t)
	defer ts.cleanUp(ctx)
	// Arrange.
	const jobName = "TestIfNoJobsThenWorkersSleepForIdleTime"

	job := newJobMock(jobName, nop, time.Second, 1)
	ts.outboxSvc.MustRegisterJob(job)

	// Action.
	cancel, errCh := runPicodataOutbox(ctx, ts)
	defer cancel()

	// Assert.
	time.Sleep(idleTime / 25)

	const jobsCount = 3
	for i := 0; i < jobsCount; i++ {
		_, err := ts.outboxSvc.Put(ctx, jobName, fmt.Sprintf(`{messageId:"%d"}`, i), time.Now())
		ts.Require().NoError(err)
	}

	count, err := ts.jobsRepo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Require().Equal(int64(jobsCount), count) // Workers fell asleep before the jobsrepo appearing.
	ts.Equal(0, job.ExecutedTimes())

	time.Sleep(2 * idleTime)

	count, err = ts.jobsRepo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Require().Equal(int64(0), count) // Workers woke up and processed the jobsrepo.
	count, err = ts.jobsFailedRepo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Require().Equal(int64(0), count)
	ts.Equal(jobsCount, job.ExecutedTimes())

	cancel()
	ts.NoError(<-errCh)
}

func runPicodataOutboxFor(ctx context.Context, ts *TestPicodataSuite, timeout time.Duration) {
	ts.T().Helper()

	cancel, errCh := runPicodataOutbox(ctx, ts)
	defer cancel()

	time.Sleep(timeout)
	cancel()
	ts.NoError(<-errCh) // No error expected because of graceful shutdown via cancel ctx.
}

func runPicodataOutbox(ctx context.Context, ts *TestPicodataSuite) (context.CancelFunc, <-chan error) {
	ts.T().Helper()

	ctx, cancel := context.WithCancel(ctx)

	errCh := make(chan error)
	go func() { errCh <- ts.outboxSvc.Run(ctx) }()

	return cancel, errCh
}
