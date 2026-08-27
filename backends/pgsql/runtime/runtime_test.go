package runtime_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	pgsqlruntime "github.com/assurrussa/outbox/backends/pgsql/runtime"
)

func TestOpenRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	_, err := pgsqlruntime.Open(context.Background(), pgsqlruntime.Config{})
	require.Error(t, err)
}

func TestOpenRejectsPartialPoolConfiguration(t *testing.T) {
	t.Parallel()

	for _, config := range []pgsqlruntime.Config{
		{DSN: "postgres://localhost/outbox", MinConnectionsCount: 1},
		{DSN: "postgres://localhost/outbox", MaxConnectionsCount: 1},
	} {
		_, err := pgsqlruntime.Open(context.Background(), config)
		require.ErrorContains(t, err, "must both be positive or both be zero")
	}
}

func TestOpenRejectsPoolMinimumAboveMaximum(t *testing.T) {
	t.Parallel()

	_, err := pgsqlruntime.Open(context.Background(), pgsqlruntime.Config{
		DSN:                 "postgres://localhost/outbox",
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
