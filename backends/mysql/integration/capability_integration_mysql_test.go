//go:build integration

package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/mysql/repositories/jobsfailedrepo"
	"github.com/assurrussa/outbox/backends/mysql/repositories/jobsrepo"
	"github.com/assurrussa/outbox/backends/mysql/storage/transaction"
	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/sharederrors"
	"github.com/assurrussa/outbox/shared/types"
)

func TestMySQLCapabilityClaimLeavesUnsupportedSchemaPending(t *testing.T) {
	ctx, _, ts := NewTestMySQLSuite(t)
	defer ts.cleanUp(ctx)

	jobID, err := ts.jobsRepo.CreateJobVersioned(ctx, "versioned", 2, `{}`, time.Now().UTC())
	require.NoError(t, err)
	_, err = ts.jobsRepo.FindAndReserveJobsForCapabilities(
		ctx, time.Now().UTC(), time.Now().UTC().Add(time.Second), types.NewLeaseToken(),
		[]outbox.JobCapability{{Name: "versioned", SchemaVersion: 1}},
		1,
	)
	require.ErrorIs(t, err, sharederrors.ErrNoJobs)
	job, err := ts.jobsRepo.GetByID(ctx, jobID)
	require.NoError(t, err)
	require.Zero(t, job.Attempts)
	require.Equal(t, outbox.SchemaVersion(2), job.SchemaVersion)
}

func TestMySQLCapabilityLeaseRejectsStaleOwner(t *testing.T) {
	ctx, _, ts := NewTestMySQLSuite(t)
	defer ts.cleanUp(ctx)

	_, err := ts.jobsRepo.CreateJobVersioned(ctx, "versioned", 2, `{}`, time.Now().UTC())
	require.NoError(t, err)
	now := time.Now().UTC()
	token := types.NewLeaseToken()
	job, err := claimMySQLOne(
		ctx, now, now.Add(time.Second), token,
		ts.jobsRepo,
		[]outbox.JobCapability{{Name: "versioned", SchemaVersion: 2}},
	)
	require.NoError(t, err)
	require.Equal(t, token, job.LeaseToken)

	affected, err := ts.jobsRepo.ExtendJobLeases(
		ctx, []types.JobID{job.ID}, types.NewLeaseToken(), now, now.Add(2*time.Second),
	)
	require.NoError(t, err)
	require.Zero(t, affected)
	affected, err = ts.jobsRepo.ExtendJobLeases(
		ctx, []types.JobID{job.ID}, token, now, now.Add(2*time.Second),
	)
	require.NoError(t, err)
	require.Equal(t, int64(1), affected)
	affected, err = ts.jobsRepo.DeleteJobWithLease(ctx, job.ID, types.NewLeaseToken(), now)
	require.NoError(t, err)
	require.Zero(t, affected)
	affected, err = ts.jobsRepo.DeleteJobWithLease(ctx, job.ID, token, now)
	require.NoError(t, err)
	require.Equal(t, int64(1), affected)
}

func TestMySQLCapabilityRescheduleIsFencedAndReleasesLease(t *testing.T) {
	ctx, _, ts := NewTestMySQLSuite(t)
	defer ts.cleanUp(ctx)

	now := time.Now().UTC().Truncate(time.Microsecond)
	jobID, err := ts.jobsRepo.CreateJobVersioned(ctx, "versioned", 2, `{}`, now)
	require.NoError(t, err)
	token := types.NewLeaseToken()
	job, err := claimMySQLOne(
		ctx, now, now.Add(time.Minute), token,
		ts.jobsRepo,
		[]outbox.JobCapability{{Name: "versioned", SchemaVersion: 2}},
	)
	require.NoError(t, err)
	retryAt := now.Add(time.Hour)

	affected, err := ts.jobsRepo.RescheduleJobWithLease(ctx, job.ID, types.NewLeaseToken(), now, retryAt)
	require.NoError(t, err)
	require.Zero(t, affected)
	affected, err = ts.jobsRepo.RescheduleJobWithLease(ctx, job.ID, token, now, retryAt)
	require.NoError(t, err)
	require.Equal(t, int64(1), affected)

	stored, err := ts.jobsRepo.GetByID(ctx, jobID)
	require.NoError(t, err)
	require.WithinDuration(t, retryAt, stored.AvailableAt, time.Microsecond)
	require.False(t, stored.ReservedAt.Valid)
	require.True(t, stored.LeaseToken.IsZero())
}

