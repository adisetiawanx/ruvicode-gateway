package masking

import (
	"log/slog"
	"regexp"
	"strings"
)

// forbiddenIdentifiers are terms that should never appear in a response body.
// If found, we log a warning (the model output them, not our fault; monitoring only).
var forbiddenIdentifiers = []string{
	"surplusintelligence",
	"surplus intelligence",
	"surplus.ai",
}

// CheckBodyForLeaks scans a response body for forbidden identifiers.
// This is MONITORING only — we log a warning but never modify the body (which
// would break token/stream integrity).
func CheckBodyForLeaks(body []byte, requestID string) {
	lower := strings.ToLower(string(body))
	for _, id := range forbiddenIdentifiers {
		if strings.Contains(lower, id) {
			slog.Warn("response body leak warning",
				"request_id", requestID,
				"identifier", id,
				"note", "model output mentioned provider — monitoring only",
			)
			break
		}
	}
}

var (
	costWithLeadComma  = regexp.MustCompile(`,\s*"cost"\s*:\s*\{[^{}]*\}`)
	costWithTrailComma = regexp.MustCompile(`"cost"\s*:\s*\{[^{}]*\},\s*`)
	costBare           = regexp.MustCompile(`"cost"\s*:\s*\{[^{}]*\}`)
)

// StripCostField removes the provider-reported "cost" object from an
// OpenAI-compatible JSON body or SSE chunk. The upstream cost is internal
// margin data and must never reach the user, so any response that echoes it
// verbatim (non-streaming bodies and the final streaming chunk) is stripped
// here before forwarding. The provider cost is typically a flat object such
// as {"usd":0.00001073,"diem":0}, so a brace-balanced scan is sufficient.
func StripCostField(data []byte) []byte {
	d := costWithLeadComma.ReplaceAll(data, nil)
	d = costWithTrailComma.ReplaceAll(d, nil)
	return costBare.ReplaceAll(d, nil)
}
