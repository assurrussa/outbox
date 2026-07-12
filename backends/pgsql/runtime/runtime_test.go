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

func TestNilRuntimeFailsClosed(t *testing.T) {
	t.Parallel()
	var runtime *pgsqlruntime.Runtime
	require.Error(t, runtime.DatabaseReadiness(context.Background()))
	runtime.BeginDrain()
	require.NoError(t, runtime.Close())
}
