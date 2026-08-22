package pricing

import (
	"testing"

	"github.com/ruvicode/gateway/internal/provider"
)

// TestCalculateUserPriceCacheReconstruct verifies the ADR-032 reference
// reconstruction against live marketplace numbers (glm-5.2, observed
// 2026-08-22): best_input 3080, direct_input 966000, best_cache_read 572
// (credits per 1M, divided by 1M in the parser).
func TestCalculateUserPriceCacheReconstruct(t *testing.T) {
	e := &Engine{spreadPP: 20}
	p := provider.PricingData{
		RefInputPer1M:      0.966,
		RefOutputPer1M:     3.936,
		ProviderInputPer1M: 0.00308,
		DiscountPct:        (1 - 0.00308/0.966) * 100,
		BestCacheReadPer1M: 0.000572,
	}
	_, _, _, refCacheRead, userCacheRead := e.calculateUserPrice(p)

	// Live-verified: ref_cache_read ~0.1794, user_cache_read ~0.036452
	if refCacheRead < 0.1793 || refCacheRead > 0.1795 {
		t.Errorf("refCacheRead = %v, want ~0.1794", refCacheRead)
	}
	if userCacheRead < 0.0364 || userCacheRead > 0.0365 {
		t.Errorf("userCacheRead = %v, want ~0.03645", userCacheRead)
	}

	// Margin on cache reads must stay positive: user price above provider best.
	if userCacheRead <= p.BestCacheReadPer1M {
		t.Errorf("userCacheRead %v must exceed provider best %v", userCacheRead, p.BestCacheReadPer1M)
	}
}

func TestCalculateUserPriceNoCachePrice(t *testing.T) {
	e := &Engine{spreadPP: 20}
	p := provider.PricingData{
		RefInputPer1M:      0.966,
		ProviderInputPer1M: 0.00308,
		DiscountPct:        94,
		BestCacheReadPer1M: 0, // feed has no cache price
	}
	_, _, _, refCacheRead, userCacheRead := e.calculateUserPrice(p)
	if refCacheRead != 0 || userCacheRead != 0 {
		t.Errorf("no cache price must yield zeros, got ref=%v user=%v", refCacheRead, userCacheRead)
	}
}

func TestCalculateUserPriceFullDiscountGuard(t *testing.T) {
	e := &Engine{spreadPP: 20}
	// discount 100 or 0 would divide by zero / negative ratio; must not panic.
	for _, dc := range []float64{0, 100, 101} {
		p := provider.PricingData{
			RefInputPer1M:      1,
			ProviderInputPer1M: 0.5,
			DiscountPct:        dc,
			BestCacheReadPer1M: 0.1,
		}
		_, _, _, refCacheRead, _ := e.calculateUserPrice(p)
		if dc >= 100 && refCacheRead != 0 {
			t.Errorf("discount %v must yield zero refCacheRead, got %v", dc, refCacheRead)
		}
	}
}
