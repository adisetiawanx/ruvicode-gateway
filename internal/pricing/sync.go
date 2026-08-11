package pricing

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ruvicode/gateway/internal/provider"
)

// calculateUserPrice applies the spread formula from PROJECT.md:
//
//	user_discount  = provider_discount − spreadPP (clamped at 0 minimum)
//	user_input     = reference_input * (1 − user_discount/100)
//	user_output    = reference_output * (1 − user_discount/100)
//
// The clamp means Ruvicode never charges MORE than the reference price, even
// if the provider discount is smaller than the spread.
func (e *Engine) calculateUserPrice(p provider.PricingData) (input, output, userDiscount float64) {
	userDiscount = p.DiscountPct - float64(e.spreadPP)
	if userDiscount < 0 {
		userDiscount = 0
	}

	input = p.RefInputPer1M * (1 - userDiscount/100)
	output = p.RefOutputPer1M * (1 - userDiscount/100)

	return input, output, userDiscount
}

// SyncAllProviders fetches pricing from all registered providers, calculates
// user prices, and updates Postgres + Redis. It returns the last non-fatal
// error so the caller sees a failure was observed but partial success happened.
func (e *Engine) SyncAllProviders(ctx context.Context) error {
	err := e.syncAllProvidersInner(ctx)

	e.mu.Lock()
	if err != nil {
		e.consecutiveFailures++
		// Alert once when the threshold is crossed, not on every later tick.
		if e.consecutiveFailures == e.maxFailures {
			slog.Error("pricing sync: repeated consecutive failures",
				"error", err,
				"consecutive_failures", e.consecutiveFailures,
			)
		}
	} else {
		e.consecutiveFailures = 0
	}
	e.mu.Unlock()

	return err
}

func (e *Engine) syncAllProvidersInner(ctx context.Context) error {
	providers := e.registry.List()
	if len(providers) == 0 {
		return fmt.Errorf("no providers registered")
	}

	totalUpdated := 0
	var lastErr error

	for _, providerName := range providers {
		p, err := e.registry.Get(providerName)
		if err != nil {
			continue
		}

		prices, err := p.FetchPricing(ctx)
		if err != nil {
			slog.Error("pricing fetch failed",
				"provider", providerName,
				"error", err,
			)
			lastErr = err
			continue
		}

		updated, err := e.updatePrices(ctx, providerName, prices)
		if err != nil {
			slog.Error("pricing update failed",
				"provider", providerName,
				"error", err,
			)
			lastErr = err
			continue
		}
		totalUpdated += updated
	}

	slog.Info("pricing sync complete",
		"models_updated", totalUpdated,
		"providers", len(providers),
	)
	return lastErr
}

// updatePrices calculates user prices for each model, upserts into Postgres
// model_prices, and refreshes the Redis cache. Models whose reference or
// provider input price is not positive are skipped (free or error), per the
// ADR (inactive models are never inserted).
func (e *Engine) updatePrices(
	ctx context.Context,
	providerName string,
	prices []provider.PricingData,
) (int, error) {
	updated := 0
	var lastErr error

	for _, p := range prices {
		if p.RefInputPer1M <= 0 || p.ProviderInputPer1M <= 0 {
			continue
		}

		userInput, userOutput, userDiscount := e.calculateUserPrice(p)

		_, err := e.pg.Pool.Exec(ctx, `
			INSERT INTO model_prices (
				model, display_name, provider,
				ref_input, ref_output,
				provider_input, provider_output,
				user_input, user_output,
				discount_pct, user_discount_pct,
				is_active, updated_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, true, NOW())
			ON CONFLICT (model) DO UPDATE SET
				display_name = EXCLUDED.display_name,
				provider = EXCLUDED.provider,
				ref_input = EXCLUDED.ref_input,
				ref_output = EXCLUDED.ref_output,
				provider_input = EXCLUDED.provider_input,
				provider_output = EXCLUDED.provider_output,
				user_input = EXCLUDED.user_input,
				user_output = EXCLUDED.user_output,
				discount_pct = EXCLUDED.discount_pct,
				user_discount_pct = EXCLUDED.user_discount_pct,
				is_active = true,
				updated_at = NOW()
		`,
			p.Model, p.DisplayName, providerName,
			p.RefInputPer1M, p.RefOutputPer1M,
			p.ProviderInputPer1M, p.ProviderOutputPer1M,
			userInput, userOutput,
			p.DiscountPct, userDiscount,
		)
		if err != nil {
			slog.Warn("price upsert failed", "model", p.Model, "error", err)
			lastErr = err
			continue
		}

		modelPrice := &ModelPrice{
			Model:           p.Model,
			Provider:        providerName,
			DisplayName:     p.DisplayName,
			UserInputPer1M:  userInput,
			UserOutputPer1M: userOutput,
			RefInputPer1M:   p.RefInputPer1M,
			RefOutputPer1M:  p.RefOutputPer1M,
			DiscountPct:     p.DiscountPct,
			UserDiscountPct: userDiscount,
		}
		e.cachePrice(ctx, modelPrice)
		updated++
	}

	return updated, lastErr
}

// GetAllCachedPrices returns all active model prices for the dashboard pricing
// page. It reads from Postgres (the dashboard does not need Redis speed) and
// never exposes the provider column to the caller.
func (e *Engine) GetAllCachedPrices(ctx context.Context) ([]ModelPrice, error) {
	rows, err := e.pg.Pool.Query(ctx, `
		SELECT model, provider, display_name,
		       user_input, user_output,
		       ref_input, ref_output,
		       discount_pct, user_discount_pct
		FROM model_prices
		WHERE is_active = true
		ORDER BY user_input ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var prices []ModelPrice
	for rows.Next() {
		var mp ModelPrice
		var providerName string
		if err := rows.Scan(
			&mp.Model, &providerName, &mp.DisplayName,
			&mp.UserInputPer1M, &mp.UserOutputPer1M,
			&mp.RefInputPer1M, &mp.RefOutputPer1M,
			&mp.DiscountPct, &mp.UserDiscountPct,
		); err != nil {
			continue
		}
		mp.Provider = providerName
		prices = append(prices, mp)
	}

	return prices, rows.Err()
}
