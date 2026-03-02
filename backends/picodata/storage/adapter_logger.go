package storage

import (
	"context"
	"fmt"

	picodatalogger "github.com/picodata/picodata-go/logger"

	"github.com/assurrussa/outbox/outbox/logger"
)

var _ picodatalogger.Logger = (*AdapterLog)(nil)

type AdapterLog struct {
	log   logger.Logger
	level picodatalogger.LogLevel
}

func NewAdapterLog(log logger.Logger) *AdapterLog {
	if log == nil {
		log = logger.Default()
	}
	return &AdapterLog{
		log:   log,
		level: picodatalogger.LevelWarn,
	}
}

func (a AdapterLog) Log(level picodatalogger.LogLevel, msg string, fields ...any) {
	if level > a.level {
		return
	}

	switch level {
	case picodatalogger.LevelDebug:
		a.log.DebugContext(context.Background(), fmt.Sprintf(msg, fields...))
	case picodatalogger.LevelInfo:
		a.log.InfoContext(context.Background(), fmt.Sprintf(msg, fields...))
	case picodatalogger.LevelWarn:
		a.log.WarnContext(context.Background(), fmt.Sprintf(msg, fields...))
	case picodatalogger.LevelError:
		a.log.ErrorContext(context.Background(), fmt.Sprintf(msg, fields...))
	case picodatalogger.LevelNone:
	}
}

func (a AdapterLog) SetLevel(level picodatalogger.LogLevel) error {
	if _, err := level.String(); err != nil {
		return err
	}
	a.level = level //nolint:govet,staticcheck // it's normal
	return nil
}
