package provider

import (
	"net/http"
	"testing"
)

func TestKeyPoolSelectRotates(t *testing.T) {
	pool := NewKeyPool([]string{"k1", "k2", "k3"})

	selected := map[string]bool{}
	for i := 0; i < 3; i++ {
		selected[pool.Select()] = true
	}

	if len(selected) != 3 {
		t.Fatalf("expected all 3 keys to be selected, got: %v", selected)
	}
}

func TestKeyPoolSkipsExhausted(t *testing.T) {
	pool := NewKeyPool([]string{"k1", "k2"})

	// Drain k1 below the threshold to simulate exhaustion.
	pool.mu.Lock()
	pool.keys[0].remaining.Store(minRemaining - 1)
	pool.mu.Unlock()

	// k2 should always be selected.
	for i := 0; i < 5; i++ {
		if got := pool.Select(); got != "k2" {
			t.Fatalf("expected k2, got %q", got)
		}
	}
}

func TestKeyPoolAllExhaustedReturnsEmpty(t *testing.T) {
	pool := NewKeyPool([]string{"k1", "k2"})

	pool.mu.Lock()
	pool.keys[0].remaining.Store(0)
	pool.keys[1].remaining.Store(minRemaining - 1)
	pool.mu.Unlock()

	if got := pool.Select(); got != "" {
		t.Fatalf("expected empty string when all exhausted, got %q", got)
	}
}

func TestKeyPoolUpdateFromHeaders(t *testing.T) {
	pool := NewKeyPool([]string{"k1"})

	h := http.Header{}
	h.Set("X-RateLimit-Remaining", "77")
	pool.UpdateFromHeaders("k1", h)

	pool.mu.Lock()
	got := pool.keys[0].remaining.Load()
	pool.mu.Unlock()
	if got != 77 {
		t.Fatalf("expected remaining 77, got %d", got)
	}
}

func TestKeyPoolMarkError(t *testing.T) {
	pool := NewKeyPool([]string{"k1"})
	pool.MarkError("k1")
	pool.MarkError("k1")
	if got := pool.ErrorCount("k1"); got != 2 {
		t.Fatalf("expected error count 2, got %d", got)
	}
}
