package runtime_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	sqliteruntime "github.com/assurrussa/outbox/backends/sqlite/runtime"
	"github.com/assurrussa/outbox/backends/sqlite/storage"
)

func TestOpenRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	_, err := sqliteruntime.Open(context.Background(), sqliteruntime.Config{})
	require.Error(t, err)
}

func TestOpenSynchronousMode(t *testing.T) {
	for _, mode := range []storage.SynchronousMode{"", storage.SynchronousNormal, storage.SynchronousFull} {
		runtime, err := sqliteruntime.Open(t.Context(), sqliteruntime.Config{
			DSN: filepath.Join(t.TempDir(), "runtime.db"), SynchronousMode: mode,
		})
		require.NoError(t, err)
		var actual int
		require.NoError(t, runtime.Client().DB().QueryRowContext(t.Context(), "PRAGMA synchronous").Scan(&actual))
		want := 1
		if mode == storage.SynchronousFull {
			want = 2
		}
		require.Equal(t, want, actual)
		require.NoError(t, runtime.Close())
	}
	_, err := sqliteruntime.Open(t.Context(), sqliteruntime.Config{
		DSN: filepath.Join(t.TempDir(), "invalid.db"), SynchronousMode: "OFF",
	})
	require.ErrorContains(t, err, "synchronous mode must be NORMAL or FULL")
}

func TestNilRuntimeFailsClosed(t *testing.T) {
	t.Parallel()
	var runtime *sqliteruntime.Runtime
	require.Error(t, runtime.DatabaseReadiness(context.Background()))
	runtime.BeginDrain()
	require.NoError(t, runtime.Close())
}
