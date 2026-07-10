package outbox_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/sharederrors"
	"github.com/assurrussa/outbox/shared/types"
)

func TestCapabilityModeLeavesUnsupportedSchemaPending(t *testing.T) {
	repo := newCapabilityRepo()
	svc := newCapabilityService(t, repo)
	svc.MustRegisterJob(capabilityJob{name: "publish", version: 1})

	_, err := svc.PutVersioned(context.Background(), "publish", 2, `{"revision":2}`, time.Now().UTC())
	require.NoError(t, err)
	_, err = svc.PutVersioned(context.Background(), "publish", 1, `{"revision":1}`, time.Now().UTC())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	require.NoError(t, svc.Run(ctx))

	jobs := repo.Jobs()
	require.Len(t, jobs, 1)
	require.Equal(t, outbox.SchemaVersion(2), jobs[0].SchemaVersion)
	require.Zero(t, jobs[0].Attempts)
	require.Empty(t, repo.Failed())
}

func TestCapabilityModeExtendsLeaseWhileHandlerRuns(t *testing.T) {
	repo := newCapabilityRepo()
	svc := newCapabilityService(t, repo)
	svc.MustRegisterJob(capabilityJob{
		name:    "slow",
		version: 1,
		handle: func(ctx context.Context, _ string) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(750 * time.Millisecond):
				return nil
			}
		},
		timeout: 2 * time.Second,
	})

	_, err := svc.PutVersioned(context.Background(), "slow", 1, `{}`, time.Now().UTC())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		runErr <- svc.Run(ctx)
	}()

	require.Eventually(t, func() bool {
		return len(repo.Jobs()) == 0
	}, 2*time.Second, 20*time.Millisecond)
	cancel()
	require.NoError(t, <-runErr)
	require.GreaterOrEqual(t, repo.ExtendCount(), 1)
}

func TestCapabilityModeLostLeaseCancelsHandlerAndKeepsJob(t *testing.T) {
	repo := newCapabilityRepo()
	repo.loseLeaseOnExtend = true
	svc := newCapabilityService(t, repo)
	svc.MustRegisterJob(capabilityJob{
		name:    "lost",
		version: 1,
		handle: func(ctx context.Context, _ string) error {
			<-ctx.Done()
			return ctx.Err()
		},
		timeout: 2 * time.Second,
	})

	_, err := svc.PutVersioned(context.Background(), "lost", 1, `{}`, time.Now().UTC())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = svc.Run(ctx)
	require.ErrorIs(t, err, outbox.ErrLeaseLost)
	require.Len(t, repo.Jobs(), 1)
	require.Empty(t, repo.Failed())
}

func TestCapabilityModeConditionalAckKeepsJobAfterFenceLoss(t *testing.T) {
	repo := newCapabilityRepo()
	repo.loseLeaseOnDelete = true
	svc := newCapabilityService(t, repo)
	svc.MustRegisterJob(capabilityJob{name: "ack", version: 1})

	_, err := svc.PutVersioned(context.Background(), "ack", 1, `{}`, time.Now().UTC())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = svc.Run(ctx)
	require.ErrorIs(t, err, outbox.ErrLeaseLost)
	require.Len(t, repo.Jobs(), 1)
}

func TestCapabilityModeDLQPreservesSchemaVersion(t *testing.T) {
	repo := newCapabilityRepo()
	svc := newCapabilityService(t, repo)
	svc.MustRegisterJob(capabilityJob{
		name:        "fail",
		version:     2,
		maxAttempts: 1,
		handle: func(context.Context, string) error {
			return errors.New("failed")
		},
	})

	_, err := svc.PutVersioned(context.Background(), "fail", 2, `{}`, time.Now().UTC())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	require.NoError(t, svc.Run(ctx))

	require.Empty(t, repo.Jobs())
	failed := repo.Failed()
	require.Len(t, failed, 1)
	require.Equal(t, outbox.SchemaVersion(2), failed[0].SchemaVersion)
}

