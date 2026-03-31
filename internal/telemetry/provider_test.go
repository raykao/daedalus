package telemetry_test

import (
	"context"
	"testing"

	"github.com/raykao/agent-forge/internal/telemetry"
)

func TestNewProvider_Noop(t *testing.T) {
	ctx := context.Background()
	p, err := telemetry.NewProvider(ctx, telemetry.Config{
		ServiceName:  "test-svc",
		ExporterType: "noop",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
	tr := p.Tracer("test")
	if tr == nil {
		t.Fatal("expected non-nil tracer")
	}
	if err := p.Shutdown(ctx); err != nil {
		t.Errorf("shutdown error: %v", err)
	}
}

func TestNewProvider_EmptyExporter_DefaultsToNoop(t *testing.T) {
	ctx := context.Background()
	p, err := telemetry.NewProvider(ctx, telemetry.Config{
		ServiceName: "test-svc",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
	if err := p.Shutdown(ctx); err != nil {
		t.Errorf("shutdown error: %v", err)
	}
}

func TestNewProvider_Stdout(t *testing.T) {
	ctx := context.Background()
	p, err := telemetry.NewProvider(ctx, telemetry.Config{
		ServiceName:    "test-svc",
		ServiceVersion: "0.1.0",
		ExporterType:   "stdout",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() {
		if err := p.Shutdown(ctx); err != nil {
			t.Errorf("shutdown error: %v", err)
		}
	}()
	tr := p.Tracer("test")
	_, span := tr.Start(ctx, "test-span")
	span.End()
}

func TestNewProvider_UnknownExporter(t *testing.T) {
	ctx := context.Background()
	_, err := telemetry.NewProvider(ctx, telemetry.Config{
		ServiceName:  "test-svc",
		ExporterType: "unknown",
	})
	if err == nil {
		t.Fatal("expected error for unknown exporter type")
	}
}
