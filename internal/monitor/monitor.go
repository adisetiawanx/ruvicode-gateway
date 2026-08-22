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

// Chain constants. The USDC contract differs between mainnet and
// testnet, so it is configurable; these are the defaults for Base
// mainnet (ADR-027).
const (
	USDCContractMainnet = "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"
	// USDCContractSepolia is Circle's testnet USDC on Base Sepolia.
	USDCContractSepolia = "0x036CbD53842c5426634e7929541eC2318f3dCF7e"
	USDCDecimals        = 6
	ConfirmationsReq    = 3
	PollInterval        = 15 * time.Second
	// LookBackBlocks covers the poll window plus reorg slack. Base emits
	// a block every ~2s, so 50 blocks ≈ 100s — several poll cycles.
	// BootstrapBlocks is how far behind the head the first scan starts
	// when no cursor exists yet.
	// BootstrapBlocks is how far behind the head the first scan starts
	// when no cursor exists yet. 10000 blocks ≈ 5.5 hours at 2s/block
	// on Base, enough to cover a typical outage. For longer gaps an
	// operator can set the cursor to 0 for a full backfill.
	BootstrapBlocks = 10000
	// MaxCatchUpBlocks caps how far behind the cursor may be before it is
	// treated as stale and fast-forwarded to the bootstrap window. Without
	// this, a stale cursor (e.g. testnet reorg or manual experiment) would
	// attempt an unbounded chunked catch-up that can never finish.
	MaxCatchUpBlocks = 20000
	// ScanChunkBlocks is the per-request block range. Alchemy free tier
	// (Sepolia) caps eth_getLogs at 10 blocks; mainnet free allows 10k+.
	// Five keeps us safely inside every limit.
	ScanChunkBlocks = 5
	// MinDepositUSD prunes micro-deposits that cost more to track than
	// they are worth. Testnet drips are small, so the threshold is
	// configurable for E2E runs.
)

// transferTopic is the Transfer(address,address,uint256) event signature.
var transferTopic = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))

// Monitor polls Base for USDC transfers to deposit addresses.
type Monitor struct {
	primary     *ethclient.Client
	fallback    *ethclient.Client
	pg          *store.PostgresStore
	usdc        common.Address
	minDeposit  float64
}