func TestMySQLUniqueResultDistinguishesCreateFromReplay(t *testing.T) {
	ctx, _, ts := NewTestMySQLSuite(t)
	defer ts.cleanUp(ctx)

	availableAt := time.Now().UTC().Truncate(time.Microsecond)
	first, err := ts.jobsRepo.CreateJobVersionedUniqueResult(
		ctx, "message-1", "publish", 1, `{}`, availableAt,
	)
	require.NoError(t, err)
	require.True(t, first.Created)
	replayed, err := ts.jobsRepo.CreateJobVersionedUniqueResult(
		ctx, "message-1", "publish", 1, `{}`, availableAt,
	)
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, first.JobID, replayed.JobID)
}

func TestMySQLFailedJobPreservesSchemaVersion(t *testing.T) {
	ctx, _, ts := NewTestMySQLSuite(t)
	defer ts.cleanUp(ctx)

	failedID, err := ts.jobsFailedRepo.CreateFailedJobVersioned(
		ctx, types.NewJobID(), "versioned", 3, `{}`, "test failure",
	)
	require.NoError(t, err)
	failed, err := ts.jobsFailedRepo.GetByID(ctx, failedID)
	require.NoError(t, err)
	require.Equal(t, outbox.SchemaVersion(3), failed.SchemaVersion)
}

func TestMySQLIdempotencyTombstoneSurvivesAckUntilPruned(t *testing.T) {
	ctx, _, ts := NewTestMySQLSuite(t)
	defer ts.cleanUp(ctx)

	availableAt := time.Now().UTC()
	jobID, err := ts.jobsRepo.CreateJobVersionedUnique(
		ctx, "delivery:event:target", "fanout.webhook.topic", 2, `{}`, availableAt,
	)
	require.NoError(t, err)
	leaseToken := types.NewLeaseToken()
	job, err := claimMySQLOne(
		ctx,
		time.Now().UTC(),
		time.Now().UTC().Add(time.Minute),
		leaseToken,
		ts.jobsRepo,
		[]outbox.JobCapability{{Name: "fanout.webhook.topic", SchemaVersion: 2}},
	)
	require.NoError(t, err)
	deleted, err := ts.jobsRepo.DeleteJobWithLease(ctx, job.ID, leaseToken, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)

	replayedID, err := ts.jobsRepo.CreateJobVersionedUnique(
		ctx, "delivery:event:target", "fanout.webhook.topic", 2, `{}`, availableAt,
	)
	require.NoError(t, err)
	require.Equal(t, jobID, replayedID)
	_, err = ts.jobsRepo.GetByID(ctx, replayedID)
	require.ErrorIs(t, err, sharederrors.ErrNoJobs)
	_, err = ts.jobsRepo.CreateJobVersionedUnique(
		ctx, "delivery:event:target", "fanout.webhook.topic", 2, `{"changed":true}`, availableAt,
	)
	require.ErrorIs(t, err, outbox.ErrIdempotencyConflict)

	pruned, err := ts.jobsRepo.PruneJobIdempotencyKeys(ctx, time.Now().UTC().Add(time.Minute), 10)
	require.NoError(t, err)
	require.Equal(t, int64(1), pruned)
	newJobID, err := ts.jobsRepo.CreateJobVersionedUnique(
		ctx, "delivery:event:target", "fanout.webhook.topic", 2, `{}`, availableAt,
	)
	require.NoError(t, err)
	require.NotEqual(t, jobID, newJobID)
}

