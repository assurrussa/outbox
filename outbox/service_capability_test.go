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

func TestCapabilityHandlerReceivesPersistedAttemptMetadata(t *testing.T) {
	t.Parallel()

	repo := newCapabilityRepo()
	svc := newCapabilityService(t, repo)
	metadataCh := make(chan outbox.JobMetadata, 1)
	svc.MustRegisterJob(capabilityJob{
		name:    "metadata",
		version: 1,
		handle: func(ctx context.Context, _ string) error {
			metadata, ok := outbox.JobMetadataFromContext(ctx)
			if !ok {
				return errors.New("job metadata is missing")
			}
			if outbox.JobIDFromContext(ctx) != metadata.ID {
				return errors.New("legacy job ID accessor disagrees with metadata")
			}
			metadataCh <- metadata
			return nil
		},
	})

	jobID, err := svc.PutVersioned(context.Background(), "metadata", 1, `{}`, time.Now().UTC())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	require.NoError(t, svc.Run(ctx))

	metadata := <-metadataCh
	require.Equal(t, jobID, metadata.ID)
	require.Equal(t, 1, metadata.Attempt)
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

func TestCapabilityModeDrainFinishesReservedJobAndStopsNewClaims(t *testing.T) {
	repo := newCapabilityRepo()
	svc := newCapabilityService(t, repo)
	started := make(chan struct{})
	release := make(chan struct{})
	svc.MustRegisterJob(capabilityJob{
		name:    "drain",
		version: 1,
		handle: func(ctx context.Context, _ string) error {
			close(started)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
				return nil
			}
		},
		timeout: 2 * time.Second,
	})

	_, err := svc.PutVersioned(context.Background(), "drain", 1, `{}`, time.Now().UTC())
	require.NoError(t, err)
	_, err = svc.PutVersioned(context.Background(), "drain", 1, `{}`, time.Now().UTC())
	require.NoError(t, err)

	runErr := make(chan error, 1)
	go func() { runErr <- svc.Run(context.Background()) }()
	<-started
	svc.BeginDrain()
	require.True(t, svc.IsDraining())
	require.Eventually(t, func() bool { return repo.ExtendCount() >= 1 }, time.Second, 20*time.Millisecond)
	close(release)
	require.NoError(t, <-runErr)

	jobs := repo.Jobs()
	require.Len(t, jobs, 1, "drain must not claim the second job")
	require.Zero(t, jobs[0].Attempts)
}

func TestCapabilityModeDrainDeadlineCancelsHandlerWithoutAck(t *testing.T) {
	repo := newCapabilityRepo()
	svc := newCapabilityService(t, repo)
	started := make(chan struct{})
	svc.MustRegisterJob(capabilityJob{
		name:    "deadline",
		version: 1,
		handle: func(ctx context.Context, _ string) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
		timeout: 2 * time.Second,
	})
	_, err := svc.PutVersioned(context.Background(), "deadline", 1, `{}`, time.Now().UTC())
	require.NoError(t, err)

	runCtx, cancelRun := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- svc.Run(runCtx) }()
	<-started
	svc.BeginDrain()
	cancelRun()
	require.NoError(t, <-runErr)

	jobs := repo.Jobs()
	require.Len(t, jobs, 1, "cancelled drain must leave the fenced job for lease recovery")
	require.Equal(t, 1, jobs[0].Attempts)
}

func TestCapabilityModeDrainBeforeRunLeavesQueueUntouched(t *testing.T) {
	repo := newCapabilityRepo()
	svc := newCapabilityService(t, repo)
	svc.MustRegisterJob(capabilityJob{name: "pending", version: 1})
	_, err := svc.PutVersioned(context.Background(), "pending", 1, `{}`, time.Now().UTC())
	require.NoError(t, err)

	svc.BeginDrain()
	svc.BeginDrain()
	require.NoError(t, svc.Run(context.Background()))
	jobs := repo.Jobs()
	require.Len(t, jobs, 1)
	require.Zero(t, jobs[0].Attempts)
}

