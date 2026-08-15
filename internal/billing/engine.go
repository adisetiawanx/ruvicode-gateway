// Package billing implements the optimistic pre-deduction ("credit-card hold")
// billing pattern: PreCheck reserves funds in Redis before forwarding to the
// provider, FinalizeDeduction settles the actual cost atomically in Postgres.
// Postgres is the source of truth for balances; Redis is a cache only.
package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/ruvicode/gateway/internal/pricing"
	"github.com/ruvicode/gateway/internal/provider"
	"github.com/ruvicode/gateway/internal/store"
)

// Engine coordinates wallet holds and settlements.
type Engine struct {
	pg  *store.PostgresStore
	rdb *store.RedisStore
}

// New builds a billing Engine.
func New(pg *store.PostgresStore, rdb *store.RedisStore) *Engine {
	return &Engine{pg: pg, rdb: rdb}
}

// PreCheckResult holds the state needed to finalize or release a billing hold.
type PreCheckResult struct {
	UserID        string
	APIKeyID      string
	EstimatedCost float64
	HoldID        string
}

// UsageInfo is the data needed to record a usage event after completion.
type UsageInfo struct {
	Model            string
	PromptTokens     int
	CompletionTokens int
	ReasoningTokens  int
	APIKeyID         string
	UpstreamCost     float64
	RequestID        string
}

// estimatedInputTokensPerMessage is the conservative per-message token
// assumption used by EstimateCost.
const estimatedInputTokensPerMessage = 500

// EstimateCost calculates an upper-bound cost estimate for the request,
// used for the pre-deduction hold. If the actual cost is lower, the
// difference is refunded at settlement.
func (e *Engine) EstimateCost(modelPrice *pricing.ModelPrice, messageCount int, maxTokens *int) float64 {
	estimatedInputTokens := messageCount * estimatedInputTokensPerMessage
	estimatedOutputTokens := 1024
	if maxTokens != nil {
		estimatedOutputTokens = *maxTokens
	}

	inputCost := float64(estimatedInputTokens) / 1_000_000 * modelPrice.UserInputPer1M
	outputCost := float64(estimatedOutputTokens) / 1_000_000 * modelPrice.UserOutputPer1M
	return inputCost + outputCost
}

// CalculateActualCost computes the real cost from usage data and cached pricing.
func (e *Engine) CalculateActualCost(modelPrice *pricing.ModelPrice, usage *provider.Usage) float64 {
	if usage == nil {
		return 0
	}
	inputCost := float64(usage.PromptTokens) / 1_000_000 * modelPrice.UserInputPer1M
	outputCost := float64(usage.CompletionTokens) / 1_000_000 * modelPrice.UserOutputPer1M
	return inputCost + outputCost
}

// PreCheck verifies the user has sufficient balance, applies a Redis hold, and
// checks spend limits. Returns a PreCheckResult for later finalization.
func (e *Engine) PreCheck(ctx context.Context, userID string, estimatedCost float64, keyData *store.APIKeyData) (*PreCheckResult, error) {
	// Spend limit checks. The Redis trackers are optimistic caches of the
	// Postgres usage_records sums; on a Redis miss the fallback query is the
	// authoritative number.
	if keyData.SpendLimitDaily != nil {
		if limit, ok := parseLimit(*keyData.SpendLimitDaily); ok {
			spendToday, _ := e.getDailySpend(ctx, userID, keyData.KeyID)
			if spendToday+estimatedCost > limit {
				return nil, fmt.Errorf("daily spend limit exceeded")
			}
		}
	}
	if keyData.SpendLimitMonthly != nil {
		if limit, ok := parseLimit(*keyData.SpendLimitMonthly); ok {
			spendMonth, _ := e.getMonthlySpend(ctx, userID, keyData.KeyID)
			if spendMonth+estimatedCost > limit {
				return nil, fmt.Errorf("monthly spend limit exceeded")
			}
		}
	}

	// Ensure the Redis balance cache is populated from Postgres.
	if err := e.ensureBalanceCache(ctx, userID); err != nil {
		// Fail SAFE: never grant access we cannot verify.
		slog.Error("billing precheck: balance cache failed", "error", err, "user_id", userID)
		return nil, fmt.Errorf("billing system temporarily unavailable")
	}

	holdKey := "balance:" + userID
	holdID := fmt.Sprintf("hold_%d_%s", time.Now().UnixNano(), keyData.KeyID[:min(8, len(keyData.KeyID))])

	// Atomic: increment the held amount, then read the balance in one pipeline.
	pipe := e.rdb.Client.TxPipeline()
	pipe.HIncrByFloat(ctx, holdKey, "held", estimatedCost)
	balCmd := pipe.HGet(ctx, holdKey, "balance")
	if _, err := pipe.Exec(ctx); err != nil {
		slog.Error("billing precheck: redis hold failed", "error", err, "user_id", userID)
		return nil, fmt.Errorf("billing system temporarily unavailable")
	}

	balance, _ := balCmd.Float64()
	held, err := e.rdb.Client.HGet(ctx, holdKey, "held").Float64()
	if err != nil {
		held = 0
	}

	available := balance - held
	if available < 0 {
		// Insufficient balance: reverse the hold we just placed.
		e.rdb.Client.HIncrByFloat(ctx, holdKey, "held", -estimatedCost)
		return nil, fmt.Errorf("insufficient balance")
	}

	// Optimistic spend trackers. Both windows are written: a key with only a
	// monthly limit must find its tracker populated too. The keys are scoped
	// to the UTC calendar day/month so they rotate naturally at the boundary
	// (the TTL below is cleanup only, not the reset mechanism).
	dailyKey := spendKeyDaily(userID, keyData.KeyID)
	monthlyKey := spendKeyMonthly(userID, keyData.KeyID)
	e.rdb.Client.IncrByFloat(ctx, dailyKey, estimatedCost)
	e.rdb.Client.Expire(ctx, dailyKey, 48*time.Hour)
	e.rdb.Client.IncrByFloat(ctx, monthlyKey, estimatedCost)
	e.rdb.Client.Expire(ctx, monthlyKey, 45*24*time.Hour)

	return &PreCheckResult{
		UserID:        userID,
		APIKeyID:      keyData.KeyID,
		EstimatedCost: estimatedCost,
		HoldID:        holdID,
	}, nil
}

