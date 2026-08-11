package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ruvicode/gateway/internal/config"
	"github.com/ruvicode/gateway/internal/server"
	"github.com/ruvicode/gateway/internal/store"
)

func main() {
	// Load config.
	cfg, err := config.Load()
	if err != nil {
		slog.Error("Failed to load config", "error", err)
		os.Exit(1)
	}

	// Init logger.
	level := slog.LevelInfo
	if cfg.LogLevel == "debug" {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	slog.Info("Starting Ruvicode Gateway", "env", cfg.Env, "port", cfg.Port)

	// Connect to Postgres.
	pg, err := store.NewPostgresStore(cfg.DatabaseURL)
	if err != nil {
		slog.Error("Failed to connect to Postgres", "error", err)
		os.Exit(1)
	}
	defer pg.Close()

	// Connect to Redis.
	rdb, err := store.NewRedisStore(cfg.RedisURL)
	if err != nil {
		slog.Error("Failed to connect to Redis", "error", err)
		os.Exit(1)
	}
	defer func() { _ = rdb.Close() }()

	// Create server.
	srv := server.New(cfg, pg, rdb)

	// Start HTTP server.
	httpServer := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      srv.Routes(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 300 * time.Second, // Long for streaming responses.
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown.
	go func() {
		slog.Info("Server listening", "port", cfg.Port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("Server error", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("Shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		slog.Error("Forced shutdown", "error", err)
	}

	slog.Info("Server stopped")
}
