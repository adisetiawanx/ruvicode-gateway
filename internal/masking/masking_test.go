package masking

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ruvicode/gateway/internal/provider"
)

func TestSanitizeHeadersStripsProviderHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("X-Si-Served-By", "marketplace-node-3")
	h.Set("X-Si-Provider-Family", "claude")
	h.Set("X-Powered-By", "Express")
	h.Set("Server", "nginx")
	h.Set("X-RateLimit-Remaining", "5")
	h.Set("ETag", "abc123")

	SanitizeHeaders(h, "req-123", 60, 45)

	for _, header := range []string{
		"X-Si-Served-By", "X-Si-Provider-Family", "X-Powered-By",
		"Server", "ETag",
	} {
		if h.Get(header) != "" {
			t.Errorf("expected %q to be stripped, got %q", header, h.Get(header))
		}
	}

	if h.Get("X-Ruvicode-Request-ID") != "req-123" {
		t.Errorf("expected request id injected, got %q", h.Get("X-Ruvicode-Request-ID"))
	}
	if h.Get("X-Ruvicode-Version") != "1.0" {
		t.Errorf("expected version 1.0, got %q", h.Get("X-Ruvicode-Version"))
	}
	if h.Get("X-RateLimit-Limit") != "60" || h.Get("X-RateLimit-Remaining") != "45" {
		t.Errorf("expected user rate limit headers, got %q/%q", h.Get("X-RateLimit-Limit"), h.Get("X-RateLimit-Remaining"))
	}
}

func TestSanitizeHeadersCatchAllXSI(t *testing.T) {
	h := http.Header{}
	h.Set("X-Si-Future-Header", "leak")
	SanitizeHeaders(h, "id", 60, 60)
	if h.Get("X-Si-Future-Header") != "" {
		t.Errorf("catch-all should strip x-si- prefix headers")
	}
}

func TestWriteProviderErrorGeneric(t *testing.T) {
	rw := httptest.NewRecorder()
	WriteProviderError(rw, &provider.ProviderError{StatusCode: http.StatusNotFound, RawBody: "no_sellers_for_model"})

	if rw.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rw.Code)
	}

	var resp struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)

	lower := strings.ToLower(rw.Body.String())
	for _, forbidden := range []string{"surplus", "marketplace", "seller", "inf_"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("error body leaked forbidden term %q: %s", forbidden, rw.Body.String())
		}
	}
	if resp.Error.Type != "api_error" {
		t.Errorf("expected api_error, got %q", resp.Error.Type)
	}
}

func TestWriteProviderErrorInsufficientBalance(t *testing.T) {
	rw := httptest.NewRecorder()
	WriteProviderError(rw, &provider.ProviderError{StatusCode: http.StatusPaymentRequired, RawBody: "payment_required"})
	if rw.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %d", rw.Code)
	}
	if !strings.Contains(rw.Body.String(), "insufficient_balance") {
		t.Errorf("expected insufficient_balance error, got %s", rw.Body.String())
	}
}

func TestWriteProviderErrorUnknown(t *testing.T) {
	rw := httptest.NewRecorder()
	WriteProviderError(rw, &customError{})
	if rw.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected generic 503 for unknown error, got %d", rw.Code)
	}
}

type customError struct{}

func (c *customError) Error() string { return "boom" }
