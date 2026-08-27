package outbox_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/sharederrors"
	"github.com/assurrussa/outbox/shared/types"
)

const (
	testValueActive    = "active"
	testValueBatch     = "batch"
	testValueFail      = "fail"
	testValueOne       = "one"
	testValuePermanent = "permanent"
	testValueRetry     = "retry"
	testValueSlow      = "slow"
	testValueTail      = "tail"
)

func TestReservationBatchOptionValidationAndCompatibility(t *testing.T) {
	repo := newCapabilityRepo()
	service, err := outbox.New(
		outbox.WithJobsRepo(&limitedBatchRepo{capabilityRepo: repo, maximum: 1}),
		outbox.WithJobsFailedRepo(repo),
		outbox.WithTransactor(repo),
		outbox.WithLogger(logger.Discard()),
	)
	require.NoError(t, err)
	require.NotNil(t, service)

	tests := []struct {
		name      string
		size      int
		repoMax   int
		wantError error
	}{
		{name: "zero", size: 0, repoMax: 1000, wantError: outbox.ErrOption},
		{name: "above maximum", size: 1001, repoMax: 1000, wantError: outbox.ErrOption},
		{name: "invalid repository maximum", size: 1, repoMax: 0, wantError: outbox.ErrOption},
		{name: "default compatible repository", size: 1, repoMax: 1},
		{
			name:      "above repository maximum",
			size:      2,
			repoMax:   1,
			wantError: outbox.ErrReservationBatchSizeUnsupported,
		},
		{name: "supported", size: 1000, repoMax: 1000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newCapabilityRepo()
			service, err := outbox.New(
				outbox.WithReservationBatchSize(tt.size),
				outbox.WithJobsRepo(&limitedBatchRepo{capabilityRepo: repo, maximum: tt.repoMax}),
				outbox.WithJobsFailedRepo(repo),
				outbox.WithTransactor(repo),
				outbox.WithLogger(logger.Discard()),
			)
			if tt.wantError != nil {
				require.ErrorIs(t, err, tt.wantError)
				assert.Nil(t, service)
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, service)
		})
	}
}

func TestReservationSizeOneAndManyUseTheSameClaimPath(t *testing.T) {
	for _, size := range []int{1, 3} {
		t.Run(fmt.Sprintf("limit=%d", size), func(t *testing.T) {
			repo := newCapabilityRepo()
			_, err := repo.CreateJobVersioned(
				t.Context(), testValueBatch, 1, testValueOne, time.Now().UTC().Add(-time.Second),
			)
			require.NoError(t, err)

			service := newBatchService(t, repo, size)
			require.NoError(t, service.RegisterJob(capabilityJob{name: testValueBatch, version: 1}))
			runFor(t, service, 250*time.Millisecond)

			limits := repo.ClaimLimits()
			require.NotEmpty(t, limits)
			for _, actual := range limits {
				require.Equal(t, size, actual)
			}
		})
	}
}

func TestReservationBatchRejectsEmptySuccessfulClaim(t *testing.T) {
	repo := newCapabilityRepo()
	service, err := outbox.New(
		outbox.WithWorkers(1),
		outbox.WithIdleTime(100*time.Millisecond),
		outbox.WithReserveFor(time.Second),
		outbox.WithJobsRepo(&emptySuccessfulBatchRepo{capabilityRepo: repo}),
		outbox.WithJobsFailedRepo(repo),
		outbox.WithTransactor(repo),
		outbox.WithLogger(logger.Discard()),
	)
	require.NoError(t, err)
	require.NoError(t, service.RegisterJob(capabilityJob{name: testValueBatch, version: 1}))

	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	err = service.Run(ctx)
	require.ErrorIs(t, err, outbox.ErrEmptyReservationBatch)
	require.ErrorContains(t, err, "return ErrNoJobs when no jobs are available")
}

func TestErrNoJobsCompatibilityAlias(t *testing.T) {
	require.Same(t, sharederrors.ErrNoJobs, outbox.ErrNoJobs)
}

func TestCapabilityReservationBatchContinuesAfterOrdinaryFailure(t *testing.T) {
	repo := newCapabilityRepo()
	now := time.Now().UTC().Add(-time.Second)
	for _, payload := range []string{testValueOne, testValueFail, "three"} {
		_, err := repo.CreateJobVersioned(t.Context(), testValueBatch, 1, payload, now)
		require.NoError(t, err)
	}

	var (
		mu      sync.Mutex
		handled []string
	)
	service := newBatchService(t, repo, 3)
	require.NoError(t, service.RegisterJob(capabilityJob{
		name:    testValueBatch,
		version: 1,
		handle: func(_ context.Context, payload string) error {
			mu.Lock()
			handled = append(handled, payload)
			mu.Unlock()
			if payload == testValueFail {
				return errors.New("retry later")
			}
			return nil
		},
	}))

	runFor(t, service, 250*time.Millisecond)
	mu.Lock()
	assert.Equal(t, []string{testValueOne, testValueFail, "three"}, handled)
	mu.Unlock()
	jobs := repo.Jobs()
	require.Len(t, jobs, 1)
	assert.Equal(t, testValueFail, jobs[0].Payload)
	assert.Equal(t, 1, jobs[0].Attempts)
	assert.True(t, jobs[0].ReservedAt.Valid)
}

