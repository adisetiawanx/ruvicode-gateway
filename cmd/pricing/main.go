package main

import (
	"log/slog"
	"os"

	"github.com/ruvicode/gateway/internal/config"
)

// main is a placeholder entrypoint for the pricing cron worker.
// The full implementation lands in ADR-020 (Pricing Engine).
func main() {
	if _, err := config.Load(); err != nil {
		slog.Error("pricing worker: config load failed", "error", err)
		os.Exit(1)
	}
	slog.Info("pricing worker placeholder — implemented in ADR-020")
	os.Exit(0)
}