// ReleaseHold releases a previously placed hold (when the request fails) and
// rolls the optimistic spend trackers back so failed requests do not consume
// the key's spend limits. A request that fails across a UTC day/month
// boundary rolls back into the new window (sub-second edge case; the Postgres
// fallback remains the authoritative sum).
func (e *Engine) ReleaseHold(ctx context.Context, userID, keyID string, estimatedCost float64) {
	key := "balance:" + userID
	e.rdb.Client.HIncrByFloat(ctx, key, "held", -estimatedCost)
	if keyID == "" {
		return
	}
	e.rdb.Client.IncrByFloat(ctx, spendKeyDaily(userID, keyID), -estimatedCost)
	e.rdb.Client.IncrByFloat(ctx, spendKeyMonthly(userID, keyID), -estimatedCost)
}

// FinalizeDeduction performs the atomic wallet update + usage record insert
// and adjusts the pre-deduction hold to match the actual cost.
func (e *Engine) FinalizeDeduction(
	ctx context.Context,
	userID string,
	estimatedCost float64,
	actualCost float64,
	preCheck *PreCheckResult,
	info *UsageInfo,
) {
	tx, err := e.pg.Pool.Begin(ctx)
	if err != nil {
		slog.Error("billing finalize: begin tx failed", "error", err)
		e.ReleaseHold(ctx, userID, info.APIKeyID, estimatedCost)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op if committed

	// Atomic wallet update: deduct the actual cost, only if the balance covers
	// it. The optimistic hold lives in Redis (5s TTL cache), so Postgres held
	// stays 0 and is not modified here. Postgres is the source of truth.
	tag, err := tx.Exec(ctx, `
		UPDATE wallets
		SET balance = balance - $1,
		    total_spent = total_spent + $1,
		    updated_at = NOW()
		WHERE user_id = $2 AND balance >= $1
	`, actualCost, userID)
	if err != nil {
		slog.Error("billing finalize: wallet update failed", "error", err)
		e.ReleaseHold(ctx, userID, info.APIKeyID, estimatedCost)
		return
	}

	if tag.RowsAffected() == 0 {
		// Balance dropped below the actual cost at settlement time. Record the
		// request as failed (no charge), release the hold, and roll the spend
		// trackers fully back (nothing was charged).
		_, _ = tx.Exec(ctx, `
			INSERT INTO usage_records (id, user_id, api_key_id, model, prompt_tokens, completion_tokens, reasoning_tokens, cost, upstream_cost, request_id, status, created_at)
			VALUES (gen_random_uuid()::text, $1, $2, $3, $4, $5, $6, 0, 0, $7, 'failed', NOW())
		`,
			userID,
			info.APIKeyID,
			info.Model,
			info.PromptTokens,
			info.CompletionTokens,
			info.ReasoningTokens,
			info.RequestID,
		)
		_ = tx.Commit(ctx)
		e.ReleaseHold(ctx, userID, info.APIKeyID, estimatedCost)
		slog.Warn("billing finalize: insufficient balance at settlement",
			"user_id", userID, "model", info.Model, "actual_cost", actualCost)
		return
	}

	// Insert the usage record in the same transaction (metadata only).
	_, err = tx.Exec(ctx, `
		INSERT INTO usage_records (id, user_id, api_key_id, model, prompt_tokens, completion_tokens, reasoning_tokens, cost, upstream_cost, request_id, status, created_at)
		VALUES (gen_random_uuid()::text, $1, $2, $3, $4, $5, $6, $7, $8, $9, 'completed', NOW())
	`,
		userID,
		info.APIKeyID,
		info.Model,
		info.PromptTokens,
		info.CompletionTokens,
		info.ReasoningTokens,
		actualCost,
		info.UpstreamCost,
		info.RequestID,
	)
	if err != nil {
		slog.Error("billing finalize: usage insert failed", "error", err)
		return // tx rolls back, wallet update reverted
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Error("billing finalize: commit failed", "error", err)
		return
	}

	// Sync the Redis balance cache from Postgres (source of truth).
	if err := e.SyncBalanceFromPostgres(ctx, userID); err != nil {
		slog.Warn("billing finalize: balance sync failed", "error", err, "user_id", userID)
	}

	// Roll the optimistic spend trackers from the estimate to the actual
	// cost. Failed and no-charge settlements roll back fully below.
	dailyKey := spendKeyDaily(userID, info.APIKeyID)
	monthlyKey := spendKeyMonthly(userID, info.APIKeyID)
	e.rdb.Client.IncrByFloat(ctx, dailyKey, actualCost-estimatedCost)
	e.rdb.Client.IncrByFloat(ctx, monthlyKey, actualCost-estimatedCost)
	e.rdb.Client.Expire(ctx, dailyKey, 48*time.Hour)
	e.rdb.Client.Expire(ctx, monthlyKey, 45*24*time.Hour)

	slog.Info("billing_finalized",
		"user_id", userID,
		"model", info.Model,
		"estimated_cost", estimatedCost,
		"actual_cost", actualCost,
		"upstream_cost", info.UpstreamCost,
		"tokens_in", info.PromptTokens,
		"tokens_out", info.CompletionTokens,
	)
}

// SyncBalanceFromPostgres reads the canonical balance from Postgres and
// writes it to the Redis cache. If the user does not yet have a wallet row,
// it is created with zero balance (first top-up will credit it).
func (e *Engine) SyncBalanceFromPostgres(ctx context.Context, userID string) error {
	balance, held, err := e.pg.GetWalletBalanceAndHeld(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Wallet does not exist yet — create one with zero balance.
			if createErr := e.pg.EnsureWallet(ctx, userID); createErr != nil {
				return createErr
			}
			balance, held = 0, 0
		} else {
			return err
		}
	}
	key := "balance:" + userID
	e.rdb.Client.HSet(ctx, key, "balance", balance, "held", held)
	e.rdb.Client.Expire(ctx, key, 5*time.Second)
	return nil
}