func TestServiceReadinessTracksRunAndDrainWithoutClaiming(t *testing.T) {
	repo := newCapabilityRepo()
	svc := newCapabilityService(t, repo)
	started := make(chan struct{})
	release := make(chan struct{})
	svc.MustRegisterJob(capabilityJob{
		name:    "readiness",
		version: 1,
		handle: func(context.Context, string) error {
			close(started)
			<-release
			return nil
		},
	})
	require.ErrorIs(t, svc.Readiness(context.Background()), outbox.ErrServiceNotRunning)

	runErr := make(chan error, 1)
	go func() { runErr <- svc.Run(context.Background()) }()
	require.Eventually(t, func() bool {
		return svc.Readiness(context.Background()) == nil
	}, time.Second, 10*time.Millisecond)
	require.Empty(t, repo.Jobs(), "readiness must not reserve a synthetic job")
	_, err := svc.PutVersioned(context.Background(), "readiness", 1, `{}`, time.Now().UTC())
	require.NoError(t, err)
	<-started

	svc.BeginDrain()
	require.ErrorIs(t, svc.Readiness(context.Background()), outbox.ErrServiceDraining)
	close(release)
	require.NoError(t, <-runErr)
	require.ErrorIs(t, svc.Readiness(context.Background()), outbox.ErrServiceNotRunning)
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

func TestPutVersionedUniqueReportsCreatedAndReplay(t *testing.T) {
	repo := newCapabilityRepo()
	svc := newCapabilityService(t, repo)
	availableAt := time.Now().UTC().Truncate(time.Microsecond)

	first, err := svc.PutVersionedUnique(
		context.Background(), "message-1", "publish", 2, `{"revision":2}`, availableAt,
	)
	require.NoError(t, err)
	require.True(t, first.Created)
	require.False(t, first.JobID.IsZero())

	replayed, err := svc.PutVersionedUnique(
		context.Background(), "message-1", "publish", 2, `{"revision":2}`, availableAt,
	)
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, first.JobID, replayed.JobID)
	require.Len(t, repo.Jobs(), 1)

	_, err = svc.PutVersionedUnique(
		context.Background(), "message-1", "publish", 2, `{"revision":3}`, availableAt,
	)
	require.ErrorIs(t, err, outbox.ErrIdempotencyConflict)
}

func TestUniqueAndReschedulableRepositoriesRequireCapabilityMode(t *testing.T) {
	t.Parallel()

	for name, option := range map[string]outbox.OptOptionsSetter{
		"unique":        outbox.WithUniqueJobsRepo(newCapabilityRepo()),
		"reschedulable": outbox.WithReschedulableJobsRepo(newCapabilityRepo()),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := newCapabilityRepo()
			_, err := outbox.New(
				outbox.WithJobsRepo(repo),
				outbox.WithJobsFailedRepo(repo),
				outbox.WithTransactor(repo),
				option,
			)
			require.ErrorContains(t, err, "requires capabilityJobsRepo")
		})
	}
}

func TestCapabilityModePermanentFailureMovesDirectlyToDLQ(t *testing.T) {
	repo := newCapabilityRepo()
	svc := newCapabilityService(t, repo)
	svc.MustRegisterJob(capabilityJob{
		name:        "permanent",
		version:     1,
		maxAttempts: 10,
		handle: func(context.Context, string) error {
			return outbox.Permanent(errors.New("invalid payload"))
		},
	})
	_, err := svc.PutVersioned(context.Background(), "permanent", 1, `{}`, time.Now().UTC())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	require.NoError(t, svc.Run(ctx))
	require.Empty(t, repo.Jobs())
	require.Len(t, repo.Failed(), 1)
	require.Contains(t, repo.Failed()[0].Reason, "permanent failure")
}

func TestCapabilityModeRetryAtPersistsScheduleAndReleasesLease(t *testing.T) {
	repo := newCapabilityRepo()
	svc := newCapabilityService(t, repo)
	retryAt := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	svc.MustRegisterJob(capabilityJob{
		name:    "scheduled-retry",
		version: 1,
		handle: func(context.Context, string) error {
			return outbox.RetryAt(errors.New("broker unavailable"), retryAt)
		},
	})
	_, err := svc.PutVersioned(context.Background(), "scheduled-retry", 1, `{}`, time.Now().UTC())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	require.NoError(t, svc.Run(ctx))

	jobs := repo.Jobs()
	require.Len(t, jobs, 1)
	require.Equal(t, retryAt, jobs[0].AvailableAt)
	require.False(t, jobs[0].ReservedAt.Valid)
	require.Equal(t, types.LeaseTokenNil, jobs[0].LeaseToken)
	require.Equal(t, 1, jobs[0].Attempts)
	require.Empty(t, repo.Failed())
}

func TestCapabilityModeRetryAtStillHonorsMaxAttempts(t *testing.T) {
	repo := newCapabilityRepo()
	svc := newCapabilityService(t, repo)
	svc.MustRegisterJob(capabilityJob{
		name:        "bounded-retry",
		version:     1,
		maxAttempts: 1,
		handle: func(context.Context, string) error {
			return outbox.RetryAt(errors.New("still unavailable"), time.Now().UTC().Add(time.Hour))
		},
	})
	_, err := svc.PutVersioned(context.Background(), "bounded-retry", 1, `{}`, time.Now().UTC())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	require.NoError(t, svc.Run(ctx))
	require.Empty(t, repo.Jobs())
	require.Len(t, repo.Failed(), 1)
}

