package server

import (
	"github.com/ruvicode/gateway/internal/config"
	"github.com/ruvicode/gateway/internal/provider"
	"github.com/ruvicode/gateway/internal/store"
)

// Server bundles the gateway's dependencies.
type Server struct {
	cfg      *config.Config
	pg       *store.PostgresStore
	rdb      *store.RedisStore
	registry *provider.Registry
}

// New creates a Server with the given configuration, stores, and provider registry.
func New(cfg *config.Config, pg *store.PostgresStore, rdb *store.RedisStore) *Server {
	s := &Server{cfg: cfg, pg: pg, rdb: rdb}

	// Initialize the provider registry. The MVP ships a single masked provider
	// ("provider"), so it is also the default. Additional providers are added
	// by registering them here; the gateway core never changes.
	s.registry = provider.NewRegistry(provider.DefaultName)
	if hasProvider(cfg.ActiveProviders, provider.DefaultName) {
		client := provider.NewClient(cfg.ProviderBaseURL, cfg.ProviderAPIKeys)
		s.registry.Register(client)
	}

	return s
}

// hasProvider reports whether a string is present in a slice.
func hasProvider(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
