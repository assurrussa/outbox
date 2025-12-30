package pgsqlinit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pgsql "github.com/assurrussa/outbox/infrastructure/pgsql/storage"
	"github.com/assurrussa/outbox/infrastructure/pgsql/storage/pgsqlclient"
	"github.com/assurrussa/outbox/infrastructure/pgsql/storage/pgsqlinit"
)

func TestCreate_Success(t *testing.T) {
	ctx := context.Background()

	dsn := "postgres://jack:secret@pg.example.com:5432/mydb?sslmode=verify-ca&pool_max_conns=10"
	pool, err := pgsqlinit.Create(ctx, dsn, pgsqlclient.WithCheck(false))
	require.NoError(t, err)
	assert.NotNil(t, pool)
}

func TestCreate_Error(t *testing.T) {
	ctx := context.Background()

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