func TestCapabilityModeRetryAtFailsClosedAfterFenceLoss(t *testing.T) {
	repo := newCapabilityRepo()
	repo.loseLeaseOnReschedule = true
	svc := newCapabilityService(t, repo)
	svc.MustRegisterJob(capabilityJob{
		name:    "lost-reschedule",
		version: 1,
		handle: func(context.Context, string) error {
			return outbox.RetryAt(errors.New("retry"), time.Now().UTC().Add(time.Hour))
		},
	})
	_, err := svc.PutVersioned(context.Background(), "lost-reschedule", 1, `{}`, time.Now().UTC())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = svc.Run(ctx)
	require.ErrorIs(t, err, outbox.ErrLeaseLost)
	require.Len(t, repo.Jobs(), 1)
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

func TestRegisterJobsIsAtomicOnExistingOrBatchDuplicate(t *testing.T) {
	t.Parallel()

	svc := newCapabilityService(t, newCapabilityRepo())
	existing := capabilityJob{name: "existing", version: 1}
	newJob := capabilityJob{name: "new", version: 1}
	svc.MustRegisterJob(existing)

	require.Error(t, svc.RegisterJobs(newJob, existing))
	require.NoError(t, svc.RegisterJob(newJob), "failed batch must not partially install new job")
	require.Error(t, svc.RegisterJobs(
		capabilityJob{name: "duplicate", version: 1},
		capabilityJob{name: "duplicate", version: 1},
	))
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

	jobs            []models.Job
	failed          []models.JobFailed
	idempotencyJobs map[string]idempotencyJob

	extendCount           int
	loseLeaseOnExtend     bool
	loseLeaseOnDelete     bool
	loseLeaseOnReschedule bool
}

func newCapabilityRepo() *capabilityRepo {
	return &capabilityRepo{idempotencyJobs: make(map[string]idempotencyJob)}
}

type idempotencyJob struct {
	id            types.JobID
	name          string
	schemaVersion outbox.SchemaVersion
	payload       string
	availableAt   time.Time
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

func (r *capabilityRepo) CreateJobVersionedUnique(
	ctx context.Context,
	deduplicationKey string,
	name string,
	schemaVersion outbox.SchemaVersion,
	payload string,
	availableAt time.Time,
) (types.JobID, error) {
	result, err := r.CreateJobVersionedUniqueResult(
		ctx, deduplicationKey, name, schemaVersion, payload, availableAt,
	)
	return result.JobID, err
}

func (r *capabilityRepo) CreateJobVersionedUniqueResult(
	_ context.Context,
	deduplicationKey string,
	name string,
	schemaVersion outbox.SchemaVersion,
	payload string,
	availableAt time.Time,
) (outbox.UniquePutResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.idempotencyJobs[deduplicationKey]; ok {
		if existing.name != name || existing.schemaVersion != schemaVersion ||
			existing.payload != payload || !existing.availableAt.Equal(availableAt) {
			return outbox.UniquePutResult{}, outbox.ErrIdempotencyConflict
		}

		return outbox.UniquePutResult{JobID: existing.id, Created: false}, nil
	}

	id := types.NewJobID()
	r.idempotencyJobs[deduplicationKey] = idempotencyJob{
		id:            id,
		name:          name,
		schemaVersion: schemaVersion,
		payload:       payload,
		availableAt:   availableAt,
	}
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

	return outbox.UniquePutResult{JobID: id, Created: true}, nil
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

func (r *capabilityRepo) RescheduleJobWithLease(
	_ context.Context,
	jobID types.JobID,
	leaseToken outbox.LeaseToken,
	now time.Time,
	availableAt time.Time,
) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.loseLeaseOnReschedule {
		return 0, nil
	}
	for i := range r.jobs {
		job := r.jobs[i]
		if job.ID != jobID || job.LeaseToken != leaseToken || !job.ReservedAt.Time.After(now) {
			continue
		}
		job.AvailableAt = availableAt.UTC()
		job.ReservedAt = sql.NullTime{}
		job.LeaseToken = types.LeaseTokenNil
		r.jobs[i] = job
		return 1, nil
	}
	return 0, nil
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
	_ outbox.FanoutJobsRepository           = (*capabilityRepo)(nil)
	_ outbox.UniqueJobsRepository           = (*capabilityRepo)(nil)
	_ outbox.ReschedulableJobsRepository    = (*capabilityRepo)(nil)
	_ outbox.JobsFailedRepository           = (*capabilityRepo)(nil)
	_ outbox.CapabilityJobsFailedRepository = (*capabilityRepo)(nil)
	_ outbox.Transactor                     = (*capabilityRepo)(nil)
)
