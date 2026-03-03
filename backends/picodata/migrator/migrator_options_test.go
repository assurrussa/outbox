package migrator

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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