func TestVersionedCapabilityRequiresCapabilityRepositories(t *testing.T) {
	repo := newRuntimeRepo()
	svc := newRuntimeService(t, repo)

	_, err := svc.PutVersioned(context.Background(), "publish", 2, `{}`, time.Now().UTC())
	require.ErrorIs(t, err, outbox.ErrCapabilityRepositoryNotConfigured)
	require.ErrorIs(
		t,
		svc.RegisterJob(capabilityJob{name: "publish", version: 2}),
		outbox.ErrCapabilityRepositoryNotConfigured,
	)
}

type capabilityJob struct {
	name        string
	version     outbox.SchemaVersion
	handle      func(context.Context, string) error
	timeout     time.Duration
	maxAttempts int
}

func (j capabilityJob) Name() string { return j.name }

func (j capabilityJob) SchemaVersion() outbox.SchemaVersion { return j.version }

func (j capabilityJob) Handle(ctx context.Context, payload string) error {
	if j.handle == nil {
		return nil
	}

	return j.handle(ctx, payload)
}

func (j capabilityJob) ExecutionTimeout() time.Duration {
	if j.timeout <= 0 {
		return time.Second
	}

	return j.timeout
}

func (j capabilityJob) MaxAttempts() int {
	if j.maxAttempts <= 0 {
		return 3
	}

	return j.maxAttempts
}

type capabilityRepo struct {
	mu sync.Mutex

	jobs   []models.Job
	failed []models.JobFailed

	extendCount       int
	loseLeaseOnExtend bool
	loseLeaseOnDelete bool
}

func newCapabilityRepo() *capabilityRepo {
	return &capabilityRepo{}
}

func (r *capabilityRepo) CreateJob(
	ctx context.Context,
	name string,
	payload string,
	availableAt time.Time,
) (types.JobID, error) {
	return r.CreateJobVersioned(ctx, name, outbox.DefaultSchemaVersion, payload, availableAt)
}

func (r *capabilityRepo) CreateJobVersioned(
	_ context.Context,
	name string,
	schemaVersion outbox.SchemaVersion,
	payload string,
	availableAt time.Time,
) (types.JobID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	id := types.NewJobID()
	r.jobs = append(r.jobs, models.Job{
		ID:            id,
		Queue:         testQueue,
		Name:          name,
		SchemaVersion: schemaVersion,
		Payload:       payload,
		ReservedAt:    sql.NullTime{Time: availableAt, Valid: true},
		AvailableAt:   availableAt,
		CreatedAt:     time.Now().UTC(),
	})

	return id, nil
}

func (r *capabilityRepo) FindAndReserveJob(
	ctx context.Context,
	now time.Time,
	until time.Time,
) (models.Job, error) {
	return r.FindAndReserveJobForCapabilities(
		ctx,
		now,
		until,
		types.NewLeaseToken(),
		[]outbox.JobCapability{{Name: "", SchemaVersion: outbox.DefaultSchemaVersion}},
	)
}

func (r *capabilityRepo) FindAndReserveJobForCapabilities(
	_ context.Context,
	now time.Time,
	until time.Time,
	leaseToken outbox.LeaseToken,
	capabilities []outbox.JobCapability,
) (models.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	supported := make(map[outbox.JobCapability]struct{}, len(capabilities))
	for _, capability := range capabilities {
		supported[capability] = struct{}{}
	}

	for i := range r.jobs {
		job := r.jobs[i]
		capability := outbox.JobCapability{Name: job.Name, SchemaVersion: job.SchemaVersion}
		if _, ok := supported[capability]; !ok {
			continue
		}
		if job.AvailableAt.After(now) || job.ReservedAt.Time.After(now) {
			continue
		}

		job.Attempts++
		job.ReservedAt = sql.NullTime{Time: until, Valid: true}
		job.LeaseToken = leaseToken
		r.jobs[i] = job

		return job, nil
	}

	return models.Job{}, sharederrors.ErrNoJobs
}

