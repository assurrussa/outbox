//go:build integration

package pgsqlclient_test

import (
	"context"
	"testing"

	sq "github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	pgsqlstorage "github.com/assurrussa/outbox/backends/pgsql/storage"
	pgsqltests "github.com/assurrussa/outbox/backends/pgsql/tests"
)

type tableRow struct {
	TableName string `db:"tablename"`
}

func TestSelectx_WithTx_ReturnsMultipleRows(t *testing.T) {
	ctx := context.Background()

	db, _, cleanUp := pgsqltests.PrepareDB(ctx, t, "TestSelectxWithTx")
	defer cleanUp(ctx)

	tx, err := db.DB().BeginTx(ctx, pgx.TxOptions{})
	require.NoError(t, err)
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	ctxWithTx := pgsqlstorage.WithTx(ctx, tx)

	var rows []tableRow
	err = db.DB().Selectx(
		ctxWithTx,
		"selectx_with_tx",
		&rows,
		sq.Select("tablename").From("pg_catalog.pg_tables").OrderBy("tablename").Limit(2),
	)
	require.NoError(t, err)
	require.Len(t, rows, 2)
}
