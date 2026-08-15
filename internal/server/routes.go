package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/ruvicode/gateway/internal/handler"
	"github.com/ruvicode/gateway/internal/middleware"
)

// Routes returns the configured HTTP handler.
//
// unauthenticated:
//   GET /health
//
// authenticated (Bearer rvcd_ key + per-key rate limit):
//   POST /v1/chat/completions
//   GET  /v1/models
func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()

	// Standard middleware.
	r.Use(chimw.Recoverer)
	r.Use(chimw.RequestID)
	r.Use(middleware.Logging)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"}, // OpenAI SDK clients from anywhere.
		AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type"},
		ExposedHeaders:   []string{"X-Ruvicode-Request-ID", "X-Cost", "X-RateLimit-Limit", "X-RateLimit-Remaining"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	// Health check (no auth).
	r.Get("/health", s.handleHealth)

	// Internal playground endpoint (shared-token auth, not rvcd_ Bearer).
	// Used by the Next.js dashboard to run the playground against the
	// signed-in user's own key and wallet.
	r.Post("/internal/playground/chat", s.internal.Handle)

	// Chat + models handlers.
	chatHandler := handler.NewChatHandler(s.registry, s.billing, s.pricing, s.pg)
	modelsHandler := handler.NewModelsHandler(s.pg)

	// OpenAI-compatible routes.
	r.Group(func(gr chi.Router) {
		gr.Use(s.auth.Handler)
		gr.Use(s.rateLimit.Handler)

		gr.Post("/v1/chat/completions", chatHandler.Handle)
		gr.Get("/v1/models", modelsHandler.Handle)
	})

	// Anthropic-compatible routes (registered; full support is a future ADR).
	r.Group(func(gr chi.Router) {
		gr.Use(s.auth.Handler)
		gr.Use(s.rateLimit.Handler)

	})

	return r
}