// New dials the primary RPC (Alchemy) and the fallback (public Base)
// against the given USDC contract address.
func New(primaryURL, fallbackURL, usdcContract string, minDeposit float64, pg *store.PostgresStore) (*Monitor, error) {
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

	if usdcContract == "" {
		usdcContract = USDCContractMainnet
	}

	if minDeposit <= 0 {
		minDeposit = 1.0
	}
	return &Monitor{
		primary:    primary,
		fallback:   fallback,
		pg:         pg,
		usdc:       common.HexToAddress(usdcContract),
		minDeposit: minDeposit,
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
		"min_deposit_usd", m.minDeposit,
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

// getCursor returns the last processed block, bootstrapping to
// currentBlock - BootstrapBlocks on first run so we do not scan from
// genesis.
func (m *Monitor) getCursor(ctx context.Context, head uint64) (uint64, error) {
	var last uint64
	err := m.pg.Pool.QueryRow(ctx,
		`SELECT last_processed_block FROM monitor_cursor WHERE id = 1`,
	).Scan(&last)
	if err == nil {
		return last, nil
	}
	// No cursor yet: start BootstrapBlocks behind the head.
	if head > BootstrapBlocks {
		return head - BootstrapBlocks, nil
	}
	return 0, nil
}

// saveCursor persists the last processed block (best effort).
func (m *Monitor) saveCursor(ctx context.Context, block uint64) {
	_, err := m.pg.Pool.Exec(ctx, `
		INSERT INTO monitor_cursor (id, last_processed_block, updated_at)
		VALUES (1, $1, NOW())
		ON CONFLICT (id) DO UPDATE
		SET last_processed_block = EXCLUDED.last_processed_block, updated_at = NOW()
	`, block)
	if err != nil {
		slog.Warn("monitor: save cursor failed", "error", err)
	}
}

// checkForDeposits scans USDC Transfer logs from the persisted cursor
// forward, so a monitor outage never loses deposits: on restart the scan
// resumes exactly where it stopped.
func (m *Monitor) checkForDeposits(ctx context.Context) {
	currentBlock, err := m.blockNumberWithFallback(ctx)
	if err != nil {
		slog.Error("monitor: get block number failed", "error", err)
		return
	}

	lastProcessed, err := m.getCursor(ctx, currentBlock)
	if err != nil {
		slog.Error("monitor: read cursor failed", "error", err)
		return
	}

	// A cursor far behind the head means the scan cannot realistically
	// catch up chunk-by-chunk (stale testnet state, long downtime beyond
	// the documented backfill path). Fast-forward to the bootstrap window
	// and log loudly — deposits older than the window need a manual
	// backfill (see ADR-035).
	if currentBlock > lastProcessed && currentBlock-lastProcessed > MaxCatchUpBlocks {
		slog.Warn("monitor: cursor too far behind head, fast-forwarding",
			"cursor", lastProcessed,
			"head", currentBlock,
			"gap", currentBlock-lastProcessed,
			"new_start", currentBlock-BootstrapBlocks,
		)
		lastProcessed = currentBlock - BootstrapBlocks
		m.saveCursor(ctx, lastProcessed)
	}

	// Scan forward from cursor+1 in Alchemy-free-tier-safe chunks.
	// The window never extends past safeHead = head - ConfirmationsReq,
	// so every log inside it already has the required confirmations
	// relative to the chain head. processLog re-checks against the head
	// as a secondary guard; it must NEVER measure against chunkTo, or a
	// deposit landing near a chunk boundary gets skipped while the cursor
	// still advances past it (permanent miss).
	from := lastProcessed + 1
	if from > currentBlock {
		return // Already at head
	}

	addresses, err := m.getWatchedAddresses(ctx)
	if err != nil {
		slog.Error("monitor: get addresses failed", "error", err)
		return
	}
	if len(addresses) == 0 {
		return
	}

	// The safe head is the newest block with enough confirmations.
	// We never advance the cursor beyond this point.
	safeHead := uint64(0)
	if currentBlock > ConfirmationsReq {
		safeHead = currentBlock - ConfirmationsReq
	}

	for chunkFrom := from; chunkFrom <= safeHead; chunkFrom += ScanChunkBlocks {
		chunkTo := chunkFrom + ScanChunkBlocks - 1
		if chunkTo > safeHead {
			chunkTo = safeHead
		}

		query := ethereum.FilterQuery{
			FromBlock: new(big.Int).SetUint64(chunkFrom),
			ToBlock:   new(big.Int).SetUint64(chunkTo),
			Addresses: []common.Address{m.usdc},
			Topics: [][]common.Hash{
				{transferTopic},
			},
		}

		logs, err := m.filterLogsWithFallback(ctx, query)
		if err != nil {
			slog.Error("monitor: filter logs failed",
				"error", err,
				"chunk", fmt.Sprintf("%d-%d", chunkFrom, chunkTo))
			return // Cursor NOT advanced: retry this chunk next cycle.
		}

		for _, vLog := range logs {
			m.processLog(ctx, vLog, addresses, currentBlock)
		}

		m.saveCursor(ctx, chunkTo)

		// Pace the scan: Alchemy free tier enforces compute units per
		// second, and a burst of back-to-back eth_getLogs trips it. A
		// short pause between chunks keeps the request rate friendly.
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// processLog credits a deposit when the log matches one of our addresses.
// head is the CURRENT chain head at scan time: the scan window already
// stops at safeHead = head - ConfirmationsReq, so any log inside the
// window has the required confirmations; the check below is a secondary
// guard for callers that pass logs directly.
func (m *Monitor) processLog(ctx context.Context, vLog types.Log, addresses map[string]string, head uint64) {
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

	if amountFloat < m.minDeposit {
		slog.Debug("monitor: micro-deposit ignored",
			"user", userID, "amount", amountFloat)
		return
	}

	if vLog.BlockNumber+ConfirmationsReq > head {
		slog.Debug("monitor: deposit pending confirmation",
			"user", userID,
			"amount", amountFloat,
			"head", head,
			"block", vLog.BlockNumber,
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
