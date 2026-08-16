package server

import (
	"encoding/json"
	"net/http"

	"github.com/ruvicode/gateway/internal/billing"
	"github.com/ruvicode/gateway/internal/config"
	"github.com/ruvicode/gateway/internal/handler"
	"github.com/ruvicode/gateway/internal/middleware"
	"github.com/ruvicode/gateway/internal/pricing"
	"github.com/ruvicode/gateway/internal/provider"
	"github.com/ruvicode/gateway/internal/wallet"
	"github.com/ruvicode/gateway/internal/store"
)

// Server bundles the gateway's dependencies.
type Server struct {
	cfg       *config.Config
	pg        *store.PostgresStore
	rdb       *store.RedisStore
	registry  *provider.Registry
	auth      *middleware.AuthMiddleware
	rateLimit *middleware.RateLimitMiddleware
	billing   *billing.Engine
	pricing   *pricing.Engine
	internal  *handler.InternalChatHandler
	deposit   *handler.DepositAddressHandler
}

// New creates a Server with the given configuration, stores, and dependencies.
func New(cfg *config.Config, pg *store.PostgresStore, rdb *store.RedisStore) *Server {
	// Provider registry: the MVP ships a single masked provider ("provider"),
	// which is also the default. Additional providers are registered here.
	registry := provider.NewRegistry(provider.DefaultName)
	if hasProvider(cfg.ActiveProviders, provider.DefaultName) {
		client := provider.NewClient(cfg.ProviderBaseURL, cfg.ProviderAPIKeys)
		registry.Register(client)
	}

	s := &Server{
		cfg:       cfg,
		pg:        pg,
		rdb:       rdb,
		registry:  registry,
		auth:      middleware.NewAuth(rdb, pg),
		rateLimit: middleware.NewRateLimit(rdb),
		billing:   billing.New(pg, rdb, cfg.PricingSpreadPP),
		pricing:   pricing.New(pg, rdb, registry, cfg.PricingSpreadPP),
	}

	// Internal playground endpoint: the dashboard calls it with the shared
	// token and a signed-in user's key id, and the gateway bills the user's
	// wallet through the normal pipeline. Rate limiting applies per user+key
	// just like a regular request.
	chatHandler := handler.NewChatHandler(registry, s.billing, s.pricing, pg)
	s.internal = handler.NewInternalChatHandler(
		pg,
		s.rateLimit.Handler(http.HandlerFunc(chatHandler.Handle)),
		cfg.InternalAPIToken,
	)

	// Deposit address endpoint (ADR-027): only wired when the HD wallet
	// mnemonic is configured. Without it the monitor is disabled anyway and
	// addresses cannot be derived, so the endpoint answers 503.
	if cfg.HDWalletMnemonic != "" {
		if hd, err := wallet.NewFromMnemonic(cfg.HDWalletMnemonic); err == nil {
			s.deposit = handler.NewDepositAddressHandler(
				wallet.NewAddressManager(pg, hd),
				cfg.InternalAPIToken,
			)
		} else {
			// Config error: fail loudly at startup, not silently per request.
			panic("invalid HD_WALLET_MNEMONIC: " + err.Error())
		}
	}

	return s
}

// handleHealth reports service status and provider health (no auth).
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := map[string]interface{}{
		"status": "ok",
	}

	if p, err := s.registry.GetDefault(); err == nil {
		healthy := p.HealthCheck(r.Context()) == nil
		status["provider_healthy"] = healthy
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(status)
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
