//go:build integration

package outbox_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/assurrussa/outbox/backends/sqlite"
	"github.com/assurrussa/outbox/backends/sqlite/repositories/jobsfailedrepo"
	"github.com/assurrussa/outbox/backends/sqlite/repositories/jobsrepo"
	"github.com/assurrussa/outbox/backends/sqlite/storage/transaction"
	sqlitetests "github.com/assurrussa/outbox/backends/sqlite/tests"
	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/shared/tests"
)

type TestSQLiteSuite struct {
	suite.Suite

	db       sqlite.Client
	dbHelper *sqlitetests.DBHelper
	cleanUp  func(context.Context)

	outboxSvc *outbox.Service

	jobsRepo       *jobsrepo.Repo
	jobsFailedRepo *jobsfailedrepo.Repo
}

func NewTestSQLiteSuite(
	t *testing.T,
	opts ...sqlitetests.OptionDatabase,
) (context.Context, context.CancelFunc, *TestSQLiteSuite) {
	t.Helper()

	return tests.NewSuite[*TestSQLiteSuite](
		t,
		func(t *testing.T, ctx context.Context) *TestSQLiteSuite {
			t.Helper()

			db, dbHelper, cleanUp := sqlitetests.PrepareDB(ctx, t, "TestSQLiteSuite", opts...)
			trx := transaction.New(db.DB())
			jobsRepo := jobsrepo.Must(db)
			jobsFailedRepo := jobsfailedrepo.Must(db)
			log := logger.Discard()

			outboxSvc, err := outbox.New(
				outbox.WithWorkers(1),
				outbox.WithIdleTime(100*time.Millisecond),
				outbox.WithReserveFor(time.Second),
				outbox.WithJobsRepo(jobsRepo),
				outbox.WithJobsStatRepo(jobsRepo),
				outbox.WithJobsFailedRepo(jobsFailedRepo),
				outbox.WithTransactor(trx),
				outbox.WithLogger(log),
			)
			require.NoError(t, err)

			return &TestSQLiteSuite{
				db:             db,
				dbHelper:       dbHelper,
				cleanUp:        cleanUp,
				outboxSvc:      outboxSvc,
				jobsRepo:       jobsRepo,
				jobsFailedRepo: jobsFailedRepo,
			}
		},
		tests.WithIsParallel(false),
	)
}

