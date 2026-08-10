package masking

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/ruvicode/gateway/internal/provider"
)

// openaiError is the standard OpenAI-compatible error envelope.
type openaiError struct {
	Error openaiErrorDetail `json:"error"`
}

type openaiErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

type sanitizedError struct {
	StatusCode int
	Body       openaiError
}

// WriteOpenAIError writes a generic OpenAI-compatible error response. It is
// used by the gateway for its own errors (auth, rate limit, validation).
func WriteOpenAIError(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(openaiError{
		Error: openaiErrorDetail{Type: errType, Message: message},
	})
}

// WriteProviderError converts a provider error into a safe, generic
// OpenAI-compatible error, revealing no provider identity.
func WriteProviderError(w http.ResponseWriter, err error) {
	var statusCode int
	var rawBody string

	if pe, ok := err.(*provider.ProviderError); ok {
		statusCode = pe.StatusCode
		rawBody = pe.RawBody
	} else {
		statusCode = http.StatusServiceUnavailable
	}

	generic := mapProviderError(statusCode, rawBody)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(generic.StatusCode)
	_ = json.NewEncoder(w).Encode(generic.Body)
}

// mapProviderError converts raw provider status codes and error bodies into
// generic, safe error messages that reveal NO provider identity.
func mapProviderError(statusCode int, rawBody string) sanitizedError {
	lower := strings.ToLower(rawBody)

	switch statusCode {
	case http.StatusPaymentRequired: // 402
		return sanitizedError{
			StatusCode: http.StatusPaymentRequired,
			Body:       openaiError{Error: openaiErrorDetail{Type: "insufficient_balance", Message: "Insufficient balance. Please top up your wallet."}},
		}

	case http.StatusNotFound: // 404 — no sellers for the model
		return sanitizedError{
			StatusCode: http.StatusServiceUnavailable,
			Body:       openaiError{Error: openaiErrorDetail{Type: "api_error", Message: "Model temporarily unavailable. Please try again."}},
		}

	case http.StatusServiceUnavailable: // 503 — all sellers unhealthy
		return sanitizedError{
			StatusCode: http.StatusServiceUnavailable,
			Body:       openaiError{Error: openaiErrorDetail{Type: "api_error", Message: "Service temporarily overloaded. Please retry."}},
		}

	case http.StatusTooManyRequests: // 429 — upstream rate limited
		return sanitizedError{
			StatusCode: http.StatusServiceUnavailable,
			Body:       openaiError{Error: openaiErrorDetail{Type: "api_error", Message: "Service temporarily overloaded. Please retry."}},
		}

	case http.StatusBadRequest: // 400
		if strings.Contains(lower, "minimum_discount") {
			return sanitizedError{
				StatusCode: http.StatusServiceUnavailable,
				Body:       openaiError{Error: openaiErrorDetail{Type: "api_error", Message: "No available capacity for this model. Please try again."}},
			}
		}
		if strings.Contains(lower, "unsupported_provider") {
			return sanitizedError{
				StatusCode: http.StatusBadRequest,
				Body:       openaiError{Error: openaiErrorDetail{Type: "invalid_request_error", Message: "Invalid request parameters."}},
			}
		}
		return sanitizedError{
			StatusCode: http.StatusBadRequest,
			Body:       openaiError{Error: openaiErrorDetail{Type: "invalid_request_error", Message: "Invalid request parameters."}},
		}

	default:
		return sanitizedError{
			StatusCode: http.StatusServiceUnavailable,
			Body:       openaiError{Error: openaiErrorDetail{Type: "api_error", Message: "An unexpected error occurred. Please try again."}},
		}
	}
}

// FormatCost renders a cost as a plain 8-decimal USD number for the X-Cost header.
func FormatCost(cost float64) string {
	return strconv.FormatFloat(cost, 'f', 8, 64)
}
