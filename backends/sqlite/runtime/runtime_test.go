package runtime_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	sqliteruntime "github.com/assurrussa/outbox/backends/sqlite/runtime"
)

func TestOpenRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	_, err := sqliteruntime.Open(context.Background(), sqliteruntime.Config{})
	require.Error(t, err)
}

func TestNilRuntimeFailsClosed(t *testing.T) {
	t.Parallel()
	var runtime *sqliteruntime.Runtime
	require.Error(t, runtime.DatabaseReadiness(context.Background()))
	runtime.BeginDrain()
	require.NoError(t, runtime.Close())
}
