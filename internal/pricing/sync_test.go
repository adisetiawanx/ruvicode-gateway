package pricing

import (
	"context"
	"errors"
	"testing"

	"github.com/ruvicode/gateway/internal/provider"
)

// stubProvider satisfies the provider.Provider interface for sync tests.
// It returns an error when err is set, and no prices otherwise (so the sync
// loop has nothing to upsert and never touches Postgres).
type stubProvider struct {
	name string
	err  error
}

func (s *stubProvider) Name() string { return s.name }

func (s *stubProvider) ChatCompletion(context.Context, *provider.ChatRequest) (*provider.ChatResult, error) {
	return nil, nil
}

func (s *stubProvider) ListModels(context.Context) ([]provider.ModelInfo, error) { return nil, nil }

func (s *stubProvider) FetchPricing(context.Context) ([]provider.PricingData, error) {
	if s.err != nil {
		return nil, s.err
	}
	return nil, nil
}

func (s *stubProvider) HealthCheck(context.Context) error { return s.err }

func TestSyncFailureCounter(t *testing.T) {
	reg := provider.NewRegistry("provider")
	p := &stubProvider{name: "provider", err: errors.New("boom")}
	reg.Register(p)

	e := New(nil, nil, reg, 20)
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		if err := e.SyncAllProviders(ctx); err == nil {
			t.Fatalf("expected error on call %d", i)
		}
		e.mu.Lock()
		if e.consecutiveFailures != i {
			e.mu.Unlock()
			t.Fatalf("expected %d consecutive failures, got %d", i, e.consecutiveFailures)
		}
		e.mu.Unlock()
	}

	// A successful run resets the counter.
	p.err = nil
	if err := e.SyncAllProviders(ctx); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.consecutiveFailures != 0 {
		t.Fatalf("expected counter reset to 0, got %d", e.consecutiveFailures)
	}
}