//go:build integration

package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
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
	"github.com/assurrussa/outbox/backends/pgsql/storage/pgsqlclient"
	"github.com/assurrussa/outbox/backends/pgsql/storage/pgsqlinit"
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
	ts.False(j.ReservedAt.Valid)
	ts.NotEmpty(j.CreatedAt)
}

func TestStandardRuntimePlansFanoutAndDrains(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)
	runtime, err := pgsqlruntime.Open(ctx, pgsqlruntime.Config{
		DSN: ts.db.DB().Pool().Config().ConnString(), Workers: 2,
		IdleTime: 100 * time.Millisecond, ReserveFor: time.Second, ReservationBatchSize: 2,
		Logger: logger.Discard(),
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, runtime.Close()) }()
	require.Equal(t, int32(5), runtime.Client().DB().Pool().Config().MinConns)
	require.Equal(t, int32(10), runtime.Client().DB().Pool().Config().MaxConns)
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

func TestSplitPoolsPreserveAtomicStagingAndRelayProgress(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	dsn := ts.db.DB().Pool().Config().ConnString()
	producer, err := pgsqlinit.Create(
		ctx,
		dsn,
		pgsqlclient.WithMinConnectionsCount(1),
		pgsqlclient.WithMaxConnectionsCount(1),
		pgsqlclient.WithLogger(logger.Discard()),
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, producer.Close()) }()

	relay, err := pgsqlruntime.Open(ctx, pgsqlruntime.Config{
		DSN:                 dsn,
		Workers:             1,
		IdleTime:            100 * time.Millisecond,
		ReserveFor:          time.Second,
		MinConnectionsCount: 1,
		MaxConnectionsCount: 1,
		Logger:              logger.Discard(),
	})
	require.NoError(t, err)
	relayClosed := false
	defer func() {
		if !relayClosed {
			require.NoError(t, relay.Close())
		}
	}()
	require.Equal(t, int32(1), relay.Client().DB().Pool().Config().MinConns)
	require.Equal(t, int32(1), relay.Client().DB().Pool().Config().MaxConns)

	_, err = producer.DB().Exec(ctx, "create_split_pool_orders", `
		CREATE TABLE split_pool_orders (
			id text PRIMARY KEY
		)
	`)
	require.NoError(t, err)

	producerTx := transaction.New(producer.DB())
	job := newJobMock("split_pool_delivery", nop, time.Second, 1)
	relay.Service().MustRegisterJob(job)

	rollbackErr := errors.New("rollback split-pool staging")
	err = producerTx.RunInTx(ctx, func(txCtx context.Context) error {
		_, execErr := producer.DB().Exec(txCtx, "insert_rolled_back_order", `
			INSERT INTO split_pool_orders (id) VALUES ($1)
		`, "rolled-back")
		if execErr != nil {
			return execErr
		}
		if _, putErr := relay.Service().Put(txCtx, job.Name(), `{}`, time.Now().UTC()); putErr != nil {
			return putErr
		}
		return rollbackErr
	})
	require.ErrorIs(t, err, rollbackErr)
	requireSplitPoolCounts(t, ctx, producer, relay, 0, 0)

	err = producerTx.RunInTx(ctx, func(txCtx context.Context) error {
		if _, execErr := producer.DB().Exec(txCtx, "insert_committed_order", `
			INSERT INTO split_pool_orders (id) VALUES ($1)
		`, "committed"); execErr != nil {
			return execErr
		}
		_, putErr := relay.Service().Put(txCtx, job.Name(), `{}`, time.Now().UTC())
		return putErr
	})
	require.NoError(t, err)
	requireSplitPoolCounts(t, ctx, producer, relay, 1, 1)

	producerAcquired := make(chan struct{})
	releaseProducer := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseProducer) })
	producerErr := make(chan error, 1)
	go func() {
		producerErr <- producerTx.RunInTx(ctx, func(context.Context) error {
			close(producerAcquired)
			select {
			case <-releaseProducer:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-producerAcquired:
	case <-time.After(time.Second):
		t.Fatal("producer pool was not acquired")
	}
	require.Equal(t, int32(1), producer.Pool().Stat().AcquiredConns())

	runErr := make(chan error, 1)
	go func() { runErr <- relay.Run(ctx) }()
	require.Eventually(t, func() bool { return job.ExecutedTimes() == 1 }, 2*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool {
		count, countErr := activeJobsCount(ctx, relay.Jobs())
		return countErr == nil && count == 0
	}, 2*time.Second, 20*time.Millisecond)
	require.Equal(t, int32(1), producer.Pool().Stat().AcquiredConns())

	relay.BeginDrain()
	require.NoError(t, <-runErr)
	require.NoError(t, relay.Close())
	relayClosed = true
	releaseOnce.Do(func() { close(releaseProducer) })
	require.NoError(t, <-producerErr)
	require.NoError(t, producer.DB().Ping(ctx), "closing the relay runtime must not close the host-owned producer pool")
}

func requireSplitPoolCounts(
	t *testing.T,
	ctx context.Context,
	producer pgsql.Client,
	relay *pgsqlruntime.Runtime,
	wantOrders int64,
	wantJobs int64,
) {
	t.Helper()

	var orders int64
	require.NoError(t, producer.DB().QueryRow(ctx, "count_split_pool_orders", `
		SELECT COUNT(*) FROM split_pool_orders
	`).Scan(&orders))
	require.Equal(t, wantOrders, orders)

	jobs, err := activeJobsCount(ctx, relay.Jobs())
	require.NoError(t, err)
	require.Equal(t, wantJobs, jobs)
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

	count, err := activeJobsCount(ctx, ts.jobsRepo)
	ts.Require().NoError(err)
	ts.Require().Equal(int64(0), count)
	count, err = ts.jobsFailedRepo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Require().Equal(int64(0), count)
}

func TestUnsupportedNameRemainsPending(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)
	// Arrange.
	const jobName = "unknown-job"
	const jobPayload = "{}"
	jobID, err := ts.outboxSvc.Put(ctx, jobName, jobPayload, time.Now().UTC())
	ts.Require().NoError(err)

	// Action.
	runOutboxFor(ctx, ts, time.Second)

	// Assert.
	count, err := activeJobsCount(ctx, ts.jobsRepo)
	ts.Require().NoError(err)
	ts.Require().Equal(int64(1), count)
	count, err = ts.jobsFailedRepo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Require().Equal(int64(0), count)

	job, err := ts.jobsRepo.GetByID(ctx, jobID)
	ts.Require().NoError(err)
	ts.Zero(job.Attempts)
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
	count, err := activeJobsCount(ctx, ts.jobsRepo)
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
	const (
		jobName      = "TestIfNoJobsThenWorkersSleepForIdleTime"
		testIdleTime = 2 * time.Second
	)

	observedRepo := &firstEmptyClaimRepo{
		Repo:       ts.jobsRepo,
		firstEmpty: make(chan struct{}),
	}
	outboxSvc, err := outbox.New(
		outbox.WithWorkers(1),
		outbox.WithIdleTime(testIdleTime),
		outbox.WithReserveFor(reserveFor),
		outbox.WithJobsRepo(observedRepo),
		outbox.WithJobsStatRepo(ts.jobsRepo),
		outbox.WithJobsFailedRepo(ts.jobsFailedRepo),
		outbox.WithTransactor(transaction.New(ts.db.DB())),
		outbox.WithLogger(logger.Discard()),
	)
	ts.Require().NoError(err)
	ts.outboxSvc = outboxSvc

	job := newJobMock(jobName, nop, time.Second, 1)
	ts.outboxSvc.MustRegisterJob(job)

	// Action.
	cancel, errCh := runOutbox(ctx, ts)
	defer cancel()

	// Assert.
	select {
	case <-observedRepo.firstEmpty:
	case <-time.After(5 * time.Second):
		ts.FailNow("worker did not complete its initial empty claim")
	}

	const jobsCount = 3
	for i := 0; i < jobsCount; i++ {
		_, err := ts.outboxSvc.Put(ctx, jobName, fmt.Sprintf(`{messageId:"%d"}`, i), time.Now().Local())
		ts.Require().NoError(err)
	}

	count, err := activeJobsCount(ctx, ts.jobsRepo)
	ts.Require().NoError(err)
	ts.Require().Equal(int64(jobsCount), count) // Workers fell asleep before the jobsrepo appearing.
	ts.Equal(0, job.ExecutedTimes())

	ts.Require().Eventually(func() bool {
		count, countErr := activeJobsCount(ctx, ts.jobsRepo)
		return countErr == nil && count == 0 && job.ExecutedTimes() == jobsCount
	}, 3*testIdleTime, 10*time.Millisecond) // Worker woke up and processed the jobsrepo.
	count, err = ts.jobsFailedRepo.CountExact(ctx)
	ts.Require().NoError(err)
	ts.Require().Equal(int64(0), count)
	ts.Equal(jobsCount, job.ExecutedTimes())

	cancel()
	ts.NoError(<-errCh)
}

type firstEmptyClaimRepo struct {
	*jobsrepo.Repo
	firstEmpty chan struct{}
	once       sync.Once
}

func (r *firstEmptyClaimRepo) FindAndReserveJobsForCapabilities(
	ctx context.Context,
	now time.Time,
	until time.Time,
	leaseToken outbox.LeaseToken,
	capabilities []outbox.JobCapability,
	limit int,
) ([]outboxmodels.Job, error) {
	jobs, err := r.Repo.FindAndReserveJobsForCapabilities(
		ctx,
		now,
		until,
		leaseToken,
		capabilities,
		limit,
	)
	if errors.Is(err, outbox.ErrNoJobs) {
		r.once.Do(func() { close(r.firstEmpty) })
	}

	return jobs, err
}

func TestQueueStats_ReservedJobIsNotAvailable(t *testing.T) {
	ctx, _, ts := NewTestRepoSuite(t)
	defer ts.cleanUp(ctx)

	const jobName = "TestQueueStats_ReservedJobIsNotAvailable"
	job := newJobMock(jobName, nop, time.Second, 1)
	ts.outboxSvc.MustRegisterJob(job)

	_, err := ts.outboxSvc.Put(ctx, jobName, `{}`, time.Now().Local())
	ts.Require().NoError(err)

	_, err = ts.jobsRepo.FindAndReserveJobsForCapabilities(
		ctx,
		time.Now().UTC(),
		time.Now().UTC().Add(time.Minute),
		types.NewLeaseToken(),
		[]outbox.JobCapability{{Name: jobName, SchemaVersion: outbox.DefaultSchemaVersion}},
		1,
	)
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
	err = runServiceFor(ctx, svc, 10*time.Second)
	ts.Require().ErrorIs(err, outbox.ErrLeaseLost)

	jobs, err := ts.jobsRepo.All(ctx)
	ts.Require().NoError(err)
	ts.Len(jobs, len(targets)+1)

	// A failed ACK retains the finalization guard. Retry after the persisted
	// lease expires instead of assuming the initial reservation duration.
	var retryAfter time.Time
	for _, job := range jobs {
		if job.Name == outbox.FanoutDispatcherJobName {
			ts.Require().True(job.ReservedAt.Valid)
			retryAfter = job.ReservedAt.Time
		}
	}
	ts.Require().False(retryAfter.IsZero())
	time.Sleep(time.Until(retryAfter) + 100*time.Millisecond)
	// Exercise a claim slower than the old 300ms wall-clock run budget.
	// Completion is the persisted dispatcher ACK, not elapsed runner time.
	retryRepo := &notifyingAckRepo{Repo: ts.jobsRepo, acked: make(chan struct{})}
	retrySvc := newFanoutIntegrationService(t, retryRepo, retryRepo, ts.jobsFailedRepo, txManager)
	ts.Require().NoError(runServiceUntilAck(ctx, retrySvc, retryRepo.acked))

	jobs, err = ts.jobsRepo.All(ctx)
	ts.Require().NoError(err)
	ts.Require().Len(jobs, len(targets))
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
	jobsRepo outbox.JobsRepository,
	fanoutRepo outbox.FanoutJobsRepository,
	failedRepo *jobsfailedrepo.Repo,
	txManager outbox.Transactor,
) *outbox.Service {
	t.Helper()

	svc, err := outbox.New(
		outbox.WithWorkers(1),
		outbox.WithIdleTime(100*time.Millisecond),
		outbox.WithReserveFor(reserveFor),
		outbox.WithJobsRepo(jobsRepo),
		outbox.WithFanoutJobsRepo(fanoutRepo),
		outbox.WithJobsFailedRepo(failedRepo),
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

// runServiceUntilAck treats the timeout only as a failure bound and joins
// Run before the caller inspects the database or tears it down.
func runServiceUntilAck(ctx context.Context, svc *outbox.Service, acked <-chan struct{}) error {
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- svc.Run(runCtx) }()

	select {
	case <-acked:
		cancel()
		return <-done
	case err := <-done:
		if err != nil {
			return err
		}
		if err := runCtx.Err(); err != nil {
			return err
		}
		return errors.New("service stopped before dispatcher ACK")
	}
}

type notifyingAckRepo struct {
	*jobsrepo.Repo
	acked chan struct{}
}

func (r *notifyingAckRepo) FindAndReserveJobsForCapabilities(
	ctx context.Context,
	now time.Time,
	until time.Time,
	leaseToken outbox.LeaseToken,
	capabilities []outbox.JobCapability,
	limit int,
) ([]outboxmodels.Job, error) {
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}

	return r.Repo.FindAndReserveJobsForCapabilities(ctx, now, until, leaseToken, capabilities, limit)
}

func (r *notifyingAckRepo) DeleteJobWithLease(
	ctx context.Context,
	jobID types.JobID,
	leaseToken outbox.LeaseToken,
	now time.Time,
) (int64, error) {
	affected, err := r.Repo.DeleteJobWithLease(ctx, jobID, leaseToken, now)
	if err == nil && affected == 1 {
		close(r.acked)
	}
	return affected, err
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
