package outbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/outbox/logger"
)

func TestRetryPolicySingleRuntime(t *testing.T) {
	for _, test := range []struct {
		name              string
		reserve           time.Duration
		policy, lostFence bool
		delay             time.Duration
		wantErr           error
	}{
		{name: "short lease", reserve: time.Second, policy: true, delay: time.Hour},
		{name: "long lease", reserve: 10 * time.Minute, policy: true, delay: time.Hour},
		{name: "legacy lease recovery", reserve: time.Second},
		{name: "lost fence", reserve: time.Second, policy: true, delay: time.Hour, lostFence: true, wantErr: outbox.ErrLeaseLost},
		{name: "invalid policy", reserve: time.Second, policy: true, delay: -time.Second, wantErr: outbox.ErrRetryPolicy},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newCapabilityRepo()
			repo.loseLeaseOnReschedule = test.lostFence
			var received outbox.RetryAttempt
			options := []outbox.OptOptionsSetter{
				outbox.WithWorkers(1), outbox.WithIdleTime(100 * time.Millisecond), outbox.WithReserveFor(test.reserve),
				outbox.WithJobsRepo(repo), outbox.WithJobsFailedRepo(repo), outbox.WithTransactor(repo), outbox.WithLogger(logger.Discard()),
			}
			if test.policy {
				options = append(options, outbox.WithRetryPolicy(outbox.RetryPolicyFunc(func(attempt outbox.RetryAttempt) time.Duration {
					received = attempt
					return test.delay
				})))
			}
			svc, err := outbox.New(options...)
			require.NoError(t, err)
			cause := errors.New("transient")
			svc.MustRegisterJob(capabilityJob{
				name: "policy-retry", version: 1, handle: func(context.Context, string) error { return cause },
			})
			id, err := svc.Put(t.Context(), "policy-retry", "{}", time.Now().UTC())
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), 120*time.Millisecond)
			defer cancel()
			before := time.Now().UTC()
			err = svc.Run(ctx)
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
			} else {
				require.NoError(t, err)
			}
			jobs := repo.Jobs()
			require.Len(t, jobs, 1)
			require.Equal(t, 1, jobs[0].Attempts)
			require.Empty(t, repo.Failed())
			if test.policy {
				require.Equal(t, id, received.JobID)
				require.Equal(t, 1, received.Attempt)
				require.ErrorIs(t, received.Err, cause)
			}
			if test.policy && test.wantErr == nil {
				require.False(t, jobs[0].ReservedAt.Valid)
				require.True(t, jobs[0].LeaseToken.IsZero())
				require.False(t, jobs[0].AvailableAt.Before(before.Add(test.delay)))
				require.False(t, jobs[0].AvailableAt.After(time.Now().UTC().Add(test.delay)))
			} else {
				require.True(t, jobs[0].ReservedAt.Valid)
			}
		})
	}
}

func TestRetryPolicySingleDispositionPrecedence(t *testing.T) {
	cause := errors.New("transient")
	explicit := time.Now().UTC().Add(time.Hour)
	for _, test := range []struct {
		name        string
		handlerErr  error
		maxAttempts int
		wantDLQ     bool
	}{
		{name: "explicit timestamp", handlerErr: outbox.RetryAt(cause, explicit)},
		{name: "permanent failure", handlerErr: outbox.Permanent(cause), wantDLQ: true},
		{name: "exhausted attempt", handlerErr: outbox.RetryAt(cause, explicit), maxAttempts: 1, wantDLQ: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newCapabilityRepo()
			svc, err := outbox.New(
				outbox.WithJobsRepo(repo), outbox.WithJobsFailedRepo(repo), outbox.WithTransactor(repo),
				outbox.WithIdleTime(100*time.Millisecond), outbox.WithLogger(logger.Discard()),
				outbox.WithRetryPolicy(outbox.RetryPolicyFunc(func(outbox.RetryAttempt) time.Duration { panic("policy must not run") })),
			)
			require.NoError(t, err)
			svc.MustRegisterJob(capabilityJob{
				name: "precedence", version: 1, maxAttempts: test.maxAttempts,
				handle: func(context.Context, string) error { return test.handlerErr },
			})
			_, err = svc.Put(t.Context(), "precedence", "{}", time.Now().UTC())
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), 120*time.Millisecond)
			defer cancel()
			require.NoError(t, svc.Run(ctx))
			if test.wantDLQ {
				require.Empty(t, repo.Jobs())
				require.Len(t, repo.Failed(), 1)
			} else {
				jobs := repo.Jobs()
				require.Len(t, jobs, 1)
				require.Equal(t, explicit, jobs[0].AvailableAt)
				require.False(t, jobs[0].ReservedAt.Valid)
			}
		})
	}
}

func TestRetryPolicyCancellationLeavesClaim(t *testing.T) {
	repo := newCapabilityRepo()
	svc, err := outbox.New(
		outbox.WithJobsRepo(repo), outbox.WithJobsFailedRepo(repo), outbox.WithTransactor(repo),
		outbox.WithLogger(logger.Discard()),
		outbox.WithRetryPolicy(outbox.RetryPolicyFunc(func(outbox.RetryAttempt) time.Duration { panic("policy must not run") })),
	)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	svc.MustRegisterJob(capabilityJob{name: "cancelled", version: 1, handle: func(context.Context, string) error {
		cancel()
		return errors.New("cancelled handler")
	}})
	_, err = svc.Put(t.Context(), "cancelled", "{}", time.Now().UTC())
	require.NoError(t, err)
	require.NoError(t, svc.Run(ctx))
	jobs := repo.Jobs()
	require.Len(t, jobs, 1)
	require.True(t, jobs[0].ReservedAt.Valid)
	require.Equal(t, 1, jobs[0].Attempts)
	require.Empty(t, repo.Failed())
}
