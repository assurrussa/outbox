//go:build integration

package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.uber.org/goleak"

	"github.com/assurrussa/outbox/backends/pgsql"
	"github.com/assurrussa/outbox/backends/pgsql/repositories/jobsfailedrepo"
	"github.com/assurrussa/outbox/backends/pgsql/repositories/jobsrepo"
	pgsqlruntime "github.com/assurrussa/outbox/backends/pgsql/runtime"
	"github.com/assurrussa/outbox/backends/pgsql/storage/transaction"
	pgsqltests "github.com/assurrussa/outbox/backends/pgsql/tests"
	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
	outboxmodels "github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/tests"
	"github.com/assurrussa/outbox/shared/types"
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

	outboxSvc *outbox.Service

	jobsRepo       *jobsrepo.Repo
	jobsFailedRepo *jobsfailedrepo.Repo
}

func NewTestRepoSuite(t *testing.T, opts ...pgsqltests.OptionDatabase) (context.Context, context.CancelFunc, *TestSuite) {
	t.Helper()

	return tests.NewSuite[*TestSuite](t, func(t *testing.T, ctx context.Context) *TestSuite {
		t.Helper()

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

func TestStandardRuntimePlansFanoutAndDrains(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)
	runtime, err := pgsqlruntime.Open(ctx, pgsqlruntime.Config{
		DSN: ts.db.DB().Pool().Config().ConnString(), Workers: 2,
		IdleTime: 100 * time.Millisecond, ReserveFor: time.Second, Logger: logger.Discard(),
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, runtime.Close()) }()
	require.NoError(t, runtime.DatabaseReadiness(ctx))
	event := integrationFanoutEvent()
	targets := integrationFanoutTargets()
	_, err = runtime.Service().PutFanout(ctx, event, targets, time.Now().UTC())
	require.NoError(t, err)
	runErr := make(chan error, 1)
	go func() { runErr <- runtime.Run(ctx) }()
	require.Eventually(t, func() bool { return runtime.Readiness(ctx) == nil }, time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		jobs, queryErr := ts.jobsRepo.All(ctx)
		return queryErr == nil && len(jobs) == len(targets)
	}, 2*time.Second, 20*time.Millisecond)
	runtime.BeginDrain()
	require.NoError(t, <-runErr)
	jobs, err := ts.jobsRepo.All(ctx)
	require.NoError(t, err)
	require.Len(t, jobs, len(targets))
	assertUniqueIntegrationDeliveries(t, jobs)
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

func TestQueueStats_ReservedJobIsNotAvailable(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	const jobName = "TestQueueStats_ReservedJobIsNotAvailable"
	job := newJobMock(jobName, nop, time.Second, 1)
	ts.outboxSvc.MustRegisterJob(job)

	_, err := ts.outboxSvc.Put(ctx, jobName, `{}`, time.Now().Local())
	ts.Require().NoError(err)

	_, err = ts.jobsRepo.FindAndReserveJob(ctx, time.Now().Local(), time.Now().Local().Add(time.Minute))
	ts.Require().NoError(err)

	stats, err := ts.outboxSvc.GetQueueStats(ctx)
	ts.Require().NoError(err)
	ts.Equal(int64(1), stats.Total)
	ts.Equal(int64(0), stats.Available)
	ts.Equal(int64(1), stats.Processing)
}

func TestFanoutCrashAfterCommitBeforeAckDoesNotDuplicateDeliveries(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	txManager := transaction.New(ts.db.DB())
	losingRepo := &loseFirstAckRepo{Repo: ts.jobsRepo}
	svc := newFanoutIntegrationService(t, losingRepo, losingRepo, ts.jobsFailedRepo, txManager)
	event := integrationFanoutEvent()
	targets := integrationFanoutTargets()

	_, err := svc.PutFanout(ctx, event, targets, time.Now().UTC())
	ts.Require().NoError(err)
	err = runServiceFor(ctx, svc, 500*time.Millisecond)
	ts.Require().ErrorIs(err, outbox.ErrLeaseLost)

	jobs, err := ts.jobsRepo.All(ctx)
	ts.Require().NoError(err)
	ts.Len(jobs, len(targets)+1)

	time.Sleep(reserveFor + 100*time.Millisecond)
	retrySvc := newFanoutIntegrationService(t, ts.jobsRepo, ts.jobsRepo, ts.jobsFailedRepo, txManager)
	ts.Require().NoError(runServiceFor(ctx, retrySvc, 300*time.Millisecond))

	jobs, err = ts.jobsRepo.All(ctx)
	ts.Require().NoError(err)
	ts.Len(jobs, len(targets))
	assertUniqueIntegrationDeliveries(t, jobs)
}

func TestFanoutPartialPlanningRollsBackCompleteDeliverySet(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	txManager := transaction.New(ts.db.DB())
	failingRepo := &failSecondDeliveryRepo{Repo: ts.jobsRepo}
	svc := newFanoutIntegrationService(t, ts.jobsRepo, failingRepo, ts.jobsFailedRepo, txManager)
	event := integrationFanoutEvent()
	targets := integrationFanoutTargets()

	_, err := svc.PutFanout(ctx, event, targets, time.Now().UTC())
	ts.Require().NoError(err)
	ts.Require().NoError(runServiceFor(ctx, svc, 300*time.Millisecond))

	jobs, err := ts.jobsRepo.All(ctx)
	ts.Require().NoError(err)
	ts.Len(jobs, 1)
	ts.Equal(outbox.FanoutDispatcherJobName, jobs[0].Name)

	time.Sleep(reserveFor + 100*time.Millisecond)
	retrySvc := newFanoutIntegrationService(t, ts.jobsRepo, ts.jobsRepo, ts.jobsFailedRepo, txManager)
	ts.Require().NoError(runServiceFor(ctx, retrySvc, 300*time.Millisecond))

	jobs, err = ts.jobsRepo.All(ctx)
	ts.Require().NoError(err)
	ts.Len(jobs, len(targets))
	assertUniqueIntegrationDeliveries(t, jobs)
}

func newFanoutIntegrationService(
	t *testing.T,
	capabilityRepo outbox.CapabilityJobsRepository,
	fanoutRepo outbox.FanoutJobsRepository,
	failedRepo *jobsfailedrepo.Repo,
	txManager outbox.Transactor,
) *outbox.Service {
	t.Helper()

	legacyRepo, ok := capabilityRepo.(outbox.JobsRepository)
	require.True(t, ok)

	svc, err := outbox.New(
		outbox.WithWorkers(1),
		outbox.WithIdleTime(100*time.Millisecond),
		outbox.WithReserveFor(reserveFor),
		outbox.WithJobsRepo(legacyRepo),
		outbox.WithCapabilityJobsRepo(capabilityRepo),
		outbox.WithFanoutJobsRepo(fanoutRepo),
		outbox.WithJobsFailedRepo(failedRepo),
		outbox.WithCapabilityJobsFailedRepo(failedRepo),
		outbox.WithTransactor(txManager),
		outbox.WithLogger(logger.Discard()),
	)
	require.NoError(t, err)

	return svc
}

func integrationFanoutEvent() outbox.FanoutEvent {
	return outbox.FanoutEvent{
		ID:            types.NewMessageID(),
		Topic:         "cms.entry.published",
		SchemaVersion: 2,
		Payload:       json.RawMessage(`{"entryId":"entry-1"}`),
		OccurredAt:    time.Now().UTC(),
	}
}

func integrationFanoutTargets() []outbox.FanoutTarget {
	return []outbox.FanoutTarget{
		{Kind: "nitro", ID: "site", Snapshot: json.RawMessage(`{"namespace":"public"}`)},
		{Kind: "webhook", ID: "subscription-a", Snapshot: json.RawMessage(`{"revision":1}`)},
		{Kind: "webhook", ID: "subscription-b", Snapshot: json.RawMessage(`{"revision":2}`)},
	}
}

func assertUniqueIntegrationDeliveries(t *testing.T, jobs []outboxmodels.Job) {
	t.Helper()

	ids := make(map[types.MessageID]struct{}, len(jobs))
	for _, job := range jobs {
		delivery, err := outbox.DecodeFanoutDelivery(job.Payload)
		require.NoError(t, err)
		require.Zero(t, job.Attempts)
		ids[delivery.ID] = struct{}{}
	}
	require.Len(t, ids, len(jobs))
}

func runServiceFor(ctx context.Context, svc *outbox.Service, duration time.Duration) error {
	runCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()

	return svc.Run(runCtx)
}

type loseFirstAckRepo struct {
	*jobsrepo.Repo
	lost atomic.Bool
}

func (r *loseFirstAckRepo) DeleteJobWithLease(
	ctx context.Context,
	jobID types.JobID,
	leaseToken outbox.LeaseToken,
	now time.Time,
) (int64, error) {
	if r.lost.CompareAndSwap(false, true) {
		return 0, nil
	}

	return r.Repo.DeleteJobWithLease(ctx, jobID, leaseToken, now)
}

type failSecondDeliveryRepo struct {
	*jobsrepo.Repo
	deliveries atomic.Int32
}

func (r *failSecondDeliveryRepo) CreateJobVersionedUnique(
	ctx context.Context,
	deduplicationKey string,
	name string,
	schemaVersion outbox.SchemaVersion,
	payload string,
	availableAt time.Time,
) (types.JobID, error) {
	if strings.HasPrefix(name, "fanout.") && r.deliveries.Add(1) == 2 {
		return types.JobIDNil, errors.New("injected partial planning failure")
	}

	return r.Repo.CreateJobVersionedUnique(
		ctx,
		deduplicationKey,
		name,
		schemaVersion,
		payload,
		availableAt,
	)
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

var nop = func(_ context.Context, _ string) error {
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
