// Command sweep consolidates USDC from user deposit addresses into the
// treasury (ADR-027 §9). Operator tool, run manually:
//
//	sweep                — dry-run: show what would be swept
//	sweep --execute      — actually send the transfers
//
// Gas note: each deposit address must hold a little ETH to pay for its
// transfer (~$0.01). The dry-run flags addresses that need funding.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/ruvicode/gateway/internal/config"
	"github.com/ruvicode/gateway/internal/store"
	"github.com/ruvicode/gateway/internal/sweep"
	"github.com/ruvicode/gateway/internal/wallet"
)

func main() {
	execute := flag.Bool("execute", false, "send the transfers (default is dry-run)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Println("config load failed:", err)
		os.Exit(1)
	}

	if cfg.HDWalletMnemonic == "" {
		fmt.Println("HD_WALLET_MNEMONIC is required")
		os.Exit(1)
	}

	// Treasury default: derive from the mnemonic at the reserved index
	// 0x7FFFFFFF (max BIP-44 index — never collides with user addresses).
	treasury := cfg.TreasuryAddress
	hd, err := wallet.NewFromMnemonic(cfg.HDWalletMnemonic)
	if err != nil {
		fmt.Println("invalid mnemonic:", err)
		os.Exit(1)
	}
	if treasury == "" {
		addr, _, err := hd.DeriveAddress(0x7FFFFFFF)
		if err != nil {
			fmt.Println("derive treasury failed:", err)
			os.Exit(1)
		}
		treasury = addr.Hex()
		fmt.Println("treasury (derived from mnemonic at reserved index):", treasury)
	}

	pg, err := store.NewPostgresStore(cfg.DatabaseURL)
	if err != nil {
		fmt.Println("postgres connect failed:", err)
		os.Exit(1)
	}
	defer pg.Close()

	runner, err := sweep.New(cfg.BaseRPCURL, cfg.USDCContract, treasury, cfg.SweepMinUSD, 8453, pg, hd)
	if err != nil {
		fmt.Println("sweep init failed:", err)
		os.Exit(1)
	}

	mode := "DRY-RUN (no transactions sent)"
	if *execute {
		mode = "EXECUTE — real transfers will be sent"
	}
	fmt.Println("=== USDC sweep ===")
	fmt.Println("mode:", mode)
	fmt.Println("treasury:", treasury)
	fmt.Println()

	results, err := runner.Run(context.Background(), !*execute)
	if err != nil {
		fmt.Println("sweep failed:", err)
		os.Exit(1)
	}

	var total float64
	for _, r := range results {
		switch {
		case r.SkippedMsg != "":
			fmt.Printf("SKIP  %-44s %s\n", r.Address, r.SkippedMsg)
		case r.TxHash == "(dry-run)":
			fmt.Printf("WOULD %-44s $%.2f\n", r.Address, r.SweptUSDC)
			total += r.SweptUSDC
		default:
			fmt.Printf("SENT  %-44s $%.2f  tx=%s\n", r.Address, r.SweptUSDC, r.TxHash)
			total += r.SweptUSDC
		}
	}
	fmt.Printf("\ntotal: $%.2f\n", total)
	if !*execute && total > 0 {
		fmt.Println("\nrun with --execute to send these transfers")
	}
}
