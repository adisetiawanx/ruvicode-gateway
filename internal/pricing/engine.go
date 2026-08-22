// Package pricing reads and caches model pricing for the gateway hot path.
// The full cron-powered sync (ADR-020) lives in cmd/pricing; this package
// provides the ModelPrice type and the Redis->Postgres read path used per
// request by the chat handler.
package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/ruvicode/gateway/internal/provider"
	"github.com/ruvicode/gateway/internal/store"
)

// ModelPrice is the cached pricing data used by the gateway hot path.
type ModelPrice struct {
	Model              string  `json:"model"`
	Provider           string  `json:"provider"`
	DisplayName        string  `json:"display_name"`
	UserInputPer1M     float64 `json:"user_input_per_1m"`
	UserOutputPer1M    float64 `json:"user_output_per_1m"`
	RefInputPer1M      float64 `json:"ref_input_per_1m"`
	RefOutputPer1M     float64 `json:"ref_output_per_1m"`
	UserCacheReadPer1M float64 `json:"user_cache_read_per_1m"`
	RefCacheReadPer1M  float64 `json:"ref_cache_read_per_1m"`
	// Provider (marketplace best) prices — the estimated real wallet charge.
	ProviderInputPer1M     float64 `json:"provider_input_per_1m"`
	ProviderOutputPer1M    float64 `json:"provider_output_per_1m"`
	ProviderCacheReadPer1M float64 `json:"provider_cache_read_per_1m"`
	DiscountPct            float64 `json:"discount_pct"`
	UserDiscountPct        float64 `json:"user_discount_pct"`
}

// Engine reads and caches pricing, and runs the ADR-020 sync worker.
type Engine struct {
	pg       *store.PostgresStore
	rdb      *store.RedisStore
	registry *provider.Registry // used by the ADR-020 sync worker
	spreadPP int                // percentage-point spread for the sync worker

	// staleness tracking for the sync worker
	mu                  sync.Mutex
	consecutiveFailures int
	maxFailures         int
}

// New builds a pricing Engine.
func New(pg *store.PostgresStore, rdb *store.RedisStore, registry *provider.Registry, spreadPP int) *Engine {
	if spreadPP <= 0 {
		spreadPP = 20
	}
	return &Engine{
		pg:          pg,
		rdb:         rdb,
		registry:    registry,
		spreadPP:    spreadPP,
		maxFailures: 3,
	}
}

// cacheTTL is longer than the cron interval (2 min) so prices survive
// transient sync failures.
const cacheTTL = 10 * time.Minute

// GetCachedPrice retrieves pricing from Redis (hot path). On a cache miss it
// reads Postgres and populates Redis.
func (e *Engine) GetCachedPrice(ctx context.Context, model string) (*ModelPrice, error) {
	if e.rdb != nil {
		key := "pricing:" + model
		data, err := e.rdb.Client.Get(ctx, key).Bytes()
		if err == nil && len(data) > 0 {
			var mp ModelPrice
			if json.Unmarshal(data, &mp) == nil && mp.Model != "" {
				return &mp, nil
			}
		}
	}

	var mp ModelPrice
	var isActive bool
	err := e.pg.Pool.QueryRow(ctx, `
		SELECT model, provider, display_name,
		       user_input, user_output,
		       ref_input, ref_output,
		       user_cache_read_per_1m, ref_cache_read_per_1m,
		       provider_input, provider_output, provider_cache_read_per_1m,
		       discount_pct, user_discount_pct,
		       is_active
		FROM model_prices
		WHERE model = $1
	`, model).Scan(
		&mp.Model, &mp.Provider, &mp.DisplayName,
		&mp.UserInputPer1M, &mp.UserOutputPer1M,
		&mp.RefInputPer1M, &mp.RefOutputPer1M,
		&mp.UserCacheReadPer1M, &mp.RefCacheReadPer1M,
		&mp.ProviderInputPer1M, &mp.ProviderOutputPer1M, &mp.ProviderCacheReadPer1M,
		&mp.DiscountPct, &mp.UserDiscountPct,
		&isActive,
	)
	if err != nil {
		return nil, fmt.Errorf("model not found: %s", model)
	}
	if !isActive {
		return nil, fmt.Errorf("model not available: %s", model)
	}

	e.cachePrice(ctx, &mp)
	return &mp, nil
}

// cachePrice stores a ModelPrice in Redis.
func (e *Engine) cachePrice(ctx context.Context, mp *ModelPrice) {
	if e.rdb == nil {
		return
	}
	data, err := json.Marshal(mp)
	if err != nil {
		return
	}
	e.rdb.Client.Set(ctx, "pricing:"+mp.Model, data, cacheTTL)
}
