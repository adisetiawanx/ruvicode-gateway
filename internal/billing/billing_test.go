package billing

import (
	"testing"

	"github.com/ruvicode/gateway/internal/pricing"
	"github.com/ruvicode/gateway/internal/provider"
)

func TestEstimateCost(t *testing.T) {
	e := &Engine{}
	mp := &pricing.ModelPrice{UserInputPer1M: 1.0, UserOutputPer1M: 2.0}

	// 2 messages * 500 tokens = 1000 input tokens; default 1024 output tokens.
	got := e.EstimateCost(mp, 2, nil)
	want := (1000.0/1_000_000*1.0) + (1024.0/1_000_000*2.0)
	if got != want {
		t.Fatalf("expected %f, got %f", want, got)
	}

	// maxTokens overrides output estimate.
	got = e.EstimateCost(mp, 1, intPtr(512))
	want = (500.0/1_000_000*1.0) + (512.0/1_000_000*2.0)
	if got != want {
		t.Fatalf("expected %f, got %f", want, got)
	}
}

func TestCalculateActualCost(t *testing.T) {
	e := &Engine{}
	mp := &pricing.ModelPrice{UserInputPer1M: 1.0, UserOutputPer1M: 2.0}

	// 500 input tokens, 1000 output tokens.
	cost := e.CalculateActualCost(mp, &provider.Usage{PromptTokens: 500, CompletionTokens: 1000})
	want := (500.0/1_000_000*1.0) + (1000.0/1_000_000*2.0)
	if cost != want {
		t.Fatalf("expected %f, got %f", want, cost)
	}

	// nil usage -> 0.
	if got := e.CalculateActualCost(mp, nil); got != 0 {
		t.Fatalf("expected 0 for nil usage, got %f", got)
	}
}

func intPtr(v int) *int { return &v }
