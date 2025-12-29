package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
)

type SlogAdapter struct {
	log *slog.Logger
}

var LogLevel slog.LevelVar

func init() {
	LogLevel.Set(slog.LevelInfo)
}

func WrapNamed(logger Logger, names ...string) *SlogAdapter {
	l := wrap(logger)

	name := ""
	for _, n := range names {
		name = n
		break
	}

	if name == "" {
		return l
	}

	return l.Named(name)
}

func WrapWithAttrs(logger Logger, attrs ...slog.Attr) *SlogAdapter {
	return wrap(logger).With(attrs...)
}

func Default() *SlogAdapter {
	return DefaultJSONWithWriter(os.Stdout)
}

func DefaultText() *SlogAdapter {
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: &LogLevel})
	return NewLogger(handler)
}

// Discard empty logger.
func Discard() *SlogAdapter {
	return DefaultJSONWithWriter(io.Discard)
}

func DefaultJSONWithWriter(w io.Writer) *SlogAdapter {
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: &LogLevel})
	return NewLogger(handler)
}

// Error returns an Attr for the error value.
func Error(err error) slog.Attr {
	return slog.Any("err", err)
}

// NewLogger builds a default logger from config.
func NewLogger(handler slog.Handler) *SlogAdapter {
	return &SlogAdapter{log: slog.New(handler)}
}

// Named returns a Logger that includes the given attribute named for logger in each output operation.
func (l *SlogAdapter) Named(name string) *SlogAdapter {
	if name == "" {
		return l
	}
	return &SlogAdapter{log: l.log.With(slog.String("logger", name))}
}

// With returns a Logger that includes the given attributes in each output operation.
func (l *SlogAdapter) With(attrs ...slog.Attr) *SlogAdapter {
	if len(attrs) == 0 {
		return l
	}
	args := make([]any, 0, len(attrs))
	for _, attr := range attrs {
		args = append(args, attr)
	}
	return &SlogAdapter{log: l.log.With(args...)}
}

// Handler returns l's Handler.
func (l *SlogAdapter) Handler() slog.Handler {
	return l.log.Handler()
}

// DebugContext logs at [LevelDebug] with the given context.
func (l *SlogAdapter) DebugContext(ctx context.Context, msg string, args ...any) {
	l.log.DebugContext(ctx, msg, args...)
}

// InfoContext logs at [LevelInfo] with the given context.
func (l *SlogAdapter) InfoContext(ctx context.Context, msg string, args ...any) {
	l.log.InfoContext(ctx, msg, args...)
}

// WarnContext logs at [LevelWarn] with the given context.
func (l *SlogAdapter) WarnContext(ctx context.Context, msg string, args ...any) {
	l.log.WarnContext(ctx, msg, args...)
}

// ErrorContext logs at [LevelError] with the given context.
func (l *SlogAdapter) ErrorContext(ctx context.Context, msg string, args ...any) {
	l.log.ErrorContext(ctx, msg, args...)
}

func wrap(logger Logger) *SlogAdapter {
	return NewLogger(logger.Handler())
}
