package telemetry

import (
	"context"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel/trace"
)

// LogHandler wraps an slog.Handler to inject trace_id and span_id from context.
type LogHandler struct {
	inner slog.Handler
}

// NewLogHandler creates a LogHandler wrapping h.
func NewLogHandler(h slog.Handler) *LogHandler {
	return &LogHandler{inner: h}
}

// Enabled reports whether the handler handles records at the given level.
func (lh *LogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return lh.inner.Enabled(ctx, level)
}

// Handle adds trace_id and span_id attributes when a valid span is in ctx.
func (lh *LogHandler) Handle(ctx context.Context, r slog.Record) error {
	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
		sc := span.SpanContext()
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return lh.inner.Handle(ctx, r)
}

// WithAttrs returns a new handler with the given attributes added.
func (lh *LogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &LogHandler{inner: lh.inner.WithAttrs(attrs)}
}

// WithGroup returns a new handler with the given group name.
func (lh *LogHandler) WithGroup(name string) slog.Handler {
	return &LogHandler{inner: lh.inner.WithGroup(name)}
}

// NewLogger creates a slog.Logger configured for structured JSON output
// with trace ID correlation.
func NewLogger(level slog.Level) *slog.Logger {
	jsonHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(NewLogHandler(jsonHandler))
}
