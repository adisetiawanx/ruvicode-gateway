package handler

import (
	"encoding/json"
	"net/http"
)

// HandleHealth reports service status as JSON.
// It is the unauthenticated liveness endpoint for the gateway (ADRs 016, 023).
func HandleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