func TestCapabilityReservationBatchHeartbeatsWaitingTail(t *testing.T) {
	repo := newCapabilityRepo()
	now := time.Now().UTC().Add(-time.Second)
	for _, payload := range []string{testValueSlow, testValueTail} {
		_, err := repo.CreateJobVersioned(t.Context(), testValueBatch, 1, payload, now)
		require.NoError(t, err)
	}

	service := newBatchService(t, repo, 2)
	require.NoError(t, service.RegisterJob(capabilityJob{
		name:    testValueBatch,
		version: 1,
		timeout: 2 * time.Second,
		handle: func(_ context.Context, payload string) error {
			if payload == testValueSlow {
				time.Sleep(450 * time.Millisecond)
			}
			return nil
		},
	}))

	runFor(t, service, 700*time.Millisecond)
	assert.GreaterOrEqual(t, repo.ExtendCount(), 1)
	assert.Empty(t, repo.Jobs())
}

func TestReservationBatchDrainReleasesUnstartedAttempts(t *testing.T) {
	repo := newCapabilityRepo()
	now := time.Now().UTC().Add(-time.Second)
	for _, payload := range []string{testValueActive, "tail-one", "tail-two"} {
		_, err := repo.CreateJobVersioned(t.Context(), testValueBatch, 1, payload, now)
		require.NoError(t, err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	service := newBatchService(t, repo, 3)
	require.NoError(t, service.RegisterJob(capabilityJob{
		name:    testValueBatch,
		version: 1,
		timeout: 2 * time.Second,
		handle: func(_ context.Context, payload string) error {
			if payload == testValueActive {
				close(started)
				<-release
			}
			return nil
		},
	}))

	runErr := make(chan error, 1)
	go func() { runErr <- service.Run(context.Background()) }()
	<-started
	service.BeginDrain()
	close(release)
	require.NoError(t, <-runErr)

	jobs := repo.Jobs()
	require.Len(t, jobs, 2)
	for _, job := range jobs {
		assert.Zero(t, job.Attempts)
		assert.False(t, job.ReservedAt.Valid)
		assert.Equal(t, types.LeaseTokenNil, job.LeaseToken)
	}
}

func TestReservationBatchHeartbeatLossFailsClosedAndReleasesTail(t *testing.T) {
	repo := newCapabilityRepo()
	repo.loseLeaseOnExtend = true
	now := time.Now().UTC().Add(-time.Second)
	for _, payload := range []string{testValueActive, testValueTail} {
		_, err := repo.CreateJobVersioned(t.Context(), testValueBatch, 1, payload, now)
		require.NoError(t, err)
	}

	handled := make(chan string, 2)
	service := newBatchService(t, repo, 2)
	require.NoError(t, service.RegisterJob(capabilityJob{
		name:    testValueBatch,
		version: 1,
		timeout: 2 * time.Second,
		handle: func(ctx context.Context, payload string) error {
			handled <- payload
			<-ctx.Done()
			return ctx.Err()
		},
	}))

	err := service.Run(context.Background())
	require.ErrorIs(t, err, outbox.ErrLeaseLost)
	assert.Equal(t, testValueActive, <-handled)
	select {
	case payload := <-handled:
		t.Fatalf("unexpected tail handler: %s", payload)
	default:
	}

	jobs := repo.Jobs()
	require.Len(t, jobs, 2)
	assert.Equal(t, 1, jobs[0].Attempts)
	assert.True(t, jobs[0].ReservedAt.Valid)
	assert.Zero(t, jobs[1].Attempts)
	assert.False(t, jobs[1].ReservedAt.Valid)
}

func TestReservationBatchFinalizationFenceFailsClosedAndReleasesTail(t *testing.T) {
	tests := []struct {
		name      string
		handle    func() error
		configure func(*capabilityRepo)
	}{
		{
			name:   "ack",
			handle: func() error { return nil },
			configure: func(repo *capabilityRepo) {
				repo.loseLeaseOnDelete = true
			},
		},
		{
			name: testValueRetry,
			handle: func() error {
				return outbox.RetryAt(errors.New("busy"), time.Now().UTC().Add(time.Hour))
			},
			configure: func(repo *capabilityRepo) {
				repo.loseLeaseOnReschedule = true
			},
		},
		{
			name:   "dlq",
			handle: func() error { return outbox.Permanent(errors.New("invalid")) },
			configure: func(repo *capabilityRepo) {
				repo.loseLeaseOnDelete = true
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newCapabilityRepo()
			now := time.Now().UTC().Add(-time.Second)
			for _, payload := range []string{testValueActive, testValueTail} {
				_, err := repo.CreateJobVersioned(t.Context(), testValueBatch, 1, payload, now)
				require.NoError(t, err)
			}
			tt.configure(repo)

			service := newBatchService(t, repo, 2)
			require.NoError(t, service.RegisterJob(capabilityJob{
				name:    testValueBatch,
				version: 1,
				handle: func(_ context.Context, _ string) error {
					return tt.handle()
				},
			}))

			err := service.Run(context.Background())
			require.ErrorIs(t, err, outbox.ErrLeaseLost)
			jobs := repo.Jobs()
			require.Len(t, jobs, 2)
			assert.Equal(t, 1, jobs[0].Attempts)
			assert.True(t, jobs[0].ReservedAt.Valid)
			assert.Zero(t, jobs[1].Attempts)
			assert.False(t, jobs[1].ReservedAt.Valid)
		})
	}
}

func TestReservationBatchSizeTwoUsesFencedPerJobFinalization(t *testing.T) {
	repo := newCapabilityRepo()
	now := time.Now().UTC().Add(-time.Second)
	for _, payload := range []string{testValueOne, "two"} {
		_, err := repo.CreateJobVersioned(t.Context(), "versioned", 1, payload, now)
		require.NoError(t, err)
	}

	service := newBatchService(t, repo, 2)
	require.NoError(t, service.RegisterJob(capabilityJob{name: "versioned", version: 1}))
	runFor(t, service, 250*time.Millisecond)
	assert.Empty(t, repo.Jobs())
}

func TestCapabilityReservationBatchFinalizesRetryAndPermanentIndependently(t *testing.T) {
	repo := newCapabilityRepo()
	now := time.Now().UTC().Add(-time.Second)
	for _, payload := range []string{testValueRetry, testValuePermanent, "success"} {
		_, err := repo.CreateJobVersioned(t.Context(), testValueBatch, 1, payload, now)
		require.NoError(t, err)
	}

	retryAt := time.Now().UTC().Add(time.Hour)
	service := newBatchService(t, repo, 3)
	require.NoError(t, service.RegisterJob(capabilityJob{
		name:    testValueBatch,
		version: 1,
		handle: func(_ context.Context, payload string) error {
			switch payload {
			case testValueRetry:
				return outbox.RetryAt(errors.New("busy"), retryAt)
			case testValuePermanent:
				return outbox.Permanent(errors.New("bad payload"))
			default:
				return nil
			}
		},
	}))

	runFor(t, service, 250*time.Millisecond)
	jobs := repo.Jobs()
	require.Len(t, jobs, 1)
	assert.Equal(t, testValueRetry, jobs[0].Payload)
	assert.WithinDuration(t, retryAt, jobs[0].AvailableAt, time.Millisecond)
	assert.False(t, jobs[0].ReservedAt.Valid)
	failed := repo.Failed()
	require.Len(t, failed, 1)
	assert.Equal(t, testValuePermanent, failed[0].Payload)
}

func newBatchService(
	t *testing.T,
	repo *capabilityRepo,
	size int,
) *outbox.Service {
	t.Helper()

	options := []outbox.OptOptionsSetter{
		outbox.WithWorkers(1),
		outbox.WithIdleTime(100 * time.Millisecond),
		outbox.WithReserveFor(time.Second),
		outbox.WithReservationBatchSize(size),
		outbox.WithJobsRepo(repo),
		outbox.WithJobsFailedRepo(repo),
		outbox.WithTransactor(repo),
		outbox.WithLogger(logger.Discard()),
	}
	service, err := outbox.New(options...)
	require.NoError(t, err)

	return service
}

func runFor(t *testing.T, service *outbox.Service, duration time.Duration) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	require.NoError(t, service.Run(ctx))
}

type limitedBatchRepo struct {
	*capabilityRepo
	maximum int
}

func (r *limitedBatchRepo) MaxReservationBatchSize() int { return r.maximum }

var _ outbox.JobsRepository = (*limitedBatchRepo)(nil)

type emptySuccessfulBatchRepo struct {
	*capabilityRepo
}

func (*emptySuccessfulBatchRepo) FindAndReserveJobsForCapabilities(
	context.Context,
	time.Time,
	time.Time,
	outbox.LeaseToken,
	[]outbox.JobCapability,
	int,
) ([]models.Job, error) {
	return []models.Job{}, nil
}

var _ outbox.JobsRepository = (*emptySuccessfulBatchRepo)(nil)
