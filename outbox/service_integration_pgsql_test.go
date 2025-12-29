//go:build integration

package outbox_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.uber.org/goleak"
	"go.uber.org/mock/gomock"

	"github.com/assurrussa/outbox/infrastructure/pgsql"
	"github.com/assurrussa/outbox/infrastructure/pgsql/repositories/jobsfailedrepo"
	"github.com/assurrussa/outbox/infrastructure/pgsql/repositories/jobsrepo"
	"github.com/assurrussa/outbox/infrastructure/pgsql/storage/transaction"
	pgsqltests "github.com/assurrussa/outbox/infrastructure/pgsql/tests"
	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/shared/tests"
)

var (
	workers    = 10
	idleTime   = 250 * time.Millisecond
	reserveFor = time.Second
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

type TestSuite struct {
	suite.Suite

	db       pgsql.Client
	dbHelper *pgsqltests.DBHelper
	cleanUp  func(context.Context)

	ctrl *gomock.Controller

	outboxSvc *outbox.Service

	jobsRepo       *jobsrepo.Repo
	jobsFailedRepo *jobsfailedrepo.Repo
}

func NewTestRepoSuite(t *testing.T, opts ...pgsqltests.OptionDatabase) (context.Context, context.CancelFunc, *TestSuite) {
	return tests.NewSuite[*TestSuite](t, func(t *testing.T, ctx context.Context) *TestSuite {
		db, dbHelper, cleanUp := pgsqltests.PrepareDB(ctx, t, "TestJobsSuite", opts...)
		trx := transaction.New(db.DB())
		jobsRepo := jobsrepo.Must(jobsrepo.NewOptions(db))
		jobsFailedRepo := jobsfailedrepo.Must(jobsfailedrepo.NewOptions(db))
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

		return &TestSuite{
			db:             db,
			dbHelper:       dbHelper,
			cleanUp:        cleanUp,
			outboxSvc:      outboxSvc,
			jobsRepo:       jobsRepo,
			jobsFailedRepo: jobsFailedRepo,
		}
	})
}

func TestMustRegisterJob(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	job := newJobMock("duplicated_job", nop, time.Second, 1)

	ts.NotPanics(func() {
		ts.outboxSvc.MustRegisterJob(job)
	})

	ts.Panics(func() {
		ts.outboxSvc.MustRegisterJob(job)
	})
}

func TestPutJob(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)
	// Arrange.
	const jobName = "TestPutJob"
	const jobPayload = "{}"
	availableAt := time.Now().Local()

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
	ts.NotEmpty(j.ReservedAt)
	ts.NotEmpty(j.CreatedAt)
}

func TestAllJobsProcessed(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)
	// Arrange.
	const jobName = "TestAllJobsProcessed"

	job := newJobMock(jobName, nop, time.Second, 1)
	ts.outboxSvc.MustRegisterJob(job)

	const jobsCount = 30
	for i := 0; i < jobsCount; i++ {
		_, err := ts.outboxSvc.Put(ctx, jobName, `{messageId:"4242"}`, time.Now().Local())
		ts.Require().NoError(err)
	}

	// Action.
	runOutboxFor(ctx, ts, time.Second)

	// Assert.
	ts.Equal(jobsCount, job.ExecutedTimes())

	count, err := ts.jobsRepo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Require().Equal(int64(0), count)
	count, err = ts.jobsFailedRepo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Require().Equal(int64(0), count)
}

func TestDLQ_UnknownJob(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)
	// Arrange.
	const jobName = "unknown-job"
	const jobPayload = "{}"
	_, err := ts.outboxSvc.Put(ctx, jobName, jobPayload, time.Now().Local())
	ts.Require().NoError(err)

	// Action.
	runOutboxFor(ctx, ts, time.Second)

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

func TestDLQ_AfterMaxAttemptsExceeding(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)
	// Arrange.
	const jobName = "TestDLQ_AfterMaxAttemptsExceeding"
	const jobPayload = "{}"
	const maxAttempts = 3
	availableAt := time.Now().Local()

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
	runOutboxFor(ctx, ts, maxAttempts*time.Second)

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

func TestIfNoJobsThenWorkersSleepForIdleTime(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)
	// Arrange.
	const jobName = "TestIfNoJobsThenWorkersSleepForIdleTime"

	job := newJobMock(jobName, nop, time.Second, 1)
	ts.outboxSvc.MustRegisterJob(job)

	// Action.
	cancel, errCh := runOutbox(ctx, ts)
	defer cancel()

	// Assert.
	time.Sleep(idleTime / 25)

	const jobsCount = 3
	for i := 0; i < jobsCount; i++ {
		_, err := ts.outboxSvc.Put(ctx, jobName, fmt.Sprintf(`{messageId:"%d"}`, i), time.Now().Local())
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

func runOutboxFor(ctx context.Context, ts *TestSuite, timeout time.Duration) {
	ts.T().Helper()

	cancel, errCh := runOutbox(ctx, ts)
	defer cancel()

	time.Sleep(timeout)
	cancel()
	ts.NoError(<-errCh) // No error expected because of graceful shutdown via cancel ctx.
}

func runOutbox(ctx context.Context, ts *TestSuite) (context.CancelFunc, <-chan error) {
	ts.T().Helper()

	ctx, cancel := context.WithCancel(ctx)

	errCh := make(chan error)
	go func() { errCh <- ts.outboxSvc.Run(ctx) }()

	return cancel, errCh
}

var nop = func(ctx context.Context, s string) error {
	time.Sleep(10 * time.Millisecond) // Prevent PSQL DDoS.
	return nil
}

type jobMock struct {
	name          string
	handler       func(ctx context.Context, s string) error
	timeout       time.Duration
	maxAttempts   int
	executedTimes int32
}

func newJobMock(
	name string,
	h func(ctx context.Context, s string) error,
	executionTimeout time.Duration,
	maxAttempts int,
) *jobMock {
	return &jobMock{
		name:          name,
		handler:       h,
		timeout:       executionTimeout,
		maxAttempts:   maxAttempts,
		executedTimes: 0,
	}
}

func (j *jobMock) Name() string {
	return j.name
}

func (j *jobMock) Handle(ctx context.Context, payload string) error {
	atomic.AddInt32(&j.executedTimes, 1)
	return j.handler(ctx, payload)
}

func (j *jobMock) ExecutionTimeout() time.Duration {
	return j.timeout
}

func (j *jobMock) MaxAttempts() int {
	return j.maxAttempts
}

// ExecutedTimes returns global (for all different jobsrepo of this type
// processed at different times) execution counter.
func (j *jobMock) ExecutedTimes() int {
	return int(atomic.LoadInt32(&j.executedTimes))
}
