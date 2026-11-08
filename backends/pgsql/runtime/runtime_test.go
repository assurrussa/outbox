package runtime_test

import (
	"context"
	"testing"
	"time"

	coreoutbox "github.com/assurrussa/outbox/outbox"
	"github.com/stretchr/testify/require"

	pgsqlruntime "github.com/assurrussa/outbox/backends/pgsql/runtime"
)

const testPostgresDSN = "postgres://localhost/outbox"

func TestOpenRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	policy, err := coreoutbox.NewExponentialRetryPolicy(time.Second, time.Minute, nil)
	require.NoError(t, err)
	observer := make(chan coreoutbox.RuntimeOutcome, 1)
	_, err = pgsqlruntime.Open(context.Background(), pgsqlruntime.Config{
		RetryPolicy: policy,
		Observer:    observer,
	})
	require.Error(t, err)
}

func TestOpenRejectsPartialPoolConfiguration(t *testing.T) {
	t.Parallel()

	for _, config := range []pgsqlruntime.Config{
		{DSN: testPostgresDSN, MinConnectionsCount: 1},
		{DSN: testPostgresDSN, MaxConnectionsCount: 1},
	} {
		_, err := pgsqlruntime.Open(context.Background(), config)
		require.ErrorContains(t, err, "must both be positive or both be zero")
	}
}

func TestOpenRejectsPoolMinimumAboveMaximum(t *testing.T) {
	t.Parallel()

	_, err := pgsqlruntime.Open(context.Background(), pgsqlruntime.Config{
		DSN:                 testPostgresDSN,
		MinConnectionsCount: 2,
		MaxConnectionsCount: 1,
	})
	require.ErrorContains(t, err, "must not exceed")
}

func TestNilRuntimeFailsClosed(t *testing.T) {
	t.Parallel()
	var runtime *pgsqlruntime.Runtime
	require.Error(t, runtime.DatabaseReadiness(context.Background()))
	runtime.BeginDrain()
	require.NoError(t, runtime.Close())
}
