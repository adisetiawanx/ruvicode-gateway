package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/ruvicode/gateway/internal/catalog"
	"github.com/ruvicode/gateway/internal/store"
)

// ModelsHandler implements GET /v1/models (OpenAI-compatible).
//
// The endpoint is intentionally PUBLIC (no Bearer key): coding tools such as
// OpenCode and OpenClaw discover custom providers by fetching {baseURL}/models
// WITHOUT credentials when the user leaves the model list empty. Requiring
// auth here breaks that discovery — Venice AI and OpenRouter serve their
// model lists publicly for the same reason. The payload contains only public
// catalog data (ids, display names, context windows); chat completions stay
// strictly authenticated.
type ModelsHandler struct {
	pg *store.PostgresStore

	// Small in-memory cache so an unauthenticated endpoint cannot hammer
	// Postgres. The catalog changes only when the pricing worker runs.
	mu        sync.Mutex
	cached    *modelsResponse
	cachedAt  time.Time
	cacheTTL  time.Duration
}

// NewModelsHandler builds a ModelsHandler.
func NewModelsHandler(pg *store.PostgresStore) *ModelsHandler {
	return &ModelsHandler{
		pg:       pg,
		cacheTTL: 30 * time.Second,
	}
}

type modelsResponse struct {
	Object string       `json:"object"`
	Data   []modelEntry `json:"data"`
}

// modelEntry follows the OpenRouter/OpenAI model shape so IDEs and coding
// tools can auto-detect models and their context windows: the standard
// fields (id, object, created, owned_by) plus context_length and
// max_completion_tokens, which tooling reads to size requests.
type modelEntry struct {
	ID                  string `json:"id"`
	Object              string `json:"object"`
	Created             int64  `json:"created"`
	OwnedBy            string `json:"owned_by"`
	Name                string `json:"name,omitempty"`
	ContextLength       int    `json:"context_length"`
	MaxCompletionTokens int    `json:"max_completion_tokens,omitempty"`
}

// Handle lists active models from the model_prices table. Context and max
// output come from the curated catalog (verified vendor specs); models that
// somehow lack catalog metadata still list, with zeroed token fields.
func (h *ModelsHandler) Handle(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	if h.cached != nil && time.Since(h.cachedAt) < h.cacheTTL {
		resp := h.cached
		h.mu.Unlock()
		writeModels(w, resp)
		return
	}
	h.mu.Unlock()

	models, err := h.pg.ListActiveModels(context.Background())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "api_error", "Failed to list models")
		return
	}

	created := time.Now().Unix()

	resp := &modelsResponse{Object: "list", Data: make([]modelEntry, 0, len(models))}
	for _, m := range models {
		entry := modelEntry{
			ID:        m,
			Object:    "model",
			Created:   created,
			OwnedBy:   "ruvicode",
		}
		if meta, ok := catalog.Meta(m); ok {
			entry.Name = meta.DisplayName
			entry.ContextLength = meta.Context
			entry.MaxCompletionTokens = meta.MaxOutput
		}
		resp.Data = append(resp.Data, entry)
	}

	h.mu.Lock()
	h.cached = resp
	h.cachedAt = time.Now()
	h.mu.Unlock()

	writeModels(w, resp)
}

func writeModels(w http.ResponseWriter, resp *modelsResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
