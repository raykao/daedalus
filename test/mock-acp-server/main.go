package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"time"
)

func main() {
	cfg := DefaultConfig()

	flag.IntVar(&cfg.Port, "port", cfg.Port, "TCP port to listen on")
	streamDelay := flag.Duration("streaming-delay", cfg.StreamingDelay, "Delay between streaming chunks")
	flag.BoolVar(&cfg.SendPermissions, "send-permissions", cfg.SendPermissions, "Send permission requests during session/prompt")
	flag.BoolVar(&cfg.FailOnPrompt, "fail-on-prompt", cfg.FailOnPrompt, "Return error for session/prompt")
	verbose := flag.Bool("verbose", false, "Enable debug logging")
	flag.Parse()

	cfg.StreamingDelay = *streamDelay

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	// Allow ResponseDelay to scale with StreamingDelay so tests stay snappy.
	cfg.ResponseDelay = time.Duration(float64(*streamDelay) * 5)

	srv := NewServer(cfg)
	if err := srv.Listen(); err != nil {
		slog.Error("failed to start server", "err", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	slog.Info("mock ACP server started", "port", cfg.Port)
	if err := srv.Serve(ctx); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}
