package masking

import (
	"net/http"
	"strconv"
	"strings"
)

// Headers that MUST be stripped from provider responses because they leak
// upstream identity or internals.
var strippedHeaders = []string{
	// Provider-specific identity headers.
	"x-si-served-by",
	"x-si-provider-family",
	"x-si-preferred-key-status",
	"x-si-marketplace-status",
	"x-si-marketplace-attempts",
	"x-si-routing-decision-ms",
	"x-si-cache-mode",
	"x-si-cache-warm",
	"x-si-cache-served-by-warm",
	"x-si-route-objective",

	// Generic upstream stack leaks.
	"x-powered-by",
	"etag",
	"server",

	// Provider rate limits (not ours).
	"x-ratelimit-limit",
	"x-ratelimit-remaining",
	"x-ratelimit-reset",

	// Provider payment internals.
	"payment-required",
	"payment-response",
	"x-payment-required",
	"x-payment-response",
	"x-x402-version",
	"retry-after",

	// Upstream CORS headers (we set our own).
	"access-control-allow-origin",
	"access-control-allow-methods",
	"access-control-allow-headers",
	"access-control-expose-headers",
	"access-control-allow-credentials",
	"access-control-max-age",
}

// SanitizeHeaders removes all provider-identifying headers from a response,
// then injects Ruvicode-branded headers and the user's rate-limit values.
func SanitizeHeaders(header http.Header, requestID string, userRateLimit, userRateLimitRemaining int) {
	for _, h := range strippedHeaders {
		header.Del(h)
	}

	// Catch-all: strip any header starting with "x-si-" (future internal headers).
	for h := range header {
		if strings.HasPrefix(strings.ToLower(h), "x-si-") {
			header.Del(h)
		}
	}

	header.Set("X-Ruvicode-Request-ID", requestID)
	header.Set("X-Ruvicode-Version", "1.0")
	header.Set("X-RateLimit-Limit", strconv.Itoa(userRateLimit))
	header.Set("X-RateLimit-Remaining", strconv.Itoa(userRateLimitRemaining))
}