// ensureBalanceCache populates the Redis balance hash if it is missing.
func (e *Engine) ensureBalanceCache(ctx context.Context, userID string) error {
	key := "balance:" + userID
	exists, err := e.rdb.Client.Exists(ctx, key).Result()
	if err != nil {
		return err
	}
	if exists == 1 {
		return nil
	}
	return e.SyncBalanceFromPostgres(ctx, userID)
}

// spendKeyDaily returns the Redis key for the current UTC calendar day's
// spend tracker. Scoping the key to the day (not a TTL) makes the window
// reset itself at UTC midnight.
func spendKeyDaily(userID, keyID string) string {
	return "spend:" + userID + ":" + keyID + ":d" + time.Now().UTC().Format("20060102")
}

// spendKeyMonthly returns the Redis key for the current UTC calendar month's
// spend tracker.
func spendKeyMonthly(userID, keyID string) string {
	return "spend:" + userID + ":" + keyID + ":m" + time.Now().UTC().Format("200601")
}

// getDailySpend returns the user's spend for the current day.
func (e *Engine) getDailySpend(ctx context.Context, userID, keyID string) (float64, error) {
	if spend, err := e.rdb.Client.Get(ctx, spendKeyDaily(userID, keyID)).Float64(); err == nil {
		return spend, nil
	}
	midnight := startOfDayUTC()
	var total float64
	err := e.pg.Pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(cost), 0) FROM usage_records
		WHERE user_id = $1 AND api_key_id = $2 AND created_at >= $3
	`, userID, keyID, midnight).Scan(&total)
	return total, err
}

// getMonthlySpend returns the user's spend for the current month.
func (e *Engine) getMonthlySpend(ctx context.Context, userID, keyID string) (float64, error) {
	if spend, err := e.rdb.Client.Get(ctx, spendKeyMonthly(userID, keyID)).Float64(); err == nil {
		return spend, nil
	}
	monthStart := startOfMonthUTC()
	var total float64
	err := e.pg.Pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(cost), 0) FROM usage_records
		WHERE user_id = $1 AND api_key_id = $2 AND created_at >= $3
	`, userID, keyID, monthStart).Scan(&total)
	return total, err
}

func startOfDayUTC() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

func startOfMonthUTC() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// parseLimit converts a spend-limit string to a float64.
func parseLimit(s string) (float64, bool) {
	var v float64
	if _, err := fmt.Sscanf(s, "%f", &v); err != nil {
		return 0, false
	}
	return v, v >= 0
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
