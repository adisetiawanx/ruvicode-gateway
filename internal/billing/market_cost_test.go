package billing

import (
	"testing"

	"github.com/ruvicode/gateway/internal/pricing"
	"github.com/ruvicode/gateway/internal/provider"
)

// TestCalculateMarketCostMatchesVerifiedWalletCharge pins the market-cost
// estimate against the live-verified wallet charge (2026-08-22 screenshot
// calibration): a 411k-token request with 405,728 cached settled at $0.0003
// on the wallet. With the marketplace best prices observed that day
// (in 0.00308, out 0.0112, cache read 0.000572 per 1M) the formula must
// land in the same range.
func TestCalculateMarketCostMatchesVerifiedWalletCharge(t *testing.T) {
	e := New(nil, nil, 20)
	mp := &pricing.ModelPrice{
		ProviderInputPer1M:     0.00308,
		ProviderOutputPer1M:    0.0112,
		ProviderCacheReadPer1M: 0.000572,
	}
	cost := e.CalculateMarketCost(mp, &provider.Usage{
		PromptTokens:     411000,
		CompletionTokens: 235,
		CacheReadTokens:  405728,
	})
	// Verified wallet charge: $0.0003 (dashboard shows rounded credits).
	// Formula gives 0.000251 — same order, within the display rounding.
	if cost < 0.0002 || cost > 0.0004 {
		t.Errorf("market cost %v not in the verified wallet range 0.0002-0.0004", cost)
	}
}

func TestCalculateMarketCostNoCachePriceFallsBack(t *testing.T) {
	e := New(nil, nil, 20)
	mp := &pricing.ModelPrice{
		ProviderInputPer1M:  0.003,
		ProviderOutputPer1M: 0.01,
		// ProviderCacheReadPer1M == 0
	}
	withCache := e.CalculateMarketCost(mp, &provider.Usage{PromptTokens: 1000, CacheReadTokens: 500})
	without := e.CalculateMarketCost(mp, &provider.Usage{PromptTokens: 1000})
	if withCache != without {
		t.Errorf("zero cache price must bill at input rate: %v vs %v", withCache, without)
	}
}

func TestCalculateMarketCostNilUsage(t *testing.T) {
	e := New(nil, nil, 20)
	if g := e.CalculateMarketCost(&pricing.ModelPrice{}, nil); g != 0 {
		t.Errorf("nil usage must be 0, got %v", g)
	}
}

func TestMarketCostBelowUserCost(t *testing.T) {
	// The spread guarantees the estimated provider cost stays below the user
	// charge for the same usage — margin must never be structurally negative.
	e := New(nil, nil, 20)
	mp := &pricing.ModelPrice{
		UserInputPer1M:         0.04,
		UserOutputPer1M:        0.15,
		ProviderInputPer1M:     0.00308,
		ProviderOutputPer1M:    0.0112,
		ProviderCacheReadPer1M: 0.000572,
	}
	usage := &provider.Usage{PromptTokens: 400000, CompletionTokens: 500, CacheReadTokens: 395000}
	user := e.CalculateActualCost(mp, usage)
	market := e.CalculateMarketCost(mp, usage)
	if market >= user {
		t.Errorf("market cost %v must stay below user charge %v", market, user)
	}
}
