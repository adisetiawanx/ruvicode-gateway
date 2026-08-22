package billing

import (
	"testing"

	"github.com/ruvicode/gateway/internal/pricing"
	"github.com/ruvicode/gateway/internal/provider"
)

func TestCalculateActualCostCacheSplit(t *testing.T) {
	e := New(nil, nil, 20)
	mp := &pricing.ModelPrice{
		UserInputPer1M:     1.0,
		UserOutputPer1M:    2.0,
		UserCacheReadPer1M: 0.2,
	}

	// 1000 prompt tokens, 800 cached, 100 completion.
	g := e.CalculateActualCost(mp, &provider.Usage{
		PromptTokens:     1000,
		CompletionTokens: 100,
		CacheReadTokens:  800,
	})
	// billable 200 * 1.0/1M + cached 800 * 0.2/1M + 100 * 2.0/1M
	want := 200.0/1_000_000*1.0 + 800.0/1_000_000*0.2 + 100.0/1_000_000*2.0
	if diff := g - want; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("cost = %v, want %v", g, want)
	}
}

func TestCalculateActualCostNoCacheCheaperThanFull(t *testing.T) {
	e := New(nil, nil, 20)
	mp := &pricing.ModelPrice{
		UserInputPer1M:     1.0,
		UserOutputPer1M:    2.0,
		UserCacheReadPer1M: 0.2,
	}
	noCache := e.CalculateActualCost(mp, &provider.Usage{PromptTokens: 1000, CompletionTokens: 100})
	full := e.CalculateActualCost(mp, &provider.Usage{PromptTokens: 1000, CompletionTokens: 100, CacheReadTokens: 1000})
	if full >= noCache {
		t.Errorf("fully cached cost %v should be below uncached %v", full, noCache)
	}
}

func TestCalculateActualCostZeroCachePriceFallsBackToInputRate(t *testing.T) {
	e := New(nil, nil, 20)
	mp := &pricing.ModelPrice{
		UserInputPer1M:  1.0,
		UserOutputPer1M: 2.0,
		// UserCacheReadPer1M == 0: no cache price synced
	}
	with := e.CalculateActualCost(mp, &provider.Usage{PromptTokens: 1000, CompletionTokens: 0, CacheReadTokens: 500})
	without := e.CalculateActualCost(mp, &provider.Usage{PromptTokens: 1000, CompletionTokens: 0})
	if with != without {
		t.Errorf("zero cache price must bill at input rate: with=%v without=%v", with, without)
	}
}

func TestCalculateActualCostClampsOvershootingCache(t *testing.T) {
	e := New(nil, nil, 20)
	mp := &pricing.ModelPrice{
		UserInputPer1M:     1.0,
		UserOutputPer1M:    2.0,
		UserCacheReadPer1M: 0.2,
	}
	// Upstream accounting overshoot: cache read above prompt tokens.
	g := e.CalculateActualCost(mp, &provider.Usage{PromptTokens: 1000, CompletionTokens: 0, CacheReadTokens: 1200})
	want := 1000.0 / 1_000_000 * 0.2 // everything at the cache rate
	if diff := g - want; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("overshoot cost = %v, want %v", g, want)
	}
}

func TestCalculateActualCostNegativeCacheClamped(t *testing.T) {
	e := New(nil, nil, 20)
	mp := &pricing.ModelPrice{UserInputPer1M: 1.0, UserOutputPer1M: 2.0, UserCacheReadPer1M: 0.2}
	g := e.CalculateActualCost(mp, &provider.Usage{PromptTokens: 1000, CompletionTokens: 0, CacheReadTokens: -5})
	want := 1000.0 / 1_000_000 * 1.0
	if diff := g - want; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("negative cache cost = %v, want %v", g, want)
	}
}

func TestCalculateRefCostCacheSplit(t *testing.T) {
	e := New(nil, nil, 20)
	mp := &pricing.ModelPrice{
		RefInputPer1M:     2.0,
		RefOutputPer1M:    4.0,
		RefCacheReadPer1M: 0.4,
	}
	g := e.CalculateRefCost(mp, &provider.Usage{PromptTokens: 1000, CompletionTokens: 100, CacheReadTokens: 800})
	want := 200.0/1_000_000*2.0 + 800.0/1_000_000*0.4 + 100.0/1_000_000*4.0
	if diff := g - want; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("ref cost = %v, want %v", g, want)
	}
}

func TestNilUsageZeroCost(t *testing.T) {
	e := New(nil, nil, 20)
	if g := e.CalculateActualCost(&pricing.ModelPrice{}, nil); g != 0 {
		t.Errorf("nil usage must cost 0, got %v", g)
	}
	if g := e.CalculateRefCost(&pricing.ModelPrice{}, nil); g != 0 {
		t.Errorf("nil usage ref must cost 0, got %v", g)
	}
}
