package telemetry_test

import (
	"context"
	"strings"
	"testing"

	"github.com/raykao/daedalus/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestInjectTraceContext_NilMetadata(t *testing.T) {
	ctx := context.Background()
	m := telemetry.InjectTraceContext(ctx, nil)
	if m == nil {
		t.Fatal("expected non-nil map")
	}
}

func TestInjectTraceContext_WithSpan(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	defer tp.Shutdown(context.Background())

	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()

	m := telemetry.InjectTraceContext(ctx, nil)
	v, ok := m["traceparent"]
	if !ok {
		t.Fatal("expected traceparent in metadata")
	}
	s, ok := v.(string)
	if !ok || !strings.HasPrefix(s, "00-") {
		t.Errorf("unexpected traceparent value: %v", v)
	}
}

func TestExtractTraceContext_NilMetadata(t *testing.T) {
	ctx := telemetry.ExtractTraceContext(context.Background(), nil)
	if ctx == nil {
		t.Fatal("expected non-nil context")
	}
}

func TestInjectExtractTraceContext_RoundTrip(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	defer tp.Shutdown(context.Background())

	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()

	traceID := span.SpanContext().TraceID().String()

	m := telemetry.InjectTraceContext(ctx, nil)
	recovered := telemetry.ExtractTraceContext(context.Background(), m)

	// Re-inject from recovered context to verify trace ID preserved
	m2 := telemetry.InjectTraceContext(recovered, nil)
	tp2, ok := m2["traceparent"].(string)
	if !ok {
		t.Fatal("expected traceparent after round-trip")
	}
	if !strings.Contains(tp2, traceID) {
		t.Errorf("round-trip trace ID mismatch: got %s, want contains %s", tp2, traceID)
	}
}

func TestExtractTraceContext_Invalid(t *testing.T) {
	m := map[string]any{"traceparent": "invalid"}
	ctx := telemetry.ExtractTraceContext(context.Background(), m)
	sc := trace.SpanFromContext(ctx).SpanContext()
	if sc.IsValid() {
		t.Error("expected invalid span context from bad traceparent")
	}
}
