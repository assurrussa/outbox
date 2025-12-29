package storage_test

import (
	"context"
	"testing"

	picodatalogger "github.com/picodata/picodata-go/logger"
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

	pool, err := storage.Create(ctx, storage.NewOption(
		"postgres://admin:pass@localhost:4387?sslmode=disable",
		nil,
		logger.Discard(),
		false,
	))
	require.NoError(t, err)
	assert.NotNil(t, pool)
}
