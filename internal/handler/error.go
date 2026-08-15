package handler

import (
	"net/http"

	"github.com/ruvicode/gateway/internal/masking"
)

// writeJSONError writes an OpenAI-compatible error response.
func writeJSONError(w http.ResponseWriter, status int, errType, message string) {
	masking.WriteOpenAIError(w, status, errType, message)
}
