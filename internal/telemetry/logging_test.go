package telemetry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/raykao/daedalus/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestNewLogger_JSONOutput(t *testing.T) {
	logger := telemetry.NewLogger(slog.LevelInfo)
	if logger == nil {
		t.Fatal("expected non-nil logger")
	}
}

func TestLogHandler_InjectsTraceID(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	defer tp.Shutdown(context.Background())

	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()

	traceID := span.SpanContext().TraceID().String()
	spanID := span.SpanContext().SpanID().String()

	var buf bytes.Buffer
	jsonHandler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	lh := telemetry.NewLogHandler(jsonHandler)
	logger := slog.New(lh)

	logger.InfoContext(ctx, "test message")

	output := buf.String()
	if !strings.Contains(output, traceID) {
		t.Errorf("expected trace_id %s in log output: %s", traceID, output)
	}
	if !strings.Contains(output, spanID) {
		t.Errorf("expected span_id %s in log output: %s", spanID, output)
	}

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("log output is not valid JSON: %v", err)
	}
}

func TestLogHandler_NoTraceID_WithoutSpan(t *testing.T) {
	var buf bytes.Buffer
	jsonHandler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	lh := telemetry.NewLogHandler(jsonHandler)
	logger := slog.New(lh)

	logger.Info("no span")

	output := buf.String()
	if strings.Contains(output, "trace_id") {
		t.Error("expected no trace_id when no span in context")
	}
}
