package masking

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ruvicode/gateway/internal/provider"
)

// TestWriteProviderErrorMappings exercises every status-code branch of
// mapProviderError and asserts the generic OpenAI-compatible body, plus that
// no provider-identifying term ever leaks into the user-facing error.
func TestWriteProviderErrorMappings(t *testing.T) {
	// forbiddenTerms must never appear in any sanitized error, regardless of
	// which upstream error triggered it.
	forbiddenTerms := []string{"surplus", "marketplace", "seller", "inf_", "venice", "bankr", "zai", "surplusintelligence"}

	cases := []struct {
		name           string
		status         int
		rawBody        string
		wantStatus     int
		wantType       string
		wantMessageSub string
	}{
		{
			name:       "404 no sellers for model",
			status:     http.StatusNotFound,
			rawBody:    `{"error":{"code":"no_sellers_for_model"}}`,
			wantStatus: http.StatusServiceUnavailable,
			wantType:   "api_error",
		},
		{
			name:       "503 all sellers unhealthy",
			status:     http.StatusServiceUnavailable,
			rawBody:    `all_sellers_unhealthy`,
			wantStatus: http.StatusServiceUnavailable,
			wantType:   "api_error",
		},
		{
			name:       "400 minimum discount not met",
			status:     http.StatusBadRequest,
			rawBody:    `minimum_discount_not_met`,
			wantStatus: http.StatusServiceUnavailable,
			wantType:   "api_error",
		},
		{
			name:       "400 unsupported provider",
			status:     http.StatusBadRequest,
			rawBody:    `{"error":{"code":"unsupported_provider"}}`,
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_request_error",
		},
		{
			name:       "400 generic invalid",
			status:     http.StatusBadRequest,
			rawBody:    `{"error":{"message":"bad thing"}}`,
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_request_error",
		},
		{
			name:       "402 payment required maps to user balance",
			status:     http.StatusPaymentRequired,
			rawBody:    `payment_required`,
			wantStatus: http.StatusPaymentRequired,
			wantType:   "insufficient_balance",
		},
		{
			name:       "429 upstream rate limit becomes overloaded",
			status:     http.StatusTooManyRequests,
			rawBody:    `rate limit reached`,
			wantStatus: http.StatusServiceUnavailable,
			wantType:   "api_error",
		},
		{
			name:       "500 default generic",
			status:     http.StatusInternalServerError,
			rawBody:    `upstream exploded`,
			wantStatus: http.StatusServiceUnavailable,
			wantType:   "api_error",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rw := httptest.NewRecorder()
			WriteProviderError(rw, &provider.ProviderError{StatusCode: tc.status, RawBody: tc.rawBody})

			if rw.Code != tc.wantStatus {
				t.Fatalf("expected status %d, got %d", tc.wantStatus, rw.Code)
			}

			var resp struct {
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
				t.Fatalf("response is not valid JSON (%v): %s", err, rw.Body.String())
			}
			if resp.Error.Type != tc.wantType {
				t.Errorf("expected error type %q, got %q", tc.wantType, resp.Error.Type)
			}
			if resp.Error.Message == "" {
				t.Errorf("expected a non-empty message")
			}

			for _, term := range forbiddenTerms {
				if strings.Contains(strings.ToLower(rw.Body.String()), term) {
					t.Errorf("error body leaked forbidden term %q: %s", term, rw.Body.String())
				}
			}
		})
	}
}

// TestWriteProviderErrorAlwaysGeneric asserts that even an error with a body
// built to look like an upstream leak is fully replaced, never echoed.
func TestWriteProviderErrorDoesNotEchoProviderBody(t *testing.T) {
	rw := httptest.NewRecorder()
	WriteProviderError(rw, &provider.ProviderError{
		StatusCode: http.StatusServiceUnavailable,
		RawBody:    `{"error": "upstream refused connection to api.surplusintelligence.ai"}`,
	})

	if strings.Contains(strings.ToLower(rw.Body.String()), "api.surplusintelligence.ai") {
		t.Fatalf("provider URL leaked into sanitized error: %s", rw.Body.String())
	}
}