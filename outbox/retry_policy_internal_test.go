package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/outbox/logger"
	"github.com/assurrussa/outbox/outbox/models"
	"github.com/assurrussa/outbox/shared/types"
)

func TestRetryPolicyCompletionClockAndPrecedence(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.FixedZone("offset", 3600))
	cause := errors.New("transient")
	job := models.Job{ID: NewJobID(), Name: "email", SchemaVersion: 2, Attempts: 3}
	for _, lease := range []time.Duration{time.Second, 10 * time.Minute} {
		var received RetryAttempt
		service := &Service{Options: Options{reserveFor: lease, retryPolicy: RetryPolicyFunc(func(attempt RetryAttempt) time.Duration {
			received = attempt
			return 7 * time.Second
		})}}
		at, err := service.retryTime(job, cause, now, time.Hour)
		require.NoError(t, err)
		require.Equal(t, now.UTC().Add(7*time.Second), at)
		require.Equal(t, RetryAttempt{
			JobID: job.ID, Capability: JobCapability{Name: "email", SchemaVersion: 2}, Attempt: 3, Err: cause,
		}, received)
		// Explicit scheduling does not invoke the policy, even if it would fail.
		service.retryPolicy = RetryPolicyFunc(func(RetryAttempt) time.Duration { panic("must not run") })
		at, err = service.retryTime(job, RetryAt(cause, now.Add(time.Hour)), now, 0)
		require.NoError(t, err)
		require.Equal(t, now.UTC().Add(time.Hour), at)
		at, err = service.retryTime(job, RetryAt(cause, now.Add(-time.Hour)), now, 0)
		require.NoError(t, err)
		require.Equal(t, now.UTC(), at)
	}
	service := &Service{}
	at, err := service.retryTime(job, cause, now, batchAttemptBackoff(job.Attempts))
	require.NoError(t, err)
	require.Equal(t, now.UTC().Add(400*time.Millisecond), at)
}

func TestRetryPolicyInvalidCallbacksFailClosed(t *testing.T) {
	for _, policy := range []RetryPolicy{
		RetryPolicyFunc(func(RetryAttempt) time.Duration { return -time.Second }),
		RetryPolicyFunc(func(RetryAttempt) time.Duration { panic("broken policy") }),
		RetryPolicyFunc(nil),
	} {
		_, err := retryPolicyDelay(policy, RetryAttempt{Attempt: 1})
		require.ErrorIs(t, err, ErrRetryPolicy)
	}
	delay, err := retryPolicyDelay(RetryPolicyFunc(func(RetryAttempt) time.Duration { return 0 }), RetryAttempt{Attempt: 1})
	require.NoError(t, err)
	require.Zero(t, delay)
}

func TestRetryPolicyExecutionBatchOutcomes(t *testing.T) {
	cause := errors.New("transient")
	explicit := time.Now().UTC().Add(2 * time.Hour)
	for _, test := range []struct {
		name            string
		itemErr, topErr error
		attempts        int
		policy          bool
		delay           time.Duration
		kind            BatchJobOutcomeKind
		wantCalls       int
		wantErr         error
	}{
		{
			name: "configured item", itemErr: cause, attempts: 1, policy: true, delay: time.Hour,
			kind: BatchJobOutcomeRetry, wantCalls: 1,
		},
		{name: "legacy item", itemErr: cause, attempts: 1, kind: BatchJobOutcomeRetry},
		{name: "explicit retry", itemErr: RetryAt(cause, explicit), attempts: 1, policy: true, kind: BatchJobOutcomeRetry},
		{name: "top-level defer", topErr: cause, attempts: 1, policy: true, kind: BatchJobOutcomeDefer},
		{name: "permanent", itemErr: Permanent(cause), attempts: 1, policy: true, kind: BatchJobOutcomeDLQ},
		{name: "exhausted", itemErr: cause, attempts: 3, policy: true, kind: BatchJobOutcomeDLQ},
		{name: "defer", itemErr: DeferAt(cause, explicit), attempts: 1, policy: true, kind: BatchJobOutcomeDefer},
		{name: "successful item", attempts: 1, policy: true, kind: BatchJobOutcomeSuccess},
		{name: "invalid delay", itemErr: cause, attempts: 1, policy: true, delay: -time.Second, wantCalls: 1, wantErr: ErrRetryPolicy},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &executionBatchTestRepo{}
			job := executionBatchTestJob(testBatchJobName, types.NewLeaseToken())
			job.Attempts = test.attempts
			repo.findBatch = func(_ context.Context, _ JobCapability, token LeaseToken, _ int) ([]models.Job, error) {
				job.LeaseToken = token
				return []models.Job{job}, nil
			}
			service := newExecutionBatchTestService(repo, &executionBatchTestFailedRepo{}, &executionBatchTestTransactor{})
			calls := 0
			if test.policy {
				service.retryPolicy = RetryPolicyFunc(func(attempt RetryAttempt) time.Duration {
					calls++
					require.Equal(t, job.ID, attempt.JobID)
					require.Equal(t, job.Attempts, attempt.Attempt)
					return test.delay
				})
			}
			handler := &executionBatchTestHandler{
				name: testBatchJobName,
				handle: func(_ context.Context, items []BatchJobItem) (BatchResult, error) {
					if test.topErr != nil {
						return BatchResult{}, test.topErr
					}
					return BatchResult{Items: []BatchItemResult{{JobID: items[0].JobID, Err: test.itemErr}}}, nil
				},
			}
			service.MustRegisterBatchJob(handler, BatchConfig{MaxMessages: 1})
			before := time.Now().UTC()
			processed, err := service.findAndProcessExecutionBatch(
				t.Context(), logger.Discard(), JobCapability{Name: testBatchJobName, SchemaVersion: 1},
			)
			require.True(t, processed)
			require.Equal(t, test.wantCalls, calls)
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
				require.Zero(t, repo.applyCalls.Load())
				return
			}
			require.NoError(t, err)
			require.Len(t, repo.outcomes, 1)
			outcome := repo.outcomes[0][0]
			require.Equal(t, test.kind, outcome.Kind)
			switch test.name {
			case "configured item":
				require.False(t, outcome.AvailableAt.Before(before.Add(time.Hour)))
				require.False(t, outcome.AvailableAt.After(time.Now().UTC().Add(time.Hour)))
			case "explicit retry", "defer":
				require.Equal(t, explicit, outcome.AvailableAt)
			case "legacy item", "top-level defer":
				require.False(t, outcome.AvailableAt.Before(before.Add(batchRetryBase)))
				require.False(t, outcome.AvailableAt.After(time.Now().UTC().Add(batchRetryBase)))
			}
		})
	}
}
