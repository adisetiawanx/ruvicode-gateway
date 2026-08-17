package server

import (
	"crypto/ecdsa"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/ruvicode/gateway/internal/billing"
	"github.com/ruvicode/gateway/internal/config"
	"github.com/ruvicode/gateway/internal/handler"
	"github.com/ruvicode/gateway/internal/middleware"
	"github.com/ruvicode/gateway/internal/pricing"
	"github.com/ruvicode/gateway/internal/provider"
	"github.com/ruvicode/gateway/internal/sweep"
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
	sweep     *handler.SweepHandler
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

	// Deposit + sweep endpoints (ADR-027 + ADR-025): only wired when the
	// HD wallet mnemonic is configured. The hd variable is shared by both.
	if cfg.HDWalletMnemonic != "" {
		hd, err := wallet.NewFromMnemonic(cfg.HDWalletMnemonic)
		if err != nil {
			panic("invalid HD_WALLET_MNEMONIC: " + err.Error())
		}
		s.deposit = handler.NewDepositAddressHandler(
			wallet.NewAddressManager(pg, hd),
			cfg.InternalAPIToken,
		)

		// Sweep handler (ADR-025): treasury derived from mnemonic at
		// reserved max BIP-44 index unless overridden via env.
		derivedTreasury := ""
		var treasuryKey *ecdsa.PrivateKey
		if tAddr, tKey, err := hd.DeriveAddress(0x7FFFFFFF); err == nil {
			derivedTreasury = tAddr.Hex()
			treasuryKey = tKey
		}
		treasury := cfg.TreasuryAddress
		if treasury == "" {
			treasury = derivedTreasury
		}
		if treasury == "" {
			slog.Error("sweep: cannot determine treasury address")
		} else {
			runner, err := sweep.New(cfg.BaseRPCURL, cfg.USDCContract, treasury, cfg.SweepMinUSD, 8453, pg, hd)
			if err != nil {
				slog.Error("sweep runner init failed", "error", err)
			} else {
				// Gas funding only works when treasury is the derived address
				// (we have its private key). If TREASURY_ADDRESS is overridden
				// to a non-derived address, gas funding is disabled.
				if treasuryKey != nil && treasury == derivedTreasury {
					runner.SetTreasuryKey(treasuryKey)
				}
				s.sweep = handler.NewSweepHandler(runner, handler.NewPgSweepStore(pg), hd, treasury, cfg.InternalAPIToken)
				slog.Info("sweep handler ready", "treasury", treasury, "gas_funding", runner.HasTreasuryKey())
			}
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
