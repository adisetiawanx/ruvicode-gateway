package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ruvicode/gateway/internal/config"
	"github.com/ruvicode/gateway/internal/monitor"
	"github.com/ruvicode/gateway/internal/store"
	"github.com/ruvicode/gateway/internal/wallet"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config load failed", "error", err)
		os.Exit(1)
	}

	if cfg.HDWalletMnemonic == "" {
		slog.Error("HD_WALLET_MNEMONIC is required")
		os.Exit(1)
	}

	pg, err := store.NewPostgresStore(cfg.DatabaseURL)
	if err != nil {
		slog.Error("postgres connect failed", "error", err)
		os.Exit(1)
	}
	defer pg.Close()

	// Fail fast on an invalid mnemonic: the monitor itself only reads
	// addresses from the database, but a broken mnemonic means the whole
	// deposit pipeline is misconfigured.
	if _, err := wallet.NewFromMnemonic(cfg.HDWalletMnemonic); err != nil {
		slog.Error("HD wallet init failed", "error", err)
		os.Exit(1)
	}

	mon, err := monitor.New(cfg.BaseRPCURL, cfg.BaseRPCFallback, cfg.USDCContract, cfg.MinDepositUSD, pg)
	if err != nil {
		slog.Error("monitor init failed", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go mon.Run(ctx)

	slog.Info("USDC deposit monitor running", "rpc", cfg.BaseRPCURL)
	<-quit
	slog.Info("monitor shutting down...")
	cancel()
}
