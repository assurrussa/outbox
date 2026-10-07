package outbox_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/outbox"
)

func TestExponentialRetryPolicy(t *testing.T) {
	policy, err := outbox.NewExponentialRetryPolicy(time.Second, 5*time.Second, nil)
	require.NoError(t, err)
	for _, test := range []struct {
		attempt int
		want    time.Duration
	}{
		{1, time.Second}, {2, 2 * time.Second}, {3, 4 * time.Second}, {4, 5 * time.Second}, {1000000, 5 * time.Second},
	} {
		require.Equal(t, test.want, policy.RetryDelay(outbox.RetryAttempt{Attempt: test.attempt}))
	}
	// Avoid duration overflow even when maximum is the largest duration.
	policy, err = outbox.NewExponentialRetryPolicy(time.Duration(1<<62), time.Duration(1<<63-1), nil)
	require.NoError(t, err)
	require.Equal(t, time.Duration(1<<63-1), policy.RetryDelay(outbox.RetryAttempt{Attempt: 1000000}))
}

func TestExponentialRetryPolicyJitter(t *testing.T) {
	var received time.Duration
	policy, err := outbox.NewExponentialRetryPolicy(time.Second, 5*time.Second, func(delay time.Duration) time.Duration {
		received = delay
		return delay / 2
	})
	require.NoError(t, err)
	require.Equal(t, 2500*time.Millisecond, policy.RetryDelay(outbox.RetryAttempt{Attempt: 4}))
	require.Equal(t, 5*time.Second, received)
	policy, err = outbox.NewExponentialRetryPolicy(
		time.Second, 5*time.Second, func(time.Duration) time.Duration { return time.Hour },
	)
	require.NoError(t, err)
	require.Equal(t, 5*time.Second, policy.RetryDelay(outbox.RetryAttempt{Attempt: 1}))
}

func TestExponentialRetryPolicyInvalidBounds(t *testing.T) {
	for _, bounds := range [][2]time.Duration{
		{0, time.Second}, {-time.Second, time.Second}, {time.Second, 0}, {2 * time.Second, time.Second},
	} {
		_, err := outbox.NewExponentialRetryPolicy(bounds[0], bounds[1], nil)
		require.ErrorIs(t, err, outbox.ErrRetryPolicy)
	}
}
