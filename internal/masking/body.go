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
	modelField = regexp.MustCompile(`"model"\s*:\s*"[^"]*"`)

	// stripValue matches scalar JSON values and flat objects, which is what
	// the internal fields use. It is bounded so it never spans a comma.
	stripValue = `(?:-?\d+(?:\.\d+)?|"[^"]*"|true|false|null|\{[^{}]*\})`
)

// stripField removes "field":value occurrences from a JSON body, handling the
// leading-comma, trailing-comma, and standalone cases so the result stays
// valid JSON.
func stripField(data []byte, field string) []byte {
	lead := regexp.MustCompile(`,\s*"` + field + `"\s*:\s*` + stripValue)
	trail := regexp.MustCompile(`"` + field + `"\s*:\s*` + stripValue + `,\s*`)
	bare := regexp.MustCompile(`"` + field + `"\s*:\s*` + stripValue)
	d := lead.ReplaceAll(data, nil)
	d = trail.ReplaceAll(d, nil)
	return bare.ReplaceAll(d, nil)
}

// SanitizeResponseBody scrubs a provider response body (or a single SSE data
// line) so it no longer leaks the upstream identity: it rewrites the model
// field to the requested model id and strips the provider, settlement-cost,
// and BYOK-internal fields. Everything else stays byte-identical.
func SanitizeResponseBody(data []byte, modelID string) []byte {
	d := modelField.ReplaceAll(data, []byte(`"model":"`+modelID+`"`))
	for _, field := range []string{"provider", "cost", "cost_details", "is_byok"} {
		d = stripField(d, field)
	}
	return d
}

// StripCostField removes the provider-reported "cost" object from an
// OpenAI-compatible JSON body or SSE chunk. The upstream cost is internal
// margin data and must never reach the user, so any response that echoes it
// verbatim (non-streaming bodies and the final streaming chunk) is stripped
// here before forwarding. The provider cost is typically a flat object such
// as {"usd":0.00001073,"diem":0}, so a brace-balanced scan is sufficient.
func StripCostField(data []byte) []byte {
	return stripField(data, "cost")
}
