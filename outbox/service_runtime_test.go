package outbox_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/outbox"
	outboxlogger "github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/sharederrors"
	"github.com/assurrussa/outbox/shared/types"
)

func TestRun_PanicInJobHandler_DoesNotCrashAndMovesToDLQ(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	repo := newRuntimeRepo()
	svc := newRuntimeService(t, repo)
	svc.MustRegisterJob(panicJob{name: "panic_job", maxAttempts: 1})

	_, err := svc.Put(ctx, "panic_job", "{}", time.Now().UTC())
	require.NoError(t, err)

	err = svc.Run(ctx)
	require.NoError(t, err)

	require.Equal(t, 0, repo.JobsCount())
	require.Equal(t, 1, repo.FailedCount())
	require.Contains(t, repo.LastFailedReason(), "panic in job")
}

func TestRegisterJob_WhileRun_ReturnsErrServiceRunning(t *testing.T) {
	t.Parallel()

	repo := newRuntimeRepo(withBlockingFind())
	svc := newRuntimeService(t, repo)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- svc.Run(ctx)
	}()

	<-repo.FindStarted()

	err := svc.RegisterJob(noopJob{name: "late_job"})
	require.ErrorIs(t, err, outbox.ErrServiceRunning)

	cancel()
	require.NoError(t, <-runErrCh)
}

type runtimeRepoOption func(*runtimeRepo)

func withBlockingFind() runtimeRepoOption {
	return func(r *runtimeRepo) {
		r.blockFind = true
	}
}

type runtimeRepo struct {
	mu sync.Mutex

	jobs   []models.Job
	failed []models.JobFailed

	findStarted chan struct{}
	findOnce    sync.Once
	blockFind   bool
}

func newRuntimeRepo(opts ...runtimeRepoOption) *runtimeRepo {
	r := &runtimeRepo{
		findStarted: make(chan struct{}),
	}

	for _, opt := range opts {
		opt(r)
	}

	return r
}

func (r *runtimeRepo) CreateJob(ctx context.Context, name, payload string, availableAt time.Time) (types.JobID, error) {
	_ = ctx
	r.mu.Lock()
	defer r.mu.Unlock()

	id := types.NewJobID()
	r.jobs = append(r.jobs, models.Job{
		ID:          id,
		Queue:       "queue",
		Name:        name,
		Payload:     payload,
		Attempts:    0,
		ReservedAt:  sql.NullTime{},
		AvailableAt: availableAt.UTC(),
		CreatedAt:   time.Now().UTC(),
	})

	return id, nil
}

func (r *runtimeRepo) FindAndReserveJob(ctx context.Context, now, until time.Time) (models.Job, error) {
	r.findOnce.Do(func() {
		close(r.findStarted)
	})

	if r.blockFind {
		<-ctx.Done()
		return models.Job{}, ctx.Err()
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for i := range r.jobs {
		job := r.jobs[i]
		isAvailable := !job.AvailableAt.After(now)
		isFree := !job.ReservedAt.Valid || !job.ReservedAt.Time.After(now)
		if !isAvailable || !isFree {
			continue
		}

		job.Attempts++
		job.ReservedAt = sql.NullTime{Time: until.UTC(), Valid: true}
		r.jobs[i] = job

		return job, nil
	}

	return models.Job{}, sharederrors.ErrNoJobs
}

func (r *runtimeRepo) DeleteJob(ctx context.Context, jobID types.JobID) (int64, error) {
	_ = ctx
	r.mu.Lock()
	defer r.mu.Unlock()

	for i := range r.jobs {
		if r.jobs[i].ID == jobID {
			r.jobs = append(r.jobs[:i], r.jobs[i+1:]...)
			return 1, nil
		}
	}

	return 0, nil
}

func (r *runtimeRepo) CreateFailedJob(
	ctx context.Context,
	jobID types.JobID,
	name, payload, reason string,
) (types.JobID, error) {
	_ = ctx
	r.mu.Lock()
	defer r.mu.Unlock()

	id := types.NewJobID()
	now := time.Now().UTC()
	r.failed = append(r.failed, models.JobFailed{
		ID:        id,
		JobID:     jobID,
		Queue:     "queue",
		Name:      name,
		Payload:   payload,
		Reason:    reason,
		FailedAt:  now,
		CreatedAt: now,
	})

	return id, nil
}

func (r *runtimeRepo) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (r *runtimeRepo) JobsCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.jobs)
}

func (r *runtimeRepo) FailedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.failed)
}

func (r *runtimeRepo) LastFailedReason() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.failed) == 0 {
		return ""
	}
	return r.failed[len(r.failed)-1].Reason
}

func (r *runtimeRepo) FindStarted() <-chan struct{} {
	return r.findStarted
}

func newRuntimeService(t *testing.T, repo *runtimeRepo) *outbox.Service {
	t.Helper()

	svc, err := outbox.New(
		outbox.WithWorkers(1),
		outbox.WithIdleTime(100*time.Millisecond),
		outbox.WithReserveFor(time.Second),
		outbox.WithJobsRepo(repo),
		outbox.WithJobsFailedRepo(repo),
		outbox.WithTransactor(repo),
		outbox.WithLogger(outboxlogger.Discard()),
	)
	require.NoError(t, err)

	return svc
}

type noopJob struct {
	name string
}

func (j noopJob) Name() string { return j.name }

func (j noopJob) Handle(_ context.Context, _ string) error { return nil }

func (j noopJob) ExecutionTimeout() time.Duration { return time.Second }

func (j noopJob) MaxAttempts() int { return 1 }

type panicJob struct {
	name        string
	maxAttempts int
}

func (j panicJob) Name() string { return j.name }

func (j panicJob) Handle(_ context.Context, _ string) error {
	panic("boom")
}

func (j panicJob) ExecutionTimeout() time.Duration { return time.Second }

func (j panicJob) MaxAttempts() int {
	if j.maxAttempts <= 0 {
		return 1
	}
	return j.maxAttempts
}

var (
	_ outbox.JobsRepository       = (*runtimeRepo)(nil)
	_ outbox.JobsFailedRepository = (*runtimeRepo)(nil)
	_ outbox.Transactor           = (*runtimeRepo)(nil)
	_ outbox.Job                  = noopJob{}
	_ outbox.Job                  = panicJob{}
)
