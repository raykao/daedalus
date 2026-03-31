package telemetry_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/raykao/agent-forge/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestInjectNATSHeaders_NilHeaders(t *testing.T) {
	ctx := context.Background()
	h := telemetry.InjectNATSHeaders(ctx, nil)
	if h == nil {
		t.Fatal("expected non-nil headers")
	}
}

func TestInjectNATSHeaders_WithSpan(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	defer tp.Shutdown(context.Background())

	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()

	h := telemetry.InjectNATSHeaders(ctx, make(nats.Header))
	v := h.Get("traceparent")
	if v == "" {
		t.Fatal("expected traceparent header")
	}
	if !strings.HasPrefix(v, "00-") {
		t.Errorf("unexpected traceparent value: %s", v)
	}
}

func TestExtractNATSHeaders_NilHeaders(t *testing.T) {
	ctx := telemetry.ExtractNATSHeaders(context.Background(), nil)
	if ctx == nil {
		t.Fatal("expected non-nil context")
	}
}

func TestInjectExtractNATSHeaders_RoundTrip(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	defer tp.Shutdown(context.Background())

	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()

	traceID := span.SpanContext().TraceID().String()

	h := telemetry.InjectNATSHeaders(ctx, make(nats.Header))
	recovered := telemetry.ExtractNATSHeaders(context.Background(), h)

	// Verify by re-injecting
	h2 := telemetry.InjectNATSHeaders(recovered, make(nats.Header))
	tp2 := h2.Get("traceparent")
	if tp2 == "" {
		t.Fatal("expected traceparent after round-trip")
	}
	if !strings.Contains(tp2, traceID) {
		t.Errorf("round-trip trace ID mismatch: got %s, want contains %s", tp2, traceID)
	}
}
