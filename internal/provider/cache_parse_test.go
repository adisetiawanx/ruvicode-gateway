package provider

import (
	"encoding/json"
	"testing"
)

func TestParseCacheReadTokensOpenAIHit(t *testing.T) {
	got := ParseCacheReadTokens(5184, 0, 0, nil)
	if got != 5184 {
		t.Errorf("hit = %d, want 5184", got)
	}
}

func TestParseCacheReadTokensAnthropicFallback(t *testing.T) {
	got := ParseCacheReadTokens(0, 700, 0, nil)
	if got != 700 {
		t.Errorf("anthropic = %d, want 700", got)
	}
}

func TestParseCacheReadTokensCachedTopLevel(t *testing.T) {
	got := ParseCacheReadTokens(0, 0, 300, nil)
	if got != 300 {
		t.Errorf("cached top = %d, want 300", got)
	}
}

func TestParseCacheReadTokensPromptDetailsFallback(t *testing.T) {
	details := json.RawMessage(`{"cached_tokens": 250}`)
	got := ParseCacheReadTokens(0, 0, 0, details)
	if got != 250 {
		t.Errorf("prompt details = %d, want 250", got)
	}
}

func TestParseCacheReadTokensZero(t *testing.T) {
	got := ParseCacheReadTokens(0, 0, 0, nil)
	if got != 0 {
		t.Errorf("zero = %d, want 0", got)
	}
}

func TestParseCacheReadTokensHitTakesPriority(t *testing.T) {
	details := json.RawMessage(`{"cached_tokens": 100}`)
	got := ParseCacheReadTokens(500, 300, 200, details)
	if got != 500 {
		t.Errorf("hit priority = %d, want 500", got)
	}
}
