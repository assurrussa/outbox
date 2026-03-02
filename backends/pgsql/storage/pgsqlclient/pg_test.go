package pgsqlclient_test

import (
	"context"
	"testing"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/backends/pgsql/storage/pgsqlclient"
)

func TestNewPool_Selects(t *testing.T) {
	ctx := context.Background()

	pool, err := pgsqlclient.NewPool(ctx, pgsqlclient.NewOptions(
		"localhost:54752",
		"test-username",
		"test-pwd",
		"test-db-name",
		pgsqlclient.WithEnvironment("local"),
		pgsqlclient.WithMinConnectionsCount(5),
		pgsqlclient.WithMaxConnectionsCount(10),
		pgsqlclient.WithMaxConnIdleTime(5*time.Minute),
		pgsqlclient.WithMaxConnLifeTime(1*time.Hour),
		pgsqlclient.WithSSLMode("disable"),
		pgsqlclient.WithDebug(false),
		pgsqlclient.WithTLSPath("", ""),
		pgsqlclient.WithTLSConfig(nil),
		pgsqlclient.WithCheck(false),
	))
	require.NoError(t, err)

	_, err = pool.DB().Query(ctx, "test", "") //nolint:sqlclosecheck // unit-check
	require.Error(t, err)

	err = pool.DB().Ping(ctx)
	require.Error(t, err)

	_, err = pool.DB().Exec(ctx, "test", "")
	require.Error(t, err)

	_, err = pool.DB().BeginTx(ctx, pgx.TxOptions{})
	require.Error(t, err)
	_, err = pool.DB().CopyFrom(ctx, "test", pgx.Identifier{}, nil, nil)
	require.Error(t, err)

	require.NotPanics(t, func() {
		pool.DB().QueryRow(ctx, "test", "")
		pool.DB().SendBatch(ctx, "test", nil)
	})

	_, err = pool.DB().Execx(ctx, "test", sq.Insert("accounts"))
	require.Error(t, err)

	var val any
	err = pool.DB().Selectx(ctx, "test", &val, sq.Insert("accounts"))
	require.Error(t, err)

	err = pool.DB().Getx(ctx, "test", &val, sq.Insert("accounts"))
	require.Error(t, err)

	err = pool.DB().ScanOne(ctx, "test", &val, "")
	require.Error(t, err)

	err = pool.DB().ScanOnex(ctx, "test", &val, sq.Insert("accounts"))
	require.Error(t, err)

	err = pool.DB().ScanAll(ctx, "test", &val, "")
	require.Error(t, err)

	err = pool.DB().ScanAllx(ctx, "test", &val, sq.Insert("accounts"))
	require.Error(t, err)
}
