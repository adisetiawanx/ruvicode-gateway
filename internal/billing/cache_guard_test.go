package billing

import (
	"testing"

	"github.com/ruvicode/gateway/internal/pricing"
	"github.com/ruvicode/gateway/internal/provider"
)

// Degenerate and adversarial cache scenarios (ADR-032 feedback item 4):
// a parsing or plumbing bug must never silently bill 100% of a prompt at the
// cheap cache rate. These tests pin the guards that prevent that class.

// TestCacheReadNeverExceedsPrompt pins the clamp: even if the upstream reports
// a cache read count larger than the prompt (buggy accounting or a parser
// regression that wires the wrong field into CacheReadTokens), billing uses at
// most prompt_tokens at the cache rate, and the remainder logic stays sane.
func TestCacheReadNeverExceedsPrompt(t *testing.T) {
	e := New(nil, nil, 20)
	mp := &pricing.ModelPrice{
		UserInputPer1M:     1.0,
		UserOutputPer1M:    1.0,
		UserCacheReadPer1M: 0.1,
	}
	cost := e.CalculateActualCost(mp, &provider.Usage{
		PromptTokens:     1000,
		CompletionTokens: 0,
		CacheReadTokens:  999999, // absurd overshoot
	})
	// Everything clamps to the full prompt at the cache rate; the result must
	// be exactly the fully-cached price, never more.
	want := 1000.0 / 1_000_000 * 0.1
	if diff := cost - want; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("overshoot cost = %v, want %v", cost, want)
	}
}

// TestZeroPromptWithCacheReported: a response with cache tokens but zero
// prompt tokens (malformed usage) must bill zero input, not a negative or
// phantom charge.
func TestZeroPromptWithCacheReported(t *testing.T) {
	e := New(nil, nil, 20)
	mp := &pricing.ModelPrice{UserInputPer1M: 1.0, UserOutputPer1M: 1.0, UserCacheReadPer1M: 0.1}
	cost := e.CalculateActualCost(mp, &provider.Usage{
		PromptTokens:     0,
		CompletionTokens: 100,
		CacheReadTokens:  500, // inconsistent with prompt=0
	})
	want := 100.0 / 1_000_000 * 1.0
	if diff := cost - want; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("zero-prompt cost = %v, want %v", cost, want)
	}
}

// TestCacheOnlyWhenPricePositive: when the synced cache price is zero the
// cached tokens fall back to the input rate, so a missing price can never
// make tokens free.
func TestCacheOnlyWhenPricePositive(t *testing.T) {
	e := New(nil, nil, 20)
	mp := &pricing.ModelPrice{UserInputPer1M: 2.0, UserOutputPer1M: 1.0}
	withCache := e.CalculateActualCost(mp, &provider.Usage{PromptTokens: 1000, CacheReadTokens: 400})
	plain := e.CalculateActualCost(mp, &provider.Usage{PromptTokens: 1000})
	if withCache != plain {
		t.Errorf("zero cache price must equal plain billing: %v vs %v", withCache, plain)
	}
}

// TestTypicalAgenticSplitIsLowerButProportional: sanity on a realistic mix,
// the cached request must cost less than uncached but never below the cost of
// just the cached tokens at the cache rate.
func TestTypicalAgenticSplitIsLowerButProportional(t *testing.T) {
	e := New(nil, nil, 20)
	mp := &pricing.ModelPrice{
		UserInputPer1M:     0.08,
		UserOutputPer1M:    0.26,
		UserCacheReadPer1M: 0.04,
	}
	uncached := e.CalculateActualCost(mp, &provider.Usage{PromptTokens: 100000, CompletionTokens: 500})
	cached := e.CalculateActualCost(mp, &provider.Usage{PromptTokens: 100000, CompletionTokens: 500, CacheReadTokens: 95000})
	if cached >= uncached {
		t.Errorf("cached cost %v must be below uncached %v", cached, uncached)
	}
	floor := 95000.0 / 1_000_000 * 0.04 // cached part alone
	if cached < floor {
		t.Errorf("cached cost %v below its own floor %v", cached, floor)
	}
}
