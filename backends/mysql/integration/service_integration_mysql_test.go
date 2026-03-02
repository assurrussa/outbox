//go:build integration

package outbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/assurrussa/outbox/backends/mysql"
	"github.com/assurrussa/outbox/backends/mysql/repositories/jobsfailedrepo"
	"github.com/assurrussa/outbox/backends/mysql/repositories/jobsrepo"
	"github.com/assurrussa/outbox/backends/mysql/storage/transaction"
	mysqltests "github.com/assurrussa/outbox/backends/mysql/tests"
	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/shared/tests"
)

type TestMySQLSuite struct {
	suite.Suite

	db       mysql.Client
	dbHelper *mysqltests.DBHelper
	cleanUp  func(context.Context)

	ctrl *gomock.Controller

	outboxSvc *outbox.Service

	jobsRepo       *jobsrepo.Repo
	jobsFailedRepo *jobsfailedrepo.Repo
}

func NewTestMySQLSuite(
	t *testing.T,
	opts ...mysqltests.OptionDatabase,
) (context.Context, context.CancelFunc, *TestMySQLSuite) {
	return tests.NewSuite[*TestMySQLSuite](t, func(t *testing.T, ctx context.Context) *TestMySQLSuite {
		db, dbHelper, cleanUp := mysqltests.PrepareDB(ctx, t, "TestMySQLSuite", opts...)
		trx := transaction.New(db.DB())
		jobsRepo := jobsrepo.Must(db)
		jobsFailedRepo := jobsfailedrepo.Must(db)
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

		return &TestMySQLSuite{
			db:             db,
			dbHelper:       dbHelper,
			cleanUp:        cleanUp,
			outboxSvc:      outboxSvc,
			jobsRepo:       jobsRepo,
			jobsFailedRepo: jobsFailedRepo,
		}
	})
}

func TestMySQLPutJob(t *testing.T) {
	ctx, _, ts := NewTestMySQLSuite(t)
	defer ts.cleanUp(ctx)

	const jobName = "TestMySQLPutJob"
	const jobPayload = "{}"
	availableAt := time.Now().UTC()

	jobID, err := ts.outboxSvc.Put(ctx, jobName, jobPayload, availableAt)
	ts.Require().NoError(err)

	j, err := ts.jobsRepo.GetByID(ctx, jobID)
	ts.Require().NoError(err)
	ts.Equal(jobID, j.ID)
	ts.Equal(jobName, j.Name)
	ts.Equal(jobPayload, j.Payload)
	ts.Equal(0, j.Attempts)
	ts.Equal(availableAt.Unix(), j.AvailableAt.Unix())
	ts.NotEmpty(j.CreatedAt)
}

func TestMySQLAllJobsProcessed(t *testing.T) {
	ctx, _, ts := NewTestMySQLSuite(t)
	defer ts.cleanUp(ctx)

	const jobName = "TestMySQLAllJobsProcessed"

	job := newJobMock(jobName, nop, time.Second, 1)
	ts.outboxSvc.MustRegisterJob(job)

	const jobsCount = 30
	for i := 0; i < jobsCount; i++ {
		_, err := ts.outboxSvc.Put(ctx, jobName, `{messageId:"4242"}`, time.Now().UTC())
		ts.Require().NoError(err)
	}

	runMySQLOutboxFor(ctx, ts, time.Second)

	ts.Equal(jobsCount, job.ExecutedTimes())

	count, err := ts.jobsRepo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Require().Equal(int64(0), count)
	count, err = ts.jobsFailedRepo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Require().Equal(int64(0), count)
}

func TestMySQLDLQUnknownJob(t *testing.T) {
	ctx, _, ts := NewTestMySQLSuite(t)
	defer ts.cleanUp(ctx)

	const jobName = "unknown-job"
	const jobPayload = "{}"
	_, err := ts.outboxSvc.Put(ctx, jobName, jobPayload, time.Now().UTC())
	ts.Require().NoError(err)

	runMySQLOutboxFor(ctx, ts, time.Second)

	count, err := ts.jobsRepo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Require().Equal(int64(0), count)
	count, err = ts.jobsFailedRepo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Require().Equal(int64(1), count)
}

func TestMySQLDLQAfterMaxAttemptsExceeding(t *testing.T) {
	ctx, _, ts := NewTestMySQLSuite(t)
	defer ts.cleanUp(ctx)

	const jobName = "TestMySQLDLQAfterMaxAttemptsExceeding"
	const jobPayload = "{}"
	const maxAttempts = 3

	var executedTimes int
	job := newJobMock(jobName, func(ctx context.Context, _ string) error {
		executedTimes++
		if executedTimes == maxAttempts-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
		return errors.New("unknown")
	}, time.Millisecond, maxAttempts)
	ts.outboxSvc.MustRegisterJob(job)

	_, err := ts.outboxSvc.Put(ctx, jobName, jobPayload, time.Now().UTC())
	ts.Require().NoError(err)

	runMySQLOutboxFor(ctx, ts, maxAttempts*time.Second)

	count, err := ts.jobsRepo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Require().Equal(int64(0), count)
	count, err = ts.jobsFailedRepo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Require().Equal(int64(1), count)
	ts.Equal(maxAttempts, job.ExecutedTimes())
}

func TestMySQLQueueStats(t *testing.T) {
	ctx, _, ts := NewTestMySQLSuite(t)
	defer ts.cleanUp(ctx)

	const jobName = "TestMySQLQueueStats"
	job := newJobMock(jobName, nop, time.Second, 1)
	ts.outboxSvc.MustRegisterJob(job)

	_, err := ts.outboxSvc.Put(ctx, jobName, `{}`, time.Now().UTC())
	ts.Require().NoError(err)

	stats, err := ts.outboxSvc.GetQueueStats(ctx)
	ts.Require().NoError(err)
	ts.Equal(int64(1), stats.Total)
	ts.Equal(int64(1), stats.Available)
}

func runMySQLOutboxFor(ctx context.Context, ts *TestMySQLSuite, timeout time.Duration) {
	ts.T().Helper()

	cancel, errCh := runMySQLOutbox(ctx, ts)
	defer cancel()

	time.Sleep(timeout)
	cancel()
	ts.NoError(<-errCh)
}

func runMySQLOutbox(ctx context.Context, ts *TestMySQLSuite) (context.CancelFunc, <-chan error) {
	ts.T().Helper()

	ctx, cancel := context.WithCancel(ctx)

	errCh := make(chan error)
	go func() { errCh <- ts.outboxSvc.Run(ctx) }()

	return cancel, errCh
}
