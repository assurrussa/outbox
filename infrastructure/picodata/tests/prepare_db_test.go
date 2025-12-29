//go:build integration

package tests_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/infrastructure/picodata/tests"
	"github.com/assurrussa/outbox/shared/tools"
)

func TestInitDB(t *testing.T) {
	ctx := context.Background()
	dbTestPath := tools.FindFileDir("testdata", tools.CallerCurrentFile())
	pgsql, db, cleanUp := tests.PrepareDB(
		ctx,
		t,
		"tests-dbname",
		tests.WithDatabasePathFilesMigration(dbTestPath),
		tests.WithDatabaseFixedName(false),
		tests.WithDatabaseVerbose(true),
		tests.WithDatabaseLog(t.Logf),
		tests.WithDatabaseFnReplaceTableNameGetter(nil),
		tests.WithDatabaseTableReplaces(map[string]tests.ReplaceTableName{
			"tests-dbname": {Name: "tests-dbname-new", Replace: true},
		}),
	)
	require.NotNil(t, pgsql)
	require.NotNil(t, db)
	require.NotNil(t, cleanUp)
	assert.NotPanics(t, func() {
		cleanUp(ctx)
	})
}
