package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// APIKeyData is the cached key data used by the auth middleware.
// Spend limits are carried as strings (numeric-to-text) matching how they are
// read from Postgres.
type APIKeyData struct {
	KeyID             string  `json:"key_id"`
	UserID            string  `json:"user_id"`
	IsActive          bool    `json:"is_active"`
	RateLimitRPM      int     `json:"rate_limit_rpm"`
	SpendLimitDaily   *string `json:"spend_limit_daily"`
	SpendLimitMonthly *string `json:"spend_limit_monthly"`
	Label             string  `json:"label"`
}

// GetAPIKeyByHash looks up an API key by its SHA-256 hash.
func (s *PostgresStore) GetAPIKeyByHash(ctx context.Context, hash string) (*APIKeyData, error) {
	return s.getAPIKey(ctx, "WHERE key_hash = $1", hash)
}

// GetAPIKeyByIDAndUser looks up an API key by its id, scoped to a user. It is
// used by the internal playground endpoint, which the dashboard calls with a
// signed-in user's key id (the full key is never stored or handled by the
// web server).
func (s *PostgresStore) GetAPIKeyByIDAndUser(ctx context.Context, userID, keyID string) (*APIKeyData, error) {
	return s.getAPIKey(ctx, "WHERE id = $1 AND user_id = $2", keyID, userID)
}

func (s *PostgresStore) getAPIKey(ctx context.Context, where string, args ...any) (*APIKeyData, error) {
	var data APIKeyData

	query := `
		SELECT
			id,
			user_id,
			is_active,
			rate_limit_rpm,
			spend_limit_daily::text,
			spend_limit_monthly::text,
			label
		FROM api_keys
		` + where

	err := s.Pool.QueryRow(ctx, query, args...).Scan(
		&data.KeyID,
		&data.UserID,
		&data.IsActive,
		&data.RateLimitRPM,
		&data.SpendLimitDaily,
		&data.SpendLimitMonthly,
		&data.Label,
	)
	if err != nil {
		return nil, fmt.Errorf("api key not found: %w", err)
	}

	return &data, nil
}

// CacheAPIKey stores key data in Redis for fast validation (5-min TTL).
func (s *RedisStore) CacheAPIKey(ctx context.Context, hash string, data *APIKeyData) error {
	key := "apikey:" + hash
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.Client.Set(ctx, key, jsonData, 5*time.Minute).Err()
}

// GetAPIKeyFromCache retrieves key data from Redis.
func (s *RedisStore) GetAPIKeyFromCache(ctx context.Context, hash string) (*APIKeyData, error) {
	key := "apikey:" + hash
	raw, err := s.Client.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err // Cache miss.
	}

	var data APIKeyData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

// InvalidateAPIKeyCache removes a key from the Redis cache (on revocation or
// limit change so changes take effect immediately).
func (s *RedisStore) InvalidateAPIKeyCache(ctx context.Context, hash string) error {
	return s.Client.Del(ctx, "apikey:"+hash).Err()
}