func (r *capabilityRepo) ExtendJobLease(
	_ context.Context,
	jobID types.JobID,
	leaseToken outbox.LeaseToken,
	now time.Time,
	until time.Time,
) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.extendCount++
	if r.loseLeaseOnExtend {
		return 0, nil
	}

	for i := range r.jobs {
		job := r.jobs[i]
		if job.ID != jobID || job.LeaseToken != leaseToken || !job.ReservedAt.Time.After(now) {
			continue
		}

		job.ReservedAt = sql.NullTime{Time: until, Valid: true}
		r.jobs[i] = job
		return 1, nil
	}

	return 0, nil
}

func (r *capabilityRepo) DeleteJob(_ context.Context, jobID types.JobID) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.deleteJob(jobID, types.LeaseTokenNil, time.Time{}, false), nil
}

func (r *capabilityRepo) DeleteJobWithLease(
	_ context.Context,
	jobID types.JobID,
	leaseToken outbox.LeaseToken,
	now time.Time,
) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.loseLeaseOnDelete {
		return 0, nil
	}

	return r.deleteJob(jobID, leaseToken, now, true), nil
}

func (r *capabilityRepo) deleteJob(
	jobID types.JobID,
	leaseToken outbox.LeaseToken,
	now time.Time,
	checkLease bool,
) int64 {
	for i := range r.jobs {
		job := r.jobs[i]
		if job.ID != jobID {
			continue
		}
		if checkLease && (job.LeaseToken != leaseToken || !job.ReservedAt.Time.After(now)) {
			return 0
		}

		r.jobs = append(r.jobs[:i], r.jobs[i+1:]...)
		return 1
	}

	return 0
}

func (r *capabilityRepo) CreateFailedJob(
	ctx context.Context,
	jobID types.JobID,
	name string,
	payload string,
	reason string,
) (types.JobID, error) {
	return r.CreateFailedJobVersioned(
		ctx,
		jobID,
		name,
		outbox.DefaultSchemaVersion,
		payload,
		reason,
	)
}

func (r *capabilityRepo) CreateFailedJobVersioned(
	_ context.Context,
	jobID types.JobID,
	name string,
	schemaVersion outbox.SchemaVersion,
	payload string,
	reason string,
) (types.JobID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	id := types.NewJobID()
	r.failed = append(r.failed, models.JobFailed{
		ID:            id,
		JobID:         jobID,
		Queue:         testQueue,
		Name:          name,
		SchemaVersion: schemaVersion,
		Payload:       payload,
		Reason:        reason,
		FailedAt:      time.Now().UTC(),
		CreatedAt:     time.Now().UTC(),
	})

	return id, nil
}

func (r *capabilityRepo) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (r *capabilityRepo) Jobs() []models.Job {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]models.Job(nil), r.jobs...)
}

func (r *capabilityRepo) Failed() []models.JobFailed {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]models.JobFailed(nil), r.failed...)
}

func (r *capabilityRepo) ExtendCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.extendCount
}

func newCapabilityService(t *testing.T, repo *capabilityRepo) *outbox.Service {
	t.Helper()

	svc, err := outbox.New(
		outbox.WithWorkers(1),
		outbox.WithIdleTime(100*time.Millisecond),
		outbox.WithReserveFor(time.Second),
		outbox.WithJobsRepo(repo),
		outbox.WithCapabilityJobsRepo(repo),
		outbox.WithJobsFailedRepo(repo),
		outbox.WithCapabilityJobsFailedRepo(repo),
		outbox.WithTransactor(repo),
		outbox.WithLogger(logger.Discard()),
	)
	require.NoError(t, err)

	return svc
}

var (
	_ outbox.Job                            = capabilityJob{}
	_ outbox.VersionedJob                   = capabilityJob{}
	_ outbox.JobsRepository                 = (*capabilityRepo)(nil)
	_ outbox.CapabilityJobsRepository       = (*capabilityRepo)(nil)
	_ outbox.JobsFailedRepository           = (*capabilityRepo)(nil)
	_ outbox.CapabilityJobsFailedRepository = (*capabilityRepo)(nil)
	_ outbox.Transactor                     = (*capabilityRepo)(nil)
)
