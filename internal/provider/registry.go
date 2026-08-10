package provider

import (
	"fmt"
	"sync"
)

// Registry holds all active providers and maps models to providers.
// The gateway core uses it to select which provider handles each request.
// Model-to-provider mapping is derived from the model_prices table (via its
// provider column), falling back to a default provider when unknown.
type Registry struct {
	mu              sync.RWMutex
	providers       map[string]Provider
	defaultProvider string
}

// NewRegistry creates a Registry with the given default provider name.
func NewRegistry(defaultProvider string) *Registry {
	return &Registry{
		providers:       make(map[string]Provider),
		defaultProvider: defaultProvider,
	}
}

// Register adds a provider to the registry.
func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.Name()] = p
}

// Get retrieves a provider by name.
func (r *Registry) Get(name string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("provider not found: %s", name)
	}
	return p, nil
}

// GetDefault returns the default provider (used when model->provider mapping
// is unknown or the mapped provider is unavailable).
func (r *Registry) GetDefault() (Provider, error) {
	return r.Get(r.defaultProvider)
}

// GetForModel returns the provider for a specific model.
//
// modelProviderFromDB is the value of the model_prices.provider column for the
// model. If it names a registered provider, that provider is used. Otherwise
// (unknown, unregistered, or empty) it falls back to the default provider.
// If the provider is ever needed but missing, the default is returned so the
// request still gets served by the MVP's sole provider.
func (r *Registry) GetForModel(modelName string, modelProviderFromDB string) (Provider, error) {
	if modelProviderFromDB != "" {
		if p, err := r.Get(modelProviderFromDB); err == nil {
			_ = modelName // future: per-model overrides
			return p, nil
		}
	}

	return r.GetDefault()
}

// List returns all registered provider names.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}
