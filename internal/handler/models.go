package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/ruvicode/gateway/internal/catalog"
	"github.com/ruvicode/gateway/internal/store"
)

// ModelsHandler implements GET /v1/models (OpenAI-compatible).
type ModelsHandler struct {
	pg *store.PostgresStore
}

// NewModelsHandler builds a ModelsHandler.
func NewModelsHandler(pg *store.PostgresStore) *ModelsHandler {
	return &ModelsHandler{pg: pg}
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
	models, err := h.pg.ListActiveModels(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "api_error", "Failed to list models")
		return
	}

	// Stable timestamp for the listing.
	created := time.Now().Unix()

	resp := modelsResponse{Object: "list", Data: make([]modelEntry, 0, len(models))}
	for _, m := range models {
		entry := modelEntry{
			ID:        m,
			Object:    "model",
			Created:   created,
			OwnedBy:   "ruvicode",
			ContextLength: 0,
		}
		if meta, ok := catalog.Meta(m); ok {
			entry.Name = meta.DisplayName
			entry.ContextLength = meta.Context
			entry.MaxCompletionTokens = meta.MaxOutput
		}
		resp.Data = append(resp.Data, entry)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
