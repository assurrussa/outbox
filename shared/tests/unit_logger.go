package tests

import (
	"bytes"
	"log/slog"
	"sync"

	"github.com/assurrussa/outbox/outbox/logger"
)

// SafeBuffer is a concurrency-safe buffer suitable for use as a slog writer in tests.
// It guards all reads and writes with a mutex to avoid data races under -race.
type SafeBuffer struct {
	mu  sync.RWMutex
	buf bytes.Buffer
}

func (b *SafeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *SafeBuffer) String() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.buf.String()
}

// CreateUnitLogger returns a test logger and a concurrency-safe buffer capturing JSON logs.
func CreateUnitLogger(levels ...slog.Level) (logger.Logger, *SafeBuffer) {
	bf := &SafeBuffer{}

	lvl := slog.LevelError
	for _, l := range levels {
		lvl = l
		break
	}

	logger.LogLevel.Set(lvl)
	return logger.DefaultJSONWithWriter(bf), bf
}
