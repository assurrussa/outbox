package runtime

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreoutbox "github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/stretchr/testify/require"
)

type recordingRetryPolicy struct {
	delay    time.Duration
	mu       sync.Mutex
	attempts []int
}

func (p *recordingRetryPolicy) RetryDelay(attempt coreoutbox.RetryAttempt) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.attempts = append(p.attempts, attempt.Attempt)
	return p.delay
}

func (p *recordingRetryPolicy) Attempts() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]int, len(p.attempts))
	copy(out, p.attempts)
	return out
}

type flakyRuntimeJob struct {
	coreoutbox.DefaultJob
	calls atomic.Int32
}

func (j *flakyRuntimeJob) Name() string { return "runtime.retry" }

func (j *flakyRuntimeJob) MaxAttempts() int { return 3 }

func (j *flakyRuntimeJob) Handle(_ context.Context, _ string) error {
	if j.calls.Add(1) == 1 {
		return errors.New("transient error")
	}
	return nil
}

type memoryRuntimeRepo struct {
	mu   sync.Mutex
	jobs []models.Job
}

func (r *memoryRuntimeRepo) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (*memoryRuntimeRepo) SupportsAtomicDLQ() bool { return true }

func (r *memoryRuntimeRepo) CreateJobVersioned(
	_ context.Context,
	name string,
	schemaVersion coreoutbox.SchemaVersion,
	payload string,
	availableAt time.Time,
) (coreoutbox.JobID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := coreoutbox.NewJobID()
	r.jobs = append(r.jobs, models.Job{
		ID:            id,
		Queue:         "queue",
		Name:          name,
		SchemaVersion: schemaVersion,
		Payload:       payload,
		AvailableAt:   availableAt.UTC(),
		CreatedAt:     time.Now().UTC(),
	})
	return id, nil
}

func (r *memoryRuntimeRepo) FindAndReserveJobsForCapabilities(
	_ context.Context,
	now time.Time,
	until time.Time,
	leaseToken coreoutbox.LeaseToken,
	capabilities []coreoutbox.JobCapability,
	limit int,
) ([]models.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now = now.UTC()
	until = until.UTC()
	allowed := make(map[coreoutbox.JobCapability]struct{}, len(capabilities))
	for _, cap := range capabilities {
		allowed[cap] = struct{}{}
	}
	var claimed []models.Job
	for i := range r.jobs {
		if len(claimed) >= limit {
			break
		}
		job := &r.jobs[i]
		if _, ok := allowed[coreoutbox.JobCapability{Name: job.Name, SchemaVersion: job.SchemaVersion}]; !ok {
			continue
		}
		if job.AvailableAt.After(now) {
			continue
		}
		if job.ReservedAt.Valid && job.ReservedAt.Time.After(now) {
			continue
		}
		job.Attempts++
		job.ReservedAt = sql.NullTime{Time: until, Valid: true}
		job.LeaseToken = leaseToken
		claimed = append(claimed, *job)
	}
	if len(claimed) == 0 {
		return nil, coreoutbox.ErrNoJobs
	}
	return claimed, nil
}

func (r *memoryRuntimeRepo) ExtendJobLeases(
	_ context.Context,
	jobIDs []coreoutbox.JobID,
	leaseToken coreoutbox.LeaseToken,
	now time.Time,
	until time.Time,
) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var affected int64
	for i := range r.jobs {
		for _, id := range jobIDs {
			if r.jobs[i].ID == id && r.jobs[i].LeaseToken == leaseToken &&
				r.jobs[i].ReservedAt.Valid && r.jobs[i].ReservedAt.Time.After(now.UTC()) {
				r.jobs[i].ReservedAt = sql.NullTime{Time: until.UTC(), Valid: true}
				affected++
			}
		}
	}
	return affected, nil
}

