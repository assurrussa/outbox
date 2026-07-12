package pgsqlinit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pgsql "github.com/assurrussa/outbox/backends/pgsql/storage"
	"github.com/assurrussa/outbox/backends/pgsql/storage/pgsqlclient"
	"github.com/assurrussa/outbox/backends/pgsql/storage/pgsqlinit"
)

func TestCreate_Success(t *testing.T) {
	ctx := context.Background()

	//nolint:gosec // test-only credential
	dsn := "postgres://jack:secret@pg.example.com:5432/mydb?sslmode=verify-ca&pool_max_conns=10"
	pool, err := pgsqlinit.Create(ctx, dsn, pgsqlclient.WithCheck(false))
	require.NoError(t, err)
	assert.NotNil(t, pool)
}

func TestCreate_PreservesRuntimeParameters(t *testing.T) {
	t.Parallel()

	//nolint:gosec // test-only credential
	dsn := "postgres://jack:secret@pg.example.com:5432/mydb?sslmode=disable&search_path=cms_runtime&application_name=outbox"
	pool, err := pgsqlinit.Create(
		context.Background(),
		dsn,
		pgsqlclient.WithCheck(false),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	params := pool.Pool().Config().ConnConfig.RuntimeParams
	require.Equal(t, "cms_runtime", params["search_path"])
	require.Equal(t, "outbox", params["application_name"])
}

func TestCreate_Error(t *testing.T) {
	ctx := context.Background()

	//nolint:gosec // test-only credential
	dsn := "postgres://jack:secret@pg.example.com:5432/mydb?sslmode=verify-ca&pool_max_conns=10"
	pool, err := pgsqlinit.Create(ctx, dsn)
	require.Error(t, err)
	assert.Nil(t, pool)
}

func TestNewPool_Success(t *testing.T) {
	ctx := context.Background()

	pool, err := pgsqlinit.CreateWithConfig(ctx, pgsql.PSQLConfig{
		Address:             "localhost:54752",
		Username:            "test-username",
		Password:            "test-pwd",
		Database:            "test-db-name",
		SSLMode:             "disable",
		DebugMode:           false,
		MinConnectionsCount: 5,
		MaxConnectionsCount: 10,
		MaxConnIdleTime:     5 * time.Minute,
		MaxConnLifeTime:     1 * time.Hour,
	}, pgsqlclient.WithCheck(false))
	require.NoError(t, err)
	assert.NotNil(t, pool)
}

func TestNewPool_Error(t *testing.T) {
	ctx := context.Background()

	pool, err := pgsqlinit.CreateWithConfig(ctx, pgsql.PSQLConfig{
		Address:             "localhost:54752",
		Username:            "test-username",
		Password:            "test-pwd",
		Database:            "test-db-name",
		SSLMode:             "disable",
		DebugMode:           false,
		MinConnectionsCount: 5,
		MaxConnectionsCount: 10,
		MaxConnIdleTime:     5 * time.Minute,
		MaxConnLifeTime:     1 * time.Hour,
	})
	require.Error(t, err)
	assert.Nil(t, pool)
}