func TestMySQLFanoutPartialPlanningRollsBackCompleteSet(t *testing.T) {
	ctx, _, ts := NewTestMySQLSuite(t)
	defer ts.cleanUp(ctx)

	txManager := transaction.New(ts.db.DB())
	failingRepo := &mysqlFailSecondDeliveryRepo{Repo: ts.jobsRepo}
	svc := newMySQLFanoutService(t, ts.jobsRepo, failingRepo, ts.jobsFailedRepo, txManager)
	event := mysqlFanoutEvent()
	targets := mysqlFanoutTargets()
	_, err := svc.PutFanout(ctx, event, targets, time.Now().UTC())
	require.NoError(t, err)
	require.NoError(t, runMySQLServiceFor(ctx, svc, 300*time.Millisecond))

	jobs, err := ts.jobsRepo.All(ctx)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, outbox.FanoutDispatcherJobName, jobs[0].Name)

	time.Sleep(time.Second + 100*time.Millisecond)
	retry := newMySQLFanoutService(t, ts.jobsRepo, ts.jobsRepo, ts.jobsFailedRepo, txManager)
	require.NoError(t, runMySQLServiceFor(ctx, retry, 300*time.Millisecond))
	jobs, err = ts.jobsRepo.All(ctx)
	require.NoError(t, err)
	require.Len(t, jobs, len(targets))
	assertMySQLUniqueDeliveries(t, jobs)
}

func TestMySQLFanoutLostAckDoesNotDuplicateDeliveries(t *testing.T) {
	ctx, _, ts := NewTestMySQLSuite(t)
	defer ts.cleanUp(ctx)

	txManager := transaction.New(ts.db.DB())
	losingRepo := &mysqlLoseFirstAckRepo{Repo: ts.jobsRepo}
	svc := newMySQLFanoutService(t, losingRepo, losingRepo, ts.jobsFailedRepo, txManager)
	event := mysqlFanoutEvent()
	targets := mysqlFanoutTargets()
	_, err := svc.PutFanout(ctx, event, targets, time.Now().UTC())
	require.NoError(t, err)
	require.ErrorIs(t, runMySQLServiceFor(ctx, svc, 500*time.Millisecond), outbox.ErrLeaseLost)

	jobs, err := ts.jobsRepo.All(ctx)
	require.NoError(t, err)
	require.Len(t, jobs, len(targets)+1)

	// A failed ACK retains the finalization guard. Retry after the persisted
	// lease expires instead of assuming the initial one-second reservation.
	var retryAfter time.Time
	for _, job := range jobs {
		if job.Name == outbox.FanoutDispatcherJobName {
			require.True(t, job.ReservedAt.Valid)
			retryAfter = job.ReservedAt.Time
		}
	}
	require.False(t, retryAfter.IsZero())
	time.Sleep(time.Until(retryAfter) + 100*time.Millisecond)
	retry := newMySQLFanoutService(t, ts.jobsRepo, ts.jobsRepo, ts.jobsFailedRepo, txManager)
	require.NoError(t, runMySQLServiceFor(ctx, retry, 300*time.Millisecond))
	jobs, err = ts.jobsRepo.All(ctx)
	require.NoError(t, err)
	require.Len(t, jobs, len(targets))
	assertMySQLUniqueDeliveries(t, jobs)
}

func newMySQLFanoutService(
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
		outbox.WithReserveFor(time.Second),
		outbox.WithJobsRepo(jobsRepo),
		outbox.WithFanoutJobsRepo(fanoutRepo),
		outbox.WithJobsFailedRepo(failedRepo),
		outbox.WithTransactor(txManager),
		outbox.WithLogger(logger.Discard()),
	)
	require.NoError(t, err)
	return svc
}

func mysqlFanoutEvent() outbox.FanoutEvent {
	return outbox.FanoutEvent{
		ID: types.NewMessageID(), Topic: "cms.entry.published", SchemaVersion: 2,
		Payload: json.RawMessage(`{"entryId":"entry-1"}`), OccurredAt: time.Now().UTC(),
	}
}

func mysqlFanoutTargets() []outbox.FanoutTarget {
	return []outbox.FanoutTarget{
		{Kind: "nitro", ID: "site", Snapshot: json.RawMessage(`{"namespace":"public"}`)},
		{Kind: "webhook", ID: "a", Snapshot: json.RawMessage(`{"revision":1}`)},
		{Kind: "webhook", ID: "b", Snapshot: json.RawMessage(`{"revision":2}`)},
	}
}

