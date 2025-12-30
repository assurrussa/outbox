package storage_test

import (
	"context"
	"testing"

	picodatalogger "github.com/picodata/picodata-go/logger"
	"github.com/picodata/picodata-go/strategies"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/assurrussa/outbox/infrastructure/picodata/storage"
	"github.com/assurrussa/outbox/outbox/logger"
)

func TestCreate(t *testing.T) {
	ctx := context.Background()

	adapterLog := storage.NewAdapterLog(logger.Discard())
	require.NoError(t, adapterLog.SetLevel(picodatalogger.LevelDebug))
	adapterLog.Log(picodatalogger.LevelError, "test message")

	pool, err := storage.Create(ctx,
		"postgres://admin:pass@localhost:4387?sslmode=disable",
		storage.WithDSN("postgres://admin:pass@localhost:4387?sslmode=disable"),
		storage.WithBalanceStrategy(strategies.NewRoundRobinStrategy()),
		storage.WithLogger(logger.Discard()),
		storage.WithCheckPing(false),
	)
	require.NoError(t, err)
	assert.NotNil(t, pool)
}
