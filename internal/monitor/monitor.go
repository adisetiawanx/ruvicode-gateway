// Package monitor polls the Base blockchain for incoming USDC deposits
// to Ruvicode-owned deposit addresses and credits user wallets (ADR-027).
package monitor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/ruvicode/gateway/internal/store"
)

// USDC on Base mainnet.
const (
	USDCContractBase = "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"
	USDCDecimals     = 6
	ConfirmationsReq = 3
	PollInterval     = 15 * time.Second
	// LookBackBlocks covers the poll window plus reorg slack. Base emits
	// a block every ~2s, so 50 blocks ≈ 100s — several poll cycles.
	LookBackBlocks = 50
	// MinDepositUSD prunes micro-deposits that cost more to track than
	// they are worth.
	MinDepositUSD = 1.0
)

// transferTopic is the Transfer(address,address,uint256) event signature.
var transferTopic = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))

// Monitor polls Base for USDC transfers to deposit addresses.
type Monitor struct {
	primary  *ethclient.Client
	fallback *ethclient.Client
	pg       *store.PostgresStore
	usdc     common.Address
}

// New dials the primary RPC (Alchemy) and the fallback (public Base).
// The fallback may be nil if not configured.
func New(primaryURL, fallbackURL string, pg *store.PostgresStore) (*Monitor, error) {
	primary, err := ethclient.Dial(primaryURL)
	if err != nil {
		return nil, fmt.Errorf("connect primary RPC: %w", err)
	}

	var fallback *ethclient.Client
	if fallbackURL != "" && fallbackURL != primaryURL {
		fallback, err = ethclient.Dial(fallbackURL)
		if err != nil {
			// Not fatal: log and continue with the primary only.
			slog.Warn("monitor: fallback RPC dial failed", "error", err)
		}
	}

	return &Monitor{
		primary:  primary,
		fallback: fallback,
		pg:       pg,
		usdc:     common.HexToAddress(USDCContractBase),
	}, nil
}

// blockNumberWithFallback reads the head block, falling back to the
// public RPC when the primary (Alchemy) fails or rate-limits.
func (m *Monitor) blockNumberWithFallback(ctx context.Context) (uint64, error) {
	n, err := m.primary.BlockNumber(ctx)
	if err == nil {
		return n, nil
	}
	if m.fallback != nil {
		slog.Warn("monitor: primary RPC failed, trying fallback", "error", err)
		return m.fallback.BlockNumber(ctx)
	}
	return 0, err
}

// filterLogsWithFallback scans Transfer logs, falling back to the public
// RPC when the primary fails.
func (m *Monitor) filterLogsWithFallback(ctx context.Context, query ethereum.FilterQuery) ([]types.Log, error) {
	logs, err := m.primary.FilterLogs(ctx, query)
	if err == nil {
		return logs, nil
	}
	if m.fallback != nil {
		slog.Warn("monitor: primary RPC failed, trying fallback", "error", err)
		return m.fallback.FilterLogs(ctx, query)
	}
	return nil, err
}

// Run starts the polling loop. Blocks until the context is cancelled.
func (m *Monitor) Run(ctx context.Context) {
	slog.Info("USDC deposit monitor started",
		"poll_interval", PollInterval.String(),
		"confirmations_required", ConfirmationsReq,
		"min_deposit_usd", MinDepositUSD,
	)

	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()

	m.checkForDeposits(ctx)

	for {
		select {
		case <-ticker.C:
			m.checkForDeposits(ctx)
		case <-ctx.Done():
			slog.Info("USDC deposit monitor stopped")
			return
		}
	}
}

// checkForDeposits scans the recent USDC Transfer logs for our addresses.
func (m *Monitor) checkForDeposits(ctx context.Context) {
	currentBlock, err := m.blockNumberWithFallback(ctx)
	if err != nil {
		slog.Error("monitor: get block number failed", "error", err)
		return
	}

	fromBlock := new(big.Int).Sub(new(big.Int).SetUint64(currentBlock), big.NewInt(LookBackBlocks))
	if fromBlock.Sign() < 0 {
		fromBlock = big.NewInt(0)
	}

	addresses, err := m.getWatchedAddresses(ctx)
	if err != nil {
		slog.Error("monitor: get addresses failed", "error", err)
		return
	}
	if len(addresses) == 0 {
		return
	}

	query := ethereum.FilterQuery{
		FromBlock: fromBlock,
		ToBlock:   new(big.Int).SetUint64(currentBlock),
		Addresses: []common.Address{m.usdc},
		Topics: [][]common.Hash{
			{transferTopic},
		},
	}

	logs, err := m.filterLogsWithFallback(ctx, query)
	if err != nil {
		slog.Error("monitor: filter logs failed", "error", err)
		return
	}

	for _, vLog := range logs {
		m.processLog(ctx, vLog, addresses, currentBlock)
	}
}

