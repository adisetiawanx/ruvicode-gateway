package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/ruvicode/gateway/internal/handler"
)

// Routes returns the configured HTTP handler.
//
// ADR-018 adds the authenticated OpenAI-compatible routes (auth, rate limit,
// chat completion, models) and the Anthropic-compatible routes. ADR-025 adds
// the /metrics endpoint. This ADR registers only the unauthenticated /health
// check so the server skeleton boots and is testable.
func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)

	// Health check (no auth).
	r.Get("/health", handler.HandleHealth)

	return r
}
