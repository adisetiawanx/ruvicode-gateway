package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ruvicode/gateway/internal/config"
	"github.com/ruvicode/gateway/internal/pricing"
	"github.com/ruvicode/gateway/internal/provider"
	"github.com/ruvicode/gateway/internal/store"
)

// main runs the pricing worker. It polls the provider's market API on an
// interval, calculates user-facing prices with the spread formula, upserts
// them into Postgres, and refreshes the Redis cache used by the gateway hot
// path. It runs once on startup, then on the configured interval.
func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("pricing worker: config load failed", "error", err)
		os.Exit(1)
	}

	level := slog.LevelInfo
	if cfg.LogLevel == "debug" {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	pg, err := store.NewPostgresStore(cfg.DatabaseURL)
	if err != nil {
		slog.Error("pricing worker: postgres connect failed", "error", err)
		os.Exit(1)
	}
	defer pg.Close()

	rdb, err := store.NewRedisStore(cfg.RedisURL)
	if err != nil {
		slog.Error("pricing worker: redis connect failed", "error", err)
		os.Exit(1)
	}
	defer func() { _ = rdb.Close() }()

	// Register the sole MVP provider (identity masked as the generic name).
	registry := provider.NewRegistry(provider.DefaultName)
	client := provider.NewClient(cfg.ProviderBaseURL, cfg.ProviderAPIKeys)
	registry.Register(client)

	engine := pricing.New(pg, rdb, registry, cfg.PricingSpreadPP)

	interval, err := time.ParseDuration(cfg.PricingCronInterval)
	if err != nil || interval <= 0 {
		interval = 2 * time.Minute
	}

	slog.Info("starting pricing worker", "interval", interval.String(), "spread_pp", cfg.PricingSpreadPP)

	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if err := engine.SyncAllProviders(ctx); err != nil {
			slog.Error("pricing sync failed", "error", err)
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
			slog.Info("pricing worker shutting down")
			return
		}
	}
}
