package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ruvicode/gateway/internal/billing"
	"github.com/ruvicode/gateway/internal/config"
	"github.com/ruvicode/gateway/internal/store"
)

// main runs the reconciliation worker (billing safety net). It sums Ruvicode's
// usage records against the cost the provider reported each interval and logs
// the gross margin, flagging any window where the margin is negative or far
// below the expected spread.
func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("reconcile worker: config load failed", "error", err)
		os.Exit(1)
	}

	level := slog.LevelInfo
	if cfg.LogLevel == "debug" {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	pg, err := store.NewPostgresStore(cfg.DatabaseURL)
	if err != nil {
		slog.Error("reconcile worker: postgres connect failed", "error", err)
		os.Exit(1)
	}
	defer pg.Close()

	rdb, err := store.NewRedisStore(cfg.RedisURL)
	if err != nil {
		slog.Error("reconcile worker: redis connect failed", "error", err)
		os.Exit(1)
	}
	defer func() { _ = rdb.Close() }()

	engine := billing.New(pg, rdb)

	interval, err := time.ParseDuration(cfg.ReconcileCronInterval)
	if err != nil || interval <= 0 {
		interval = time.Hour
	}

	slog.Info("starting reconcile worker", "interval", interval.String())

	// Run once immediately, then on the ticker.
	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if _, err := engine.Reconcile(ctx); err != nil {
			slog.Error("reconcile run failed", "error", err)
		}
	}
	run()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case <-ticker.C:
			run()
		case <-quit:
			slog.Info("reconcile worker shutting down")
			return
		}
	}
}
