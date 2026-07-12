package migrator

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

const raftLogCompactedError = "ERROR: RaftLogCompacted: another DDL is in progress"

func TestNewDefaultOptions_UsesDefaultMigrationsTable(t *testing.T) {
	t.Parallel()

	options := newDefaultOptions("migrations")

	require.Equal(t, "status", options.command)
	require.Equal(t, "migrations", options.directory)
	require.Equal(t, defaultMigrationsTableName, options.tableName)
	require.Equal(t, 1, options.steps)
	require.Nil(t, options.args)
	require.Nil(t, options.databaseTableReplacesList)
}

func TestSanitizeTableName_EmptyUsesDefault(t *testing.T) {
	t.Parallel()

	tableName, err := sanitizeTableName("")
	require.NoError(t, err)
	require.Equal(t, defaultMigrationsTableName, tableName)
}

func TestExecMigrationStatement_RetriesIdempotentDDLAfterRaftCompaction(t *testing.T) {
	t.Parallel()

	attempts := 0
	err := execMigrationStatement(context.Background(), "DROP TABLE IF EXISTS jobs;", func() error {
		attempts++
		if attempts == 1 {
			return errors.New(raftLogCompactedError)
		}

		return nil
	})

	require.NoError(t, err)
	require.Equal(t, 2, attempts)
}

func TestExecMigrationStatement_DoesNotRetryAmbiguousDDL(t *testing.T) {
	t.Parallel()

	attempts := 0
	wantErr := errors.New(raftLogCompactedError)
	err := execMigrationStatement(context.Background(), "ALTER TABLE jobs ADD COLUMN lease UUID;", func() error {
		attempts++

		return wantErr
	})

	require.ErrorIs(t, err, wantErr)
	require.Equal(t, 1, attempts)
}