func (r *memoryRuntimeRepo) ReleaseUnstartedJobsWithLease(
	_ context.Context,
	jobIDs []coreoutbox.JobID,
	leaseToken coreoutbox.LeaseToken,
	now time.Time,
) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var affected int64
	for i := range r.jobs {
		for _, id := range jobIDs {
			if r.jobs[i].ID == id && r.jobs[i].LeaseToken == leaseToken &&
				r.jobs[i].ReservedAt.Valid && r.jobs[i].ReservedAt.Time.After(now.UTC()) && r.jobs[i].Attempts > 0 {
				r.jobs[i].Attempts--
				r.jobs[i].ReservedAt = sql.NullTime{}
				r.jobs[i].LeaseToken = coreoutbox.LeaseToken{}
				affected++
			}
		}
	}
	return affected, nil
}

func (r *memoryRuntimeRepo) DeleteJobWithLease(
	_ context.Context,
	jobID coreoutbox.JobID,
	leaseToken coreoutbox.LeaseToken,
	now time.Time,
) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, job := range r.jobs {
		if job.ID == jobID && job.LeaseToken == leaseToken &&
			job.ReservedAt.Valid && job.ReservedAt.Time.After(now.UTC()) {
			r.jobs = append(r.jobs[:i], r.jobs[i+1:]...)
			return 1, nil
		}
	}
	return 0, nil
}

func (r *memoryRuntimeRepo) RescheduleJobWithLease(
	_ context.Context,
	jobID coreoutbox.JobID,
	leaseToken coreoutbox.LeaseToken,
	now time.Time,
	availableAt time.Time,
) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.jobs {
		if r.jobs[i].ID == jobID && r.jobs[i].LeaseToken == leaseToken &&
			r.jobs[i].ReservedAt.Valid && r.jobs[i].ReservedAt.Time.After(now.UTC()) {
			r.jobs[i].AvailableAt = availableAt.UTC()
			r.jobs[i].ReservedAt = sql.NullTime{}
			r.jobs[i].LeaseToken = coreoutbox.LeaseToken{}
			return 1, nil
		}
	}
	return 0, nil
}

func (*memoryRuntimeRepo) MaxReservationBatchSize() int { return coreoutbox.MaxReservationBatchSize }

func (*memoryRuntimeRepo) CreateFailedJobVersioned(
	_ context.Context,
	_ coreoutbox.JobID,
	_ string,
	_ coreoutbox.SchemaVersion,
	_ string,
	_ string,
) (coreoutbox.JobID, error) {
	return coreoutbox.NewJobID(), nil
}

func TestNewServiceHonorsRetryPolicyAndObserver(t *testing.T) {
	t.Parallel()
	const customRetryDelay = 120 * time.Millisecond
	policy := &recordingRetryPolicy{delay: customRetryDelay}
	outcomes := make(chan coreoutbox.RuntimeOutcome, 4)
	repo := &memoryRuntimeRepo{}

	svc, err := newService(Config{
		Workers:     1,
		IdleTime:    100 * time.Millisecond,
		ReserveFor:  time.Second,
		RetryPolicy: policy,
		Observer:    outcomes,
		Logger:      logger.Discard(),
	}, repo, nil, repo, repo)
	require.NoError(t, err)

	job := &flakyRuntimeJob{}
	svc.MustRegisterJob(job)

	enqueuedAt := time.Now().UTC().Truncate(time.Millisecond)
	jobID, err := svc.Put(t.Context(), job.Name(), `{}`, enqueuedAt)
	require.NoError(t, err)

	runCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- svc.Run(runCtx) }()

	var first coreoutbox.RuntimeOutcome
	select {
	case first = <-outcomes:
	case <-runCtx.Done():
		t.Fatal("timed out waiting for retry outcome")
	}
	require.Equal(t, jobID, first.JobID)
	require.Equal(t, coreoutbox.OutcomeRetryPersisted, first.Kind)
	require.Equal(t, 1, first.Attempt)
	require.False(t, first.AvailableAt.Before(enqueuedAt.Add(customRetryDelay)))

	var second coreoutbox.RuntimeOutcome
	select {
	case second = <-outcomes:
	case <-runCtx.Done():
		t.Fatal("timed out waiting for ack outcome")
	}
	require.Equal(t, jobID, second.JobID)
	require.Equal(t, coreoutbox.OutcomeACKConfirmed, second.Kind)
	require.Equal(t, 2, second.Attempt)
	require.Equal(t, []int{1}, policy.Attempts())

	cancel()
	require.NoError(t, <-done)
}
