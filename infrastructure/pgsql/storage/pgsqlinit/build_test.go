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

func TestNewPool_Success(t *testing.T) {
	ctx := context.Background()

	pool, err := pgsqlinit.NewPool(ctx, pgsql.PSQLConfig{
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

	pool, err := pgsqlinit.NewPool(ctx, pgsql.PSQLConfig{
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