func TestSQLitePutJob(t *testing.T) {
	ctx, _, ts := NewTestSQLiteSuite(t)
	defer ts.cleanUp(ctx)

	const jobName = "TestSQLitePutJob"
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

func TestSQLiteAllJobsProcessed(t *testing.T) {
	ctx, _, ts := NewTestSQLiteSuite(t)
	defer ts.cleanUp(ctx)

	const jobName = "TestSQLiteAllJobsProcessed"
	job := newSQLiteJobMock(jobName, nopSQLite, time.Second, 1)
	ts.outboxSvc.MustRegisterJob(job)

	const jobsCount = 30
	for i := 0; i < jobsCount; i++ {
		_, err := ts.outboxSvc.Put(ctx, jobName, `{messageId:"4242"}`, time.Now().UTC())
		ts.Require().NoError(err)
	}

	runSQLiteOutboxFor(ctx, ts, time.Second)

	ts.Equal(jobsCount, job.ExecutedTimes())

	count, err := activeJobsCount(ctx, ts.jobsRepo)
	ts.Require().NoError(err)
	ts.Equal(int64(0), count)
	count, err = ts.jobsFailedRepo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Equal(int64(0), count)
}

func TestSQLiteRealBatchUsesOneHandlerInvocationAndOneOutcomeTransaction(t *testing.T) {
	ctx, _, ts := NewTestSQLiteSuite(t)
	defer ts.cleanUp(ctx)

	const jobName = "TestSQLiteRealBatchUsesOneHandlerInvocationAndOneOutcomeTransaction"
	handler := &sqliteBatchJobMock{name: jobName, handled: make(chan []outbox.BatchJobItem, 1)}
	ts.outboxSvc.MustRegisterBatchJob(handler, outbox.BatchConfig{
		MaxMessages: 4,
		MaxBytes:    1024,
		// The collector claims one candidate per SQLite transaction. Keep the
		// fill window well above scheduler and race-detector jitter because this
		// test verifies count-based flushing, not the MaxWait boundary.
		MaxWait: 5 * time.Second,
	})
	for index := 0; index < 4; index++ {
		_, err := ts.outboxSvc.Put(ctx, jobName, `{}`, time.Now().UTC())
		ts.Require().NoError(err)
	}

	cancel, errCh := runSQLiteOutbox(ctx, ts)
	select {
	case items := <-handler.handled:
		ts.Len(items, 4)
	case <-time.After(time.Second):
		t.Fatal("batch handler was not called")
	}
	require.Eventually(t, func() bool {
		count, err := activeJobsCount(ctx, ts.jobsRepo)
		return err == nil && count == 0
	}, time.Second, 10*time.Millisecond)
	cancel()
	ts.NoError(<-errCh)
	ts.Equal(int32(1), handler.calls.Load())
}

func TestSQLiteRealBatchDLQUsesConfiguredFailedTable(t *testing.T) {
	ctx, _, ts := NewTestSQLiteSuite(t)
	defer ts.cleanUp(ctx)

	const failedTable = "batch_jobs_failed_custom"
	_, err := ts.db.DB().ExecContext(
		ctx,
		"CREATE TABLE batch_jobs_failed_custom AS SELECT * FROM jobs_failed WHERE 0",
	)
	require.NoError(t, err)
	customFailedRepo := jobsfailedrepo.Must(ts.db, failedTable)
	service, err := outbox.New(
		outbox.WithWorkers(1),
		outbox.WithIdleTime(100*time.Millisecond),
		outbox.WithReserveFor(time.Second),
		outbox.WithJobsRepo(ts.jobsRepo),
		outbox.WithJobsFailedRepo(customFailedRepo),
		outbox.WithTransactor(transaction.New(ts.db.DB())),
		outbox.WithLogger(logger.Discard()),
	)
	require.NoError(t, err)

	handler := &sqliteBatchJobMock{
		name:    "TestSQLiteRealBatchDLQUsesConfiguredFailedTable",
		handled: make(chan []outbox.BatchJobItem, 1),
		itemErr: outbox.Permanent(errors.New("invalid payload")),
	}
	service.MustRegisterBatchJob(handler, outbox.BatchConfig{MaxMessages: 1})
	_, err = service.Put(ctx, handler.name, `{"invalid":true}`, time.Now().UTC())
	require.NoError(t, err)

	runCtx, cancelRun := context.WithCancel(ctx)
	errCh := make(chan error, 1)
	go func() { errCh <- service.Run(runCtx) }()
	require.Eventually(t, func() bool {
		count, countErr := customFailedRepo.CountExact(ctx)
		return countErr == nil && count == 1
	}, time.Second, 10*time.Millisecond)
	cancelRun()
	require.NoError(t, <-errCh)

	defaultCount, err := ts.jobsFailedRepo.CountExact(ctx)
	require.NoError(t, err)
	require.Zero(t, defaultCount)
	activeCount, err := activeJobsCount(ctx, ts.jobsRepo)
	require.NoError(t, err)
	require.Zero(t, activeCount)
}

func TestSQLiteUnsupportedNameRemainsPending(t *testing.T) {
	ctx, _, ts := NewTestSQLiteSuite(t)
	defer ts.cleanUp(ctx)

	const jobName = "unknown-job"
	jobID, err := ts.outboxSvc.Put(ctx, jobName, "{}", time.Now().UTC())
	ts.Require().NoError(err)

	runSQLiteOutboxFor(ctx, ts, time.Second)

	count, err := activeJobsCount(ctx, ts.jobsRepo)
	ts.Require().NoError(err)
	ts.Equal(int64(1), count)
	count, err = ts.jobsFailedRepo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Equal(int64(0), count)
	job, err := ts.jobsRepo.GetByID(ctx, jobID)
	ts.Require().NoError(err)
	ts.Zero(job.Attempts)
}

func TestSQLiteDLQAfterMaxAttemptsExceeding(t *testing.T) {
	ctx, _, ts := NewTestSQLiteSuite(t)
	defer ts.cleanUp(ctx)

	const jobName = "TestSQLiteDLQAfterMaxAttemptsExceeding"
	const maxAttempts = 3

	var executedTimes int
	job := newSQLiteJobMock(jobName, func(ctx context.Context, _ string) error {
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

	_, err := ts.outboxSvc.Put(ctx, jobName, `{}`, time.Now().UTC())
	ts.Require().NoError(err)

	runSQLiteOutboxFor(ctx, ts, maxAttempts*time.Second)

	count, err := activeJobsCount(ctx, ts.jobsRepo)
	ts.Require().NoError(err)
	ts.Equal(int64(0), count)
	count, err = ts.jobsFailedRepo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Equal(int64(1), count)
	ts.Equal(maxAttempts, job.ExecutedTimes())
}

func TestSQLiteQueueStats(t *testing.T) {
	ctx, _, ts := NewTestSQLiteSuite(t)
	defer ts.cleanUp(ctx)

	const jobName = "TestSQLiteQueueStats"
	job := newSQLiteJobMock(jobName, nopSQLite, time.Second, 1)
	ts.outboxSvc.MustRegisterJob(job)

	_, err := ts.outboxSvc.Put(ctx, jobName, `{}`, time.Now().UTC())
	ts.Require().NoError(err)

	stats, err := ts.outboxSvc.GetQueueStats(ctx)
	ts.Require().NoError(err)
	ts.Equal(int64(1), stats.Total)
	ts.Equal(int64(1), stats.Available)
}

func runSQLiteOutboxFor(ctx context.Context, ts *TestSQLiteSuite, timeout time.Duration) {
	ts.T().Helper()

	cancel, errCh := runSQLiteOutbox(ctx, ts)
	defer cancel()

	time.Sleep(timeout)
	cancel()
	ts.NoError(<-errCh)
}

func runSQLiteOutbox(ctx context.Context, ts *TestSQLiteSuite) (context.CancelFunc, <-chan error) {
	ts.T().Helper()

	ctx, cancel := context.WithCancel(ctx)

	errCh := make(chan error)
	go func() { errCh <- ts.outboxSvc.Run(ctx) }()

	return cancel, errCh
}

var nopSQLite = func(_ context.Context, _ string) error {
	time.Sleep(10 * time.Millisecond)
	return nil
}

type sqliteJobMock struct {
	name          string
	handler       func(ctx context.Context, s string) error
	timeout       time.Duration
	maxAttempts   int
	executedTimes int32
}

type sqliteBatchJobMock struct {
	name    string
	handled chan []outbox.BatchJobItem
	itemErr error
	calls   atomic.Int32
}

func (j *sqliteBatchJobMock) Name() string { return j.name }

func (j *sqliteBatchJobMock) HandleBatch(
	_ context.Context,
	items []outbox.BatchJobItem,
) (outbox.BatchResult, error) {
	j.calls.Add(1)
	copyItems := append([]outbox.BatchJobItem(nil), items...)
	j.handled <- copyItems
	result := outbox.BatchResult{Items: make([]outbox.BatchItemResult, len(items))}
	for index, item := range items {
		result.Items[index] = outbox.BatchItemResult{JobID: item.JobID, Err: j.itemErr}
	}
	return result, nil
}

func (*sqliteBatchJobMock) ExecutionTimeout() time.Duration { return time.Second }

func (*sqliteBatchJobMock) MaxAttempts() int { return 3 }

func newSQLiteJobMock(
	name string,
	h func(ctx context.Context, s string) error,
	executionTimeout time.Duration,
	maxAttempts int,
) *sqliteJobMock {
	return &sqliteJobMock{
		name:          name,
		handler:       h,
		timeout:       executionTimeout,
		maxAttempts:   maxAttempts,
		executedTimes: 0,
	}
}

func (j *sqliteJobMock) Name() string {
	return j.name
}

func (j *sqliteJobMock) Handle(ctx context.Context, s string) error {
	atomic.AddInt32(&j.executedTimes, 1)
	return j.handler(ctx, s)
}

func (j *sqliteJobMock) ExecutionTimeout() time.Duration {
	return j.timeout
}

func (j *sqliteJobMock) MaxAttempts() int {
	return j.maxAttempts
}

func (j *sqliteJobMock) ExecutedTimes() int {
	return int(atomic.LoadInt32(&j.executedTimes))
}