func assertMySQLUniqueDeliveries(t *testing.T, jobs []models.Job) {
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

func runMySQLServiceFor(ctx context.Context, svc *outbox.Service, duration time.Duration) error {
	runCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	return svc.Run(runCtx)
}

func claimMySQLOne(
	ctx context.Context,
	now time.Time,
	until time.Time,
	leaseToken outbox.LeaseToken,
	repo outbox.JobsRepository,
	capabilities []outbox.JobCapability,
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

type mysqlLoseFirstAckRepo struct {
	*jobsrepo.Repo
	lost atomic.Bool
}

func (r *mysqlLoseFirstAckRepo) DeleteJobWithLease(
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

type mysqlFailSecondDeliveryRepo struct {
	*jobsrepo.Repo
	deliveries atomic.Int32
}

func (r *mysqlFailSecondDeliveryRepo) CreateJobVersionedUnique(
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
		ctx, deduplicationKey, name, schemaVersion, payload, availableAt,
	)
}

func TestMySQLCustomTablePruneJobIdempotencyKeys(t *testing.T) {
	ctx, _, ts := NewTestMySQLSuite(t)
	defer ts.cleanUp(ctx)

	_, err := ts.db.DB().ExecContext(ctx, `
		CREATE TABLE outbox_custom_jobs LIKE jobs;
	`)
	require.NoError(t, err)
	defer func() {
		_, _ = ts.db.DB().ExecContext(ctx, "DROP TABLE IF EXISTS outbox_custom_jobs;")
	}()

	customRepo, err := jobsrepo.New(ts.db, jobsrepo.WithJobsTable("outbox_custom_jobs"))
	require.NoError(t, err)

	availableAt := time.Now().UTC()
	dedupKey := "custom:mysql:test:prune"

	// 1. Put versioned unique job into custom table
	res, err := customRepo.CreateJobVersionedUniqueResult(
		ctx, dedupKey, "custom.job", 1, `{"val":1}`, availableAt,
	)
	require.NoError(t, err)
	require.True(t, res.Created)

	// 2. Active job in custom table must PROTECT the tombstone from pruning
	pruned, err := customRepo.PruneJobIdempotencyKeys(ctx, time.Now().UTC().Add(time.Minute), 10)
	require.NoError(t, err)
	require.Zero(t, pruned, "tombstone must not be pruned while active job exists in custom table")

	// 3. Replay before job removal returns identical JobID
	replayedRes, err := customRepo.CreateJobVersionedUniqueResult(
		ctx, dedupKey, "custom.job", 1, `{"val":1}`, availableAt,
	)
	require.NoError(t, err)
	require.False(t, replayedRes.Created)
	require.Equal(t, res.JobID, replayedRes.JobID)

	// 4. Delete active job from custom table
	_, err = ts.db.DB().ExecContext(ctx, "DELETE FROM outbox_custom_jobs WHERE id = ?", res.JobID.String())
	require.NoError(t, err)

	// 5. Now pruning should remove the tombstone
	pruned, err = customRepo.PruneJobIdempotencyKeys(ctx, time.Now().UTC().Add(time.Minute), 10)
	require.NoError(t, err)
	require.Equal(t, int64(1), pruned, "tombstone must be pruned after active job is deleted")

	// 6. After prune, creating new job with same dedupKey succeeds with new JobID
	newRes, err := customRepo.CreateJobVersionedUniqueResult(
		ctx, dedupKey, "custom.job", 1, `{"val":2}`, availableAt,
	)
	require.NoError(t, err)
	require.True(t, newRes.Created)
	require.NotEqual(t, res.JobID, newRes.JobID)
}

func TestMySQLMultiTableIdempotencyIsolation(t *testing.T) {
	ctx, _, ts := NewTestMySQLSuite(t)
	defer ts.cleanUp(ctx)

	_, err := ts.db.DB().ExecContext(ctx, `
		CREATE TABLE tenant_a_jobs LIKE jobs;
		CREATE TABLE tenant_a_idemp LIKE outbox_job_idempotency_keys;
		CREATE TABLE tenant_b_jobs LIKE jobs;
		CREATE TABLE tenant_b_idemp LIKE outbox_job_idempotency_keys;
	`)
	require.NoError(t, err)
	defer func() {
		_, _ = ts.db.DB().ExecContext(ctx, `
			DROP TABLE IF EXISTS tenant_a_jobs, tenant_a_idemp, tenant_b_jobs, tenant_b_idemp;
		`)
	}()

	repoA, err := jobsrepo.New(ts.db,
		jobsrepo.WithJobsTable("tenant_a_jobs"),
		jobsrepo.WithIdempotencyTable("tenant_a_idemp"),
	)
	require.NoError(t, err)

	repoB, err := jobsrepo.New(ts.db,
		jobsrepo.WithJobsTable("tenant_b_jobs"),
		jobsrepo.WithIdempotencyTable("tenant_b_idemp"),
	)
	require.NoError(t, err)

	availableAt := time.Now().UTC()
	dedupKey := "shared:key:tenant_isolation"

	// 1. Put unique job in repoA
	resA, err := repoA.CreateJobVersionedUniqueResult(
		ctx, dedupKey, "tenant.job", 1, `{"tenant":"a"}`, availableAt,
	)
	require.NoError(t, err)
	require.True(t, resA.Created)

	// 2. Prune in repoB must NOT touch tenant A's idempotency table
	prunedB, err := repoB.PruneJobIdempotencyKeys(ctx, time.Now().UTC().Add(time.Minute), 10)
	require.NoError(t, err)
	require.Zero(t, prunedB)

	// 3. Replay in repoA is still protected by its tombstone
	replayA, err := repoA.CreateJobVersionedUniqueResult(
		ctx, dedupKey, "tenant.job", 1, `{"tenant":"a"}`, availableAt,
	)
	require.NoError(t, err)
	require.False(t, replayA.Created)
	require.Equal(t, resA.JobID, replayA.JobID)
}

func TestMySQLTableNameValidation(t *testing.T) {
	ctx, _, ts := NewTestMySQLSuite(t)
	defer ts.cleanUp(ctx)

	_, err := jobsrepo.New(ts.db, jobsrepo.WithJobsTable(""))
	require.Error(t, err)

	_, err = jobsrepo.New(ts.db, jobsrepo.WithJobsTable("invalid;drop table"))
	require.Error(t, err)

	_, err = jobsrepo.New(ts.db, jobsrepo.WithJobsTable("123invalid"))
	require.Error(t, err)

	_, err = jobsrepo.New(ts.db, jobsrepo.WithJobsTable("a.b.c"))
	require.Error(t, err)

	_, err = jobsrepo.New(ts.db, jobsrepo.WithIdempotencyTable("invalid;drop table"))
	require.Error(t, err)

	// Valid simple identifier
	valid, err := jobsrepo.New(ts.db,
		jobsrepo.WithJobsTable("valid_table_name"),
		jobsrepo.WithIdempotencyTable("valid_idemp_name"),
	)
	require.NoError(t, err)
	require.NotNil(t, valid)

	// Reserved word as table name succeeds because identifier is quoted
	reservedRepo, err := jobsrepo.New(ts.db,
		jobsrepo.WithJobsTable("select"),
		jobsrepo.WithIdempotencyTable("order"),
	)
	require.NoError(t, err)
	require.NotNil(t, reservedRepo)

	// Verify live execution against reserved word table name
	_, err = ts.db.DB().ExecContext(ctx, "CREATE TABLE `select` LIKE jobs;")
	require.NoError(t, err)
	defer func() {
		_, _ = ts.db.DB().ExecContext(ctx, "DROP TABLE IF EXISTS `select`;")
	}()

	createdID, err := reservedRepo.CreateJobVersioned(ctx, "test.job", 1, `{"val":1}`, time.Now().UTC())
	require.NoError(t, err)
	require.NotEqual(t, types.JobIDNil, createdID)

	// Test jobsfailedrepo validation
	_, err = jobsfailedrepo.New(ts.db, jobsfailedrepo.WithFailedJobsTable(""))
	require.Error(t, err)

	failedReserved, err := jobsfailedrepo.New(ts.db, jobsfailedrepo.WithFailedJobsTable("select"))
	require.NoError(t, err)
	require.NotNil(t, failedReserved)
}
