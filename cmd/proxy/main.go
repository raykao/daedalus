package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/raykao/agent-forge/internal/acp"
	"github.com/raykao/agent-forge/internal/proxy"
	"github.com/raykao/agent-forge/internal/queue"
	"github.com/raykao/agent-forge/internal/telemetry"
)

func main() {
	// Config from flags with env var fallbacks
	acpAddr := flag.String("acp-addr", envOrDefault("ACP_ADDR", "localhost:3000"), "ACP agent TCP address")
	natsURL := flag.String("nats-url", envOrDefault("NATS_URL", "nats://localhost:4222"), "NATS server URL")
	stream := flag.String("stream", envOrDefault("NATS_STREAM", "AGENT_TASKS"), "JetStream stream name")
	subject := flag.String("subject", envOrDefault("NATS_SUBJECT", "agent.tasks.>"), "NATS subscribe subject")
	workDir := flag.String("work-dir", envOrDefault("WORK_DIR", "/workspace"), "Agent session working directory")
	logLevel := flag.String("log-level", envOrDefault("LOG_LEVEL", "info"), "Log level (debug, info, warn, error)")
	gracePeriodStr := flag.String("grace-period", envOrDefault("GRACE_PERIOD", "30s"), "Graceful shutdown grace period (e.g. 30s)")
	otelExporter := flag.String("otel-exporter", envOrDefault("OTEL_EXPORTER", "noop"), "OTel exporter type (otlp, stdout, noop)")
	otelEndpoint := flag.String("otel-endpoint", envOrDefault("OTEL_ENDPOINT", ""), "OTel OTLP collector endpoint")
	flag.Parse()

	gracePeriod, err := time.ParseDuration(*gracePeriodStr)
	if err != nil {
		slog.Error("invalid grace-period", "value", *gracePeriodStr, "err", err)
		os.Exit(1)
	}

	logger := telemetry.NewLogger(parseLogLevel(*logLevel))
	slog.SetDefault(logger)

	// Initialize telemetry provider
	telProvider, err := telemetry.NewProvider(context.Background(), telemetry.Config{
		ServiceName:    "agent-forge-proxy",
		ServiceVersion: "0.1.0",
		ExporterType:   *otelExporter,
		OTLPEndpoint:   *otelEndpoint,
	})
	if err != nil {
		logger.Error("failed to create telemetry provider", "err", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telProvider.Shutdown(shutdownCtx); err != nil {
			logger.Warn("telemetry shutdown error", "err", err)
		}
	}()

	logger.Info("agent-forge proxy starting",
		"acp_addr", *acpAddr,
		"nats_url", *natsURL,
		"stream", *stream,
		"subject", *subject,
		"work_dir", *workDir,
		"grace_period", gracePeriod,
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Connect ACP client
	acpClient := acp.NewClient(*acpAddr, logger)
	connectCtx, connectCancel := context.WithTimeout(ctx, 30*time.Second)
	if err := acpClient.Connect(connectCtx); err != nil {
		connectCancel()
		logger.Error("failed to connect to ACP agent", "addr", *acpAddr, "err", err)
		os.Exit(1)
	}
	connectCancel()
	defer acpClient.Close()
	logger.Info("acp client connected", "addr", *acpAddr)

	// Initialize ACP protocol
	initCtx, initCancel := context.WithTimeout(ctx, 10*time.Second)
	if _, err := acpClient.Initialize(initCtx); err != nil {
		initCancel()
		logger.Error("acp initialize failed", "err", err)
		os.Exit(1)
	}
	initCancel()
	logger.Info("acp initialized")

	// Create NATS consumer
	consumer, err := queue.NewConsumer(ctx, queue.Config{
		URL:     *natsURL,
		Stream:  *stream,
		Subject: *subject,
		Logger:  logger,
	})
	if err != nil {
		logger.Error("failed to create NATS consumer", "err", err)
		os.Exit(1)
	}
	defer consumer.Close()
	logger.Info("nats consumer ready", "stream", *stream, "subject", *subject)

	// Create publisher (shares NATS connection)
	publisher, err := queue.NewPublisher(consumer.Conn(), logger)
	if err != nil {
		logger.Error("failed to create NATS publisher", "err", err)
		os.Exit(1)
	}

	// Create handler. Mark it as already initialized so Handle does not call
	// Initialize again on the first message - we already initialized above.
	handler := proxy.NewHandler(acpClient, publisher, *workDir, logger)
	handler.SetInitialized()

	// Create handler and shutdown manager
	sm := proxy.NewShutdownManager(handler, gracePeriod, logger)

	// Run consumer in a goroutine using the shutdown manager's work context for
	// ACP operations. This ensures in-flight Handle calls are not aborted the
	// moment SIGTERM cancels the consumer context; they get the full grace period.
	consumerErr := make(chan error, 1)
	go func() {
		consumerErr <- consumer.Run(ctx, func(consumerCtx context.Context, data []byte) error {
			return handler.Handle(sm.WorkContext(), data)
		})
	}()

	// Wait for shutdown signal.
	<-ctx.Done()
	stop()
	logger.Info("received shutdown signal, starting graceful shutdown", "grace_period", gracePeriod)

	// Orchestrate graceful shutdown. Use a background context so the shutdown
	// manager is not constrained by the already-cancelled main ctx.
	if err := sm.Shutdown(context.Background()); err != nil {
		logger.Warn("graceful shutdown incomplete", "err", err)
	}

	// Wait for the consumer goroutine to exit.
	if err := <-consumerErr; err != nil {
		logger.Error("consumer error", "err", err)
		os.Exit(1)
	}

	logger.Info("proxy shutdown complete")
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func parseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
