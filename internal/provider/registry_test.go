package provider

import (
	"context"
	"testing"
)

type stubProvider struct {
	name string
}

func (s *stubProvider) Name() string                  { return s.name }
func (s *stubProvider) ChatCompletion(ctx context.Context, req *ChatRequest) (*ChatResult, error) {
	return &ChatResult{ProviderName: s.name}, nil
}
func (s *stubProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	return nil, nil
}
func (s *stubProvider) FetchPricing(ctx context.Context) ([]PricingData, error) {
	return nil, nil
}
func (s *stubProvider) HealthCheck(ctx context.Context) error { return nil }

func TestRegistryRegisterAndGet(t *testing.T) {
	r := NewRegistry(DefaultName)
	prov := &stubProvider{name: DefaultName}
	r.Register(prov)

	got, err := r.Get(DefaultName)
	if err != nil {
		t.Fatalf("Get(default) error: %v", err)
	}
	if got.Name() != DefaultName {
		t.Fatalf("expected name %q, got %q", DefaultName, got.Name())
	}

	if _, err := r.Get("does-not-exist"); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestRegistryGetDefaultAndList(t *testing.T) {
	r := NewRegistry(DefaultName)
	prov := &stubProvider{name: DefaultName}
	alpha := &stubProvider{name: "alpha"}
	r.Register(prov)
	r.Register(alpha)

	def, err := r.GetDefault()
	if err != nil {
		t.Fatalf("GetDefault error: %v", err)
	}
	if def.Name() != DefaultName {
		t.Fatalf("expected default %q, got %q", DefaultName, def.Name())
	}

	names := r.List()
	if len(names) != 2 {
		t.Fatalf("expected 2 providers, got %d: %v", len(names), names)
	}
}

func TestRegistryGetForModelFallback(t *testing.T) {
	r := NewRegistry(DefaultName)
	r.Register(&stubProvider{name: DefaultName})
	alpha := &stubProvider{name: "alpha"}
	r.Register(alpha)

	// Explicit model->provider mapping uses that provider.
	p, err := r.GetForModel("glm-5.2", "alpha")
	if err != nil {
		t.Fatalf("GetForModel(alpha) error: %v", err)
	}
	if p.Name() != "alpha" {
		t.Fatalf("expected alpha, got %q", p.Name())
	}

	// Unknown/unregistered provider falls back to default.
	p, err = r.GetForModel("glm-5.2", "unknown")
	if err != nil {
		t.Fatalf("GetForModel(unknown) error: %v", err)
	}
	if p.Name() != DefaultName {
		t.Fatalf("expected fallback %q, got %q", DefaultName, p.Name())
	}

	// Empty mapping falls back to default.
	p, err = r.GetForModel("glm-5.2", "")
	if err != nil {
		t.Fatalf("GetForModel(empty) error: %v", err)
	}
	if p.Name() != DefaultName {
		t.Fatalf("expected default for empty mapping, got %q", p.Name())
	}
}

func TestRegistryGetDefaultNotRegistered(t *testing.T) {
	// No provider registered at all -> GetDefault returns an error.
	r := NewRegistry(DefaultName)
	if _, err := r.GetDefault(); err == nil {
		t.Fatal("expected error when default provider is not registered")
	}
}
