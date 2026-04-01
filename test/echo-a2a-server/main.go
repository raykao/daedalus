package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
)

func main() {
	port := flag.Int("port", 8080, "HTTP port to listen on")
	verbose := flag.Bool("verbose", false, "Enable debug logging")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	handler := newEchoHandler()
	addr := fmt.Sprintf(":%d", *port)

	slog.Info("echo A2A server starting", "port", *port)
	if err := http.ListenAndServe(addr, handler); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}
