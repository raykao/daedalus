package telemetry

import (
	"context"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel/propagation"
)

// natsHeaderCarrier adapts nats.Header to the TextMapCarrier interface.
type natsHeaderCarrier struct {
	h nats.Header
}

func (c natsHeaderCarrier) Get(key string) string {
	return c.h.Get(key)
}

func (c natsHeaderCarrier) Set(key, value string) {
	c.h.Set(key, value)
}

func (c natsHeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(c.h))
	for k := range c.h {
		keys = append(keys, k)
	}
	return keys
}

var natsProp = propagation.NewCompositeTextMapPropagator(
	propagation.TraceContext{},
	propagation.Baggage{},
)

// InjectNATSHeaders injects W3C trace context from ctx into NATS message headers.
// If headers is nil, a new nats.Header is allocated.
func InjectNATSHeaders(ctx context.Context, headers nats.Header) nats.Header {
	if headers == nil {
		headers = make(nats.Header)
	}
	natsProp.Inject(ctx, natsHeaderCarrier{h: headers})
	return headers
}

// ExtractNATSHeaders extracts W3C trace context from NATS message headers into ctx.
func ExtractNATSHeaders(ctx context.Context, headers nats.Header) context.Context {
	if headers == nil {
		return ctx
	}
	return natsProp.Extract(ctx, natsHeaderCarrier{h: headers})
}