// processLog credits a deposit when the log matches one of our addresses
// with enough confirmations.
func (m *Monitor) processLog(ctx context.Context, vLog types.Log, addresses map[string]string, currentBlock uint64) {
	if len(vLog.Topics) < 3 {
		return
	}

	toAddr := common.BytesToAddress(vLog.Topics[2].Bytes())
	userID, ok := addresses[toAddr.Hex()]
	if !ok {
		return
	}

	amount := new(big.Int).SetBytes(vLog.Data)
	amountUSDC := new(big.Float).Quo(
		new(big.Float).SetInt(amount),
		big.NewFloat(1e6),
	)
	amountFloat, _ := amountUSDC.Float64()

	if amountFloat < MinDepositUSD {
		slog.Debug("monitor: micro-deposit ignored",
			"user", userID, "amount", amountFloat)
		return
	}

	confirmations := currentBlock - vLog.BlockNumber
	if confirmations < ConfirmationsReq {
		slog.Debug("monitor: deposit pending confirmation",
			"user", userID,
			"amount", amountFloat,
			"confirmations", confirmations,
			"required", ConfirmationsReq,
		)
		return
	}

	txHash := vLog.TxHash.Hex()
	m.creditDeposit(ctx, userID, amountFloat, txHash, toAddr.Hex())
}

// getWatchedAddresses maps deposit address -> user id.
func (m *Monitor) getWatchedAddresses(ctx context.Context) (map[string]string, error) {
	rows, err := m.pg.Pool.Query(ctx,
		`SELECT user_id, address FROM deposit_addresses WHERE user_id IS NOT NULL`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	addresses := make(map[string]string)
	for rows.Next() {
		var userID, addr string
		if err := rows.Scan(&userID, &addr); err != nil {
			continue
		}
		addresses[common.HexToAddress(addr).Hex()] = userID
	}
	return addresses, rows.Err()
}

// creditDeposit inserts the topup and credits the wallet atomically.
// Idempotent via INSERT-first: the unique index on usdc_tx_hash rejects
// duplicates with SQLSTATE 23505 before any balance changes.
func (m *Monitor) creditDeposit(ctx context.Context, userID string, amount float64, txHash, depositAddr string) {
	tx, err := m.pg.Pool.Begin(ctx)
	if err != nil {
		slog.Error("monitor: begin tx failed", "error", err)
		return
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO topups (id, user_id, amount, method, usdc_tx_hash, status, fee, completed_at)
		VALUES (gen_random_uuid()::text, $1, $2, 'usdc', $3, 'completed', 0, NOW())
	`, userID, amount, txHash)

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return // Already credited — the unique index did its job.
	}
	if err != nil {
		slog.Error("monitor: insert topup failed", "error", err, "tx_hash", txHash)
		return
	}

	// Auto-create the wallet row if the user never had one, then credit.
	_, err = tx.Exec(ctx, `
		INSERT INTO wallets (user_id, balance, held, total_loaded, total_spent)
		VALUES ($1, $2, 0, $2, 0)
		ON CONFLICT (user_id) DO UPDATE
		SET balance = wallets.balance + $2,
		    total_loaded = wallets.total_loaded + $2,
		    updated_at = NOW()
	`, userID, amount)
	if err != nil {
		slog.Error("monitor: credit wallet failed", "error", err, "tx_hash", txHash)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Error("monitor: commit failed", "error", err, "tx_hash", txHash)
		return
	}

	slog.Info("USDC deposit credited",
		"user_id", userID,
		"amount", amount,
		"tx_hash", txHash,
		"address", depositAddr,
	)
}
