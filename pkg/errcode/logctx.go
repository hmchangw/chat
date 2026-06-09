package errcode

import (
	"context"
	"log/slog"
)

type loggerCtxKey struct{}

// WithLogger stores an explicit *slog.Logger in ctx (mainly for tests).
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerCtxKey{}, l)
}

// WithLogValues returns ctx with a logger enriched by key/value pairs. Call
// once at handler entry; Classify's log line carries them. SERVER-ONLY — never
// serialized into the client envelope.
func WithLogValues(ctx context.Context, args ...any) context.Context {
	return WithLogger(ctx, loggerFrom(ctx).With(args...))
}

// loggerFrom returns the ctx logger, or slog.Default().
func loggerFrom(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerCtxKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}
