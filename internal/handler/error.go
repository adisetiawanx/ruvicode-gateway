package handler

import (
	"net/http"

	"github.com/ruvicode/gateway/internal/masking"
)

// writeJSONError writes an OpenAI-compatible error response.
func writeJSONError(w http.ResponseWriter, status int, errType, message string) {
	masking.WriteOpenAIError(w, status, errType, message)
}

// HandleNotImplemented is used for endpoints registered but not yet built
// (Anthropic-compatible surface is a future ADR).
func HandleNotImplemented(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, http.StatusNotImplemented, "invalid_request_error", "This endpoint is not implemented yet.")
}
