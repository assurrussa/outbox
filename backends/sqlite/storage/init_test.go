package storage_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/sqlite/storage"
)

func TestCreate_InvalidOptions(t *testing.T) {
	ctx := context.Background()

	_, err := storage.Create(ctx, "")
	require.Error(t, err)
}

func TestCreateAndPing(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "test.db")

	client, err := storage.Create(ctx, dsn)
	require.NoError(t, err)
	require.NotNil(t, client)
	defer func() {
		require.NoError(t, client.Close())
	}()

	_, err = client.DB().ExecContext(ctx, `CREATE TABLE IF NOT EXISTS t (id INTEGER PRIMARY KEY);`)
	require.NoError(t, err)
}
