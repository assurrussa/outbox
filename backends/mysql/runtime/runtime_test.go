package runtime_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	mysqlruntime "github.com/assurrussa/outbox/backends/mysql/runtime"
)

func TestOpenRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	_, err := mysqlruntime.Open(context.Background(), mysqlruntime.Config{})
	require.Error(t, err)
}

func TestNilRuntimeFailsClosed(t *testing.T) {
	t.Parallel()
	var runtime *mysqlruntime.Runtime
	require.Error(t, runtime.DatabaseReadiness(context.Background()))
	runtime.BeginDrain()
	require.NoError(t, runtime.Close())
}
