package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/propagation"
)

// metadataCarrier adapts map[string]any to the TextMapCarrier interface.
type metadataCarrier struct {
	m map[string]any
}

func (c metadataCarrier) Get(key string) string {
	if v, ok := c.m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func (c metadataCarrier) Set(key, value string) {
	c.m[key] = value
}

func (c metadataCarrier) Keys() []string {
	keys := make([]string, 0, len(c.m))
	for k := range c.m {
		keys = append(keys, k)
	}
	return keys
}

var prop = propagation.NewCompositeTextMapPropagator(
	propagation.TraceContext{},
	propagation.Baggage{},
)

// InjectTraceContext injects W3C trace context from ctx into A2A message metadata.
// If metadata is nil, a new map is allocated and returned.
func InjectTraceContext(ctx context.Context, metadata map[string]any) map[string]any {
	if metadata == nil {
		metadata = make(map[string]any)
	}
	prop.Inject(ctx, metadataCarrier{m: metadata})
	return metadata
}

// ExtractTraceContext extracts W3C trace context from A2A message metadata into ctx.
func ExtractTraceContext(ctx context.Context, metadata map[string]any) context.Context {
	if metadata == nil {
		return ctx
	}
	return prop.Extract(ctx, metadataCarrier{m: metadata})
}
