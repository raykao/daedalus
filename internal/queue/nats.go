package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/raykao/daedalus/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// tracerName is the instrumentation library name for queue spans.
const tracerName = "github.com/raykao/daedalus/internal/queue"

const (
	defaultAckWait      = 30 * time.Second
	defaultMaxDeliver   = 3
	defaultFetchTimeout = 5 * time.Second
)

// MessageHandler is a function that processes a dequeued message
// Returns an error to nack/terminate; nil to ack
type MessageHandler func(ctx context.Context, data []byte) error

// Consumer manages a NATS JetStream pull consumer
type Consumer struct {
	nc       *nats.Conn
	js       jetstream.JetStream
	stream   string
	subject  string
	consumer jetstream.Consumer
	logger   *slog.Logger
}

// Publisher sends results back onto NATS subjects
type Publisher struct {
	js     jetstream.JetStream
	logger *slog.Logger
}

// Config holds NATS consumer configuration
type Config struct {
	URL     string
	Stream  string
	Subject string
	Logger  *slog.Logger
}

// NewConsumer creates and returns a NATS JetStream consumer
func NewConsumer(ctx context.Context, cfg Config) (*Consumer, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	nc, err := nats.Connect(cfg.URL,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			cfg.Logger.Warn("nats: disconnected", "err", err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			cfg.Logger.Info("nats: reconnected", "url", nc.ConnectedUrl())
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("nats: connect %s: %w", cfg.URL, err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("nats: jetstream: %w", err)
	}

	// Ensure stream exists (create if not present)
	stream, err := js.Stream(ctx, cfg.Stream)
	if err != nil {
		cfg.Logger.Warn("nats: stream not found, creating", "stream", cfg.Stream, "subject", cfg.Subject)
		stream, err = js.CreateStream(ctx, jetstream.StreamConfig{
			Name:     cfg.Stream,
			Subjects: []string{cfg.Subject},
		})
		if err != nil {
			nc.Close()
			return nil, fmt.Errorf("nats: create stream %s: %w", cfg.Stream, err)
		}
	}
	_ = stream

	// Create or update the durable consumer
	consumerName := "proxy-consumer"
	consumer, err := js.CreateOrUpdateConsumer(ctx, cfg.Stream, jetstream.ConsumerConfig{
		Name:          consumerName,
		Durable:       consumerName,
		FilterSubject: cfg.Subject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       defaultAckWait,
		MaxDeliver:    defaultMaxDeliver,
	})
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("nats: create consumer: %w", err)
	}

	return &Consumer{
		nc:       nc,
		js:       js,
		stream:   cfg.Stream,
		subject:  cfg.Subject,
		consumer: consumer,
		logger:   cfg.Logger,
	}, nil
}

// NewPublisher creates a publisher using an existing NATS connection
func NewPublisher(nc *nats.Conn, logger *slog.Logger) (*Publisher, error) {
	if logger == nil {
		logger = slog.Default()
	}
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("nats: jetstream publisher: %w", err)
	}
	return &Publisher{js: js, logger: logger}, nil
}

// Conn returns the underlying NATS connection (for sharing with Publisher)
func (c *Consumer) Conn() *nats.Conn {
	return c.nc
}

// Run starts the fetch loop, calling handler for each message
// It runs until ctx is cancelled
func (c *Consumer) Run(ctx context.Context, handler MessageHandler) error {
	c.logger.Info("nats: consumer started", "stream", c.stream, "subject", c.subject)

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("nats: consumer stopping")
			return nil
		default:
		}

		msgs, err := c.consumer.Fetch(1, jetstream.FetchMaxWait(defaultFetchTimeout))
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			c.logger.Debug("nats: fetch timeout or error", "err", err)
			continue
		}

		for msg := range msgs.Messages() {
			c.processMessage(ctx, msg, handler)
		}

		if err := msgs.Error(); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			c.logger.Warn("nats: fetch error", "err", err)
		}
	}
}

func (c *Consumer) processMessage(ctx context.Context, msg jetstream.Msg, handler MessageHandler) {
	meta, _ := msg.Metadata()

	// Extract trace context from NATS message headers (W3C traceparent).
	// If the publisher injected headers, the consume span becomes a child
	// of the publish span via OTel's TraceContext propagator. If not, this
	// is a no-op and the consume span starts a fresh trace.
	consumeCtx := telemetry.ExtractNATSHeaders(ctx, msg.Headers())
	tracer := otel.Tracer(tracerName)
	consumeCtx, span := tracer.Start(consumeCtx, "nats.consume",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "nats"),
			attribute.String("messaging.destination.name", msg.Subject()),
			attribute.String("messaging.operation", "receive"),
			attribute.Int64("messaging.message.body.size", int64(len(msg.Data()))),
			attribute.String("messaging.nats.stream", c.stream),
		),
	)
	defer span.End()

	c.logger.Info("nats: processing message",
		"subject", msg.Subject(),
		"numDelivered", meta.NumDelivered,
		"sequence", meta.Sequence.Stream,
	)

	err := handler(consumeCtx, msg.Data())
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		c.logger.Error("nats: handler error", "err", err, "subject", msg.Subject())
		// On permanent failure (max deliveries reached), terminate
		if meta.NumDelivered >= uint64(defaultMaxDeliver) {
			c.logger.Warn("nats: max deliveries reached, terminating message")
			if termErr := msg.Term(); termErr != nil {
				c.logger.Error("nats: term failed", "err", termErr)
			}
		} else {
			if nackErr := msg.Nak(); nackErr != nil {
				c.logger.Error("nats: nack failed", "err", nackErr)
			}
		}
		return
	}

	if ackErr := msg.Ack(); ackErr != nil {
		span.RecordError(ackErr)
		c.logger.Error("nats: ack failed", "err", ackErr)
	}
}

// Close shuts down the NATS connection
func (c *Consumer) Close() {
	c.nc.Drain()
}

// PublishJSON serializes v and publishes it to subject. Trace context from
// ctx is injected into NATS message headers as W3C traceparent so consumers
// can stitch their spans into the same trace. A "nats.publish" producer span
// is emitted around the publish call.
func (p *Publisher) PublishJSON(ctx context.Context, subject string, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	tracer := otel.Tracer(tracerName)
	ctx, span := tracer.Start(ctx, "nats.publish",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "nats"),
			attribute.String("messaging.destination.name", subject),
			attribute.String("messaging.operation", "publish"),
			attribute.Int64("messaging.message.body.size", int64(len(data))),
		),
	)
	defer span.End()

	msg := &nats.Msg{
		Subject: subject,
		Data:    data,
		Header:  telemetry.InjectNATSHeaders(ctx, nats.Header{}),
	}

	if _, err := p.js.PublishMsg(ctx, msg); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("nats: publish to %s: %w", subject, err)
	}
	p.logger.Debug("nats: published", "subject", subject, "bytes", len(data))
	return nil
}
