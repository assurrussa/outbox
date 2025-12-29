//go:build integration

package tests

import (
	"testing"

	"github.com/assurrussa/outbox/outbox/logger"
)

func CreateLogger(t *testing.T) *logger.SlogAdapter {
	t.Helper()

	return logger.Default()
}
