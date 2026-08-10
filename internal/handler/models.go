package handler

import (
	"encoding/json"
	"net/http"

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

type modelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// Handle lists active models from the model_prices table.
func (h *ModelsHandler) Handle(w http.ResponseWriter, r *http.Request) {
	models, err := h.pg.ListActiveModels(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "api_error", "Failed to list models")
		return
	}

	resp := modelsResponse{Object: "list", Data: make([]modelEntry, 0, len(models))}
	for _, m := range models {
		resp.Data = append(resp.Data, modelEntry{ID: m, Object: "model", OwnedBy: "ruvicode"})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
