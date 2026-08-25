package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/kevinletchford/open-crawl/internal/browserrun"
)

func main() {
	configPath := flag.String("config", "", "path to config.yaml (optional)")
	port := flag.Int("port", 0, "override server port")
	flag.Parse()

	cfg, err := browserrun.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}
	if *port > 0 {
		cfg.Server.Port = *port
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: levelFromString(cfg.LogLevel),
	})))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	slog.Info("initialising browser pool", "pool_size", cfg.Browser.PoolSize)

	srv, err := browserrun.New(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init error: %v\n", err)
		os.Exit(1)
	}

	if err := srv.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

func levelFromString(s string) slog.Level {
	switch s {
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
