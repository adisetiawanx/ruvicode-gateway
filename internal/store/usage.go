package store

import (
	"context"
	"time"
)

// UsageRecord is the audit entry written after each completed request.
type UsageRecord struct {
	ID               string
	UserID           string
	APIKeyID         string
	Model            string
	PromptTokens     int
	CompletionTokens int
	ReasoningTokens  int
	Cost             float64
	UpstreamCost     float64
	RefCost          float64
	RequestID        string
	Status           string
}

// InsertUsageRecord writes a usage record (metadata only, no prompt content).
func (s *PostgresStore) InsertUsageRecord(ctx context.Context, record *UsageRecord) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO usage_records (id, user_id, api_key_id, model, prompt_tokens, completion_tokens, reasoning_tokens, cost, upstream_cost, ref_cost, request_id, status, created_at)
		VALUES (COALESCE($1, gen_random_uuid()::text), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NOW())
	`,
		record.ID, record.UserID, record.APIKeyID, record.Model,
		record.PromptTokens, record.CompletionTokens, record.ReasoningTokens,
		record.Cost, record.UpstreamCost, record.RefCost, record.RequestID, record.Status,
	)
	return err
}

// UpdateLastUsed refreshes api_keys.last_used_at fire-and-forget.
// Stored as UTC wall clock to stay consistent with created_at (NOW() in the
// UTC container); timestamp columns carry no time zone.
func (s *PostgresStore) UpdateLastUsed(ctx context.Context, keyID string) {
	_, _ = s.Pool.Exec(ctx,
		"UPDATE api_keys SET last_used_at = $1 WHERE id = $2", time.Now().UTC(), keyID)
}

// ActiveModel is an active model from model_prices with the fields the public
// /v1/models listing needs. UserCacheReadPer1M is 0 when the model has no
// cache read price (the API then reports null).
type ActiveModel struct {
	Model              string
	UserCacheReadPer1M float64
}

// ListActiveModels returns the active models from model_prices.
func (s *PostgresStore) ListActiveModels(ctx context.Context) ([]ActiveModel, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT model, COALESCE(user_cache_read_per_1m, 0)
		FROM model_prices WHERE is_active = true ORDER BY model
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var models []ActiveModel
	for rows.Next() {
		var m ActiveModel
		if err := rows.Scan(&m.Model, &m.UserCacheReadPer1M); err != nil {
			return nil, err
		}
		models = append(models, m)
	}
	return models, rows.Err()
}
